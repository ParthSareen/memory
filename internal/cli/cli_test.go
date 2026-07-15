package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	workmemory "github.com/ParthSareen/memory/internal/memory"
)

func initializedDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := workmemory.Initialize(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	return path
}

func TestJSONStdinIngressAndMachineOutput(t *testing.T) {
	dbPath := initializedDB(t)
	payload := `{
  "title": "JSON-created outcome",
  "repo": "memory",
  "summary": "Created through stdin.",
  "tags": ["agent"],
  "links": [{"type": "thread", "target": "task-123", "label": "", "created_at": ""}]
}`
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run([]string{"--db", dbPath, "record", "--input", "-", "--json"}, strings.NewReader(payload), stdout, stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var record workmemory.Record
	if err := json.Unmarshal(stdout.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Title != "JSON-created outcome" || record.Links[0].Type != "thread" {
		t.Fatalf("record = %#v", record)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--db", dbPath, "context", "JSON-created", "--repo", "memory", "--json"}, strings.NewReader(""), stdout, stderr)
	if code != 0 {
		t.Fatalf("interspersed context flags failed: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), `"record"`) || !strings.Contains(stdout.String(), `"brief_bytes"`) {
		t.Fatalf("context JSON was not compact: %s", stdout.String())
	}
	compactSize := stdout.Len()

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--db", dbPath, "context", "JSON-created", "--repo", "memory", "--json-full"}, strings.NewReader(""), stdout, stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"record"`) || stdout.Len() <= compactSize {
		t.Fatalf("full context JSON was not diagnostic: code=%d output=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--db", dbPath, "update", record.ID, "--status", "completed", "--decision", "Final decision.", "--json"}, strings.NewReader(""), stdout, stderr)
	if code != 0 {
		t.Fatalf("update failed: %s", stderr.String())
	}
	var updated workmemory.Record
	if err := json.Unmarshal(stdout.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != record.ID || updated.Status != "completed" || updated.Decision != "Final decision." || updated.CreatedAt != record.CreatedAt || updated.UpdatedAt <= record.UpdatedAt {
		t.Fatalf("bad CLI update: before=%#v after=%#v", record, updated)
	}
}

func TestCLIFailureIsConcise(t *testing.T) {
	dbPath := initializedDB(t)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := Run([]string{"--db", dbPath, "record", "--input", "-"}, strings.NewReader("not-json"), stdout, stderr)
	if code != 2 || !strings.Contains(stderr.String(), "invalid JSON") || strings.Contains(stderr.String(), "goroutine") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}

	missing := filepath.Join(t.TempDir(), "missing.db")
	stderr.Reset()
	code = Run([]string{"--db", missing, "list"}, strings.NewReader(""), stdout, stderr)
	if code != 2 || !strings.Contains(stderr.String(), "database not initialized") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestScopedTopicContextDoesNotLeakGlobalFTSMatches(t *testing.T) {
	dbPath := initializedDB(t)
	record := func(arguments ...string) workmemory.Record {
		t.Helper()
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		args := append([]string{"--db", dbPath, "record"}, arguments...)
		args = append(args, "--json")
		if code := Run(args, strings.NewReader(""), stdout, stderr); code != 0 {
			t.Fatalf("record failed: %s", stderr.String())
		}
		var created workmemory.Record
		if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created
	}
	inScope := record(
		"--title", "Scoped outcome", "--repo", "ollama", "--branch", "skills",
		"--summary", "scope-target", "--tag", "agent", "--tag", "skills",
		"--link", "issue=I1", "--link", "thread=T1",
	)
	record(
		"--title", "Global FTS hit", "--repo", "other", "--branch", "wrong",
		"--summary", "scope-target", "--tag", "other", "--link", "issue=wrong",
	)

	query := []string{
		"--db", dbPath, "context", "scope-target", "--repo", "ollama", "--branch", "skills",
		"--tag", "agent", "--tag", "skills", "--link", "issue=I1", "--link", "thread=T1",
		"--json-full",
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run(query, strings.NewReader(""), stdout, stderr); code != 0 {
		t.Fatalf("context failed: %s", stderr.String())
	}
	var scoped workmemory.ContextResult
	if err := json.Unmarshal(stdout.Bytes(), &scoped); err != nil {
		t.Fatal(err)
	}
	if scoped.CandidateCount != 1 || len(scoped.Included) != 1 || scoped.Included[0].Record.ID != inScope.ID {
		t.Fatalf("scoped result leaked records: %#v", scoped)
	}
	for _, candidate := range scoped.Included {
		if candidate.Record.Repo != "ollama" || candidate.Record.Branch != "skills" ||
			!containsString(candidate.Record.Tags, "agent") || !containsString(candidate.Record.Tags, "skills") ||
			!containsLink(candidate.Record.Links, "issue", "I1") || !containsLink(candidate.Record.Links, "thread", "T1") {
			t.Fatalf("included record violates hard scope: %#v", candidate.Record)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(
		[]string{"--db", dbPath, "update", inScope.ID, "--clear-tags", "--clear-links"},
		strings.NewReader(""), stdout, stderr,
	); code != 0 {
		t.Fatalf("update failed: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(query, strings.NewReader(""), stdout, stderr); code != 0 {
		t.Fatalf("post-update context failed: %s", stderr.String())
	}
	var empty workmemory.ContextResult
	if err := json.Unmarshal(stdout.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.CandidateCount != 0 || len(empty.Included) != 0 || strings.Contains(empty.Brief, "Global FTS hit") {
		t.Fatalf("global FTS leaked through empty scope: %#v", empty)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsLink(links []workmemory.ExternalLink, linkType, target string) bool {
	for _, link := range links {
		if link.Type == linkType && link.Target == target {
			return true
		}
	}
	return false
}
