# thinkingface UI design guide

One page. Read it before adding a screen; run `bun run check:ui` before pushing.

The live inventory of everything described here is at **`/styleguide`** (development only —
the route calls `notFound()` in production).

---

## 1. Colour: semantic tokens only

Colours are declared once in `app/globals.css` as `--tf-*` custom properties, in three blocks:
the light values on `:root`, and the dark values twice — under
`@media (prefers-color-scheme: dark) :root:not([data-theme="light"])` for the system default and
under `:root[data-theme="dark"]` so the toggle wins. The `@theme inline` block maps each one to a
Tailwind colour, which is what you write in components:

| Token | Utilities | Means |
| --- | --- | --- |
| `bg` | `bg-bg` | the page canvas; set on `body`, rarely written by hand |
| `bg-raised` | `bg-bg-raised` | **above** the canvas: cards, the sticky header, menus, dialogs |
| `bg-sunken` | `bg-bg-sunken` | **below** the canvas: inputs, table headers, code blocks, wells |
| `bg-hover` | `bg-bg-hover` | hover feedback only — never a resting background |
| `border` | `border-border` | the default hairline on every box |
| `border-strong` | `border-border-strong` | emphasis: hovered cards, blockquote rules |
| `border-control` | `border-border-control` | the boundary of an **interactive control** (input, select, checkbox) — the only border token that clears 3:1 (WCAG 1.4.11) |
| `fg` | `text-fg` | primary text, headings, the value in a label/value pair |
| `fg-muted` | `text-fg-muted` | body copy, form labels, table cells |
| `fg-subtle` | `text-fg-subtle` | metadata, timestamps, placeholders, icons beside text |
| `accent` | `bg-accent` / `text-accent` | the primary action and the active state |
| `accent-fg` | `text-accent-fg` | text drawn **on** `accent` |
| `accent-muted` | `bg-accent-muted` | accent chips, the active nav item |
| `positive` | `text-positive`, `bg-positive/15` | success, "finished" |
| `negative` | `text-negative`, `bg-negative/10` | errors, destructive actions |
| `warning` | `text-warning`, `bg-warning/10` | degraded or in-progress (indexing, conflicts) |
| `*-strong` | `text-accent-strong`, `text-positive-strong`, `text-negative-strong`, `text-warning-strong` | the same hue, dense enough to be **text or an icon on its own tinted fill** |

The surface tokens form a **depth ladder** — `sunken → bg → raised`. Pick by depth, not by
lightness: the ladder inverts between themes and picking by "the grey one" breaks dark mode.

The status tokens come in **two densities**, and which one you want depends on what is behind
the text. `text-warning` is for warning-coloured text on a *neutral* surface; the moment the text
sits on a fill of its own hue (`bg-warning/20`, `bg-positive/15`, `bg-accent-muted`) the tint
darkens the background by the same hue and the pair collapses — `text-warning` on `bg-warning/20`
measured **2.12:1** in the light theme. That case takes `text-warning-strong`. `Badge` and
`Alert` already do this; follow them rather than reaching for the base token.

Every foreground/surface pair in the table clears **4.5:1 (WCAG AA) in both themes**, on every
rung of the depth ladder including `bg-hover`. `fg-subtle` in particular is *not* a decorative
grey — it is the most-used foreground token in the app and it is held to the same bar as `fg`.
This is asserted, not aspirational: `lib/design-tokens.test.ts` parses the oklch values straight
out of `globals.css`, converts them, and fails on any pair below the floor (it also catches the
two dark blocks drifting apart). Change a lightness value and run `bun run test`.

Rules:

- **Never** write `bg-[var(--tf-bg-raised)]`. The mapped utility is the only spelling.
- **Never** use a raw Tailwind palette colour (`bg-slate-800`, `text-red-500`, …). They are fixed
  values that do not swap with the theme.
- Opacity works normally: `bg-positive/15`, `border-warning/40`.
- Raw `var(--tf-*)` in a **JS value** (not a `className`) is fine and sometimes required — uPlot
  takes stroke colours as strings, for example. The check only looks inside `className`.

## 2. Spacing, radius, typography

Observed and enforced by convention, not by tooling:

- **Radius** — `rounded-lg` for boxes (cards, tables, panels, dialogs), `rounded-md` for controls
  (buttons, inputs, chips, menu items), `rounded-full` for avatars and badges.
