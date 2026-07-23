package memory

const schema = `
CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS records (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'blocked', 'completed', 'abandoned', 'superseded')),
    repo TEXT NOT NULL DEFAULT '',
    branch TEXT NOT NULL DEFAULT '',
    worktree TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    decision TEXT NOT NULL DEFAULT '',
    evidence TEXT NOT NULL DEFAULT '',
    open_questions TEXT NOT NULL DEFAULT '',
    next_action TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS records_repo_idx ON records(repo COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS records_branch_idx ON records(branch COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS records_status_updated_idx ON records(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS record_tags (
    record_id TEXT NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    tag TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (record_id, tag)
);
CREATE INDEX IF NOT EXISTS record_tags_tag_idx ON record_tags(tag COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS record_owners (
    record_id TEXT NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    owner TEXT NOT NULL COLLATE NOCASE,
    PRIMARY KEY (record_id, owner)
);
CREATE INDEX IF NOT EXISTS record_owners_owner_idx ON record_owners(owner COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS external_links (
    id INTEGER PRIMARY KEY,
    record_id TEXT NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    link_type TEXT NOT NULL,
    target TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (record_id, link_type, target)
);
CREATE INDEX IF NOT EXISTS external_links_lookup_idx
ON external_links(link_type, target COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS relationships (
    id INTEGER PRIMARY KEY,
    from_record_id TEXT NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    relationship_type TEXT NOT NULL,
    to_record_id TEXT NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    CHECK (from_record_id <> to_record_id),
    UNIQUE (from_record_id, relationship_type, to_record_id)
);
CREATE INDEX IF NOT EXISTS relationships_from_idx ON relationships(from_record_id);
CREATE INDEX IF NOT EXISTS relationships_to_idx ON relationships(to_record_id);

CREATE TABLE IF NOT EXISTS work_items (
    record_id TEXT PRIMARY KEY REFERENCES records(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('active', 'waiting', 'blocked', 'closed', 'parked')),
    class TEXT NOT NULL DEFAULT '',
    area TEXT NOT NULL DEFAULT '',
    needs_next_action INTEGER NOT NULL DEFAULT 0 CHECK (needs_next_action IN (0, 1))
);
CREATE INDEX IF NOT EXISTS work_items_queue_idx
ON work_items(status, needs_next_action DESC);
CREATE INDEX IF NOT EXISTS work_items_area_idx ON work_items(area COLLATE NOCASE);

CREATE VIRTUAL TABLE IF NOT EXISTS record_fts USING fts5(
    title,
    summary,
    decision,
    evidence,
    open_questions,
    next_action,
    content='records',
    content_rowid='rowid',
    tokenize='porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS records_fts_insert AFTER INSERT ON records BEGIN
    INSERT INTO record_fts(rowid, title, summary, decision, evidence, open_questions, next_action)
    VALUES (new.rowid, new.title, new.summary, new.decision, new.evidence, new.open_questions, new.next_action);
END;
CREATE TRIGGER IF NOT EXISTS records_fts_delete AFTER DELETE ON records BEGIN
    INSERT INTO record_fts(record_fts, rowid, title, summary, decision, evidence, open_questions, next_action)
    VALUES ('delete', old.rowid, old.title, old.summary, old.decision, old.evidence, old.open_questions, old.next_action);
END;
CREATE TRIGGER IF NOT EXISTS records_fts_update AFTER UPDATE ON records BEGIN
    INSERT INTO record_fts(record_fts, rowid, title, summary, decision, evidence, open_questions, next_action)
    VALUES ('delete', old.rowid, old.title, old.summary, old.decision, old.evidence, old.open_questions, old.next_action);
    INSERT INTO record_fts(rowid, title, summary, decision, evidence, open_questions, next_action)
    VALUES (new.rowid, new.title, new.summary, new.decision, new.evidence, new.open_questions, new.next_action);
END;
`
