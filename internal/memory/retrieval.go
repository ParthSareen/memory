package memory

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

var groupLabels = map[string]string{
	"exact":   "Exact matches",
	"related": "Related work",
	"text":    "Text matches",
	"recent":  "Recent work",
}

const minimumTruncationMarker = "\n[truncated]\n"

type ContextQuery struct {
	Topic     string        `json:"topic"`
	Repo      string        `json:"repo"`
	Branch    string        `json:"branch"`
	Worktree  string        `json:"worktree"`
	Tags      []string      `json:"tags"`
	Links     []TypedTarget `json:"links"`
	RecordIDs []string      `json:"records"`
	MaxItems  int           `json:"-"`
	MaxTokens int           `json:"-"`
	MaxBytes  int           `json:"-"`
}

type Candidate struct {
	Record    Record   `json:"record"`
	Score     float64  `json:"score"`
	Group     string   `json:"group"`
	Reasons   []string `json:"reasons"`
	tier      int
	baseScore float64
	boost     float64
	ftsRank   float64
}

type ContextLimits struct {
	Items  int `json:"items"`
	Tokens int `json:"tokens"`
	Bytes  int `json:"bytes"`
}

type ContextResult struct {
	Brief           string        `json:"brief"`
	Query           ContextQuery  `json:"query"`
	Limits          ContextLimits `json:"limits"`
	Truncated       bool          `json:"truncated"`
	EstimatedTokens int           `json:"estimated_tokens"`
	Bytes           int           `json:"bytes"`
	CandidateCount  int           `json:"candidate_count"`
	Included        []Candidate   `json:"included"`
	Candidates      []Candidate   `json:"-"`
}

