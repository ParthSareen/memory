package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	workmemory "github.com/ParthSareen/memory/internal/memory"
)

const Version = "0.1.0"

type compactContextJSON struct {
	Brief                string   `json:"brief"`
	Truncated            bool     `json:"truncated"`
	CandidateCount       int      `json:"candidate_count"`
	IncludedIDs          []string `json:"included_ids"`
	BriefBytes           int      `json:"brief_bytes"`
	BriefEstimatedTokens int      `json:"brief_estimated_tokens"`
}

type compactWorkflowItem struct {
	ID              string                    `json:"id"`
	Title           string                    `json:"title"`
	Status          workmemory.WorkflowStatus `json:"status"`
	Class           string                    `json:"class"`
	Area            string                    `json:"area"`
	NeedsNextAction bool                      `json:"needs_next_action"`
	Owners          []string                  `json:"owners"`
	Repo            string                    `json:"repo"`
	Branch          string                    `json:"branch"`
	Worktree        string                    `json:"worktree"`
	NextAction      string                    `json:"next_action"`
	Links           []workmemory.ExternalLink `json:"links"`
	Source          string                    `json:"source"`
	UpdatedAt       string                    `json:"updated_at"`
}

type compactNowJSON struct {
	Items []compactWorkflowItem `json:"items"`
	Count int                   `json:"count"`
}

type stringList []string

func (values *stringList) String() string {
	return strings.Join(*values, ",")
}

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := run(args, stdin, stdout, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "memory: %v\n", err)
		return 2
	}
	return 0
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	dbPath := workmemory.DefaultDBPath()
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		argument := args[0]
		switch {
		case argument == "--help" || argument == "-h":
			printGlobalUsage(stdout)
			return nil
		case argument == "--version":
			fmt.Fprintf(stdout, "memory %s\n", Version)
			return nil
		case argument == "--db":
			if len(args) < 2 {
				return errors.New("--db requires a path")
			}
			dbPath = args[1]
			args = args[2:]
			continue
		case strings.HasPrefix(argument, "--db="):
			dbPath = strings.TrimPrefix(argument, "--db=")
		default:
			return fmt.Errorf("unknown global option: %s", argument)
		}
		args = args[1:]
	}
	if len(args) == 0 {
		printGlobalUsage(stderr)
		return errors.New("a command is required")
	}
	command := args[0]
	commandArgs := args[1:]
	if command == "help" {
		if len(commandArgs) == 0 {
			printGlobalUsage(stdout)
			return nil
		}
		command = commandArgs[0]
		commandArgs = []string{"--help"}
	}
	switch command {
	case "init":
		return commandInit(dbPath, commandArgs, stdout, stderr)
	case "record":
		return commandRecord(dbPath, commandArgs, stdin, stdout, stderr)
	case "update":
		return commandUpdate(dbPath, commandArgs, stdin, stdout, stderr)
	case "list":
		return commandList(dbPath, commandArgs, stdout, stderr)
	case "show":
		return commandShow(dbPath, commandArgs, stdout, stderr)
	case "context":
		return commandContext(dbPath, commandArgs, stdout, stderr)
	case "link":
		return commandLink(dbPath, commandArgs, stdout, stderr)
	case "export":
		return commandExport(dbPath, commandArgs, stdout, stderr)
	case "import":
		return commandImport(dbPath, commandArgs, stdin, stdout, stderr)
	case "begin":
		return commandBegin(dbPath, commandArgs, stdout, stderr)
	case "now":
		return commandNow(dbPath, commandArgs, stdout, stderr)
	case "checkpoint", "wait", "handoff", "close", "park":
		return commandWorkflowUpdate(dbPath, command, commandArgs, stdout, stderr)
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
}

