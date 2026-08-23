# Run grouping and sweep comparison UI

Experiment tracking / Priority: medium

## Current state

- The shim's `init()` accepts `**kwargs` and **discards** them
  (`clients/python/thinkingface/trackio.py`: "Extra keyword arguments are accepted and
  ignored"). That means passing `group=` / `job_type=` does nothing
- The comparison UI is: run table + overlaid chart + scatter (`run-scatter.tsx`) +
  config diff (`config-diff-table.tsx`) + tags. There's no structure equivalent to
  MLflow's nested runs (parent/child)

Running a hyperparameter sweep across dozens of runs quickly becomes unmanageable in a
flat run list.

## To do

1. Accept `init(group=..., job_type=...)` and include them in the ingest API payload
   - Add `group_name` / `job_type` columns to `exp_runs` (migrations for both postgres
     and sqlite). Add them to `ExpRun` and run `make gen-types`
   - Also pick these up from path A (trackio parquet) if a same-named column is present
2. Make the run table collapsible by group. Group rows should show member count and a
   best-metrics summary
3. Add a parallel coordinates plot
   - Axes = hyperparameters + target metric. The standard way to see sweep trends at a
     glance
   - Plain SVG might be more straightforward than uPlot for this (worth evaluating)
4. Add sorting and filtering by metric column to the run table

## Definition of done

- Runs with a group specified are shown grouped together and can be collapsed
- Runs without a group still display flat as before (backward compatible)
- The parallel coordinates plot supports axis selection and run highlighting
