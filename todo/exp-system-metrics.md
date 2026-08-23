# System metrics (GPU / CPU / memory)

Experiment tracking / Priority: medium

## Current state

`backend/internal/experiments/layout.go` **explicitly excludes** parquet files with the
`_system` suffix (it's aware that trackio itself emits these). In other words, path A
discards them, and path B (the shim) doesn't collect them at all. There's nothing
equivalent to MLflow's system metrics.

## Why this is a problem

You can't distinguish "loss isn't dropping because of X" from "GPU utilization was
pegged" or "memory was creeping toward OOM." This quietly matters a lot as training
infrastructure.

## To do

1. Add a background collection thread to the shim (piggyback on the existing flush
   timer)
   - GPU: utilization / memory / temperature (fall back from torch to `nvidia-smi`)
   - CPU / RSS: via `psutil` if available; omit if not
   - Namespace keys under a `system/` prefix, e.g. `system/gpu.0.util`
   - Default collection interval around 10 seconds. Disable via
     `THINKINGFACE_SYSTEM_METRICS=off`
2. Decide whether to index path A's `_system` parquet too (remove the exclusion in
   `layout.go` and normalize it into the `system/` prefix when ingesting)
3. In the UI, split metric charts into tabs for `system/` vs. regular metrics
   (`frontend/components/experiments/metrics-charts.tsx`). Default to showing only
   regular metrics

## Definition of done

- Collection doesn't raise in a GPU-less environment; the corresponding keys simply
  don't appear
- `system/` metrics don't clutter the regular charts (separated by default)
