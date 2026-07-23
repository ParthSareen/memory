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

func TestWorkflowCommandsCompactJSONAndQueue(t *testing.T) {
	dbPath := initializedDB(t)
	run := func(arguments ...string) compactWorkflowItem {
		t.Helper()
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		args := append([]string{"--db", dbPath}, arguments...)
		args = append(args, "--json")
		if code := Run(args, strings.NewReader(""), stdout, stderr); code != 0 {
			t.Fatalf("%v failed: %s", arguments, stderr.String())
		}
		var item compactWorkflowItem
		if err := json.Unmarshal(stdout.Bytes(), &item); err != nil {
			t.Fatalf("decode %v: %v\n%s", arguments, err, stdout.String())
		}
		return item
	}
	agent := run("begin", "--title", "Agent follow-up", "--class", "build", "--area", "memory", "--repo", "memory", "--owner", "agent", "--next-action", "Implement tests")
	parth := run("begin", "--title", "Parth decision", "--class", "review", "--area", "memory", "--repo", "memory", "--owner", "parth", "--next-action", "Choose contract")
	if agent.Status != workmemory.WorkflowActive || parth.NeedsNextAction != true || parth.Class != "review" {
		t.Fatalf("begin output = %#v %#v", agent, parth)
	}
	if item := run("checkpoint", agent.ID, "--status", "blocked", "--evidence", "Test fixture shows the blocked dependency", "--next-action", "Wait for fixture"); item.Status != workmemory.WorkflowBlocked {
		t.Fatalf("checkpoint item = %#v", item)
	}
	if item := run("wait", parth.ID, "--next-action", "Wait for Parth"); item.Status != workmemory.WorkflowWaiting {
		t.Fatalf("wait item = %#v", item)
	}
	if item := run("handoff", agent.ID, "--evidence", "Focused workflow test passes", "--summary", "Blocked work is ready for another tool", "--next-action", "Resume after fixture"); item.Status != workmemory.WorkflowWaiting || item.NextAction != "Resume after fixture" {
		t.Fatalf("handoff item = %#v", item)
	}
	if item := run("close", parth.ID, "--summary", "Decision made"); item.Status != workmemory.WorkflowClosed || item.NeedsNextAction || item.NextAction != "" {
		t.Fatalf("close item = %#v", item)
	}
	if item := run("park", agent.ID, "--summary", "Deferred intentionally"); item.Status != workmemory.WorkflowParked || item.NeedsNextAction {
		t.Fatalf("park item = %#v", item)
	}

	queue := run("begin", "--title", "Needs Parth", "--class", "build", "--area", "memory", "--repo", "memory", "--owner", "parth", "--next-action", "Approve")
	other := run("begin", "--title", "Needs agent", "--class", "build", "--area", "other", "--repo", "other", "--owner", "agent", "--next-action", "Run")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run([]string{"--db", dbPath, "now", "--repo", "memory", "--area", "memory", "--json"}, strings.NewReader(""), stdout, stderr); code != 0 {
		t.Fatalf("now failed: %s", stderr.String())
	}
	var now compactNowJSON
	if err := json.Unmarshal(stdout.Bytes(), &now); err != nil {
		t.Fatal(err)
	}
	if now.Count != 1 || now.Items[0].ID != queue.ID || now.Items[0].Owners[0] != "parth" {
		t.Fatalf("filtered queue = %#v", now)
	}
	if strings.Contains(stdout.String(), "summary") || strings.Contains(stdout.String(), "evidence") || other.ID == "" {
		t.Fatalf("now JSON is not compact: %s", stdout.String())
	}
	priorityAgent := run("begin", "--title", "Agent priority", "--class", "build", "--area", "priority", "--repo", "memory", "--owner", "agent", "--next-action", "Handle first")
	run("begin", "--title", "Other priority", "--class", "build", "--area", "priority", "--repo", "memory", "--owner", "other", "--next-action", "Handle later")
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--db", dbPath, "now", "--area", "priority", "--priority-owner", "agent", "--json"}, strings.NewReader(""), stdout, stderr); code != 0 {
		t.Fatalf("priority now failed: %s", stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &now); err != nil {
		t.Fatal(err)
	}
	if now.Count != 2 || now.Items[0].ID != priorityAgent.ID {
		t.Fatalf("priority queue = %#v", now)
	}
}

func TestWorkflowFailedCheckpointIsAtomic(t *testing.T) {
	dbPath := initializedDB(t)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run([]string{"--db", dbPath, "begin", "--title", "Atomic checkpoint", "--class", "build", "--summary", "before", "--json"}, strings.NewReader(""), stdout, stderr); code != 0 {
		t.Fatalf("begin failed: %s", stderr.String())
	}
	var begun compactWorkflowItem
	if err := json.Unmarshal(stdout.Bytes(), &begun); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--db", dbPath, "checkpoint", begun.ID, "--status", "invalid", "--summary", "after failed command", "--json"}, strings.NewReader(""), stdout, stderr); code != 2 {
		t.Fatalf("checkpoint code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--db", dbPath, "show", begun.ID, "--json"}, strings.NewReader(""), stdout, stderr); code != 0 {
		t.Fatalf("show failed: %s", stderr.String())
	}
	var record workmemory.Record
	if err := json.Unmarshal(stdout.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.Summary != "before" || record.Status != "active" {
		t.Fatalf("failed lifecycle command mutated record: %#v", record)
	}
}

func TestWorkflowToolRunLinkResumesWithoutThread(t *testing.T) {
	dbPath := initializedDB(t)
	runBegin := func(title string, extra ...string) compactWorkflowItem {
		t.Helper()
		stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
		args := []string{"--db", dbPath, "begin", "--title", title, "--class", "build", "--tool-ref", "terminal:run-42", "--source", "terminal"}
		args = append(args, extra...)
		args = append(args, "--json")
		if code := Run(args, strings.NewReader(""), stdout, stderr); code != 0 {
			t.Fatalf("begin failed: %s", stderr.String())
		}
		var item compactWorkflowItem
		if err := json.Unmarshal(stdout.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	first := runBegin("Run without a thread", "--link", "issue=memory#42")
	second := runBegin("Resumed external run")
	if first.ID != second.ID || second.Title != "Resumed external run" {
		t.Fatalf("tool run did not resume: first=%#v second=%#v", first, second)
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := Run([]string{"--db", dbPath, "now", "--json"}, strings.NewReader(""), stdout, stderr); code != 0 {
		t.Fatalf("now failed: %s", stderr.String())
	}
	var now compactNowJSON
	if err := json.Unmarshal(stdout.Bytes(), &now); err != nil {
		t.Fatal(err)
	}
	if now.Count != 1 || len(now.Items[0].Links) != 2 || !containsWorkflowLink(now.Items[0].Links, "tool-run", "terminal:run-42") || !containsWorkflowLink(now.Items[0].Links, "issue", "memory#42") {
		t.Fatalf("external link = %#v", now)
	}
}

func containsWorkflowLink(links []workmemory.ExternalLink, linkType, target string) bool {
	for _, link := range links {
		if link.Type == linkType && link.Target == target {
			return true
		}
	}
	return false
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