- **Padding** — controls `px-3 py-1.5` (small: `px-2 py-1`); boxed content `p-4`; prose-like
  content `p-6`; table cells `px-3 py-2`.
- **Gap** — `gap-2` between siblings in a row, `gap-1.5` for icon + label, `gap-3`/`gap-4` between
  blocks in a column, `gap-6` between page sections.
- **Type** — `text-sm` is the body size, the default for controls, **and the size of table cells
  that carry the data itself** (`DataTable`); `text-xs` is for metadata *around* content —
  timestamps, badges, table headers, hints — not for content the reader came to read.
  `text-2xl font-semibold tracking-tight` for a page title; `text-3xl` only for the home hero.
  Weights: `font-medium` for labels and controls, `font-semibold` for headings.
- **`text-xs` + `text-fg-subtle` always carries `font-medium`.** Both are the quiet end of their
  scale, and 12px at 400 in the subtle grey is the least legible thing the token set can produce.
  Enforced by `check:ui`'s `subtle-xs-weight` rule.
- **Leading** — `--text-xs--line-height` / `--text-sm--line-height` are raised to 1.5 / 1.6 in
  `globals.css` (Tailwind ships 1.333 / 1.429). The UI is bilingual and Japanese sets far more ink
  per line than Latin; do not undo this with `leading-tight` on a block of running text.
- **Numbers** — anything a reader compares column-to-column gets `tabular-nums`.
- **Monospace** — `font-mono` for paths, revisions, tensor names, dtypes and tokens.

### Fonts

`--font-sans` is Figtree **plus a CJK tail** (`Hiragino Sans` → `Noto Sans JP` → `Yu Gothic UI` →
`Meiryo` → `sans-serif`), declared in `globals.css`'s `@theme inline`. next/font's variable stops
at `Figtree, "Figtree Fallback"`, neither of which has a Japanese glyph, so without the tail every
Japanese string in the UI renders in whatever the browser's default family happens to be. The app
ships a Japanese locale (§7); the font stack is part of that, not an afterthought.

## 3. Icons

`lucide-react`, always.

- `size={16}` inline with body text (`text-sm`); `size={12}`–`size={14}` inside `text-xs` rows,
  badges and small buttons.
- `size={28}` with `strokeWidth={1.5}` for the illustration in `EmptyState` / `ErrorState`.
- An icon that carries meaning on its own needs an `aria-label` on the control around it; an icon
  next to a text label is decorative and needs nothing.

## 4. Every screen has three states

A screen that only handles the happy path is not finished. Use the primitives — do not hand-roll
a "Loading…" paragraph:

| State | Component | Notes |
| --- | --- | --- |
| loading | `Skeleton` / `SkeletonLines`, or `Spinner` | Skeleton for first paint (mirror the shape of the real content); Spinner only for a short in-place refresh of content already on screen |
| empty | `EmptyState` | icon + title, optional description and a single action |
| error | `ErrorState` | `title` and `message` required, optional `hint` for "what to try", optional action |

`apiFetch` never throws — it returns `{ ok: false, status, message }` — so an error state is
always reachable and always your responsibility to render. `Alert` covers the inline case: a
message attached to a form or a banner on an otherwise working page.

## 5. Primitives first

`components/ui/` is the vocabulary. **A new look goes into `ui/` first, and only then gets used.**

