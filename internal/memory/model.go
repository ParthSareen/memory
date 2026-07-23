package memory

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion = 2
	ExportFormat  = "work-memory"
	ExportVersion = 1
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	typePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
)

var statuses = map[string]bool{
	"active":     true,
	"blocked":    true,
	"completed":  true,
	"abandoned":  true,
	"superseded": true,
}

type ExternalLink struct {
	Type      string `json:"type"`
	Target    string `json:"target"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}

type Relationship struct {
	FromRecordID     string `json:"from_record_id"`
	RelationshipType string `json:"relationship_type"`
	ToRecordID       string `json:"to_record_id"`
	CreatedAt        string `json:"created_at"`
}

type RecordInput struct {
	Title         string         `json:"title"`
	Status        string         `json:"status"`
	Repo          string         `json:"repo"`
	Branch        string         `json:"branch"`
	Worktree      string         `json:"worktree"`
	Summary       string         `json:"summary"`
	Decision      string         `json:"decision"`
	Evidence      string         `json:"evidence"`
	OpenQuestions string         `json:"open_questions"`
	NextAction    string         `json:"next_action"`
	Owners        []string       `json:"owners"`
	Tags          []string       `json:"tags"`
	Links         []ExternalLink `json:"links"`
	Source        string         `json:"source"`
}

type UpdateInput struct {
	Title         *string         `json:"title"`
	Status        *string         `json:"status"`
	Repo          *string         `json:"repo"`
	Branch        *string         `json:"branch"`
	Worktree      *string         `json:"worktree"`
	Summary       *string         `json:"summary"`
	Decision      *string         `json:"decision"`
	Evidence      *string         `json:"evidence"`
	OpenQuestions *string         `json:"open_questions"`
	NextAction    *string         `json:"next_action"`
	Owners        *[]string       `json:"owners"`
	Tags          *[]string       `json:"tags"`
	Links         *[]ExternalLink `json:"links"`
	Source        *string         `json:"source"`
}

func (input UpdateInput) Empty() bool {
	return input.Title == nil && input.Status == nil && input.Repo == nil &&
		input.Branch == nil && input.Worktree == nil && input.Summary == nil &&
		input.Decision == nil && input.Evidence == nil && input.OpenQuestions == nil &&
		input.NextAction == nil && input.Owners == nil && input.Tags == nil &&
		input.Links == nil && input.Source == nil
}

type Record struct {
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	Status        string         `json:"status"`
	Repo          string         `json:"repo"`
	Branch        string         `json:"branch"`
	Worktree      string         `json:"worktree"`
	Summary       string         `json:"summary"`
	Decision      string         `json:"decision"`
	Evidence      string         `json:"evidence"`
	OpenQuestions string         `json:"open_questions"`
	NextAction    string         `json:"next_action"`
	Source        string         `json:"source"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	Owners        []string       `json:"owners"`
	Tags          []string       `json:"tags"`
	Links         []ExternalLink `json:"links"`
	Relationships []Relationship `json:"relationships,omitempty"`
}

// WorkflowStatus is the deliberately small state vocabulary for active work.
type WorkflowStatus string

const (
	WorkflowActive  WorkflowStatus = "active"
	WorkflowWaiting WorkflowStatus = "waiting"
	WorkflowBlocked WorkflowStatus = "blocked"
	WorkflowClosed  WorkflowStatus = "closed"
	WorkflowParked  WorkflowStatus = "parked"
)

var workflowStatuses = map[WorkflowStatus]bool{
	WorkflowActive: true, WorkflowWaiting: true, WorkflowBlocked: true,
	WorkflowClosed: true, WorkflowParked: true,
}

// WorkflowItem is the operational overlay for one durable record.
// It keeps workflow state separate from the low-level record status vocabulary.
type WorkflowItem struct {
	Record          Record         `json:"record"`
	Status          WorkflowStatus `json:"status"`
	Class           string         `json:"class"`
	Area            string         `json:"area"`
	NeedsNextAction bool           `json:"needs_next_action"`
}

type WorkflowInput struct {
	Status          WorkflowStatus
	Class           string
	Area            string
	NeedsNextAction bool
}

func NormalizeWorkflowInput(input WorkflowInput) (WorkflowInput, error) {
	if input.Status == "" {
		input.Status = WorkflowActive
	}
	if !workflowStatuses[input.Status] {
		return WorkflowInput{}, fmt.Errorf("workflow status must be one of: active, waiting, blocked, closed, parked")
	}
	input.Class = strings.TrimSpace(input.Class)
	input.Area = strings.TrimSpace(input.Area)
	return input, nil
}