func printGlobalUsage(output io.Writer) {
	fmt.Fprintln(output, `work-memory: local-first durable work memory

Usage:
  memory [--db PATH] COMMAND [OPTIONS]

Commands:
  init      initialize the local SQLite database
  record    write a durable work outcome
  update    edit an existing outcome without changing its identity
  list      list recent records
  show      show one record by id or unique prefix
  context   retrieve a compact bounded work brief
  link      add a typed external link or relationship
  export    export stable JSON
  import    import stable JSON
  begin     create or resume a durable work item
  now       show the compact active, waiting, and blocked queue
  checkpoint update a work item at a meaningful state change
  wait      mark a work item waiting
  handoff   record concise evidence-backed handoff state
  close     close a work item
  park      park a work item

Global options:
  --db PATH    SQLite path (or use WORK_MEMORY_DB)
  --version    print version
  --help       show this help`)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func parseInterspersed(flags *flag.FlagSet, args []string) ([]string, error) {
	flagArgs := []string{}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			positionals = append(positionals, argument)
			continue
		}
		flagArgs = append(flagArgs, argument)
		name := strings.TrimLeft(argument, "-")
		if before, _, found := strings.Cut(name, "="); found {
			name = before
			continue
		}
		registered := flags.Lookup(name)
		if registered == nil {
			continue
		}
		if boolean, ok := registered.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if index+1 >= len(args) {
			return nil, fmt.Errorf("-%s requires a value", name)
		}
		index++
		flagArgs = append(flagArgs, args[index])
	}
	if err := flags.Parse(flagArgs); err != nil {
		return nil, err
	}
	return positionals, nil
}

func commandInit(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("init", stderr)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return errors.New("init does not accept positional arguments")
	}
	store, err := workmemory.Initialize(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	if *jsonOutput {
		return writeJSON(stdout, map[string]any{"database": store.Path(), "schema_version": workmemory.SchemaVersion}, true)
	}
	fmt.Fprintf(stdout, "Initialized work memory at %s\n", store.Path())
	return nil
}

func commandRecord(dbPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newFlagSet("record", stderr)
	inputPath := flags.String("input", "", "read a JSON object from PATH or -")
	title := flags.String("title", "", "record title")
	status := flags.String("status", "", "record status")
	repo := flags.String("repo", "", "repository")
	branch := flags.String("branch", "", "branch")
	worktree := flags.String("worktree", "", "worktree path")
	summary := flags.String("summary", "", "concise outcome")
	decision := flags.String("decision", "", "durable decision")
	evidence := flags.String("evidence", "", "supporting evidence")
	openQuestions := flags.String("open-questions", "", "unresolved questions")
	nextAction := flags.String("next-action", "", "next move")
	source := flags.String("source", "", "record source")
	var owners, tags, links stringList
	flags.Var(&owners, "owner", "owner (repeatable)")
	flags.Var(&tags, "tag", "tag (repeatable)")
	flags.Var(&links, "link", "TYPE=TARGET (repeatable)")
	jsonOutput := flags.Bool("json", false, "emit created record as JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return errors.New("record does not accept positional arguments")
	}
	input := workmemory.RecordInput{}
	if *inputPath != "" {
		reader, closeReader, err := openInput(*inputPath, stdin)
		if err != nil {
			return err
		}
		defer closeReader()
		if err := decodeJSON(reader, &input); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
	}
	visited := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	if visited["title"] {
		input.Title = *title
	}
	if visited["status"] {
		input.Status = *status
	}
	if visited["repo"] {
		input.Repo = *repo
	}
	if visited["branch"] {
		input.Branch = *branch
	}
	if visited["worktree"] {
		input.Worktree = *worktree
	}
	if visited["summary"] {
		input.Summary = *summary
	}
	if visited["decision"] {
		input.Decision = *decision
	}
	if visited["evidence"] {
		input.Evidence = *evidence
	}
	if visited["open-questions"] {
		input.OpenQuestions = *openQuestions
	}
	if visited["next-action"] {
		input.NextAction = *nextAction
	}
	if visited["source"] {
		input.Source = *source
	}
	if visited["owner"] {
		input.Owners = append(input.Owners, owners...)
	}
	if visited["tag"] {
		input.Tags = append(input.Tags, tags...)
	}
	if visited["link"] {
		for _, value := range links {
			linkType, target, err := parseTypedTarget(value)
			if err != nil {
				return err
			}
			input.Links = append(input.Links, workmemory.ExternalLink{Type: linkType, Target: target})
		}
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	record, err := store.CreateRecord(input)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, record, true)
	}
	fmt.Fprintf(stdout, "Recorded %s  %s (%s)\n", shortID(record.ID), record.Title, record.Status)
	return nil
}

