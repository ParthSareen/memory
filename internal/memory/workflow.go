package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// BeginWorkflow creates one work item, or updates the existing item carrying
// the same tool-run link. Tool-run links make non-threaded executions durable.
func (s *Store) BeginWorkflow(recordInput RecordInput, workflowInput WorkflowInput) (WorkflowItem, bool, error) {
	workflowInput, err := NormalizeWorkflowInput(workflowInput)
	if err != nil {
		return WorkflowItem{}, false, err
	}
	for _, link := range recordInput.Links {
		if link.Type != "tool-run" {
			continue
		}
		id, err := s.workflowForToolRun(link.Target)
		if err == nil {
			current, err := s.GetRecord(id)
			if err != nil {
				return WorkflowItem{}, false, err
			}
			update := UpdateInput{Title: &recordInput.Title, Status: stringPointer(RecordStatusForWorkflow(workflowInput.Status))}
			if recordInput.Summary != "" {
				update.Summary = &recordInput.Summary
			}
			if recordInput.Decision != "" {
				update.Decision = &recordInput.Decision
			}
			if recordInput.Evidence != "" {
				update.Evidence = &recordInput.Evidence
			}
			if recordInput.OpenQuestions != "" {
				update.OpenQuestions = &recordInput.OpenQuestions
			}
			if recordInput.NextAction != "" {
				update.NextAction = &recordInput.NextAction
			}
			if recordInput.Repo != "" {
				update.Repo = &recordInput.Repo
			}
			if recordInput.Branch != "" {
				update.Branch = &recordInput.Branch
			}
			if recordInput.Worktree != "" {
				update.Worktree = &recordInput.Worktree
			}
			if recordInput.Source != "" {
				update.Source = &recordInput.Source
			}
			if len(recordInput.Owners) > 0 {
				update.Owners = &recordInput.Owners
			}
			links := append(append([]ExternalLink{}, current.Links...), recordInput.Links...)
			update.Links = &links
			item, err := s.UpdateWorkflowItem(id, update, workflowInput)
			return item, false, err
		}
		if !errors.Is(err, ErrNotFound) {
			return WorkflowItem{}, false, err
		}
	}
	recordInput.Status = RecordStatusForWorkflow(workflowInput.Status)
	normalized, err := NormalizeInput(recordInput)
	if err != nil {
		return WorkflowItem{}, false, err
	}
	id, err := newUUID()
	if err != nil {
		return WorkflowItem{}, false, err
	}
	now := utcNow()
	record := recordFromInput(normalized, id, now, now)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return WorkflowItem{}, false, err
	}
	if err := insertRecord(context.Background(), tx, record); err != nil {
		tx.Rollback()
		return WorkflowItem{}, false, err
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO work_items(record_id, status, class, area, needs_next_action) VALUES (?, ?, ?, ?, ?)`, record.ID, workflowInput.Status, workflowInput.Class, workflowInput.Area, workflowInput.NeedsNextAction); err != nil {
		tx.Rollback()
		return WorkflowItem{}, false, fmt.Errorf("create work item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return WorkflowItem{}, false, err
	}
	item, err := s.WorkflowItem(record.ID)
	return item, true, err
}

func (s *Store) workflowForToolRun(target string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT w.record_id FROM work_items w JOIN external_links l ON l.record_id = w.record_id WHERE l.link_type = 'tool-run' AND l.target = ? COLLATE NOCASE`, strings.TrimSpace(target)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: tool run %s", ErrNotFound, target)
	}
	return id, err
}

