# UI/UX Review and Improvement Proposals

Target: `frontend/` (Next.js 15 App Router / Tailwind v4).
Live verification was carried out against `http://localhost:3000` with `make up` running.

The design system (semantic tokens in `app/globals.css`, the primitives in
`components/ui/`, the global `:focus-visible` ring, the consistent 3-state pattern, the
i18n dictionaries) is well maintained. What follows focuses strictly on **the usability
problems that remain on top of it**.

The problem running through all of it can be summarized as one thing:

> **When state changes, the element you're currently interacting with moves.**

"Clear filters" is the clearest example, and the same pattern recurs across forms,
dialogs, toolbars, and tables.

---

## 1. Highest priority: the thing you're interacting with moves (layout shift)

### 1-1. The facet sidebar's "Clear filters" — the issue itself

`frontend/components/repo/repo-facet-sidebar.tsx:125`

```tsx
{hasActiveFilters && (
  <Button variant="ghost" size="sm" className="self-start" ...>
    {t("repoList.clearFilters")}
  </Button>
)}
```

The button is inserted at the **top** of the `aside` the instant even one filter is
applied.

Measured (`/models`, desktop):

| | Before clearing | After clearing |
|---|---|---|
| Button height + `gap-5` | — | 26px + 20px = **46px** |
| Facet row pitch | 29px | 29px |

In other words, **checking one checkbox shifts every facet after it down by 46px**. Since
that's larger than the 29px row pitch, when a user tries to "select two tags in a row," the
second click misses its intended row (in most cases it lands on a group heading's
whitespace = no-op). When the last item is unchecked, the same thing happens in reverse —
everything jumps back up by 46px. Because `hasActiveFilters` also looks at the search
term, the sidebar jumps the instant you type into the search box at the top and hit Enter.

**Fix A (minimal)**: keep the row itself permanently present and only swap its contents.

```tsx
{/* Always reserve the height. The aside's top position never moves regardless of filters */}
<div className="flex min-h-[26px] items-center justify-between">
  <GroupTitle>{t("repoList.filters.title")}</GroupTitle>
  {hasActiveFilters && (
    <Button variant="ghost" size="sm" onClick={...}>{t("repoList.clearFilters")}</Button>
  )}
</div>
```

**Fix B (recommended — also resolves 1-2 at the same time)**: move the clear affordance
out of the top of the sidebar and place it as an "active-filter chip row" **directly above
the result list** (on the result-column side of `RepoListPage`). Keep the chip row
permanently present on the same line as the result count (`n results`); when there are no
filters, it just shows the count. This removes the variable element from the sidebar
entirely, so the click origin never moves.

### 1-2. "What am I currently filtering by" is sometimes invisible from the list side, or impossible to remove

- "Clear filters" only shows up in `EmptyState` when there are 0 results
  (`frontend/components/repo/repo-list-page.tsx:188`). **When there are, say, 3 hits, there
  is no clear affordance on the list side at all**, forcing the user's eyes back to the
  sidebar.
- `/orgs` is worse: "Clear search" appears **only** in the empty state
  (`frontend/app/orgs/page.tsx`). Pressing the native × on `type="search"` only clears the
  input without submitting, so you get a "I pressed × but nothing changed" state.
- **A selected facet value can disappear from the list entirely.** The backend's facet
  design is good — it excludes its own filter when aggregating
  (`backend/internal/store/repos_test.go:87`) — but the tag facet still honors the license /
  task / relationship filters. That means if `tags=A` + `license=X` yields 0 matches, **the
  checkbox for A disappears from the list, with no way left to uncheck it individually**
  (`FacetGroup` only renders values present in `facets.tags`). The only escape hatch is
  "clear all."

**Fix**: keep the active filters **permanently displayed as a chip row directly above the
results**, with an × on each chip and a "clear all" at the end. Because the chips are
built from the URL values rather than from the facet aggregation results, a facet value
that has disappeared from the facet list can still always be removed. `ActiveRef` (inside
`repo-facet-sidebar.tsx`) already has this exact shape for `base_model` / `dataset`, so it
makes sense to lift it into `components/ui/` as `FilterChip` and apply it uniformly to
tags / license / task / relation / archived / search.

