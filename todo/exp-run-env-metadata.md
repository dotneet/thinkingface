# Automatic recording of run environment metadata

Experiment tracking / Priority: high (low cost, big impact on reproducibility)

## Current state

`init()` in `clients/python/thinkingface/trackio.py` just forwards whatever `config`
the caller passed, as-is. There's no record of what code, or what environment, a run
was executed with.

## Why this is a problem

Half the value of MLflow's autolog comes from "what it was run with" getting captured
automatically. Right now, unless the user explicitly puts it into config, that run
becomes unreproducible a few weeks later. The chart is still there but you can't
reproduce it — that's the worst form of degradation.

## To do

In `init()`, automatically collect the following and merge it into config under the
`_meta.*` namespace (reserve this prefix so it doesn't collide with user config keys):

- `_meta.git.commit` / `_meta.git.branch` / `_meta.git.dirty` — state of the repository
  containing the training script. Silently omit if `git` is unavailable or we're
  outside a repo
- `_meta.cmdline` — `sys.argv` (may contain secrets, so follow the masking rule below)
- `_meta.python` / `_meta.platform` / `_meta.hostname`
- `_meta.gpu.name` / `_meta.gpu.count` / `_meta.cuda` — via torch if available,
  otherwise `nvidia-smi`. Omit if neither is available
- `_meta.requirements_sha256` — a hash equivalent to `pip freeze` (hash only, since the
  full text is long; if the full text needs to be kept, handle it via artifacts ->
  `todo/exp-run-artifacts.md`)

Constraints:

- Collection is **best-effort**. If any single item fails, silently drop that key.
  `init()` must never raise an exception due to environment quirks (same existing
  policy as the shim: don't propagate network failures to the caller)
- Allow disabling the whole thing via `THINKINGFACE_META=off`
- `_meta.cmdline` should mask values following flags that look like `--token`
  `--password` `--api-key`, etc. Document clearly in the README that collected values
  get sent to the server without the user necessarily noticing

On the UI side, show this in its own section separate from config on the run detail
page (-> `todo/exp-run-detail-page.md`). In the config diff view
(`frontend/components/experiments/config-diff-table.tsx`), it would be good to collapse
`_meta.*` by default with a toggle to expand it.

## Definition of done

- `init()` doesn't raise in environments without `git`, without a GPU, or run outside a
  repository
- Collected values land in the run's config and are visible in the UI
- There's a unit test for the masking logic
- `clients/python/README.md` documents what gets collected and how to disable it
