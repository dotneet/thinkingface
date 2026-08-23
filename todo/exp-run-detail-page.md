# Run detail page

Experiment tracking / Priority: high

## Current state

There are only three routes around experiments:

- `frontend/app/experiments/page.tsx` — repository list
- `frontend/app/experiments/[ns]/[repo]/page.tsx` — project list
- `frontend/app/experiments/[ns]/[repo]/[project]/page.tsx` — dashboard
  (`components/experiments/experiment-dashboard.tsx`: run table + chart + scatter +
  config diff + tags)

**There's no page for a single run.** There's nowhere to drill down to from a row in the
run table, so there's no place to surface run-specific information (execution
environment, notes, artifacts).

## To do

Add `frontend/app/experiments/[ns]/[repo]/[project]/[run]/page.tsx`. Content equivalent
to MLflow's run detail view:

- Header: run name / status / start & updated timestamps / tags / baseline badge /
  archive action (reuse the existing `PATCH .../runs/{run}`)
- Summary: cards showing `summary` (the final value of each metric)
- Metrics: a chart scoped to just this run (call the existing metrics API with a single
  `runs=` value)
- Hyperparameters: a table of `config`
- Execution environment: a separate section for `_meta.*` (-> `todo/exp-run-env-metadata.md`)
- Notes: Markdown. Add `note` to `ExpRun`, and include it among the updatable fields of
  `PATCH .../runs/{run}` (same treatment as the existing tags / archived / is_baseline)
- Artifacts: -> `todo/exp-run-artifacts.md`
- Link to the generated model: -> `todo/exp-run-model-link.md`

## Blast radius

- Add `ExpRun.note` to `backend/internal/apitypes` -> regenerate
  `frontend/types/api.gen.ts` via `make gen-types` (invariant 1)
- Add a migration to both postgres and sqlite
- Update `docs/api-contract.md` §7
- Turn the run name in the dashboard's run table
  (`components/experiments/run-table.tsx`) into a link
- Don't forget the loading / empty / error three-state pattern and the i18n dictionaries
  (en / ja) (`frontend/DESIGN.md`)

## Definition of done

- You can navigate from the run table to the run detail page, and note edits are saved
- Notes survive re-indexing (treated as a human-added annotation, same as tags /
  archived)