### 1-3. Whole facet groups disappear

`frontend/components/repo/repo-facet-sidebar.tsx:262`

```tsx
if (items.length === 0) return null;
```

As filtering narrows things down, entire groups vanish and the groups below shift up.
This causes the same "the thing below jumps right after you click" problem as 1-1.

**Fix**: keep the group's frame and heading, and render `count 0` values as `disabled`
with strikethrough (the Amazon / HF approach). At minimum, keep "the currently selected
value" and "any group that has ever been shown."

### 1-4. The error Alert is inserted directly above the confirm button (cross-cutting)

The same pattern appears in 10+ places:

| Location | Line |
|---|---|
| `components/ui/confirm-dialog.tsx` | 89 (Cancel / Confirm sit right after `{error && <Alert>}`) |
| `components/repo/create-repo-form.tsx` | 172 (the "Create" button right after) |
| `components/auth/login-form.tsx` | 138 |
| `components/orgs/create-org-form.tsx` | 101 |
| `components/orgs/org-profile-form.tsx` | 120 |
| `components/repo/file-editor.tsx` | 108 |
| `components/experiments/run-delete-dialog.tsx` | 70 |
| `components/settings/*-manager.tsx` | various |

The pattern "submit → fails → an error grows in and the button drops" means **a second
tap either misses the button entirely, or lands on a different control right below it**.
In delete-confirmation dialogs it isn't bad enough to swap "Cancel" and "Delete," but the
risk of a mis-click is real.

