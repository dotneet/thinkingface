# Run resume support

Experiment tracking / Priority: medium (high if running on Spot / preemptible VMs)

## Current state

`init(resume=...)` is discarded as part of `**kwargs`
(`clients/python/thinkingface/trackio.py`). Calling `init()` again with the same run
name causes the ingest side to append to the same run, but **that behavior isn't
specified and isn't tested**. Step rewinding, `status` transitions, and config
overwriting are all undefined.

## Why this is a problem

Long training runs on Spot / preemptible VMs are interrupted and resumed as a matter of
course. If each resume becomes a separate run (or breaks the step sequence), the chart
becomes unreadable.

## To do

1. Decide what the `resume` value means (align with wandb / trackio)
   - `"allow"`: continue if a same-named run exists, otherwise create new
   - `"must"`: error if it doesn't exist
   - `"never"` (default): error if a same-named run exists, or shift the name
2. Formally specify the resume behavior in `docs/api-contract.md` §7
   - step continues from the previous `last_step` (the shim calls `GET runs/{run}` to
     restore it)
   - `status` can transition back from `finished` to `running`
   - config is merged by default (on conflict, the new value wins, and the fact that a
     diff occurred is recorded on the run)
3. Decide how to handle duplicate `(step, key)` pairs on the metrics side (last write
   wins; keep this consistent between `Series`'s merge implementation
   (`backend/internal/experiments/series.go`) and the parquet side)

## Definition of done

- Interrupt -> resume produces one continuous chart
- Duplicate steps aren't drawn twice
- The spec is pinned down by tests (backend series merge + shim unit tests)
