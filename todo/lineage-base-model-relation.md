# Lineage relation kinds (finetune / adapter / quantized / merge)

Lineage / Priority: high (currently the clearest gap in HF compatibility)

## HF Hub's spec

A model card's `base_model:` can be either a single ID or a list, and the Hub
**infers the relation kind from this model to the base model**: `finetune` /
`adapter` / `quantized` / `merge`. To override the inference, you can be explicit, e.g.
`base_model_relation: quantized` (listing multiple `base_model` entries is treated as
a merge).

The model page's **Model Tree** groups derived models by this kind, and the model
listing supports filtering via `base_model_relation=base` (the "base models only"
toggle) or `?other=base_model:quantized:{id}`.

## Current state (thinkingface)

- We read `lineage.base_model` / the top-level `base_model:` and attach only a single
  edge kind, `base_model`
  (`backend/internal/repocard/repocard.go`, `backend/internal/syncer/lineage.go`)
- `LineageEdgeKind` has exactly three values: `"dataset" | "base_model" | "run"`
  (`docs/api-contract.md` §12)
- **`base_model_relation` doesn't exist anywhere in the repository** (confirmed via
  grep)
- In the UI (`frontend/components/repo-pages/lineage-section.tsx`), downstream items
  aren't split by kind — fine-tunes, quantized versions, and merges all sit in the same
  single list

Even for internal use, "show me only the quantized versions of this base model" or
"show me only the LoRA adapters" comes up quickly, and the downstream list becomes
unreadable as derivatives pile up.

## To do

1. Read `base_model_relation` from the card
   - Values follow HF: `finetune` / `adapter` / `quantized` / `merge`. Keep unknown
     values as the raw string and let the UI bucket them as "other" (same policy as
     dangling references)
   - Decide whether to also allow `relation:` inside the `lineage:` block, or only look
     at the top-level `base_model_relation:` (if prioritizing HF compatibility, make
     the latter mandatory)
2. Implement inference for when it's unspecified
   - 2+ `base_model` entries -> `merge`
   - Repository has `adapter_config.json` -> `adapter`
   - Files/config indicating GGUF or quantization are present -> `quantized`
   - Otherwise -> `finetune`
   - The signals for this are already available in `backend/internal/modelmeta`
     (reads safetensors / PyTorch headers) and `repo_files`. Do the inference within
     what's available without dropping headers
3. Add a `relation` column to `repo_lineage` (migrations for both postgres and sqlite).
   Add `relation` to `LineageRef` / `LineageDependent` and run `make gen-types`
4. Group the UI's downstream list by kind (equivalent to HF's Model Tree). Show a count
   badge, and collapse each group to a few items by default
5. Update `docs/api-contract.md` §12

## Definition of done

- A card written with `base_model_relation:` for HF is interpreted as-is
- Cards that leave it unspecified still get an inferred kind
- The model page's derivative list is displayed grouped by kind
- The inference logic has unit tests (factored out as a pure function)