**Fix**:
1. Move the error display **below** the action row (so the button's position never moves), or
2. Pin the footer that holds the action row, and put the error in the scrolling body instead.
3. The most robust fix is to add a "footer" slot to `Dialog` itself, forcing every call
   site into a `body (variable) / footer (fixed)` structure.

### 1-5. Inline Spinner insertion re-wraps the toolbar

- `components/experiments/experiment-dashboard.tsx:311` (the `annotate.isPending` spinner
  appears at the end of a `flex-wrap` toolbar)
- Same file `:410` (spinner while metrics are loading)
- `components/parquet/parquet-viewer.tsx:185` (the spinner shown while rows are loading
  appears next to the "rows per page" select)

Adding an element to a `flex-wrap` row **changes where the wrap happens, sending the
adjacent select or button onto a different line**. Because the fetch typically fires right
after the user operates that very select, this is easy to trigger.

**Fix**: reserve a fixed-width status slot and make it `invisible` (not `hidden`) when not
shown. Either add a `reserveSpace`-style wrapper to `components/ui/spinner.tsx`, or wrap it
in `<span className="inline-flex w-5 justify-center">`.

### 1-6. Experiment dashboard: the view switcher and chart sit below the table

`components/experiments/experiment-dashboard.tsx:356` (`VIEWS.map`) sits **below**
`RunTable`. With 50 runs, every time you touch a chart setting you have to scroll all the
way past the whole table. Worse, changing the table's filter changes the row count,
**bouncing the view switcher and chart below it up and down**. When the metrics filter
yields 0 results, the table is replaced with `EmptyState`, shifting things by hundreds of
pixels.

**Fix**:
- Move the view switcher (metrics / config diff / scatter plot / parallel coordinates)
  **above the table**, next to the filter bar. While at it, use
  `components/ui/segmented-control.tsx` instead of a row of `Button`s (currently the same
  concept has two different visual treatments).
- Give `RunTable` a max height plus a `sticky thead` so the table's height doesn't thrash
  with the row count.

### 1-7. Column width changes when the sort arrow appears

`components/experiments/run-table.tsx:615`

```tsx
{active && (sort?.dir === "asc" ? <ArrowUp .../> : <ArrowDown .../>)}
```

Because the icon is only added when active, **every header click changes that column's
width, shoving the adjacent column heading sideways**. This causes a miss when the user
tries to change the sort column repeatedly in quick succession.

**Fix**: always render the icon slot, and make it `opacity-0` when inactive (or show a
faint neutral `ChevronsUpDown` — the latter also communicates that the column is
sortable).

---

## 2. Consistency around search

`components/search-box.tsx:14`

```ts
const target = pathname.startsWith("/models") ? "/models" : "/datasets";
```

1. **Searching from `/experiments` or `/orgs` redirects to `/datasets`.** The placeholder
   reads "Search datasets, models…" (`lib/i18n/dictionaries/*/common.ts`), but only one of
   the two is actually searched. The promise and the behavior disagree.
2. **The current filters are all discarded.** Searching from the header while on
   `/models?tags=uiaudit&license=apache-2.0` produces `/models?q=…`, dropping both the tag
   and the license filter. The user assumed they were "searching within the filtered set."
3. **The header's input field doesn't reflect the URL's search term**
   (`defaultValue` is always empty, with no syncing either). The header stays empty even
   after a search. The list-side `RepoListFilters` already syncs via `useEffect`, leaving
   this as the one place left behind.
4. Only the header still emits the legacy `q=`. `repoListHref` normalizes it to `search=`
   so the result is the same, but there's no longer any reason for two spellings to
   coexist.

**Fix**:
- Either build a cross-cutting `/search` page (bundling models / datasets / experiments
  under tabs), or make the header search scoped (`[All ▾] Search…`). At minimum, make it
  possible to search experiment repositories from `/experiments`.
- Header search should swap out only `search=` while preserving the current `searchParams`.
- Pass `defaultValue` from the server, and add the same `useEffect` syncing that
  `RepoListFilters` has.

**Related**: `components/orgs/org-search.tsx` also only has `useState(defaultValue)` with
no syncing, so **using the browser back button leaves the input field showing the old
term** (the same bug that `RepoListFilters` already fixed via `useEffect`).

---

## 3. `/experiments` has neither search nor paging

`app/experiments/page.tsx` simply calls `listExperiments({})`. The backend already
supports `limit` / `offset` / `total` (`backend/internal/api/experiments.go:31`, capped at
100), and the Experiments tab on the namespace page already paginates — yet **the global
list effectively has no results beyond the 100th**. In the local environment there are
already 25 entries listed with no filtering, and the only way to find a specific
experiment is Ctrl+F.

**Fix**: add the same `RepoListFilters` + `Pagination` used by `/models` and `/datasets`
(at minimum search, sort, and paging).

---

## 4. Table usability

- **`RunTable`'s `thead` isn't sticky** (`components/experiments/run-table.tsx:153`), while
  `components/ui/data-table.tsx` is — behavior diverges within the same app. With dozens of
  runs, you end up scrolling vertically with no idea which column is accuracy.
- **The run-name column isn't pinned during horizontal scroll.** With `min-w-[960px]` plus
  a growing number of metric columns, scrolling right leaves you unable to identify the
  row. The name column needs `sticky left-0`.
- **The select-all checkbox has no `indeterminate` state.** Partial selection and no
  selection look identical.
- **`FileTreeTable` has no "up one level" row** (`components/repo/file-tree-table.tsx`) —
  the breadcrumb is the only way back. GitHub and HF both show a `..` row.
- The same table also **doesn't make the whole row clickable** (only the name cell's link
  works). Making the entire row clickable is the usual list convention.

---

## 5. Parquet viewer

- **Switching between Rows and SQL discards the SQL.**
  (`components/parquet/parquet-viewer.tsx:159` conditionally mounts `SqlConsole`.) Going
  back to Rows to check the schema loses your half-written query, and coming back
  **re-downloads the ~35MB wasm plus the file itself**.
  → Lift the `sql` state (and the session, if possible) up into `ParquetViewer`, and make
  `SqlConsole` toggle visibility (`hidden`) instead of unmounting. At minimum, preserve the
  query string.
- **The SQL error Alert and the truncated Alert are inserted between the Run button and
  the result table** (`components/parquet/sql-console.tsx:221,229`). Every time you fix an
  error and re-run, the result table jumps up and down. Same fix as 1-4 (message below the
  result, or a fixed-height slot).