func commandUpdate(dbPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newFlagSet("update", stderr)
	inputPath := flags.String("input", "", "read an update JSON object from PATH or -")
	title := flags.String("title", "", "replace title")
	status := flags.String("status", "", "replace status")
	repo := flags.String("repo", "", "replace repository")
	branch := flags.String("branch", "", "replace branch")
	worktree := flags.String("worktree", "", "replace worktree path")
	summary := flags.String("summary", "", "replace concise outcome")
	decision := flags.String("decision", "", "replace durable decision")
	evidence := flags.String("evidence", "", "replace supporting evidence")
	openQuestions := flags.String("open-questions", "", "replace unresolved questions")
	nextAction := flags.String("next-action", "", "replace next move")
	source := flags.String("source", "", "replace source")
	var owners, tags, links stringList
	flags.Var(&owners, "owner", "replacement owner (repeatable)")
	flags.Var(&tags, "tag", "replacement tag (repeatable)")
	flags.Var(&links, "link", "replacement TYPE=TARGET (repeatable)")
	clearOwners := flags.Bool("clear-owners", false, "remove all owners")
	clearTags := flags.Bool("clear-tags", false, "remove all tags")
	clearLinks := flags.Bool("clear-links", false, "remove all external links")
	jsonOutput := flags.Bool("json", false, "emit updated record as JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return errors.New("usage: memory update RECORD_ID [FIELDS]")
	}
	update := workmemory.UpdateInput{}
	if *inputPath != "" {
		reader, closeReader, err := openInput(*inputPath, stdin)
		if err != nil {
			return err
		}
		defer closeReader()
		if err := decodeJSON(reader, &update); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
	}
	visited := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	if visited["title"] {
		update.Title = stringPointer(*title)
	}
	if visited["status"] {
		update.Status = stringPointer(*status)
	}
	if visited["repo"] {
		update.Repo = stringPointer(*repo)
	}
	if visited["branch"] {
		update.Branch = stringPointer(*branch)
	}
	if visited["worktree"] {
		update.Worktree = stringPointer(*worktree)
	}
	if visited["summary"] {
		update.Summary = stringPointer(*summary)
	}
	if visited["decision"] {
		update.Decision = stringPointer(*decision)
	}
	if visited["evidence"] {
		update.Evidence = stringPointer(*evidence)
	}
	if visited["open-questions"] {
		update.OpenQuestions = stringPointer(*openQuestions)
	}
	if visited["next-action"] {
		update.NextAction = stringPointer(*nextAction)
	}
	if visited["source"] {
		update.Source = stringPointer(*source)
	}
	if visited["owner"] && *clearOwners {
		return errors.New("--owner and --clear-owners cannot be combined")
	}
	if visited["tag"] && *clearTags {
		return errors.New("--tag and --clear-tags cannot be combined")
	}
	if visited["link"] && *clearLinks {
		return errors.New("--link and --clear-links cannot be combined")
	}
	if visited["owner"] {
		values := []string(owners)
		update.Owners = &values
	} else if *clearOwners {
		values := []string{}
		update.Owners = &values
	}
	if visited["tag"] {
		values := []string(tags)
		update.Tags = &values
	} else if *clearTags {
		values := []string{}
		update.Tags = &values
	}
	if visited["link"] {
		values := make([]workmemory.ExternalLink, 0, len(links))
		for _, value := range links {
			linkType, target, err := parseTypedTarget(value)
			if err != nil {
				return err
			}
			values = append(values, workmemory.ExternalLink{Type: linkType, Target: target})
		}
		update.Links = &values
	} else if *clearLinks {
		values := []workmemory.ExternalLink{}
		update.Links = &values
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	record, err := store.UpdateRecord(positionals[0], update)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, record, true)
	}
	fmt.Fprintf(stdout, "Updated %s  %s (%s)\n", shortID(record.ID), record.Title, record.Status)
	return nil
}

