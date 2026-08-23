# Missed lineage-related fields from HF cards

Lineage / Priority: low-to-medium (the first one is cheap to implement)

`docs/api-contract.md` §12 claims to "fall back to the HF-model-card-compatible
top-level `datasets:` / `base_model:` (so cards written for the Hub just work)," but the
following lineage-related fields that HF has aren't read.

## 1. `source_datasets` (dataset -> dataset)

HF dataset cards declare their upstream dataset via `source_datasets` (values are Hub
IDs like `laion/laion-2b`).

Currently, thinkingface can create a dataset -> dataset edge if you write
`lineage.datasets` yourself, but **`source_datasets:` in a dataset card written for HF
is ignored** (the fallback in `backend/internal/repocard/repocard.go` only covers the
top-level `datasets:` / `base_model:`).

-> For dataset repositories, add top-level `source_datasets:` as a fallback for the
`datasets` edge. Keep the existing priority rule where an explicit `lineage:`
declaration wins if present. This is a few lines in the parser plus a test.

## 2. Evaluation dataset edges (`model-index`)

HF lets you structure evaluation results in `model-index:`, where the `dataset:` inside
it refers to **the dataset used for evaluation** (distinct from the dataset used for
training). The Hub renders this in the model page's widget.

The current `datasets` edge only means "used for training," so there's nowhere to
record "a dataset used only for evaluation."

-> Things to consider:

- Extract the evaluation dataset from `model-index` and attach it as an `eval_dataset`
  edge kind
- Displaying the evaluation results themselves (metric values) is **out of scope for
  this item**. If we want that, it's a separate TODO (it overlaps with run summaries on
  the experiment-tracking side, so decide which owns it first)
- HF also has a newer lightweight format (eval-results), so support both formats if we
  go this route

## 3. Things we're deliberately not supporting (already decided)

- `arxiv:` / Paper pages integration — low value given this is for internal use
- Space cards' `models:` / `datasets:` — Spaces-equivalent functionality is a non-goal
  (design doc §1)
- `buckets:` — an HF-specific storage feature

## Where thinkingface is already ahead of HF (keep as-is)

HF's `base_model` / `datasets` can only reference at the **repository** level.
thinkingface can pin a specific revision via `ns/name@rev`, and can also link to a run
(`ns/repo/project/run`). Don't remove this.

## Definition of done

- An HF-origin dataset card that only has `source_datasets:` shows up in lineage
- The existing behavior where an explicit `lineage:` declaration wins is unchanged
