# work-memory

`work-memory` is a local-first Go CLI for turning work into concise, durable outcomes and retrieving a bounded brief for the next task. It favors facts, decisions, evidence, open questions, and next actions over transcript storage or generic semantic note search.

V0 is deliberately small: one local SQLite database, FTS5 full-text search, typed links, deterministic ranking, and no network activity at runtime. There are no embeddings, vector database, cloud service, daemon, web UI, or automatic ingestion.

## Install

Go 1.23+ is required to build. SQLite is compiled into the binary through the pure-Go `modernc.org/sqlite` driver, so no C compiler or system SQLite installation is needed.

```sh
go install ./cmd/memory
memory init
```

For a repository-local binary:

```sh
go build -o ./bin/memory ./cmd/memory
./bin/memory init
```

The default database is `~/Library/Application Support/work-memory/memory.db` on macOS and `$XDG_DATA_HOME/work-memory/memory.db` (or `~/.local/share/...`) elsewhere. Override it with `WORK_MEMORY_DB` or `memory --db ./work.db ...`.

## Record work

```sh
memory record \
  --title "Implemented bounded work retrieval" \
  --status active \
  --repo ParthSareen/memory \
  --branch main \
  --summary "SQLite storage and a compact context brief work end to end." \
  --decision "Rank exact repo, branch, tag, and link matches before FTS." \
  --evidence "Focused tests and a temporary-database demo pass." \
  --open-questions "Which ingest adapter should come first?" \
  --next-action "Test against real work records." \
  --owner parth --tag retrieval --tag cli \
  --link issue=ParthSareen/memory#1 \
  --source codex
```

Agents can send the same shape over stdin and receive the created record as JSON:

```sh
printf '%s' '{
  "title": "Validated context ranking",
  "status": "completed",
  "repo": "ParthSareen/memory",
  "summary": "Exact issue matches precede text matches.",
  "owners": ["agent"],
  "tags": ["test"],
  "links": [{"type": "thread", "target": "task-123"}]
}' | memory record --input - --json
```

Record fields are `title`, `status`, `repo`, `branch`, `worktree`, `summary`, `decision`, `evidence`, `open_questions`, `next_action`, `owners`, `tags`, `links`, `source`, and generated timestamps. Status is one of `active`, `blocked`, `completed`, `abandoned`, or `superseded`.

## Update work

Updates preserve the record ID and creation time while refreshing `updated_at`, FTS content, and any replaced collections:

```sh
memory update 8f31a2c0 \
  --status completed \
  --decision "Ship exact-scope intersection semantics." \
  --next-action ""

printf '%s' '{"status":"blocked","open_questions":"Waiting on release evidence."}' \
  | memory update 8f31a2c0 --input - --json
```

Scalar flags replace their field and can use an empty value to clear it. Repeated `--owner`, `--tag`, and `--link` values replace the corresponding collection. `--clear-owners`, `--clear-tags`, and `--clear-links` explicitly empty collections. Adding an external link or outgoing record relationship also refreshes the source record's `updated_at` so recency reflects meaningful activity.

## Retrieve narrowly

```sh
memory context "bounded retrieval" --repo ParthSareen/memory --branch main
memory context --link issue=ParthSareen/memory#1 --max-tokens 500 --max-bytes 3000
memory context --record 8f31a2c0 --json
memory context --record 8f31a2c0 --json-full
```

Ranking is deterministic:

1. Exact record, external-link, repo, branch, worktree, and tag matches.
2. Records connected to those exact matches by a typed relationship.
3. Weighted FTS5 matches across title, summary, decision, evidence, open questions, and next action.
4. Active/blocked state, unresolved fields, and recency boost records within each tier.

Exact scope dimensions are conjunctive: `--repo`, `--branch`, `--worktree`, every repeated `--tag`, and every repeated `--link` must all match. Repeated `--record` values form an explicit allowed ID set, which is then intersected with the other scope dimensions. These are hard constraints for FTS candidates too; text matches cannot reintroduce records outside the requested scope. Explicit record relationships may still add a separately labeled related-work item outside that scope.

Every item includes a `Why:` line. With no scope, `context` returns recent work with active and blocked records first. `--max-items`, `--max-tokens`, and `--max-bytes` bound the brief. The byte limit is strict. The model-independent token estimate is the larger of lexical tokens and UTF-8 bytes divided by four, rounded up; it is predictable but is not a substitute for a model-specific tokenizer. Truncated human output always contains a visible `[truncated]` marker; budgets too small to contain that marker fail clearly.

`context --json` emits a compact agent-oriented envelope containing the brief, included IDs, candidate count, and brief measurements. The limits apply to the brief field; JSON escaping and the small envelope add fixed overhead. `context --json-full` includes full records, scores, and reasons for diagnostics and is intentionally not bounded as a whole response.

## Inspect and link

```sh
memory list --repo ParthSareen/memory
memory list --status active --tag retrieval --json
memory show 8f31a2c0

memory link 8f31a2c0 --target https://github.com/ParthSareen/memory/pull/12 --type pr
memory link 8f31a2c0 --record 2a91c044 --relation leads_to
```

External links and record-to-record relationships use typed strings with an intentionally simple schema. They can later support ingest adapters and hybrid retrieval without introducing a graph database in v0.

## Export and import

```sh
memory export --output work-memory.json
memory import work-memory.json
memory import work-memory.json --on-conflict skip
memory import work-memory.json --on-conflict replace
```

Exports are UTF-8 JSON objects identified by `{"format":"work-memory","version":1}`. They contain records, typed external links, record relationships, IDs, and timestamps. Import is transactional. The default conflict policy is `error`; `skip` and `replace` are explicit alternatives.

The Go implementation retains the original schema and interchange version, so databases and exports created by the initial prototype remain readable.

## Privacy and local data

- The binary performs no network requests and has no telemetry.
- Records, links, and FTS data remain in the selected SQLite database.
- Database directories are created with owner-only permissions where supported; databases and file exports use mode `0600`.
- Full-text search duplicates searchable record text inside the same SQLite file. Deleting the database and its `-wal`/`-shm` sidecars deletes local state.
- Exports are plaintext and may contain sensitive work context; protect them like the database.

## Development

```sh
go test ./...
go run ./cmd/memory --db /tmp/work-memory-demo.db init
```

The schema is versioned with SQLite `user_version`. FTS synchronization uses database triggers, while import/export keeps the external interchange version separate from the internal schema version.
