# Artifacts attached to a run

Experiment tracking / Priority: medium-to-high

## Current state

Only numeric metrics and config can be recorded. There's no way to attach a confusion
matrix image, eval output JSON, generated samples, or a full `pip freeze` dump to a run.
There's no equivalent of MLflow's `log_artifact`.

## Why thinkingface has the advantage here

MLflow's artifact store is just an opaque blob dump under
`experiment_id/run_id/artifacts/...`, with no history and no diffs. thinkingface
already has git + LFS + signed URLs + `exports/`, so **the artifact itself can be
version-controlled and pulled with `gcloud storage cp`**. The infrastructure is already
there; all that's missing is the API and the UI wiring.

## To do

1. Add `trackio.log_artifact(path, name=None)` to the Python shim (trackio and wandb
   both have an API of the same name, so this stays within the compatible-signature
   range)
2. Standardize the storage destination as
   `{project}/artifacts/{run}/{name}` in the same experiment dataset repository
   - Upload through the existing HF-compatible preupload / commit path as-is. Don't add
     a dedicated new entry point
   - Large files go to LFS via the default `.gitattributes` as usual
3. Add `GET /api/v1/experiments/{ns}/{repo}/{project}/runs/{run}/artifacts` (this is
   really just a git tree listing, so it can be derived straight from the path)
4. Show the list on the run detail page (-> `todo/exp-run-detail-page.md`). Link to the
   existing file viewer (images / Parquet / text are already viewable there)

## Notes

- Commit count will increase. To handle usage patterns that dump many artifacts per
  run, batch them on the shim side before sending (e.g. flush them all together on
  `finish()`)
- Fix the naming convention (`{project}/artifacts/{run}/`) in `docs/api-contract.md`.
  Confirm it doesn't collide with the metrics detection in
  `backend/internal/experiments/layout.go`

## Definition of done

- Files sent via `log_artifact` are reachable from the run detail page and included in
  `git clone`
- They can be fetched via `gcloud storage cp` through `exports/`
