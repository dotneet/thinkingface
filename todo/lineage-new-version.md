# Declaring a successor version (`new_version`)

Lineage / Priority: medium

## HF Hub's spec

Writing `new_version: l3utterfly/mistral-7b-v0.1-layla-v4` in a model card makes a link
to the latest version appear on the model page. If the linked target itself has a
`new_version`, the **chain is followed to show the very latest version**.

## Current state (thinkingface)

There's no equivalent concept (`new_version` doesn't exist anywhere in the repository —
confirmed via grep). Git revisions can only express versioning within the same
repository; there's nowhere to record a **successor relationship across separate
repositories**, e.g. `team/foo-v1` -> `team/foo-v2`.

What actually happens internally is: "we can't delete the old model (some job still
depends on it), but we want new users to use v2" — this is exactly the mechanism for
that. It also dovetails with the TODO for "delete/archive functionality for models,
datasets, and experiments" (archiving and successor links tend to be used together).

## To do

1. Read the card's top-level `new_version:` (HF-compatible) and `lineage.new_version:`
2. Add a `new_version` edge kind to `repo_lineage` (extend `LineageEdgeKind`). Allow the
   same for dataset repositories too (HF restricts this to models, but there's no
   reason for us to)
3. Resolve the chain
   - The repository page shows the **final destination** (same as HF)
   - Guard against infinite loops from circular references with a depth cap (e.g. 8).
     Once the cap is hit, show just the immediate successor and a warning
4. UI: show a "A newer version is available: {link}" banner at the top of the
   repository page (use the `Alert` primitive from `components/ui/`; don't write raw
   elements)
5. Also surface the reverse direction ("this is the successor to {old version}") as
   downstream
6. Add to `docs/api-contract.md` §12

## Definition of done

- Chaining v1 -> v2 -> v3 makes v3 show up on v1's page
- Writing a cycle doesn't cause an infinite loop or break the UI
- Pointing at a repository that doesn't exist or isn't accessible is treated as
  dangling (not turned into a link, per existing policy)
