# Search and filtering by lineage

Lineage / Priority: medium

## HF Hub's spec

Lineage acts as **a search axis**. `?other=base_model:quantized:{id}` lets you list
"only the quantized versions of a given model" across the whole hub, and the model
listing's `base_model_relation=base` (the "Base only" toggle) hides all derivatives to
show just the plain base models. On the dataset side too, you can trace "models trained
on this dataset."

## Current state (thinkingface)

`GET /api/v1/repos` filters on `kind` / `q` / `search` / `author` / `tag(s)` /
`license` / `task` / `sort`, and facets are limited to just three dimensions: `tags` /
`licenses` / `tasks` (`docs/api-contract.md` §"GET /api/v1/repos"). **Lineage is in
neither the filters nor the facets.**

In other words, edges exist in `repo_lineage`, but "list every model derived from A"
requires opening repository pages one at a time. This stops scaling once derivatives
grow into the dozens.

## To do

1. Add lineage query parameters to `GET /api/v1/repos`
   - `base_model=ns/name` (only derivatives of that base model)
   - `relation=finetune|adapter|quantized|merge` (depends on
     `todo/lineage-base-model-relation.md` being done first)
   - `dataset=ns/name` (only models trained on that dataset)
   - `base_only=true` (only repositories without a `base_model` edge; equivalent to
     HF's "Base only")
   - Even for edges that carry a revision (`@rev`), match at the **repository level**
     by default (requiring exact revision match would leave results nearly empty in
     practice)
2. Decide whether to add a `relation` dimension to `RepoFacets`. Follow the existing
   facet convention (exclude your own dimension from its own aggregation)
3. UI: add a "base models only" toggle to the listing page's sidebar, and a "view all"
   link from a repository page's derivative group into the filtered listing
4. Update the relevant section of `docs/api-contract.md`

## Implementation notes

This should just require joining `repo_lineage`, but the existing listing query builds
the same SQL as the facet aggregation, so route the added conditions through there.
Follow the same visibility handling as the existing listing (dropping private repos
based on the viewer's permissions).

## Definition of done

- From a base model's page, you can go from "12 quantized versions" to the filtered
  listing
- Results that include private repositories respect the viewer's permissions correctly
  (including in the counts)
