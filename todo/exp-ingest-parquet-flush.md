# Parquet flush for the ingest path

Experiment tracking / Priority: highest (not a new feature — fulfilling a design commitment)

## Current state

`docs/thinkingface-design.md` §8 states, for path B (the native ingest API), that "the
receiving side buffers and periodically flushes to Parquet, and the storage destination
is unified with path A's **same dataset repository**." The comment on the `exp_points`
table also says "this table only holds unflushed points"
(`backend/internal/store/migrations/sqlite/0001_init.sql`).

However, `backend/internal/store/experiments.go` only has `BulkInsert` and `SELECT` —
**neither flushing nor deletion is implemented**. `backend/internal/syncer/` has no such
job either.

## Why this is a problem

Experiment data recorded via the `thinkingface.trackio` shim exists only inside the DB:

- `exp_points` grows without bound. It bloats on long runs, and this especially bites in
  SQLite mode (single writer)
- It's invisible from `gcloud storage` and from DuckDB / BigQuery (nothing shows up
  under `exports/`)
- It doesn't land in git history, so cloning the repository doesn't bring the experiment
  data along
- On repository transfer/rename, `wal/` and `exports/` follow along, but experiment data
  stays tied to the DB

As a result, **the biggest advantage over MLflow — "Parquet is the source of truth for
experiment logs" — does not actually hold for path B**. The README and design doc
descriptions are currently inaccurate too.

## To do

1. Add a periodic flush job to the syncer (either piggyback on `sync_jobs`, or a
   dedicated ticker)
   - Target: runs where `repositories.is_experiment = true` and there are unflushed
     `exp_points`
   - Make the interval configurable (default around 1-5 minutes). Flush immediately,
     once, the moment a run becomes `finished` / `failed`
2. Write Parquet by appending unflushed points to `{project}/metrics.parquet`
   - Match the column layout to path A's (trackio) format: `run_name` / `step` /
     `timestamp` + metric columns
   - Write with `parquet-go` (pure Go — respect the no-CGo invariant; the read side is
     `backend/internal/viewer`)
   - It's fine to start with "read the whole file back and rewrite it" for appending to
     an existing file, but also consider split appends via
     `{project}/metrics-{seq}.parquet` for when row counts grow (this needs
     `DetectLayouts`'s recognized patterns extended: `backend/internal/experiments/layout.go`)
3. Create the commit server-side
   - Use `gitrepo.Repo.Commit` (`backend/internal/gitrepo/commit.go`), preserving the
     invariant that git is the single write path
   - `*.parquet` is LFS-tracked by default, so put the actual bytes into storage first
     and pass the LFS pointer via `Op.Data` (same procedure as the Web UI's file-edit
     path)
   - Use optimistic locking via `PathPrecondition` so concurrent flushes to the same
     project don't drop data
   - Make the commit message clearly machine-generated (e.g.
     `chore(trackio): flush {project} metrics`)
4. Delete the corresponding `exp_points` rows after a successful flush
5. After the flush, the existing `Indexer.IndexRepo` re-reads the parquet and
   re-indexes the run. `Series` (`backend/internal/experiments/series.go`) already
   merges parquet + live data, so pin down with a test that **the chart shows neither
   duplicates nor gaps across the flush boundary**

## Definition of done

- A run recorded via the shim can, after flushing, be fetched as parquet under
  `exports/` via `gcloud storage cp`, and read from DuckDB
- After flushing, the `exp_points` rows are gone and the UI chart is unchanged (no
  duplicates, no gaps)
- `backend/internal/experiments` has a test verifying the before/after merge
- Add an e2e path covering "record via shim -> flush -> fetch as parquet"

## Notes

More commits get generated as machine noise, which trades off against a readable
history. Decide on a policy — batching per run, squashing into one commit on
`finished`, etc. — before implementing.