func commandBegin(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("begin", stderr)
	title := flags.String("title", "", "work item title")
	status := flags.String("status", "active", "active, waiting, blocked, closed, or parked")
	class := flags.String("class", "", "work class")
	area := flags.String("area", "", "operational area")
	needsNextAction := flags.Bool("needs-next-action", true, "whether a next action is needed")
	repo := flags.String("repo", "", "repository")
	branch := flags.String("branch", "", "branch")
	worktree := flags.String("worktree", "", "worktree path")
	summary := flags.String("summary", "", "concise state")
	decision := flags.String("decision", "", "durable decision")
	evidence := flags.String("evidence", "", "supporting evidence")
	openQuestions := flags.String("open-questions", "", "unresolved questions")
	nextAction := flags.String("next-action", "", "next move")
	source := flags.String("source", "", "source or tool")
	toolRef := flags.String("tool-ref", "", "external non-threaded tool-run reference")
	var owners, links stringList
	flags.Var(&owners, "owner", "owner (repeatable)")
	flags.Var(&links, "link", "TYPE=TARGET (repeatable)")
	jsonOutput := flags.Bool("json", false, "emit compact JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return errors.New("begin does not accept positional arguments")
	}
	if strings.TrimSpace(*title) == "" {
		return errors.New("begin requires --title")
	}
	input := workmemory.RecordInput{Title: *title, Repo: *repo, Branch: *branch, Worktree: *worktree, Summary: *summary, Decision: *decision, Evidence: *evidence, OpenQuestions: *openQuestions, NextAction: *nextAction, Owners: owners, Source: *source}
	for _, value := range links {
		linkType, target, err := parseTypedTarget(value)
		if err != nil {
			return err
		}
		input.Links = append(input.Links, workmemory.ExternalLink{Type: linkType, Target: target})
	}
	if strings.TrimSpace(*toolRef) != "" {
		input.Links = append(input.Links, workmemory.ExternalLink{Type: "tool-run", Target: *toolRef})
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	item, created, err := store.BeginWorkflow(input, workmemory.WorkflowInput{Status: workmemory.WorkflowStatus(*status), Class: *class, Area: *area, NeedsNextAction: *needsNextAction})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, compactWorkflow(item), true)
	}
	verb := "Resumed"
	if created {
		verb = "Began"
	}
	fmt.Fprintf(stdout, "%s %s  %s (%s)\n", verb, shortID(item.Record.ID), item.Record.Title, item.Status)
	return nil
}