func RetrieveContext(store *Store, query ContextQuery) (ContextResult, error) {
	if query.MaxItems == 0 {
		query.MaxItems = 12
	}
	if query.MaxTokens == 0 {
		query.MaxTokens = 900
	}
	if query.MaxBytes == 0 {
		query.MaxBytes = 6000
	}
	if query.MaxItems < 1 || query.MaxTokens < 1 || query.MaxBytes < 1 {
		return ContextResult{}, errors.New("context limits must be positive integers")
	}
	if query.MaxBytes < len([]byte(minimumTruncationMarker)) || query.MaxTokens < EstimateTokens(minimumTruncationMarker) {
		return ContextResult{}, fmt.Errorf(
			"context budget too small; need at least %d bytes and %d estimated tokens",
			len([]byte(minimumTruncationMarker)), EstimateTokens(minimumTruncationMarker),
		)
	}
	if query.Tags == nil {
		query.Tags = []string{}
	}
	if query.Links == nil {
		query.Links = []TypedTarget{}
	}
	if query.RecordIDs == nil {
		query.RecordIDs = []string{}
	}
	exact, err := store.MetadataMatches(MetadataQuery{
		Repo: query.Repo, Branch: query.Branch, Worktree: query.Worktree,
		Tags: query.Tags, Links: query.Links, RecordIDs: query.RecordIDs,
	})
	if err != nil {
		return ContextResult{}, err
	}
	candidates := map[string]*Candidate{}
	exactIDs := mapKeys(exact)
	records, err := store.GetRecords(exactIDs)
	if err != nil {
		return ContextResult{}, err
	}
	for id, reasons := range exact {
		candidateReasons := append([]string(nil), reasons...)
		candidates[id] = &Candidate{
			Record: records[id], Group: "exact", Reasons: candidateReasons,
			tier: 3, baseScore: exactBase(candidateReasons),
		}
	}
	relations, err := store.RelatedRecords(exactIDs)
	if err != nil {
		return ContextResult{}, err
	}
	for _, relation := range relations {
		var relatedID, reason string
		if _, ok := exact[relation.FromID]; ok {
			relatedID = relation.ToID
			reason = fmt.Sprintf("%s from exact record %s", relation.Type, shortID(relation.FromID))
		} else {
			relatedID = relation.FromID
			reason = fmt.Sprintf("%s to exact record %s", relation.Type, shortID(relation.ToID))
		}
		if candidate, ok := candidates[relatedID]; ok {
			candidate.Reasons = append(candidate.Reasons, reason)
			continue
		}
		relatedRecords, err := store.GetRecords([]string{relatedID})
		if err != nil {
			return ContextResult{}, err
		}
		candidates[relatedID] = &Candidate{
			Record: relatedRecords[relatedID], Group: "related", Reasons: []string{reason},
			tier: 2, baseScore: 400,
		}
	}
	hasHardScope := query.Repo != "" || query.Branch != "" || query.Worktree != "" ||
		len(query.Tags) > 0 || len(query.Links) > 0 || len(query.RecordIDs) > 0
	expression := FTSExpression(query.Topic)
	if expression != "" {
		matches, err := store.FTSSearch(expression, max(50, query.MaxItems*4))
		if err != nil {
			return ContextResult{}, err
		}
		for _, match := range matches {
			if hasHardScope {
				if _, allowed := exact[match.ID]; !allowed {
					continue
				}
			}
			reason := fmt.Sprintf("text matches %q", query.Topic)
			if candidate, ok := candidates[match.ID]; ok {
				candidate.Reasons = append(candidate.Reasons, reason)
				candidate.ftsRank = match.Rank
				continue
			}
			matchedRecords, err := store.GetRecords([]string{match.ID})
			if err != nil {
				return ContextResult{}, err
			}
			candidates[match.ID] = &Candidate{
				Record: matchedRecords[match.ID], Group: "text", Reasons: []string{reason},
				tier: 1, baseScore: 200, ftsRank: match.Rank,
			}
		}
	}
	hasScope := query.Topic != "" || hasHardScope
	if !hasScope {
		ids, err := store.RecentIDs(max(50, query.MaxItems*4))
		if err != nil {
			return ContextResult{}, err
		}
		recentRecords, err := store.GetRecords(ids)
		if err != nil {
			return ContextResult{}, err
		}
		for _, id := range ids {
			candidates[id] = &Candidate{
				Record: recentRecords[id], Group: "recent", Reasons: []string{"recent work fallback"},
				tier: 0, baseScore: 50,
			}
		}
	}
	allRanked := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		applyBoost(candidate)
		candidate.Score = math.Round((candidate.baseScore+candidate.boost)*1000) / 1000
		allRanked = append(allRanked, *candidate)
	}
	sort.Slice(allRanked, func(i, j int) bool {
		left, right := allRanked[i], allRanked[j]
		if left.tier != right.tier {
			return left.tier > right.tier
		}
		if left.baseScore != right.baseScore {
			return left.baseScore > right.baseScore
		}
		if left.boost != right.boost {
			return left.boost > right.boost
		}
		if left.ftsRank != right.ftsRank {
			return left.ftsRank < right.ftsRank
		}
		leftTime, rightTime := parseTime(left.Record.UpdatedAt), parseTime(right.Record.UpdatedAt)
		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		return left.Record.ID < right.Record.ID
	})
	ranked := allRanked
	if len(ranked) > query.MaxItems {
		ranked = ranked[:query.MaxItems]
	}
	brief, includedIDs, truncated := RenderBrief(ranked, query, len(allRanked))
	includedSet := map[string]bool{}
	for _, id := range includedIDs {
		includedSet[id] = true
	}
	included := []Candidate{}
	for _, candidate := range ranked {
		if includedSet[candidate.Record.ID] {
			included = append(included, candidate)
		}
	}
	return ContextResult{
		Brief: brief, Query: query,
		Limits:    ContextLimits{Items: query.MaxItems, Tokens: query.MaxTokens, Bytes: query.MaxBytes},
		Truncated: truncated, EstimatedTokens: EstimateTokens(brief), Bytes: len([]byte(brief)),
		CandidateCount: len(allRanked), Included: included, Candidates: ranked,
	}, nil
}