- **The schema panel has no column filtering or "show all / hide all"**
  (`components/parquet/schema-panel.tsx`). Deselecting everything on a wide file is
  tedious. Worse, **every single checkbox toggle changes `queryKey` and triggers a
  refetch**, so toggling multiple columns stacks up round trips. Either debounce it or add
  an explicit "apply" step.
- The pager has no "jump to first" (only `ChevronLeft` / `ChevronRight`).

---

## 6. Mobile

Opening `/models` at 375px width requires **about 1,000px of scrolling before you reach
the first result card**. `RepoFacetSidebar` sits at the top of a `flex-col lg:flex-row`
layout, so all the facets stack vertically and push the results down (this gets worse the
more facet values there are).

**Fix**: below `lg`, collapse it into a `<details>` or a Dialog (bottom sheet), showing
only a trigger button like `Filters (3)`. `components/repo/readme-toc.tsx` already has
exactly this structure — `<details>` that's `lg:hidden` alongside a `lg:flex` sticky
version — so that pattern can be reused directly.

---

## 7. Small accessibility gaps

- The trigger in `components/ui/dropdown-menu.tsx` has no `aria-expanded` /
  `aria-haspopup` (`components/mobile-nav.tsx` does have them). Same for `RefSwitcher` /
  `UserMenu`. Since `DropdownMenu`'s `trigger` render prop already passes `open`, either
  call sites should add `aria-expanded={open}`, or the primitive itself should enforce it.
- `RefSwitcher` has no branch/tag filter input (only `max-h-80` scrolling). In a real
  repository with dozens of branches, you can't find the ref you're after.
- The word for "clear" has three different spellings: `repoList.clearFilters` (clear
  filters) / `repo.clearFilter` (dismiss filter) / `org.clearSearch` (clear search
  criteria). Unify these in the dictionary.

---

## 8. Proposal to codify as a convention (add §8 to `frontend/DESIGN.md`)

To prevent recurrence, it would help to write the following down formally and make it
something `bun run check:ui` checks for.

> **§8 Layout stability**
> 1. Asynchronous outcomes (error, success, spinner) must **never move an existing
>    interactive element**. Put messages **below** the action row, or in a slot with a
>    reserved height.
> 2. An element that appears via a toggle must **reserve its space** before it appears
>    (`invisible` / `min-h-*`). Never place `{cond && <Button/>}` at the start or middle of
>    a flex container.
> 3. A filter's "clear" affordance must be reachable **from both where the filter was set
>    and from where the results are viewed**. An implementation that puts it only in the
>    empty state is not acceptable.
> 4. A list's header (`thead`) must be sticky, and identifier columns must be
>    `sticky left-0`.

---

## Priority summary

| # | Item | Impact | Ease of fix |
|---|---|---|---|
| 1 | 1-1 the facet "Clear filters" causes a 46px shift | High (hit every time) | Small |
| 2 | 1-4 confirm buttons drop on error (10+ places, cross-cutting) | High | Medium (footer slot on `Dialog`) |
| 3 | 1-2 turn active filters into chips (including the rescue for un-removable values) | High | Medium |
| 4 | 6 on mobile, facets push down the results | High (mobile) | Small–Medium |
| 5 | 3 `/experiments` missing search/paging | High (capped at 100) | Small (reuse existing parts) |
| 6 | 1-6 move the experiment dashboard's view switcher above the table | Medium | Small |
| 7 | 2 header search scope / filter discarding / syncing | Medium | Medium |
| 8 | 5 Parquet Rows⇄SQL loses the query and re-downloads | Medium | Medium |
| 9 | 4 sticky thead / pinned name column / `..` row | Medium | Small |
| 10 | 1-3, 1-5, 1-7 other shifts, 7's a11y | Low–Medium | Small |

---

## Status (implemented 2026-08-23)

Items 1 through 10 above have all been addressed. `make check` passes in full (backend /
frontend / python / contract). The recurrence-prevention convention has been codified as
`frontend/DESIGN.md` §8 "Layout stability."

