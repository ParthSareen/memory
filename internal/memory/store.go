package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const recordColumns = `id, title, status, repo, branch, worktree, summary, decision,
evidence, open_questions, next_action, source, created_at, updated_at`

type Store struct {
	db   *sql.DB
	path string
}

type rowScanner interface {
	Scan(dest ...any) error
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func DefaultDBPath() string {
	if configured := os.Getenv("WORK_MEMORY_DB"); configured != "" {
		return expandHome(configured)
	}
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(expandHome(dataHome), "work-memory", "memory.db")
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "work-memory", "memory.db")
	}
	return filepath.Join(home, ".local", "share", "work-memory", "memory.db")
}

func expandHome(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func Initialize(path string) (*Store, error) {
	path = expandHome(path)
	parent := filepath.Dir(path)
	_, statErr := os.Stat(parent)
	createdParent := os.IsNotExist(statErr)
	if statErr != nil && !createdParent {
		return nil, fmt.Errorf("inspect database directory: %w", statErr)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	if createdParent {
		_ = os.Chmod(parent, 0o700)
	}
	db, err := openDatabase(path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	cleanup := func(cause error) (*Store, error) {
		db.Close()
		return nil, cause
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return cleanup(fmt.Errorf("read schema version: %w", err))
	}
	if version != 0 && version != SchemaVersion {
		return cleanup(fmt.Errorf("unsupported database schema version %d; expected %d", version, SchemaVersion))
	}
	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return cleanup(fmt.Errorf("enable WAL: %w", err))
	}
	if _, err := db.Exec(schema); err != nil {
		return cleanup(fmt.Errorf("initialize schema: %w", err))
	}
	if _, err := db.Exec(
		"INSERT OR REPLACE INTO metadata(key, value) VALUES ('schema_version', ?)",
		fmt.Sprint(SchemaVersion),
	); err != nil {
		return cleanup(fmt.Errorf("write schema metadata: %w", err))
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return cleanup(fmt.Errorf("set schema version: %w", err))
	}
	_ = os.Chmod(path, 0o600)
	return &Store{db: db, path: path}, nil
}

func Open(path string) (*Store, error) {
	path = expandHome(path)
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, fmt.Errorf("database not initialized: %s (run `memory init`): %w", path, err)
	}
	db, err := openDatabase(path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if version != SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("unsupported database schema version %d; expected %d", version, SchemaVersion)
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) SchemaVersion() (int, error) {
	var version int
	err := s.db.QueryRow("PRAGMA user_version").Scan(&version)
	return version, err
}

func (s *Store) CreateRecord(input RecordInput) (Record, error) {
	normalized, err := NormalizeInput(input)
	if err != nil {
		return Record{}, err
	}
	id, err := newUUID()
	if err != nil {
		return Record{}, err
	}
	now := utcNow()
	record := recordFromInput(normalized, id, now, now)
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Record{}, err
	}
	if err := insertRecord(context.Background(), tx, record); err != nil {
		tx.Rollback()
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(id)
}

func (s *Store) UpdateRecord(value string, update UpdateInput) (Record, error) {
	if update.Empty() {
		return Record{}, errors.New("at least one update field is required")
	}
	id, err := s.ResolveID(value)
	if err != nil {
		return Record{}, err
	}
	current, err := s.getRecordExact(id)
	if err != nil {
		return Record{}, err
	}
	input := RecordInput{
		Title: current.Title, Status: current.Status, Repo: current.Repo, Branch: current.Branch,
		Worktree: current.Worktree, Summary: current.Summary, Decision: current.Decision,
		Evidence: current.Evidence, OpenQuestions: current.OpenQuestions, NextAction: current.NextAction,
		Owners: current.Owners, Tags: current.Tags, Links: current.Links, Source: current.Source,
	}
	if update.Title != nil {
		input.Title = *update.Title
	}
	if update.Status != nil {
		input.Status = *update.Status
	}
	if update.Repo != nil {
		input.Repo = *update.Repo
	}
	if update.Branch != nil {
		input.Branch = *update.Branch
	}
	if update.Worktree != nil {
		input.Worktree = *update.Worktree
	}
	if update.Summary != nil {
		input.Summary = *update.Summary
	}
	if update.Decision != nil {
		input.Decision = *update.Decision
	}
	if update.Evidence != nil {
		input.Evidence = *update.Evidence
	}
	if update.OpenQuestions != nil {
		input.OpenQuestions = *update.OpenQuestions
	}
	if update.NextAction != nil {
		input.NextAction = *update.NextAction
	}
	if update.Owners != nil {
		input.Owners = *update.Owners
	}
	if update.Tags != nil {
		input.Tags = *update.Tags
	}
	if update.Links != nil {
		input.Links = *update.Links
	}
	if update.Source != nil {
		input.Source = *update.Source
	}
	normalized, err := NormalizeInput(input)
	if err != nil {
		return Record{}, err
	}
	updatedAt := nextTimestamp(current.UpdatedAt)
	updated := recordFromInput(normalized, current.ID, current.CreatedAt, updatedAt)
	for index := range updated.Links {
		if updated.Links[index].CreatedAt == "" {
			updated.Links[index].CreatedAt = updatedAt
		}
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Record{}, err
	}
	if err := replaceRecord(context.Background(), tx, updated); err != nil {
		tx.Rollback()
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(id)
}

func insertRecord(ctx context.Context, executor sqlExecutor, record Record) error {
	_, err := executor.ExecContext(ctx, `
INSERT INTO records (
    id, title, status, repo, branch, worktree, summary, decision, evidence,
    open_questions, next_action, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.Title, record.Status, record.Repo, record.Branch, record.Worktree,
		record.Summary, record.Decision, record.Evidence, record.OpenQuestions,
		record.NextAction, record.Source, record.CreatedAt, record.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert record: %w", err)
	}
	return insertCollections(ctx, executor, record)
}

func insertCollections(ctx context.Context, executor sqlExecutor, record Record) error {
	for _, tag := range record.Tags {
		if _, err := executor.ExecContext(ctx, "INSERT INTO record_tags(record_id, tag) VALUES (?, ?)", record.ID, tag); err != nil {
			return fmt.Errorf("insert tag: %w", err)
		}
	}
	for _, owner := range record.Owners {
		if _, err := executor.ExecContext(ctx, "INSERT INTO record_owners(record_id, owner) VALUES (?, ?)", record.ID, owner); err != nil {
			return fmt.Errorf("insert owner: %w", err)
		}
	}
	for _, link := range record.Links {
		createdAt := link.CreatedAt
		if createdAt == "" {
			createdAt = record.CreatedAt
		}
		if _, err := executor.ExecContext(ctx, `
INSERT INTO external_links(record_id, link_type, target, label, created_at)
VALUES (?, ?, ?, ?, ?)`, record.ID, link.Type, link.Target, link.Label, createdAt); err != nil {
			return fmt.Errorf("insert link: %w", err)
		}
	}
	return nil
}

func replaceRecord(ctx context.Context, tx *sql.Tx, record Record) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE records SET title = ?, status = ?, repo = ?, branch = ?, worktree = ?,
summary = ?, decision = ?, evidence = ?, open_questions = ?, next_action = ?,
source = ?, created_at = ?, updated_at = ? WHERE id = ?`,
		record.Title, record.Status, record.Repo, record.Branch, record.Worktree,
		record.Summary, record.Decision, record.Evidence, record.OpenQuestions,
		record.NextAction, record.Source, record.CreatedAt, record.UpdatedAt, record.ID,
	); err != nil {
		return err
	}
	for _, table := range []string{"record_tags", "record_owners", "external_links"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE record_id = ?", record.ID); err != nil {
			return err
		}
	}
	return insertCollections(ctx, tx, record)
}

func scanRecord(scanner rowScanner) (Record, error) {
	var record Record
	err := scanner.Scan(
		&record.ID, &record.Title, &record.Status, &record.Repo, &record.Branch,
		&record.Worktree, &record.Summary, &record.Decision, &record.Evidence,
		&record.OpenQuestions, &record.NextAction, &record.Source,
		&record.CreatedAt, &record.UpdatedAt,
	)
	record.Owners = []string{}
	record.Tags = []string{}
	record.Links = []ExternalLink{}
	record.Relationships = []Relationship{}
	return record, err
}

func (s *Store) ResolveID(value string) (string, error) {
	var id string
	err := s.db.QueryRow("SELECT id FROM records WHERE id = ?", value).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := s.db.Query(
		"SELECT id FROM records WHERE substr(id, 1, length(?)) = ? ORDER BY id LIMIT 2",
		value, value,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("%w: record %s", ErrNotFound, value)
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("%w: record id prefix is ambiguous: %s", ErrConflict, value)
	}
	return ids[0], nil
}

func (s *Store) GetRecord(value string) (Record, error) {
	id, err := s.ResolveID(value)
	if err != nil {
		return Record{}, err
	}
	record, err := s.getRecordExact(id)
	if err != nil {
		return Record{}, err
	}
	record.Relationships, err = s.RelationshipsFor(id)
	return record, err
}

func (s *Store) getRecordExact(id string) (Record, error) {
	record, err := scanRecord(s.db.QueryRow("SELECT "+recordColumns+" FROM records WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w: record %s", ErrNotFound, id)
	}
	if err != nil {
		return Record{}, err
	}
	if err := s.hydrateCollections(&record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) hydrateCollections(record *Record) error {
	rows, err := s.db.Query("SELECT tag FROM record_tags WHERE record_id = ? ORDER BY tag COLLATE NOCASE", record.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return err
		}
		record.Tags = append(record.Tags, tag)
	}
	rows.Close()
	rows, err = s.db.Query("SELECT owner FROM record_owners WHERE record_id = ? ORDER BY owner COLLATE NOCASE", record.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			rows.Close()
			return err
		}
		record.Owners = append(record.Owners, owner)
	}
	rows.Close()
	rows, err = s.db.Query(`
SELECT link_type, target, label, created_at FROM external_links
WHERE record_id = ? ORDER BY link_type, target COLLATE NOCASE`, record.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var link ExternalLink
		if err := rows.Scan(&link.Type, &link.Target, &link.Label, &link.CreatedAt); err != nil {
			return err
		}
		record.Links = append(record.Links, link)
	}
	return rows.Err()
}

func (s *Store) GetRecords(ids []string) (map[string]Record, error) {
	result := make(map[string]Record, len(ids))
	for _, id := range ids {
		if _, exists := result[id]; exists {
			continue
		}
		record, err := s.getRecordExact(id)
		if err != nil {
			return nil, err
		}
		result[id] = record
	}
	return result, nil
}

func (s *Store) ListRecords(options ListOptions) ([]Record, error) {
	if options.Limit == 0 {
		options.Limit = 50
	}
	if options.Limit < 1 {
		return nil, errors.New("limit must be a positive integer")
	}
	if options.Status != "" && !statuses[options.Status] {
		return nil, fmt.Errorf("status must be one of: %s", strings.Join(Statuses(), ", "))
	}
	clauses := []string{}
	args := []any{}
	if options.Status != "" {
		clauses = append(clauses, "r.status = ?")
		args = append(args, options.Status)
	}
	if options.Repo != "" {
		clauses = append(clauses, "r.repo = ? COLLATE NOCASE")
		args = append(args, options.Repo)
	}
	if options.Tag != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM record_tags t WHERE t.record_id = r.id AND t.tag = ? COLLATE NOCASE)")
		args = append(args, options.Tag)
	}
	if options.Owner != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM record_owners o WHERE o.record_id = r.id AND o.owner = ? COLLATE NOCASE)")
		args = append(args, options.Owner)
	}
	query := "SELECT " + recordColumns + " FROM records r"
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY r.updated_at DESC, r.id LIMIT ?"
	args = append(args, options.Limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var records []Record
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range records {
		if err := s.hydrateCollections(&records[index]); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func (s *Store) AddExternalLink(recordValue, linkType, target, label string) (Record, error) {
	id, err := s.ResolveID(recordValue)
	if err != nil {
		return Record{}, err
	}
	linkType, err = NormalizeType(linkType, "link type")
	if err != nil {
		return Record{}, err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return Record{}, errors.New("link target is required")
	}
	current, err := s.getRecordExact(id)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Record{}, err
	}
	result, err := tx.Exec(`
INSERT OR IGNORE INTO external_links(record_id, link_type, target, label, created_at)
VALUES (?, ?, ?, ?, ?)`, id, linkType, target, strings.TrimSpace(label), utcNow())
	if err != nil {
		tx.Rollback()
		return Record{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		tx.Rollback()
		return Record{}, fmt.Errorf("%w: link already exists", ErrConflict)
	}
	if _, err := tx.Exec("UPDATE records SET updated_at = ? WHERE id = ?", nextTimestamp(current.UpdatedAt), id); err != nil {
		tx.Rollback()
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(id)
}

func (s *Store) AddRelationship(fromValue, relationshipType, toValue string) (Record, error) {
	fromID, err := s.ResolveID(fromValue)
	if err != nil {
		return Record{}, err
	}
	toID, err := s.ResolveID(toValue)
	if err != nil {
		return Record{}, err
	}
	if fromID == toID {
		return Record{}, errors.New("a record cannot relate to itself")
	}
	relationshipType, err = NormalizeType(relationshipType, "relationship type")
	if err != nil {
		return Record{}, err
	}
	current, err := s.getRecordExact(fromID)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Record{}, err
	}
	result, err := tx.Exec(`
INSERT OR IGNORE INTO relationships(from_record_id, relationship_type, to_record_id, created_at)
VALUES (?, ?, ?, ?)`, fromID, relationshipType, toID, utcNow())
	if err != nil {
		tx.Rollback()
		return Record{}, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		tx.Rollback()
		return Record{}, fmt.Errorf("%w: relationship already exists", ErrConflict)
	}
	if _, err := tx.Exec("UPDATE records SET updated_at = ? WHERE id = ?", nextTimestamp(current.UpdatedAt), fromID); err != nil {
		tx.Rollback()
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return s.GetRecord(fromID)
}

func (s *Store) RelationshipsFor(id string) ([]Relationship, error) {
	rows, err := s.db.Query(`
SELECT from_record_id, relationship_type, to_record_id, created_at
FROM relationships WHERE from_record_id = ? OR to_record_id = ?
ORDER BY relationship_type, from_record_id, to_record_id`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Relationship{}
	for rows.Next() {
		var relationship Relationship
		if err := rows.Scan(&relationship.FromRecordID, &relationship.RelationshipType, &relationship.ToRecordID, &relationship.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, relationship)
	}
	return result, rows.Err()
}

func (s *Store) RelatedRecords(ids []string) ([]RelatedRecord, error) {
	seen := map[string]bool{}
	var result []RelatedRecord
	for _, id := range ids {
		rows, err := s.db.Query(`
SELECT from_record_id, relationship_type, to_record_id
FROM relationships WHERE from_record_id = ? OR to_record_id = ?
ORDER BY relationship_type, from_record_id, to_record_id`, id, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var relation RelatedRecord
			if err := rows.Scan(&relation.FromID, &relation.Type, &relation.ToID); err != nil {
				rows.Close()
				return nil, err
			}
			key := relation.FromID + "\x00" + relation.Type + "\x00" + relation.ToID
			if !seen[key] {
				seen[key] = true
				result = append(result, relation)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].Type + result[i].FromID + result[i].ToID
		right := result[j].Type + result[j].FromID + result[j].ToID
		return left < right
	})
	return result, nil
}

func (s *Store) MetadataMatches(query MetadataQuery) (map[string][]string, error) {
	clauses := []string{}
	args := []any{}
	reasons := []string{}
	for _, item := range []struct {
		value, column, label string
	}{
		{query.Repo, "repo", "repo"},
		{query.Branch, "branch", "branch"},
		{query.Worktree, "worktree", "worktree"},
	} {
		if item.value != "" {
			clauses = append(clauses, "r."+item.column+" = ? COLLATE NOCASE")
			args = append(args, item.value)
			reasons = append(reasons, item.label+" matches "+item.value)
		}
	}
	for _, tag := range cleanStrings(query.Tags) {
		clauses = append(clauses, `EXISTS (
SELECT 1 FROM record_tags t WHERE t.record_id = r.id AND t.tag = ? COLLATE NOCASE
)`)
		args = append(args, tag)
		reasons = append(reasons, "tag matches "+tag)
	}
	seenLinks := map[string]bool{}
	for _, link := range query.Links {
		linkType, err := NormalizeType(link.Type, "link type")
		if err != nil {
			return nil, err
		}
		target := strings.TrimSpace(link.Target)
		if target == "" {
			return nil, errors.New("link target is required")
		}
		key := linkType + "\x00" + strings.ToLower(target)
		if seenLinks[key] {
			continue
		}
		seenLinks[key] = true
		clauses = append(clauses, `EXISTS (
SELECT 1 FROM external_links l
WHERE l.record_id = r.id AND l.link_type = ? AND l.target = ? COLLATE NOCASE
)`)
		args = append(args, linkType, target)
		reasons = append(reasons, linkType+" link matches "+target)
	}
	resolvedRecords := map[string]string{}
	for _, value := range query.RecordIDs {
		id, err := s.ResolveID(value)
		if err != nil {
			return nil, err
		}
		resolvedRecords[id] = value
	}
	if len(resolvedRecords) > 0 {
		ids := make([]string, 0, len(resolvedRecords))
		for id := range resolvedRecords {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		placeholders := make([]string, len(ids))
		for index, id := range ids {
			placeholders[index] = "?"
			args = append(args, id)
		}
		clauses = append(clauses, "r.id IN ("+strings.Join(placeholders, ", ")+")")
	}
	result := map[string][]string{}
	if len(clauses) == 0 {
		return result, nil
	}
	rows, err := s.db.Query(
		"SELECT r.id FROM records r WHERE "+strings.Join(clauses, " AND ")+" ORDER BY r.id",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		recordReasons := append([]string(nil), reasons...)
		if value, ok := resolvedRecords[id]; ok {
			recordReasons = append(recordReasons, "record id matches "+value)
		}
		result[id] = recordReasons
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) FTSSearch(expression string, limit int) ([]FTSMatch, error) {
	rows, err := s.db.Query(`
SELECT r.id, bm25(record_fts, 8.0, 4.0, 5.0, 2.0, 3.0, 4.0) AS rank
FROM record_fts JOIN records r ON r.rowid = record_fts.rowid
WHERE record_fts MATCH ? ORDER BY rank, r.updated_at DESC, r.id LIMIT ?`, expression, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FTSMatch
	for rows.Next() {
		var match FTSMatch
		if err := rows.Scan(&match.ID, &match.Rank); err != nil {
			return nil, err
		}
		result = append(result, match)
	}
	return result, rows.Err()
}

func (s *Store) RecentIDs(limit int) ([]string, error) {
	rows, err := s.db.Query(`
SELECT id FROM records
ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'blocked' THEN 1 ELSE 2 END,
updated_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