func commandNow(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("now", stderr)
	repo := flags.String("repo", "", "filter repository")
	owner := flags.String("owner", "", "filter owner")
	area := flags.String("area", "", "filter area")
	priorityOwner := flags.String("priority-owner", "", "owner whose needed actions sort first")
	limit := flags.Int("limit", 25, "maximum items")
	jsonOutput := flags.Bool("json", false, "emit stable compact JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return errors.New("now does not accept positional arguments")
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	items, err := store.Now(workmemory.NowOptions{Repo: *repo, Owner: *owner, Area: *area, PriorityOwner: *priorityOwner, Limit: *limit})
	if err != nil {
		return err
	}
	compact := make([]compactWorkflowItem, 0, len(items))
	for _, item := range items {
		compact = append(compact, compactWorkflow(item))
	}
	if *jsonOutput {
		return writeJSON(stdout, compactNowJSON{Items: compact, Count: len(compact)}, true)
	}
	if len(compact) == 0 {
		_, err := fmt.Fprintln(stdout, "No active work.")
		return err
	}
	for _, item := range compact {
		marker := ""
		if item.NeedsNextAction {
			marker = " needs action"
		}
		fmt.Fprintf(stdout, "%s  %-8s  %-10s  %s%s\n", shortID(item.ID), item.Status, item.Class, item.Title, marker)
	}
	return nil
}

func commandWorkflowUpdate(dbPath, command string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet(command, stderr)
	status := flags.String("status", "", "active, waiting, blocked, closed, or parked")
	class := flags.String("class", "", "replace work class")
	area := flags.String("area", "", "replace operational area")
	needsNextAction := flags.Bool("needs-next-action", false, "whether a next action is needed")
	summary := flags.String("summary", "", "replace concise state")
	decision := flags.String("decision", "", "replace durable decision")
	evidence := flags.String("evidence", "", "replace supporting evidence")
	openQuestions := flags.String("open-questions", "", "replace unresolved questions")
	nextAction := flags.String("next-action", "", "replace next move")
	jsonOutput := flags.Bool("json", false, "emit compact JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: memory %s RECORD_ID [FIELDS]", command)
	}
	visited := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	if command == "handoff" && strings.TrimSpace(*evidence) == "" {
		return errors.New("handoff requires --evidence")
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	current, err := store.WorkflowItem(positionals[0])
	if err != nil {
		return err
	}
	update := workmemory.UpdateInput{}
	if visited["summary"] {
		update.Summary = stringPointer(*summary)
	}
	if visited["decision"] {
		update.Decision = stringPointer(*decision)
	}
	if visited["evidence"] {
		update.Evidence = stringPointer(*evidence)
	}
	if visited["open-questions"] {
		update.OpenQuestions = stringPointer(*openQuestions)
	}
	if visited["next-action"] {
		update.NextAction = stringPointer(*nextAction)
	}
	workflow := workmemory.WorkflowInput{Status: current.Status, Class: current.Class, Area: current.Area, NeedsNextAction: current.NeedsNextAction}
	if visited["status"] {
		workflow.Status = workmemory.WorkflowStatus(*status)
	}
	if visited["class"] {
		workflow.Class = *class
	}
	if visited["area"] {
		workflow.Area = *area
	}
	if visited["needs-next-action"] {
		workflow.NeedsNextAction = *needsNextAction
	}
	switch command {
	case "wait":
		if !visited["status"] {
			workflow.Status = workmemory.WorkflowWaiting
		}
		if !visited["needs-next-action"] {
			workflow.NeedsNextAction = true
		}
	case "handoff":
		if !visited["status"] {
			workflow.Status = workmemory.WorkflowWaiting
		}
	case "close":
		if !visited["status"] {
			workflow.Status = workmemory.WorkflowClosed
		}
		if !visited["needs-next-action"] {
			workflow.NeedsNextAction = false
		}
		if !visited["next-action"] {
			update.NextAction = stringPointer("")
		}
	case "park":
		if !visited["status"] {
			workflow.Status = workmemory.WorkflowParked
		}
		if !visited["needs-next-action"] {
			workflow.NeedsNextAction = false
		}
	}
	item, err := store.UpdateWorkflowItem(current.Record.ID, update, workflow)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, compactWorkflow(item), true)
	}
	fmt.Fprintf(stdout, "%s %s  %s (%s)\n", strings.Title(command), shortID(item.Record.ID), item.Record.Title, item.Status)
	return nil
}

func compactWorkflow(item workmemory.WorkflowItem) compactWorkflowItem {
	return compactWorkflowItem{ID: item.Record.ID, Title: item.Record.Title, Status: item.Status, Class: item.Class, Area: item.Area, NeedsNextAction: item.NeedsNextAction, Owners: item.Record.Owners, Repo: item.Record.Repo, Branch: item.Record.Branch, Worktree: item.Record.Worktree, NextAction: item.Record.NextAction, Links: item.Record.Links, Source: item.Record.Source, UpdatedAt: item.Record.UpdatedAt}
}

func commandList(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("list", stderr)
	status := flags.String("status", "", "filter status")
	repo := flags.String("repo", "", "filter repository")
	tag := flags.String("tag", "", "filter tag")
	owner := flags.String("owner", "", "filter owner")
	limit := flags.Int("limit", 50, "maximum records")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return errors.New("list does not accept positional arguments")
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	records, err := store.ListRecords(workmemory.ListOptions{Status: *status, Repo: *repo, Tag: *tag, Owner: *owner, Limit: *limit})
	if err != nil {
		return err
	}
	if *jsonOutput {
		if records == nil {
			records = []workmemory.Record{}
		}
		return writeJSON(stdout, records, true)
	}
	if len(records) == 0 {
		fmt.Fprintln(stdout, "No work records.")
		return nil
	}
	for _, record := range records {
		location := record.Repo
		if location != "" && record.Branch != "" {
			location += "@" + record.Branch
		}
		locationText := ""
		if location != "" {
			locationText = "  [" + location + "]"
		}
		fmt.Fprintf(stdout, "%s  %-10s  %s  %s%s\n", shortID(record.ID), record.Status, record.UpdatedAt, record.Title, locationText)
	}
	return nil
}