| # | Item | Fix |
|---|---|---|
| 1 | 1-1 the facet "Clear filters" causes a 46px shift | Made the top of the sidebar a fixed-height header row ("Filters" + clear). Measurement confirmed the facet rows' y-coordinates are **exactly identical before and after applying a filter** |
| 2 | 1-4 confirm buttons drop on error | Added a `footer` (pinned action row) and `footerNote` (message shown below it) to `Dialog`, and changed the panel to anchor from the top. Moved the error below the action row in nearly 20 forms. Confirmed in the styleguide that the button's y-coordinate **doesn't move** on a failed Confirm |
| 3 | 1-2 turn active filters into chips | Added a new `FilterChip` in `components/ui/filter-chip.tsx`; `/models`, `/datasets`, `/orgs`, and `/experiments` all now permanently show a "count + chips + clear all" row directly above the results. Because the chips are URL-derived, values that disappeared from the facet list can still be removed |
| 4 | 6 on mobile, facets push down the results | Below `lg`, collapsed into a `<details>` ("Filters (n)"). Confirmed the first card is within the initial viewport at 375px |
| 5 | 3 `/experiments` missing search/paging | Added a `search` query to the backend (delegated to `RepoFilter.Search`, with tests); added a search form + `Pagination` + `FilterChip` + `offsetOutOfRange` branching to the frontend |
| 6 | 1-6 move the experiment dashboard's view switcher above the table | Restructured into "filter bar → view switcher (`SegmentedControl`) → per-view controls → view body → run table." Gave the table area a `max-h` so what's below it no longer jumps |
| 7 | 2 header search scope / filter discarding / syncing | Now derives scope (models / datasets / experiments) from the pathname to match the placeholder, preserves existing params within the same list, syncs the URL's search term into the input, and unified `q=` into `search=`. Also added syncing to `org-search` |
| 8 | 5 Parquet Rows⇄SQL loses the query and re-downloads | Now lazy-mounts only on first use, then keeps it in the DOM via `hidden` afterward (confirmed live that it stays in the DOM). Moved SQL error/truncated messages below the result. Added column filtering and show-all/hide-all to the schema panel; column toggles are now debounced 300ms; added "jump to first" to the pager |
| 9 | 4 sticky thead / pinned name column / `..` row | Made the run table's `thead` sticky, gave the run-name column `sticky left-0` (confirmed it stays pinned to the left after 400px of horizontal scroll), added `indeterminate` to select-all. Added an "up one level" row and whole-row click to the file tree (confirmed clicking empty space navigates to the blob) |
| 10 | 1-3 / 1-5 / 1-7 and dropdown a11y | Selected facet values now always stay in the list even at count 0. Added `SpinnerSlot` (occupies space even when hidden) to keep the toolbar from re-wrapping. Sort arrows now always occupy a slot (inactive shows a faint `ChevronsUpDown`). `DropdownMenu` now passes `aria-expanded` / `aria-haspopup` to the trigger, and `RefSwitcher` now has a ref-filter input |

### Additional issues found and fixed during live verification

- Because of the experiment dashboard's reshuffling, copy such as
  `experiments.dashboard.selectPrompt` that said "from the list above" / "in the table
  above" no longer matched the new layout, so it was rewritten to be
  position-independent (4 keys × en/ja).
- `MetricsCharts` was reusing "please select a run" for the "0 series" case, so a
  dedicated `experiments.chart.noSeries` key was split out.
- Even with a pinned footer, `Dialog`'s panel was still vertically centered, so it
  re-centered and **the button moved by 27px** when an error appeared. Fixed by anchoring
  from the top plus `footerNote`.

### Remaining unverified items

- Rendering of the **row-table body** itself in the Parquet viewer could not be verified
  in the browser, because the local API cannot resolve objects from the GCS emulator (the
  known `gcs` hostname issue). The schema panel, pager, and SQL-tab persistence have all
  been verified live, and typecheck/lint/tests all pass.
