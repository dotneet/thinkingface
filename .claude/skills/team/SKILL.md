---
name: team
description: 'Work through implementations spanning multiple layers using parallel subagents. Ownership splitting, wave-based launches, and integration steps for when asked to "work in parallel as a team", "split between Opus 5 and Sonnet 5 by difficulty", or "use subagents efficiently".'
---

# team — parallel implementation with subagents

The procedure that worked when clearing 19 TODOs across a total of 16 agents in
thinkingface. **A single working tree + ownership splitting + wave-based launches** works
better than splitting into separate worktrees.

## Why not split into worktrees

`frontend/types/api.gen.ts` (generated), `backend/internal/apitypes` (shared types), the
`lib/i18n/` dictionaries, and `docs/dev/api-contract.md` get touched by almost every task.
Splitting into worktrees turns the final merge into hell. In a single tree, the Edit tool can
cleanly let edits in different areas coexist.

## Procedure

### 1. The parent locks down shared artifacts first

Before launching subagents, decide and write out the following yourself.

- The `apitypes` wire types (if they're changing) and `docs/dev/api-contract.md`
- The skeleton of routing and handlers
- **The parent assigns DB migration sequence numbers and tells each agent what to use.**
  Gaps in the numbering are harmless (`store.go` records by filename in `schema_migrations`
  and applies them in lexical order). Running without pre-assigned numbers always causes
  collisions.

### 2. Split file ownership by domain

Split it like "Python client", "experiments backend", "experiments frontend",
"lineage", "repo features" — **at most one domain, one task, per wave**. Once a domain's
owner finishes, launch the next wave without waiting for the whole wave to complete.

### 3. Standard rules to include in every prompt

```
- Never perform any git state-changing operation (checkout / stash / reset / commit / add / restore)
- Do not run make check / make up / docker (verify only with go test / tsc scoped to your area)
- Do not modify files outside your assignment. If you find another worker's edits, do not revert them
- Run backend/scripts/gen-types.sh only if you changed apitypes, and only once, at the end of your work
- Files you may touch: <list>
- Files to leave alone because they're being edited concurrently: <list>
```

**Also tell them editor diagnostics (LSP) are unreliable right now.** While other agents are
mid-edit, packages become uncompilable and unrelated identifiers show up as undefined in
bulk. Have them confirm by grepping the actual files.

### 4. Model assignment

- **Opus 5**: algorithmic cores, concurrency control, storage layout changes, parts
  involving design decisions.
- **Sonnet 5**: boilerplate CRUD, adding UI components, keeping dictionaries/docs in sync,
  adding tests.
- The parent (orchestrator) keeps its own implementation to a minimum and focuses on
  splitting, integrating, live verification, and review. It's fine for the parent to keep
  only the single hardest core piece for itself.

### 5. Have ambiguous requirements audited first

For requirements like "improve security" or "find UI bugs", have a **read-only audit
agent** write up a findings list first, then hand it to the implementation agents. The
granularity ends up usable as implementation instructions as-is.

### 6. The parent does the integration

```bash
backend/scripts/gen-types.sh
make check
make test-store-pg
make test-e2e
```

`check-types` looks at `git status --porcelain`, so it **always fails before committing**.
To confirm sync before committing, judge it by "run `gen-types` twice and get a zero diff".

## Pitfalls

- Never run a stateful command (like `tf logout`) concurrently with other commands in
  parallel Bash. It wipes credentials and breaks whatever runs after it.
- Once implementation is done, **always verify it live yourself** (`dev` skill). A
  subagent's "verified it works" often just means it compiled.
- Self-review and PRs go to the `ship` skill. When fixing pass-1 findings, grep for leftovers
  of the same nature yourself and kill them first.