| Primitive | File | Variants |
| --- | --- | --- |
| `Button`, `buttonClass` | `ui/button.tsx` | `variant`: primary / secondary / ghost / danger; `size`: sm / md |
| `Badge`, `badgeClass` | `ui/badge.tsx` | `tone`: neutral / muted / accent / positive / negative / warning |
| `Alert` | `ui/alert.tsx` | `tone`: info / positive / negative / warning; optional `icon` |
| `Input`, `Textarea`, `Select`, `Checkbox`, `Slider`, `Field` | `ui/field.tsx` | — |
| `Card`, `CardHeader`, `CardTitle` | `ui/card.tsx` | — |
| `Dialog` | `ui/dialog.tsx` | native `<dialog>`; `open` / `onClose` / `title` / `headerAction` |
| `ConfirmDialog` | `ui/confirm-dialog.tsx` | destructive-action confirmation, built on `Dialog`; `requireText` adds a type-to-confirm input (repository/run deletion), omit it for a plain Cancel/Confirm (token/SSH key/webhook deletion, archive). Never use `window.confirm` — `check-ui.mjs`'s `no-window-confirm` rule forbids it outside the allowlist. |
| `Spinner` | `ui/spinner.tsx` | `size` |
| `Skeleton`, `SkeletonLines` | `ui/skeleton.tsx` | — |
| `EmptyState`, `ErrorState` | `ui/empty-state.tsx`, `ui/error-state.tsx` | — |
| `CopyButton`, `Pagination` | `ui/copy-button.tsx`, `ui/pagination.tsx` | `CopyButton`'s `value` also takes a thunk, for strings that are expensive to build |
| `SegmentedControl` | `ui/segmented-control.tsx` | in-page mode switch (Rows/SQL, Table/Raw); not for navigation — that is `RepoTabs` |
| `Markdown` | `ui/markdown.tsx` | the one Markdown renderer (README card, file preview, editor preview, run notes): GFM + raw HTML through `lib/markdown-sanitize.ts`, highlighted fences with copy, heading permalinks, KaTeX, repo-relative link resolution (`linkContext`); wraps output in `.tf-markdown` |
| `MarkdownEditor` | `ui/markdown-editor.tsx` | textarea + edit / preview / split modes (split at `lg`+), ⌘/Ctrl+Enter → `onSubmit`; `markdown={false}` for plain text files |
| `DataTable`, `ValueCell`, `CellModal` | `ui/data-table.tsx`, `ui/value-cell.tsx`, `ui/cell-modal.tsx` | virtualized grid of `Record<string, unknown>` rows; long cells open in a dialog. `DataTableColumn.feature` (`"image"` / `"json"`, resolved by `lib/cell-value.ts`) switches a column to image thumbnails or a JSON tree in the dialog |
| `SearchInput`, `FilterInput` | `ui/search-input.tsx` | `SearchInput` submits on Enter **and** on the browser's clear × (`activeValue` + `onSearch`); `FilterInput` reports every keystroke for in-page filtering. Never hand-roll a `type="search"` box — see §9 |
| `JsonTree` | `ui/json-tree.tsx` | collapsible view of a parsed JSON value; `defaultDepth` controls how much starts expanded |
| `FileDropZone` | `ui/file-drop.tsx` | the file picker: a visually hidden `<input type="file">` inside a labelled drop area (click and drag-and-drop both land in `onFiles`). The only place allowed to write `type="file"` — enforced by `check:ui`'s `raw-file-input` |
| `ProgressBar` | `ui/progress-bar.tsx` | determinate progress (`value` 0…1) for work whose end is known, e.g. bytes sent of an upload. Indeterminate is `Spinner`; "will become content" is `Skeleton` |

Conventions inside `ui/`:

- Variants are a `Record<Variant, string>` map so the whole set is visible in one place. Copy the
  shape from `badge.tsx` / `button.tsx`; do not build class strings conditionally in a call site.
- Every primitive takes `className` and merges it with `cn()` (`lib/cn.ts` — clsx +
  tailwind-merge), so a caller can override one utility without a specificity fight.
- Spread the rest of the native props (`...props`) so `aria-*`, `disabled`, `onChange` and friends
  keep working.
- `Button` defaults `type="button"`. A submit button must say `type="submit"` out loud.
- Need a real navigation that looks like a button? Use `buttonClass({ variant })` on a `<Link>` —
  never re-create the styling.
- Same for a badge/pill that navigates (a tag or branch chip, say): `Badge` only renders a `<span>`,
  so use `badgeClass({ tone })` on a `<Link>` instead of hand-rolling the pill classes.
- Loading animation (`Spinner`'s spin, `Skeleton`'s pulse) respects
  `prefers-reduced-motion` via Tailwind's `motion-reduce:` variant on the component itself, not a
  `globals.css` media query — keeps the override next to the utility it modifies and out of the
  file §5 already restricts to tokens/resets/`.tf-markdown`. `Skeleton` drops the animation
  entirely under reduced motion (the shape alone still reads as "placeholder"); `Spinner` slows
  down instead of freezing (motion is its only signal for a sighted user, and `role="status"` only
  helps screen readers).