func RecordStatusForWorkflow(status WorkflowStatus) string {
	switch status {
	case WorkflowBlocked:
		return "blocked"
	case WorkflowClosed:
		return "completed"
	case WorkflowParked:
		return "abandoned"
	default:
		return "active"
	}
}

type ListOptions struct {
	Status string
	Repo   string
	Tag    string
	Owner  string
	Limit  int
}

type MetadataQuery struct {
	Repo      string
	Branch    string
	Worktree  string
	Tags      []string
	Links     []TypedTarget
	RecordIDs []string
}

type TypedTarget struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

type RelatedRecord struct {
	FromID string
	Type   string
	ToID   string
}

type FTSMatch struct {
	ID   string
	Rank float64
}

func Statuses() []string {
	result := make([]string, 0, len(statuses))
	for status := range statuses {
		result = append(result, status)
	}
	sort.Strings(result)
	return result
}

func NormalizeType(value, field string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if !typePattern.MatchString(normalized) {
		return "", fmt.Errorf("%s must match %s", field, typePattern.String())
	}
	return normalized, nil
}

func NormalizeInput(input RecordInput) (RecordInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" {
		return RecordInput{}, errors.New("title is required")
	}
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = "active"
	}
	if !statuses[input.Status] {
		return RecordInput{}, fmt.Errorf("status must be one of: %s", strings.Join(Statuses(), ", "))
	}
	input.Repo = strings.TrimSpace(input.Repo)
	input.Branch = strings.TrimSpace(input.Branch)
	input.Worktree = strings.TrimSpace(input.Worktree)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Decision = strings.TrimSpace(input.Decision)
	input.Evidence = strings.TrimSpace(input.Evidence)
	input.OpenQuestions = strings.TrimSpace(input.OpenQuestions)
	input.NextAction = strings.TrimSpace(input.NextAction)
	input.Source = strings.TrimSpace(input.Source)
	input.Owners = cleanStrings(input.Owners)
	input.Tags = cleanStrings(input.Tags)
	seenLinks := map[string]bool{}
	links := make([]ExternalLink, 0, len(input.Links))
	for _, link := range input.Links {
		linkType, err := NormalizeType(link.Type, "link type")
		if err != nil {
			return RecordInput{}, err
		}
		link.Type = linkType
		link.Target = strings.TrimSpace(link.Target)
		link.Label = strings.TrimSpace(link.Label)
		link.CreatedAt = strings.TrimSpace(link.CreatedAt)
		if link.Target == "" {
			return RecordInput{}, errors.New("link target is required")
		}
		key := link.Type + "\x00" + strings.ToLower(link.Target)
		if !seenLinks[key] {
			seenLinks[key] = true
			links = append(links, link)
		}
	}
	input.Links = links
	return input, nil
}

func cleanStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result
}

func recordFromInput(input RecordInput, id, createdAt, updatedAt string) Record {
	return Record{
		ID: id, Title: input.Title, Status: input.Status, Repo: input.Repo,
		Branch: input.Branch, Worktree: input.Worktree, Summary: input.Summary,
		Decision: input.Decision, Evidence: input.Evidence, OpenQuestions: input.OpenQuestions,
		NextAction: input.NextAction, Source: input.Source, CreatedAt: createdAt,
		UpdatedAt: updatedAt, Owners: input.Owners, Tags: input.Tags, Links: input.Links,
		Relationships: []Relationship{},
	}
}

func normalizeImportedRecord(record Record) (Record, error) {
	if strings.TrimSpace(record.ID) == "" {
		return Record{}, errors.New("imported record id is required")
	}
	if strings.TrimSpace(record.CreatedAt) == "" || strings.TrimSpace(record.UpdatedAt) == "" {
		return Record{}, errors.New("imported record timestamps are required")
	}
	input, err := NormalizeInput(RecordInput{
		Title: record.Title, Status: record.Status, Repo: record.Repo, Branch: record.Branch,
		Worktree: record.Worktree, Summary: record.Summary, Decision: record.Decision,
		Evidence: record.Evidence, OpenQuestions: record.OpenQuestions, NextAction: record.NextAction,
		Owners: record.Owners, Tags: record.Tags, Links: record.Links, Source: record.Source,
	})
	if err != nil {
		return Record{}, err
	}
	return recordFromInput(input, strings.TrimSpace(record.ID), strings.TrimSpace(record.CreatedAt), strings.TrimSpace(record.UpdatedAt)), nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate record id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func utcNow() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func nextTimestamp(previous string) string {
	now := time.Now().UTC().Truncate(time.Second)
	if parsed, err := time.Parse(time.RFC3339, previous); err == nil && !now.After(parsed) {
		now = parsed.Add(time.Second)
	}
	return now.Format(time.RFC3339)
}
