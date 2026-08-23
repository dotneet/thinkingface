# Run -> generated model link

Experiment tracking / Priority: medium

## Current state

The `lineage:` block in `repocard` lets you declare "model -> originating run / dataset
/ base model," and `backend/internal/syncer/lineage.go` indexes it into `repo_lineage`
so the UI can traverse it in both directions. However, the declaration is only made
from the model repository's README side — **there's no entry point for the training
script (the run) to register "this run produced this model."**

## Why this is a problem

The workflow ends up being: right after a training job pushes a model, someone has to
manually add the corresponding run info into the README. If they forget, the lineage
chain breaks. In MLflow, `mlflow.log_model` / the registry handles this connection.

## To do

1. Add `trackio.log_model("ns/name", revision=None)` to the Python shim
   - If `revision` is omitted, resolve it to HEAD right after the push
   - Store it internally as an annotation on the run (a dedicated field, not config)
2. Add `models: {repo_id: string; revision: string}[]` to `ExpRun`
   (`backend/internal/apitypes` -> `make gen-types`)
3. Link from the run detail page to the model page, and also surface "generated from
   this run" in the model's lineage view (need to decide whether to add the same edge
   to `repo_lineage`, or derive it from the run side)

## Why this is a better design than MLflow's

MLflow's Model Registry makes a copy of the artifact upon registration. thinkingface
**just points to a git revision**, so the actual content stays as a single copy, and
promotion/rollback can be expressed via tags/branches. There's no need to port over the
stage/alias concept (MLflow itself has deprecated stage in favor of alias).

## Definition of done

- From a run that called `log_model`, you can navigate to the matching model revision
- From the model's lineage view, you can navigate back to that run
- Behavior is defined for the case of pointing at a repository/revision that doesn't
  exist (e.g. keep the record but show a warning in the UI)