`app/globals.css` holds only: the token declarations, global resets, two generic utilities
(`.tabular-nums`, `.scroll-x`), and `.tf-markdown`, which styles the HTML `react-markdown`
produces and therefore cannot be expressed as component classes. Component-shaped CSS classes do
not belong there — that is what `ui/` is for.

One of those global resets is the focus ring: a single `:focus-visible { outline: 2px solid
var(--tf-accent); outline-offset: 2px; }` in `@layer base`, applied to every element rather than
per-primitive so a plain `<a>`/`<Link>` outside `components/ui/` gets it too. It only fires on
keyboard focus (`:focus-visible`, never bare `:focus`), so mouse/touch interaction is unchanged.
`outline` rather than a `ring`/box-shadow utility so it is never clipped by a parent's
`border-radius`; it can still be clipped by an ancestor's `overflow: hidden` near a viewport edge,
which is a known limitation, not something this rule can fix by itself. It sits in `@layer base`,
so a primitive that wants a different treatment (`ui/field.tsx`'s `Input`/`Textarea`/`Select`,
which use `outline-none` plus `focus:border-accent` instead) overrides it via the later
`@layer utilities` without needing `!important`.

## 6. Enforcement

```
bun run check:ui        # scripts/check-ui.mjs, no dependencies
```

It scans every `.ts`/`.tsx` under `app/`, `components/`, `hooks/`, `lib/` and fails with
`file:line` for:

| Rule | What it catches |
| --- | --- |
| `css-var` | `var(--tf-…)` inside a `className` — use the mapped utility (§1) |
| `raw-palette` | `(bg\|text\|border\|ring\|fill\|stroke)-(slate\|gray\|…\|rose)-<n>` in a `className` (§1) |
| `raw-button` | a bare `<button>` outside `components/ui/` — use `Button` (§5) |
| `no-window-confirm` | `window.confirm` / `window.alert` — use `ConfirmDialog` (§5) |
| `subtle-xs-weight` | `text-xs` + `text-fg-subtle` in one `className` with no weight utility (§2) |
| `tinted-fill-tone` | a tone's tinted fill next to that tone's **base** text token — use `-strong` (§1) |
| `raw-search-input` | a `type="search"` attribute outside `components/ui/` — use `SearchInput` / `FilterInput` (§9) |
| `raw-file-input` | a `type="file"` attribute outside `components/ui/` — use `FileDropZone` (§5) |
| `client-boundary` | a Server Component importing a plain **value** (not a component) from a `"use client"` module |

`components/ui/**` is exempt from `raw-button` by construction; genuine one-off exceptions go in
the `RAW_BUTTON_ALLOWLIST` set at the top of the script, with a comment saying why.

`client-boundary` exists because React only marshals *components* across the server/client line.
A constant or helper imported the other way resolves to `undefined` on the server — silently, and
only in a production build. That shipped once: `file-preview.tsx` read `MAX_TABULAR_BYTES` out of
`tabular-preview.tsx`, so `entry.size <= undefined` was always false and the CSV table preview
never rendered at all. Shared values belong in a framework-free module under `lib/`.

Run alongside the rest of the gate:

```
bun run typecheck && bun run lint && bun run format:check && bun run test && bun run check:ui && bun run build
```

## 7. i18n: every user-visible string comes from the dictionary

The UI is bilingual (English / Japanese). The display locale is resolved per request: the
`tf_locale` cookie (the user setting; absent = "follow browser") falls back to
`Accept-Language`. `/settings/language` writes the cookie.

- Never hard-code user-visible text in a component — headings, buttons, placeholders,
  `aria-label`s, empty/error copy, `generateMetadata` all come from `lib/i18n/dictionaries/`.
- Server Components: `const t = await getT()` (`lib/i18n/server`). Client Components:
  `const t = useT()` (`lib/i18n/client`). Keys are dot paths (`t("repo.cloneTitle")`),
  placeholders are `{name}` filled via params; plurals are `xxxOne` / `xxxOther` key pairs.
- `en/` is the shape source (no `as const`); `ja/` is typed as `Dict`, so a missing or extra
  key fails `typecheck`. Key/placeholder parity is also asserted by `lib/i18n/index.test.ts`.
- User-visible dates and relative times take the locale as the second argument:
  `formatDate(iso, locale)` with `useLocale()` / `getLocale()`.
- Do not translate identifiers: git commands, code snippets, branch names, dtype/column names,
  the "thinkingface" brand.