func commandShow(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("show", stderr)
	jsonOutput := flags.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return errors.New("usage: memory show RECORD_ID")
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	record, err := store.GetRecord(positionals[0])
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, record, true)
	}
	showHuman(stdout, record)
	return nil
}

func commandContext(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("context", stderr)
	repo := flags.String("repo", "", "exact repository")
	branch := flags.String("branch", "", "exact branch")
	worktree := flags.String("worktree", "", "exact worktree")
	var tags, links, records stringList
	flags.Var(&tags, "tag", "exact tag (repeatable)")
	flags.Var(&links, "link", "TYPE=TARGET (repeatable)")
	flags.Var(&records, "record", "record id or prefix (repeatable)")
	maxItems := flags.Int("max-items", 12, "maximum records")
	maxTokens := flags.Int("max-tokens", 900, "maximum estimated tokens")
	maxBytes := flags.Int("max-bytes", 6000, "maximum UTF-8 bytes")
	jsonOutput := flags.Bool("json", false, "emit a compact JSON brief envelope")
	jsonFull := flags.Bool("json-full", false, "emit unbounded diagnostic JSON with full records")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return errors.New("context accepts at most one topic")
	}
	if *jsonOutput && *jsonFull {
		return errors.New("--json and --json-full cannot be combined")
	}
	topic := ""
	if len(positionals) == 1 {
		topic = positionals[0]
	}
	typedLinks := []workmemory.TypedTarget{}
	for _, value := range links {
		linkType, target, err := parseTypedTarget(value)
		if err != nil {
			return err
		}
		typedLinks = append(typedLinks, workmemory.TypedTarget{Type: linkType, Target: target})
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := workmemory.RetrieveContext(store, workmemory.ContextQuery{
		Topic: topic, Repo: *repo, Branch: *branch, Worktree: *worktree,
		Tags: tags, Links: typedLinks, RecordIDs: records,
		MaxItems: *maxItems, MaxTokens: *maxTokens, MaxBytes: *maxBytes,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		includedIDs := make([]string, 0, len(result.Included))
		for _, candidate := range result.Included {
			includedIDs = append(includedIDs, candidate.Record.ID)
		}
		return writeJSON(stdout, compactContextJSON{
			Brief: result.Brief, Truncated: result.Truncated,
			CandidateCount: result.CandidateCount, IncludedIDs: includedIDs,
			BriefBytes: result.Bytes, BriefEstimatedTokens: result.EstimatedTokens,
		}, true)
	}
	if *jsonFull {
		return writeJSON(stdout, result, true)
	}
	_, err = io.WriteString(stdout, result.Brief)
	return err
}

func commandLink(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("link", stderr)
	target := flags.String("target", "", "external identifier or URL")
	relatedRecord := flags.String("record", "", "target record id or prefix")
	linkType := flags.String("type", "", "external link type")
	relation := flags.String("relation", "", "record relationship type")
	label := flags.String("label", "", "external link label")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return errors.New("usage: memory link RECORD_ID (--target VALUE --type TYPE | --record ID --relation TYPE)")
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	var record workmemory.Record
	if *target != "" {
		if *linkType == "" || *relation != "" || *relatedRecord != "" {
			return errors.New("external links require --target and --type only")
		}
		record, err = store.AddExternalLink(positionals[0], *linkType, *target, *label)
	} else {
		if *relatedRecord == "" || *relation == "" || *linkType != "" || *label != "" {
			return errors.New("record relationships require --record and --relation only")
		}
		record, err = store.AddRelationship(positionals[0], *relation, *relatedRecord)
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, record, true)
	}
	if *target != "" {
		fmt.Fprintf(stdout, "Linked %s to %s:%s\n", shortID(record.ID), *linkType, *target)
	} else {
		fmt.Fprintf(stdout, "Related %s --%s--> %s\n", shortID(record.ID), *relation, *relatedRecord)
	}
	return nil
}

