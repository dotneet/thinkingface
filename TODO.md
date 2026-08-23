- [x] Ownership transfer of models/datasets between users, designed so the transfer doesn't require moving the actual data. (`docs/repo-transfer-design.md`, feat/repo-transfer)
- [x] Delete/archive functionality for models, datasets, and experiments
- [x] Security and abnormal-case UX / input validation improvements
- [x] Make the `/<namespace>` path list that namespace's models, datasets, and experiments, matching Hugging Face's behavior.
- [x] Add SSH key registration, needed to clone private models/datasets etc.
- [x] In the experiment view, dragging to zoom in leaves no way to zoom back out. Add that, and improve the UX overall.
- [x] Experiments: Parquet flush at ingest points (the design's §8 promise was never implemented — data just piles up in the DB) ([todo/exp-ingest-parquet-flush.md](todo/exp-ingest-parquet-flush.md))
- [x] Experiments: automatic recording of run execution-environment metadata (git SHA / command line / GPU / dependencies) ([todo/exp-run-env-metadata.md](todo/exp-run-env-metadata.md))
- [x] Experiments: a run detail page (there's still nowhere to show execution environment, notes, or artifact locations) ([todo/exp-run-detail-page.md](todo/exp-run-detail-page.md))
- [x] Experiments: record artifacts attached to a run, equivalent to MLflow's log_artifact ([todo/exp-run-artifacts.md](todo/exp-run-artifacts.md))
- [x] Experiments: link a run to the model it produced, establishing lineage from the training script side ([todo/exp-run-model-link.md](todo/exp-run-model-link.md))
- [x] Experiments: an autolog callback for HF Trainer / Lightning ([todo/exp-autolog-trainer.md](todo/exp-autolog-trainer.md))
- [x] Experiments: collect and display system metrics (GPU / CPU / memory) ([todo/exp-system-metrics.md](todo/exp-system-metrics.md))
- [x] Experiments: run grouping and a sweep comparison UI (parallel coordinates plot) ([todo/exp-run-grouping.md](todo/exp-run-grouping.md))
- [x] Experiments: support resuming runs (charts must stay connected across interruption and resume) ([todo/exp-run-resume.md](todo/exp-run-resume.md))
- [x] Experiments: scaling metrics retrieval (step rollup) ([todo/exp-metrics-scale.md](todo/exp-metrics-scale.md))
- [x] Lineage: introduce relation types (finetune / adapter / quantized / merge) with automatic inference, compatible with HF's base_model_relation ([todo/lineage-base-model-relation.md](todo/lineage-base-model-relation.md))
- [x] Lineage: search and filtering by lineage (base_model= / dataset= / base models only) ([todo/lineage-search-facets.md](todo/lineage-search-facets.md))
- [x] Lineage: declaring successor versions (equivalent to HF's new_version, paired with the archive feature) ([todo/lineage-new-version.md](todo/lineage-new-version.md))
- [x] Lineage: fields missing from HF cards (source_datasets / model-index eval datasets) ([todo/lineage-hf-card-fields.md](todo/lineage-hf-card-fields.md))

## Remaining tasks (found while working on the above)

- [ ] Metrics: the unfiltered all-run chart (50 runs x no keys specified) is still at 8.5s. Row-group pruning doesn't help here, so the leading candidate is having the API cap how many runs can be rendered in a single request ([todo/exp-metrics-scale.md](todo/exp-metrics-scale.md))
- [ ] Cap the number of entries for `recursive=true` in `handleHFTree`. A `truncated` field isn't part of the HF contract and could break `huggingface_hub`'s `list_repo_tree`, so this needs proper design ([todo/security-audit-findings.md](todo/security-audit-findings.md) S9 fix proposal 4)
- [ ] A namespace-existence API. Right now `/<ns>` infers existence from "0 visible repos -> 404", so even an empty namespace returns 404
- [ ] The repo page's clone-URL widget is HTTPS-only. Showing an SSH URL requires the public host setting to be exposed in the API response
- [ ] SSH E2E tests (not added yet since they require key generation; the same protocol surface is already covered by in-process SSH tests)
- [ ] `loading.tsx` for the subroutes under `frontend/app/{datasets,models}/[ns]/[name]`
- [ ] No React/DOM test infrastructure (`vitest.config.ts` has `environment: "node"`). Adopting jsdom is a decision that needs to be made before component tests for things like `ConfirmDialog` can be written