- `ErrorState`'s `title` has no default and both Server and Client Components render it, so it
  cannot call `useT()`/`getT()` itself — every call site passes `title={t("ui.errorStateTitle")}`
  explicitly unless it has a more specific title. A missing `title` fails `bun run typecheck`.
- Never render `result.message` from a failed `apiFetch` call directly — it's backend-authored
  English. Pass the failed `ApiResult` through `errorMessage(t, result)`
  (`lib/api-error-message.ts`), which maps the backend's `error.type` to translated copy. A
  react-query `queryFn` that wraps an `apiFetch` call should `throw new ApiResultError(result)`
  (not a bare `Error`) so the component can recover `type` via `queryErrorMessage(t, error, fallback)`.

## 8. Layout stability: never move what the user is aiming at

Every control on screen is a target someone is about to click. A state change that inserts or
removes a box **above** a target moves it out from under the pointer, and the next click lands
somewhere else. The facet sidebar's "clear filters" button used to do exactly this: appearing
after the first filter pushed every facet checkbox down 46px, more than the 29px row pitch, so
selecting two tags in a row missed the second one.

1. **Async results never displace interactive elements.** An error/success `Alert` goes *below*
   the action row that produced it, or into a slot whose height is already reserved. Do not
   render `{error && <Alert/>}` immediately above a submit button.
2. **Dialogs keep their footer put.** `Dialog` takes a `footer` — the action row is pinned and
   the body scrolls — and a `footerNote` for the message that action produced, rendered
   *below* the buttons. Both are needed: the panel is top-anchored rather than centred, so
   an error grows only its bottom edge. An `Alert` inside the body still moves the buttons,
   because the panel itself grows.
3. **A control that appears on a condition reserves its space up front.** Use `SpinnerSlot`
   (`ui/spinner.tsx`) rather than `{busy && <Spinner/>}`, keep a `min-h-*` header row rather
   than `{cond && <Button/>}` at the top of a column, and render sort indicators in a slot that
   is always there (`opacity-0` when inactive) so a column header cannot change width.
4. **Filter groups keep their shape.** A facet group whose values all dropped out still renders
   its heading; a value that is currently selected is always listed, even at count 0, or it
   becomes impossible to unselect.
5. **A filter can always be removed from where the results are.** Active filters render as
   `FilterChip`s above the result list — not only inside an `EmptyState`, which is unreachable
   as soon as one row matches.
6. **Long lists keep their headers.** `thead` is `sticky top-0`; the column that identifies the
   row (name/run) is `sticky left-0` when the table scrolls horizontally.

## 9. Empty, zero, and failed are three different states

A list that failed to load, a list that came back empty, and an empty *selection* all reduce to
the same falsy value in TypeScript — and every time one of them was allowed to stand in for
another, the UI stated something untrue. Each rule below is a bug that shipped.

1. **A count is only ever rendered from a successful response.** `total` falls back to `0` when a
   request fails, so an always-on count row printed "0 repositories" directly above an
   `ErrorState`: the page said the list was empty when it had no idea what was in it. Gate the
   count on the success branch, never on the fallback value.
2. **A successful-but-empty body is not a final answer.** The GCS dialog cached its first
   response and skipped refetching whenever it already held data; for a revision the sync worker
   had not indexed yet, that response is a legitimate `files: []`, so the dialog kept showing
   "nothing here" forever. Cache the *fetch*, not the emptiness — reopening, retrying, or
   revisiting has to be able to ask again.
3. **An empty selection is not "unspecified".** Hiding every column produced `columns: []`, which
   the rows API reads as "no filter given" and answers with every column — the UI showed no
   columns while the network fetched all of them. When zero means zero, say so explicitly at the
   call site (skip the request, or send a sentinel) instead of letting an empty collection fall
   through to a default.
4. **The clear control of a search field must clear the results.** Use `SearchInput`
   (`ui/search-input.tsx`) rather than a hand-rolled `type="search"`: the browser's own × empties
   the box and fires `change` without ever submitting, so three different listings shipped a
   field that read as cleared while the URL and the results stayed on the old term.
   `check-ui.mjs`'s `raw-search-input` rule forbids the raw attribute outside `components/ui/`.
   For filtering rows already on screen, `FilterInput` is the every-keystroke variant.
