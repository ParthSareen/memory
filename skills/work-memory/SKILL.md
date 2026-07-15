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

## Record

Write one concise outcome at a meaningful boundary: a decision, implementation handoff, review result, incident analysis, or completed task.

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

- Keep the source system for live task tracking separate from work memory. Work memory is the durable decision, evidence, and handoff layer.
- Link source artifacts rather than copying them into records.
- Prefer bounded, scoped retrieval over broad historical recall.
