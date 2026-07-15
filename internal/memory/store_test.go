package memory

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func stringValue(value string) *string {
	return &value
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Initialize(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func createRecord(t *testing.T, store *Store, input RecordInput) Record {
	t.Helper()
	record, err := store.CreateRecord(input)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestSchemaStorageAndFTS(t *testing.T) {
	store := newTestStore(t)
	record := createRecord(t, store, RecordInput{
		Title: "Ship deterministic context", Status: "active",
		Repo: "ParthSareen/memory", Branch: "main", Worktree: "/tmp/memory",
		Summary:  "Implemented bounded retrieval with SQLite FTS5.",
		Decision: "Use exact metadata before full text.", Evidence: "Focused tests pass.",
		OpenQuestions: "Choose the next adapter.", NextAction: "Run the demo.",
		Owners: []string{"Parth", "Agent"}, Tags: []string{"CLI", "retrieval"},
		Links: []ExternalLink{{Type: "issue", Target: "memory#1"}}, Source: "codex",
	})
	loaded, err := store.GetRecord(record.ID[:8])
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Repo != "ParthSareen/memory" || !reflect.DeepEqual(loaded.Owners, []string{"Agent", "Parth"}) {
		t.Fatalf("unexpected loaded record: %#v", loaded)
	}
	if !reflect.DeepEqual(loaded.Tags, []string{"CLI", "retrieval"}) || loaded.Links[0].Target != "memory#1" {
		t.Fatalf("collections were not preserved: %#v", loaded)
	}
	listed, err := store.ListRecords(ListOptions{Repo: "parthsareen/MEMORY", Limit: 10})
	if err != nil || len(listed) != 1 || listed[0].ID != record.ID {
		t.Fatalf("list records = %#v, %v", listed, err)
	}
	version, err := store.SchemaVersion()
	if err != nil || version != SchemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	matches, err := store.FTSSearch(`"bounded"`, 10)
	if err != nil || len(matches) != 1 || matches[0].ID != record.ID {
		t.Fatalf("FTS match = %#v, %v", matches, err)
	}
}

func TestWeightedFTSRanksTitleAboveEvidence(t *testing.T) {
	store := newTestStore(t)
	titleMatch := createRecord(t, store, RecordInput{Title: "Quokka migration", Summary: "Routine implementation."})
	createRecord(t, store, RecordInput{Title: "Migration notes", Evidence: "The quokka benchmark passed."})
	matches, err := store.FTSSearch(`"quokka"`, 10)
	if err != nil {
		t.Fatal(err)
	}
	if matches[0].ID != titleMatch.ID {
		t.Fatalf("title match did not rank first: %#v", matches)
	}
}

func TestRelationshipAndExactLinkExcludeOutOfScopeFTS(t *testing.T) {
	store := newTestStore(t)
	exact := createRecord(t, store, RecordInput{
		Title: "Issue outcome", Status: "completed",
		Links: []ExternalLink{{Type: "issue", Target: "memory#42"}},
	})
	related := createRecord(t, store, RecordInput{Title: "Follow-up design", Status: "active", NextAction: "Prototype it."})
	createRecord(t, store, RecordInput{Title: "Frobnicate cache", Status: "active", Summary: "Frobnicate behavior."})
	if _, err := store.AddRelationship(exact.ID, "leads_to", related.ID); err != nil {
		t.Fatal(err)
	}
	result, err := RetrieveContext(store, ContextQuery{
		Topic: "frobnicate", Links: []TypedTarget{{Type: "issue", Target: "memory#42"}},
		MaxTokens: 500, MaxBytes: 3000,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{result.Candidates[0].Record.ID, result.Candidates[1].Record.ID}
	want := []string{exact.ID, related.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked ids = %v, want %v", got, want)
	}
	groups := []string{result.Candidates[0].Group, result.Candidates[1].Group}
	if !reflect.DeepEqual(groups, []string{"exact", "related"}) {
		t.Fatalf("groups = %v", groups)
	}
	if !strings.Contains(strings.Join(result.Candidates[0].Reasons, ";"), "issue link matches memory#42") {
		t.Fatalf("missing inclusion reason: %v", result.Candidates[0].Reasons)
	}
}

func TestCombinedExactScopeIsConjunctive(t *testing.T) {
	t.Run("repo and branch", func(t *testing.T) {
		store := newTestStore(t)
		match := createRecord(t, store, RecordInput{Title: "match", Repo: "ollama", Branch: "target"})
		createRecord(t, store, RecordInput{Title: "wrong branch", Repo: "ollama", Branch: "other"})
		createRecord(t, store, RecordInput{Title: "wrong repo", Repo: "other", Branch: "target"})
		result, err := RetrieveContext(store, ContextQuery{Repo: "ollama", Branch: "target"})
		if err != nil {
			t.Fatal(err)
		}
		assertCandidateIDs(t, result, match.ID)
	})

	t.Run("repo and link", func(t *testing.T) {
		store := newTestStore(t)
		match := createRecord(t, store, RecordInput{
			Title: "match", Repo: "ollama", Links: []ExternalLink{{Type: "issue", Target: "#42"}},
		})
		createRecord(t, store, RecordInput{Title: "repo only", Repo: "ollama"})
		createRecord(t, store, RecordInput{
			Title: "link only", Repo: "other", Links: []ExternalLink{{Type: "issue", Target: "#42"}},
		})
		result, err := RetrieveContext(store, ContextQuery{
			Repo: "ollama", Links: []TypedTarget{{Type: "issue", Target: "#42"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertCandidateIDs(t, result, match.ID)
	})

	t.Run("multiple tags and links", func(t *testing.T) {
		store := newTestStore(t)
		allLinks := []ExternalLink{{Type: "issue", Target: "#42"}, {Type: "thread", Target: "task-7"}}
		match := createRecord(t, store, RecordInput{
			Title: "match", Tags: []string{"release", "retrieval"}, Links: allLinks,
		})
		createRecord(t, store, RecordInput{
			Title: "partial tags", Tags: []string{"release"}, Links: allLinks,
		})
		createRecord(t, store, RecordInput{
			Title: "partial links", Tags: []string{"release", "retrieval"},
			Links: []ExternalLink{{Type: "issue", Target: "#42"}},
		})
		result, err := RetrieveContext(store, ContextQuery{
			Tags:  []string{"release", "retrieval"},
			Links: []TypedTarget{{Type: "issue", Target: "#42"}, {Type: "thread", Target: "task-7"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertCandidateIDs(t, result, match.ID)
	})
}

func TestHardScopeConstrainsFTSCandidates(t *testing.T) {
	store := newTestStore(t)
	inScope := createRecord(t, store, RecordInput{
		Title: "Scoped outcome", Repo: "ollama", Branch: "target",
		Summary: "This record discusses quokka retrieval.",
	})
	createRecord(t, store, RecordInput{
		Title: "Global text hit", Repo: "other", Branch: "other",
		Summary: "This record also discusses quokka retrieval.",
	})
	result, err := RetrieveContext(store, ContextQuery{
		Topic: "quokka", Repo: "ollama", Branch: "target",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCandidateIDs(t, result, inScope.ID)
	if !strings.Contains(strings.Join(result.Candidates[0].Reasons, ";"), `text matches "quokka"`) {
		t.Fatalf("scoped FTS reason missing: %v", result.Candidates[0].Reasons)
	}

	empty, err := RetrieveContext(store, ContextQuery{Topic: "quokka", Repo: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if empty.CandidateCount != 0 || strings.Contains(empty.Brief, "Global text hit") {
		t.Fatalf("FTS bypassed an empty hard scope: %#v", empty)
	}
}

func assertCandidateIDs(t *testing.T, result ContextResult, ids ...string) {
	t.Helper()
	got := make([]string, len(result.Candidates))
	for index, candidate := range result.Candidates {
		got[index] = candidate.Record.ID
	}
	if !reflect.DeepEqual(got, ids) {
		t.Fatalf("candidate ids = %v, want %v", got, ids)
	}
}

func TestContextHonorsByteAndTokenLimits(t *testing.T) {
	store := newTestStore(t)
	for index := 0; index < 5; index++ {
		createRecord(t, store, RecordInput{
			Title: "Bounded record", Repo: "memory",
			Summary:  strings.Repeat("Durable outcome 🚀 ", 40),
			Decision: "Keep retrieval deterministic.", NextAction: "Continue narrowly.",
		})
	}
	result, err := RetrieveContext(store, ContextQuery{
		Repo: "memory", MaxItems: 5, MaxTokens: 90, MaxBytes: 360,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len([]byte(result.Brief)) > 360 || EstimateTokens(result.Brief) > 90 {
		t.Fatalf("brief exceeded limits: bytes=%d tokens=%d\n%s", len([]byte(result.Brief)), EstimateTokens(result.Brief), result.Brief)
	}
	if !result.Truncated || !strings.Contains(result.Brief, "Why:") || !utf8.ValidString(result.Brief) {
		t.Fatalf("bad bounded brief: %#v", result)
	}
	tight, err := RetrieveContext(store, ContextQuery{
		Repo: "memory", MaxItems: 5, MaxTokens: 30, MaxBytes: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tight.Brief, "[truncated") {
		t.Fatalf("tight brief has no visible marker: %q", tight.Brief)
	}
	if len([]byte(tight.Brief)) > 120 || EstimateTokens(tight.Brief) > 30 {
		t.Fatalf("tight brief exceeded limits: %q", tight.Brief)
	}
	if _, err := RetrieveContext(store, ContextQuery{Repo: "memory", MaxTokens: 1, MaxBytes: 5}); err == nil {
		t.Fatal("too-small budget succeeded")
	}
}

func TestUpdatePreservesIdentityRefreshesFTSAndTouchesRecency(t *testing.T) {
	store := newTestStore(t)
	record := createRecord(t, store, RecordInput{
		Title: "Lifecycle outcome", Status: "active", Decision: "Use the zephyr plan.",
		NextAction: "Keep working.", Tags: []string{"old"},
		Links: []ExternalLink{{Type: "issue", Target: "#1"}},
	})
	newTags := []string{"done", "release"}
	newLinks := []ExternalLink{{Type: "pr", Target: "#9"}}
	updated, err := store.UpdateRecord(record.ID, UpdateInput{
		Status: stringValue("completed"), Decision: stringValue("Adopt the quokka plan."),
		NextAction: stringValue(""), Tags: &newTags, Links: &newLinks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != record.ID || updated.CreatedAt != record.CreatedAt || updated.UpdatedAt <= record.UpdatedAt {
		t.Fatalf("identity/timestamps changed incorrectly: before=%#v after=%#v", record, updated)
	}
	if updated.Status != "completed" || updated.Decision != "Adopt the quokka plan." || updated.NextAction != "" {
		t.Fatalf("scalar update failed: %#v", updated)
	}
	if !reflect.DeepEqual(updated.Tags, newTags) || len(updated.Links) != 1 || updated.Links[0].Type != "pr" {
		t.Fatalf("collection update failed: %#v", updated)
	}
	oldMatches, err := store.FTSSearch(`"zephyr"`, 10)
	if err != nil || len(oldMatches) != 0 {
		t.Fatalf("stale FTS content remains: %#v, %v", oldMatches, err)
	}
	newMatches, err := store.FTSSearch(`"quokka"`, 10)
	if err != nil || len(newMatches) != 1 || newMatches[0].ID != record.ID {
		t.Fatalf("updated FTS content missing: %#v, %v", newMatches, err)
	}
	if _, err := store.UpdateRecord(record.ID, UpdateInput{Title: stringValue("")}); err == nil {
		t.Fatal("invalid empty title update succeeded")
	}
	if _, err := store.UpdateRecord(record.ID, UpdateInput{}); err == nil {
		t.Fatal("empty update succeeded")
	}

	linked, err := store.AddExternalLink(record.ID, "thread", "task-9", "")
	if err != nil {
		t.Fatal(err)
	}
	if linked.UpdatedAt <= updated.UpdatedAt {
		t.Fatalf("external link did not refresh recency: %s <= %s", linked.UpdatedAt, updated.UpdatedAt)
	}
	target := createRecord(t, store, RecordInput{Title: "Relationship target"})
	targetBefore := target.UpdatedAt
	related, err := store.AddRelationship(record.ID, "leads_to", target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if related.UpdatedAt <= linked.UpdatedAt {
		t.Fatalf("relationship did not refresh source recency: %s <= %s", related.UpdatedAt, linked.UpdatedAt)
	}
	targetAfter, err := store.GetRecord(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if targetAfter.UpdatedAt != targetBefore {
		t.Fatalf("relationship unexpectedly touched target: %s != %s", targetAfter.UpdatedAt, targetBefore)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	store := newTestStore(t)
	first := createRecord(t, store, RecordInput{
		Title: "First outcome", Status: "completed", Tags: []string{"export"},
		Links: []ExternalLink{{Type: "pr", Target: "https://example.test/pr/1"}},
	})
	second := createRecord(t, store, RecordInput{Title: "Second outcome", Status: "active", Owners: []string{"agent"}})
	if _, err := store.AddRelationship(first.ID, "follows", second.ID); err != nil {
		t.Fatal(err)
	}
	exported, err := store.ExportData()
	if err != nil {
		t.Fatal(err)
	}
	target, err := Initialize(filepath.Join(t.TempDir(), "imported.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	result, err := target.ImportData(exported, "error")
	if err != nil {
		t.Fatal(err)
	}
	if result != (ImportResult{Imported: 2, Relationships: 1}) {
		t.Fatalf("import result = %#v", result)
	}
	imported, err := target.ExportData()
	if err != nil {
		t.Fatal(err)
	}
	exported.ExportedAt = ""
	imported.ExportedAt = ""
	if !reflect.DeepEqual(imported, exported) {
		t.Fatalf("round trip mismatch\nimported=%#v\nexported=%#v", imported, exported)
	}
}

func TestValidationAndConflictFailures(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateRecord(RecordInput{Summary: "No title."}); err == nil {
		t.Fatal("missing title succeeded")
	}
	if _, err := store.CreateRecord(RecordInput{Title: "Bad status", Status: "unknown"}); err == nil {
		t.Fatal("bad status succeeded")
	}
	record := createRecord(t, store, RecordInput{Title: "Unique link"})
	if _, err := store.AddExternalLink(record.ID, "issue", "memory#9", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddExternalLink(record.ID, "issue", "memory#9", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate link error = %v", err)
	}
}
