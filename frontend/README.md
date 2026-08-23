# thinkingface — frontend

Next.js (App Router) + Bun web UI for the thinkingface hub. Implements the
routes described in `docs/dev/api-contract.md` and `docs/dev/thinkingface-design.md`
§12 against the Go backend.

## Requirements

- [Bun](https://bun.sh) 1.3+

## Getting started

```bash
bun install
cp .env.example .env.local   # point at your backend, or leave defaults
bun run dev                  # http://localhost:3000
```

Environment variables (see `.env.example`):

- `API_URL` — backend URL as seen from the Next.js server process (SSR
  fetches). In docker compose this is the internal service name, e.g.
  `http://api:8080`.
- `NEXT_PUBLIC_API_URL` — backend URL as seen from the browser (client-side
  fetches, e.g. the Parquet viewer and experiment dashboard). Must be
  publicly reachable, e.g. `http://localhost:8080`.

All API calls send `credentials: "include"` so the `tf_session` cookie is
forwarded on browser requests; Server Components forward the same cookie
manually (see `lib/server-auth.ts`) since `fetch` on the server has no
browser cookie jar.

## Scripts

- `bun run dev` — start the dev server
- `bun run build` — production build (`next build`)
- `bun run start` — run the production build (`next start -p 3000`)
- `bun run typecheck` — `tsc --noEmit`
- `bun run lint` — ESLint
- `bun run duckdb-assets` — stage the DuckDB-WASM runtime into `public/duckdb/`

`dev` and `build` run `duckdb-assets` first, so the wasm module and worker the
Parquet viewer's SQL console loads are always present and always served from
this origin (never a CDN). The output is a build artifact and is gitignored;
see `scripts/copy-duckdb-assets.mjs` for why it is a copy rather than a bundler
import.

The build never depends on the backend being reachable: every route is
`export const dynamic = "force-dynamic"`, and all data fetching goes through
`lib/api.ts`'s `apiFetch`, which never throws — a failed or unreachable
request resolves to `{ ok: false, ... }` and pages render an empty/error
state instead of crashing. No page fetches data during static generation.

## Screens

```
/                                              Home: stats, recently updated datasets/models, search
/login                                         Log in / sign up (tabs)
/datasets, /models                             List with search, tag filter, sort, paging
/{datasets,models}/{ns}/{name}                 Overview: README card, sidebar meta, gcloud/git clone copy buttons
/{datasets,models}/{ns}/{name}/tree/{rev}/*    File tree (breadcrumbs, LFS badges, parquet → viewer links, dir README)
/{datasets,models}/{ns}/{name}/viewer/{rev}/*  Parquet viewer: schema panel, virtualized table, server-side paging,
                                                column visibility, click-to-expand cell values; SQL tab runs
                                                DuckDB-WASM in the browser against the file
/{datasets,models}/{ns}/{name}/blob/{rev}/*    File preview: text / markdown / image, binary → download;
                                                csv / tsv / jsonl parsed client-side into a table (≤10MB)
/experiments                                   Experiment repositories
/experiments/{ns}/{repo}                       Project list
/experiments/{ns}/{repo}/{project}             Run table (select/checkbox) + uPlot metric charts
                                                (step/time axis, EMA smoothing slider, log-scale toggle)
/settings/tokens                               Access token management (create/delete, plaintext shown once)
/new                                           Create a dataset or model repository
```

`datasets` and `models` share almost all UI: the actual page components live
under `components/repo-pages/*` and `components/repo/*`, parameterized by
`kind: "dataset" | "model"`. The two route trees under `app/datasets/...`
and `app/models/...` are thin wrappers that just supply `kind`.

## Design notes

- Light/dark theme via CSS custom properties in `app/globals.css`, following
  `prefers-color-scheme` by default with an explicit toggle
  (`components/theme-toggle.tsx`) that sets `data-theme` and persists to
  `localStorage`. An inline script (`app/theme-script.tsx`) applies the
  stored preference before paint to avoid a flash.
- Fonts: Figtree (body) + IBM Plex Mono (code/tabular), loaded via
  `next/font/google`.
- Numeric columns use `tabular-nums`.
- Wide content (tables, code blocks, the Parquet grid) scrolls in its own
  `overflow-x: auto` container; the page body never scrolls horizontally.
- Charts use `uplot` directly (`components/experiments/uplot-chart.tsx`),
  no charting framework.
- Tables use `@tanstack/react-table` for column/header structure;
  `@tanstack/react-virtual` virtualizes rows in the Parquet viewer.

## Known gaps / open questions against the API contract

See the task report for the full list — in short: private-repo access from
Server Components is cookie-forwarded for `/api/v1/me`, `/new`, and
`/settings/tokens`, but the repo/tree/blob/viewer/experiment pages fetch
without forwarding the session cookie (they work today for public repos;
wiring auth through would mean threading a `Cookie` header through every
`lib/*.ts` call site, which was out of scope here). The "Viewer" tab on a
repo overview links to the first Parquet file only — the contract doesn't
define a multi-file viewer index, so repos with several Parquet files rely
on the per-file "Open in viewer" links in the file tree instead.
