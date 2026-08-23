# Metrics fetch scaling

Experiment tracking / Priority: low-to-medium (matters once runs get long)

## Current state

`GET .../metrics` downsamples on the API side via `max_points` (default 1000), but
`scanParquetSeries` in `backend/internal/experiments/series.go` **scans the whole
parquet every time, collecting all points before thinning them down**. The live side
(`exp_points`) also reads every row for a run.

The viewer layer already has row-group-level skipping and a local LRU cache
(`docs/thinkingface-design.md` §9), so this holds up for now, but once we're at
"hundreds of thousands of steps x dozens of runs x dozens of metrics," the cost of a
single request grows linearly.

## To do (once it becomes necessary)

1. Measure first. Find out how long it actually takes for a given run count x point
   count before starting work
2. Maintain step-bucket rollups
   - At index time, aggregate min / max / mean / last per `(run, key, bucket)` and keep
     it in the DB
   - Charts return the rollup by default, and only drop down to raw data on zoom
3. Alternatively, keep steps sorted ascending on the parquet side and use row-group
   statistics to skip out-of-range groups entirely (the `viewer` already has this
   capability — it works as long as the write side guarantees ordering)
4. The `summary` shown in list views is already DB-cached, so leave that alone

## Definition of done

- Demonstrate with real measurements that first-chart-render latency stays within
  acceptable bounds under a representative load (e.g. 50 runs x 100k steps x 10 metrics)