func mapKeys(values map[string][]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func exactBase(reasons []string) float64 {
	base := 500.0
	count := 0
	for _, reason := range reasons {
		weight := 0.0
		switch {
		case strings.HasPrefix(reason, "record id"):
			weight = 1200
		case strings.Contains(reason, " link matches "):
			weight = 1100
		case strings.HasPrefix(reason, "repo matches"):
			weight = 800
		case strings.HasPrefix(reason, "branch matches"):
			weight = 700
		case strings.HasPrefix(reason, "worktree matches"):
			weight = 650
		case strings.HasPrefix(reason, "tag matches"):
			weight = 600
		}
		if weight > 0 {
			count++
			if weight > base {
				base = weight
			}
		}
	}
	if count > 1 {
		base += float64(count-1) * 5
	}
	return base
}

func applyBoost(candidate *Candidate) {
	switch candidate.Record.Status {
	case "active":
		candidate.boost += 40
		candidate.Reasons = append(candidate.Reasons, "active status boost")
	case "blocked":
		candidate.boost += 35
		candidate.Reasons = append(candidate.Reasons, "blocked status boost")
	}
	if candidate.Record.OpenQuestions != "" {
		candidate.boost += 15
		candidate.Reasons = append(candidate.Reasons, "unresolved questions boost")
	}
	if candidate.Record.NextAction != "" {
		candidate.boost += 10
		candidate.Reasons = append(candidate.Reasons, "next action boost")
	}
	age := int(time.Since(parseTime(candidate.Record.UpdatedAt)).Hours() / 24)
	if age < 0 {
		age = 0
	}
	recency := max(0, 30-min(age, 30))
	if recency > 0 {
		candidate.boost += float64(recency)
		if age == 0 {
			candidate.Reasons = append(candidate.Reasons, "updated today boost")
		} else {
			candidate.Reasons = append(candidate.Reasons, "recent update boost")
		}
	}
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Unix(0, 0)
	}
	return parsed
}

func shortID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func FTSExpression(query string) string {
	var tokens []string
	var builder strings.Builder
	flush := func() {
		token := strings.Trim(builder.String(), "./:#-")
		builder.Reset()
		if token != "" {
			tokens = append(tokens, `"`+strings.ReplaceAll(token, `"`, `""`)+`"`)
		}
	}
	for _, character := range query {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' || strings.ContainsRune("./:#-", character) {
			builder.WriteRune(character)
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(tokens, " AND ")
}

func EstimateTokens(value string) int {
	lexical := 0
	inWord := false
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' {
			if !inWord {
				lexical++
			}
			inWord = true
			continue
		}
		inWord = false
		if !unicode.IsSpace(character) {
			lexical++
		}
	}
	byteEstimate := (len([]byte(value)) + 3) / 4
	return max(lexical, byteEstimate)
}

type budget struct {
	maxTokens int
	maxBytes  int
	text      string
}

func (b *budget) fits(value string) bool {
	combined := b.text + value
	return len([]byte(combined)) <= b.maxBytes && EstimateTokens(combined) <= b.maxTokens
}

func (b *budget) append(value string) bool {
	if !b.fits(value) {
		return false
	}
	b.text += value
	return true
}

func (b *budget) appendPrefix(value string) bool {
	runes := []rune(value)
	low, high := 0, len(runes)
	for low < high {
		middle := (low + high + 1) / 2
		if b.fits(string(runes[:middle])) {
			low = middle
		} else {
			high = middle - 1
		}
	}
	if low == 0 {
		return false
	}
	prefix := strings.TrimRightFunc(string(runes[:low]), unicode.IsSpace)
	if prefix != value && b.fits(prefix+"…") {
		prefix += "…"
	}
	b.text += prefix
	return true
}

func (b *budget) forceAppend(value string) bool {
	if len([]byte(value)) > b.maxBytes || EstimateTokens(value) > b.maxTokens {
		return false
	}
	for !b.fits(value) && b.text != "" {
		runes := []rune(b.text)
		b.text = string(runes[:len(runes)-1])
	}
	if !b.fits(value) {
		return false
	}
	b.text += value
	return true
}