// UpdateWorkflowItem changes the durable record and its workflow overlay in
// one transaction. Invalid record or workflow input leaves both unchanged.
func (s *Store) UpdateWorkflowItem(value string, update UpdateInput, input WorkflowInput) (WorkflowItem, error) {
	input, err := NormalizeWorkflowInput(input)
	if err != nil {
		return WorkflowItem{}, err
	}
	id, err := s.ResolveID(value)
	if err != nil {
		return WorkflowItem{}, err
	}
	current, err := s.getRecordExact(id)
	if err != nil {
		return WorkflowItem{}, err
	}
	update.Status = stringPointer(RecordStatusForWorkflow(input.Status))
	updated, err := updatedRecord(current, update)
	if err != nil {
		return WorkflowItem{}, err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return WorkflowItem{}, err
	}
	if err := replaceRecord(context.Background(), tx, updated); err != nil {
		tx.Rollback()
		return WorkflowItem{}, err
	}
	result, err := tx.ExecContext(context.Background(), `UPDATE work_items SET status = ?, class = ?, area = ?, needs_next_action = ? WHERE record_id = ?`, input.Status, input.Class, input.Area, input.NeedsNextAction, id)
	if err != nil {
		tx.Rollback()
		return WorkflowItem{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		tx.Rollback()
		return WorkflowItem{}, fmt.Errorf("%w: workflow item %s", ErrNotFound, value)
	}
	if err := tx.Commit(); err != nil {
		return WorkflowItem{}, err
	}
	return s.WorkflowItem(id)
}

func (s *Store) WorkflowItem(value string) (WorkflowItem, error) {
	id, err := s.ResolveID(value)
	if err != nil {
		return WorkflowItem{}, err
	}
	var item WorkflowItem
	var needs int
	err = s.db.QueryRow(`SELECT status, class, area, needs_next_action FROM work_items WHERE record_id = ?`, id).Scan(&item.Status, &item.Class, &item.Area, &needs)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowItem{}, fmt.Errorf("%w: workflow item %s", ErrNotFound, value)
	}
	if err != nil {
		return WorkflowItem{}, err
	}
	item.NeedsNextAction = needs != 0
	item.Record, err = s.GetRecord(id)
	return item, err
}

type NowOptions struct {
	Repo, Owner, Area, PriorityOwner string
	Limit                            int
}

func (s *Store) Now(options NowOptions) ([]WorkflowItem, error) {
	if options.Limit == 0 {
		options.Limit = 25
	}
	if options.Limit < 1 {
		return nil, errors.New("limit must be a positive integer")
	}
	clauses := []string{"w.status IN ('active', 'waiting', 'blocked')"}
	args := []any{}
	if options.Repo != "" {
		clauses = append(clauses, "r.repo = ? COLLATE NOCASE")
		args = append(args, options.Repo)
	}
	if options.Area != "" {
		clauses = append(clauses, "w.area = ? COLLATE NOCASE")
		args = append(args, options.Area)
	}
	if options.Owner != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM record_owners o WHERE o.record_id = r.id AND o.owner = ? COLLATE NOCASE)")
		args = append(args, options.Owner)
	}
	priority := ""
	if options.PriorityOwner != "" {
		priority = " WHEN w.needs_next_action = 1 AND EXISTS (SELECT 1 FROM record_owners o WHERE o.record_id = r.id AND o.owner = ? COLLATE NOCASE) THEN 0"
		args = append(args, options.PriorityOwner)
	}
	query := `SELECT r.` + strings.ReplaceAll(recordColumns, ", ", ", r.") + `, w.status, w.class, w.area, w.needs_next_action FROM work_items w JOIN records r ON r.id = w.record_id WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY CASE` + priority + ` WHEN w.needs_next_action = 1 THEN 1 WHEN w.status = 'blocked' THEN 2 WHEN w.status = 'waiting' THEN 3 ELSE 4 END, r.updated_at DESC, r.id LIMIT ?`
	args = append(args, options.Limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	items := []WorkflowItem{}
	for rows.Next() {
		var item WorkflowItem
		var needs int
		if err := rows.Scan(&item.Record.ID, &item.Record.Title, &item.Record.Status, &item.Record.Repo, &item.Record.Branch, &item.Record.Worktree, &item.Record.Summary, &item.Record.Decision, &item.Record.Evidence, &item.Record.OpenQuestions, &item.Record.NextAction, &item.Record.Source, &item.Record.CreatedAt, &item.Record.UpdatedAt, &item.Status, &item.Class, &item.Area, &needs); err != nil {
			return nil, err
		}
		item.Record.Owners, item.Record.Tags, item.Record.Links, item.Record.Relationships = []string{}, []string{}, []ExternalLink{}, []Relationship{}
		item.NeedsNextAction = needs != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		if err := s.hydrateCollections(&items[index].Record); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func stringPointer(value string) *string { return &value }