func commandExport(dbPath string, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("export", stderr)
	output := flags.String("output", "-", "output PATH or -")
	flags.StringVar(output, "o", "-", "output PATH or -")
	compact := flags.Bool("compact", false, "compact JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return errors.New("export does not accept positional arguments")
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	document, err := store.ExportData()
	if err != nil {
		return err
	}
	if *output == "-" {
		return writeJSON(stdout, document, !*compact)
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return writeJSON(file, document, !*compact)
}

func commandImport(dbPath string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newFlagSet("import", stderr)
	onConflict := flags.String("on-conflict", "error", "error, skip, or replace")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(flags, args)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return errors.New("import accepts at most one input path")
	}
	inputPath := "-"
	if len(positionals) == 1 {
		inputPath = positionals[0]
	}
	reader, closeReader, err := openInput(inputPath, stdin)
	if err != nil {
		return err
	}
	defer closeReader()
	var document workmemory.ExportDocument
	if err := decodeJSON(reader, &document); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	store, err := workmemory.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.ImportData(document, *onConflict)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, result, true)
	}
	fmt.Fprintf(stdout, "Imported %d record(s), skipped %d, added %d relationship(s)\n", result.Imported, result.Skipped, result.Relationships)
	return nil
}

func parseTypedTarget(value string) (string, string, error) {
	linkType, target, found := strings.Cut(value, "=")
	linkType = strings.TrimSpace(linkType)
	target = strings.TrimSpace(target)
	if !found || linkType == "" || target == "" {
		return "", "", fmt.Errorf("expected TYPE=TARGET, got: %s", value)
	}
	return linkType, target, nil
}

func stringPointer(value string) *string {
	return &value
}

func openInput(path string, stdin io.Reader) (io.Reader, func(), error) {
	if path == "-" {
		return stdin, func() {}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, func() {}, err
	}
	return file, func() { file.Close() }, nil
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(output io.Writer, value any, pretty bool) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(value)
}

func shortID(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func showHuman(output io.Writer, record workmemory.Record) {
	fmt.Fprintln(output, record.Title)
	fmt.Fprintf(output, "id: %s\nstatus: %s\n", record.ID, record.Status)
	for _, item := range []struct{ label, value string }{
		{"repo", record.Repo}, {"branch", record.Branch}, {"worktree", record.Worktree}, {"source", record.Source},
	} {
		if item.value != "" {
			fmt.Fprintf(output, "%s: %s\n", item.label, item.value)
		}
	}
	if len(record.Owners) > 0 {
		fmt.Fprintf(output, "owners: %s\n", strings.Join(record.Owners, ", "))
	}
	if len(record.Tags) > 0 {
		fmt.Fprintf(output, "tags: %s\n", strings.Join(record.Tags, ", "))
	}
	for _, item := range []struct{ label, value string }{
		{"summary", record.Summary}, {"decision", record.Decision}, {"evidence", record.Evidence},
		{"open questions", record.OpenQuestions}, {"next action", record.NextAction},
	} {
		if item.value != "" {
			fmt.Fprintf(output, "\n%s:\n%s\n", item.label, item.value)
		}
	}
	if len(record.Links) > 0 {
		fmt.Fprintln(output, "\nlinks:")
		for _, link := range record.Links {
			label := ""
			if link.Label != "" {
				label = " (" + link.Label + ")"
			}
			fmt.Fprintf(output, "- %s: %s%s\n", link.Type, link.Target, label)
		}
	}
	if len(record.Relationships) > 0 {
		fmt.Fprintln(output, "\nrelationships:")
		for _, relationship := range record.Relationships {
			fmt.Fprintf(output, "- %s --%s--> %s\n", shortID(relationship.FromRecordID), relationship.RelationshipType, shortID(relationship.ToRecordID))
		}
	}
	fmt.Fprintf(output, "\ncreated: %s\nupdated: %s\n", record.CreatedAt, record.UpdatedAt)
}