func RenderBrief(candidates []Candidate, query ContextQuery, totalCandidates int) (string, []string, bool) {
	output := &budget{maxTokens: query.MaxTokens, maxBytes: query.MaxBytes}
	header := fmt.Sprintf("# Work brief\nScope: %s\n", scopeLine(query))
	if !output.append(header) {
		output.appendPrefix(header)
		output.forceAppend(minimumTruncationMarker)
		return output.text, []string{}, true
	}
	if len(candidates) == 0 {
		truncated := !output.append("\nNo matching work records.\n")
		if truncated {
			output.forceAppend(minimumTruncationMarker)
		}
		return output.text, []string{}, truncated
	}
	included := []string{}
	currentGroup := ""
	truncated := false
	for index, candidate := range candidates {
		groupHeader := ""
		if candidate.Group != currentGroup {
			groupHeader = fmt.Sprintf("\n## %s\n", groupLabels[candidate.Group])
		}
		minimal, details := itemLines(candidate)
		if output.append(groupHeader + minimal + details) {
			currentGroup = candidate.Group
			included = append(included, candidate.Record.ID)
			continue
		}
		if output.append(groupHeader + minimal) {
			currentGroup = candidate.Group
			included = append(included, candidate.Record.ID)
			if details != "" {
				output.appendPrefix(details)
			}
			truncated = true
		} else {
			truncated = true
		}
		if index < len(candidates)-1 {
			truncated = true
		}
		break
	}
	if len(included) < totalCandidates {
		truncated = true
	}
	if truncated {
		omitted := totalCandidates - len(included)
		marker := minimumTruncationMarker
		if omitted > 0 {
			marker = fmt.Sprintf("\n[truncated: %d omitted]\n", omitted)
			if len([]byte(marker)) > output.maxBytes || EstimateTokens(marker) > output.maxTokens {
				marker = minimumTruncationMarker
			}
		}
		output.forceAppend(marker)
	}
	return output.text, included, truncated
}

func scopeLine(query ContextQuery) string {
	parts := []string{}
	for _, item := range []struct{ label, value string }{
		{"repo", query.Repo}, {"branch", query.Branch}, {"worktree", query.Worktree},
	} {
		if item.value != "" {
			parts = append(parts, item.label+"="+item.value)
		}
	}
	if query.Topic != "" {
		parts = append(parts, "topic="+query.Topic)
	}
	if len(query.Tags) > 0 {
		parts = append(parts, "tags="+strings.Join(query.Tags, ","))
	}
	for _, link := range query.Links {
		parts = append(parts, link.Type+"="+link.Target)
	}
	if len(query.RecordIDs) > 0 {
		parts = append(parts, "records="+strings.Join(query.RecordIDs, ","))
	}
	if len(parts) == 0 {
		return "recent active and unresolved work"
	}
	return strings.Join(parts, "; ")
}

func itemLines(candidate Candidate) (string, string) {
	record := candidate.Record
	location := ""
	if record.Repo != "" {
		location = " · " + record.Repo
		if record.Branch != "" {
			location += "@" + record.Branch
		}
	}
	minimal := fmt.Sprintf("- %s [%s] · %s%s\n  Why: %s\n",
		oneLine(record.Title), shortID(record.ID), record.Status, location, strings.Join(candidate.Reasons, "; "))
	details := strings.Builder{}
	for _, item := range []struct{ label, value string }{
		{"Outcome", record.Summary}, {"Decision", record.Decision}, {"Evidence", record.Evidence},
		{"Next", record.NextAction}, {"Open", record.OpenQuestions},
	} {
		if item.value != "" {
			fmt.Fprintf(&details, "  %s: %s\n", item.label, oneLine(item.value))
		}
	}
	metadata := []string{}
	if len(record.Owners) > 0 {
		metadata = append(metadata, "owners="+strings.Join(record.Owners, ","))
	}
	if len(record.Tags) > 0 {
		metadata = append(metadata, "tags="+strings.Join(record.Tags, ","))
	}
	if len(record.Links) > 0 {
		links := make([]string, 0, len(record.Links))
		for _, link := range record.Links {
			links = append(links, link.Type+":"+link.Target)
		}
		metadata = append(metadata, "links="+strings.Join(links, ","))
	}
	if len(metadata) > 0 {
		fmt.Fprintf(&details, "  Meta: %s\n", strings.Join(metadata, "; "))
	}
	return minimal, details.String()
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
