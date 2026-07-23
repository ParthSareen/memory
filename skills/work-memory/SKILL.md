---
name: work-memory
description: Capture and retrieve concise durable work context with the local work-memory CLI. Use when beginning related work, recording a decision, handing work off, closing a task, or preparing a bounded brief for a new task.
---

# Work Memory

Use `memory` as a local, deliberate store of work outcomes. It is not a transcript archive.

## Retrieve

Before starting related work, retrieve a bounded brief with every known scope field:

```sh
memory context "release configuration" \
  --repo example/repository \
  --branch feature/config \
  --max-tokens 1200 --max-bytes 8000
```

- Add known repository, branch, worktree, tag, task, pull request, or issue links. Scope dimensions are conjunctive.
- Use the brief's decisions, evidence, open questions, and artifact links as task context.
- Use normal or compact `--json` output for agents. Reserve `--json-full` for human diagnostics because it is intentionally unbounded.
- If no scoped record exists, say so rather than treating an unscoped search as authoritative.

## Workflow state

For active work, after retrieval, begin or resume exactly one work item. Its durable record ID carries continuity; a thread ID is optional metadata, never required. The work item is also the durable outcome record, so do not create a second record at handoff.

```sh
memory begin \
  --title "Configuration migration" --status active --class build --area configuration \
  --owner owner --repo example/repository --branch feature/config \
  --next-action "Validate the migration." --json
```

- Use `memory now --priority-owner OWNER --json` for the compact active/waiting/blocked queue when one owner's needed actions should lead. The generic CLI has no default priority owner.
- Use `checkpoint` only at a meaningful state change. Use `wait` or `checkpoint --status blocked` for a dependency; use `handoff` with concise evidence and a next action; use `close` (which clears a stale next action unless replaced) or `park` when appropriate.
- For a non-threaded tool run, attach `--tool-ref TOOL:RUN`. A later tool can resume that work item without inventing a thread ID.
- Harnesses and scripts use `--json`; do not scrape human output.

For an external task runner, begin the item before launch, pass the returned record ID to the task, attach the task or run link afterward, and require the task to hand off the same record with evidence and a next action.

## Record

Use `record` for a standalone decision, review, incident analysis, or outcome that was never tracked as a workflow item.

```sh
memory record --input - --json <<'JSON'
{
  "title": "Configuration migration decision",
  "status": "active",
  "repo": "example/repository",
  "branch": "feature/config",
  "summary": "The migration writes configuration atomically and preserves user overrides.",
  "decision": "Use staged writes with rollback on validation failure.",
  "evidence": "Focused tests and a manual migration smoke test pass.",
  "open_questions": "Should the compatibility mode be removed in the next release?",
  "next_action": "Review the migration contract.",
  "owners": ["owner"],
  "tags": ["configuration", "migration"],
  "links": [{"type": "issue", "target": "example/repository#123"}],
  "source": "agent"
}
JSON
```

- Include concrete branches, worktrees, task IDs, pull requests, issues, and source artifacts when available.
- Store facts, decisions, proof, and next actions. Do not store secrets, credentials, full tool output, personal data, or model reasoning.
- Do not create a new record for routine progress. Update the existing outcome instead.

## Maintain

Update a record when its status, decision, evidence, open questions, next action, or linked artifacts change:

```sh
memory update RECORD_ID --status completed --next-action ""
memory link RECORD_ID --target https://example.com/pull/123 --type pr
memory link RECORD_ID --record RELATED_ID --relation informs
```

Verify consequential writes with `memory show RECORD_ID --json`.

## Boundaries

- The workflow queue is the live execution index for work tracked in this local store.
- Keep planning backlogs and external source systems separate; link their artifacts into the work item.
- Link source artifacts rather than copying them into records.
- Prefer bounded, scoped retrieval over broad historical recall.
