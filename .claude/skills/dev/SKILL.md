---
name: dev
description: 'Start and stop the local development server, and verify changes live. Use when asked to "start the local server", "get it into a state I can verify", "stop the old server", "kill the process using port 3000", or "take a look at the screen and check".'
---

# dev — local startup and live verification

## Which server to use

| Purpose | Start | URL |
| --- | --- | --- |
| Run the whole stack (DB, GCS emulator, API, web) | `make up` | api :8080 / web :3000 |
| **See UI changes** | `make dev-web` | :3100 |
| Try backend changes without rebuilding docker | `make dev-api` | :8081 (SQLite) |
| Stop (host side only) | `make dev-stop` | — |
| Stop (docker) | `make down` | — |

**`http://localhost:3000` is docker's `web` container — a production build via
`next start` — and editing source on the host doesn't show up there.** Don't conclude
"the change isn't showing" from looking at that. Always check UI changes via
`make dev-web` (:3100). Login cookies are shared across ports, so if you're logged in as
admin on :3000, you're automatically authenticated on :3100 too.

From the Browser pane, launch **`web-dev`** from `.claude/launch.json` with `preview_start`
(`web-docker` just points at docker's :3000).

## Prerequisites for starting

- **The Makefile resolves bun / node to absolute paths via mise.** Don't run
  `bun run dev` / `bun run test` directly without going through `make` (on the plain PATH,
  `node` resolves to v18 and vitest fails on `node:util`'s `styleText`).
- A git worktree has no `frontend/node_modules`. `make dev-web` runs `bun install`
  automatically, so the first run takes tens of seconds.
- If a port is already in use (e.g. another session's dev server), dodge it with
  `make dev-web WEB_DEV_PORT=3111`. `make dev-stop` only stops :3100 / :8081 / :14443 and
  **never touches docker services**.
- `bun run build` overwrites the same `.next` the dev server uses. **Don't build while the
  dev server is running** (it breaks the dev server).

## Host-side API (`make dev-api`)

Used to try the branch's API without rebuilding docker. It's a separate instance with its
own SQLite under `.dev/`, so **its users and repos are separate from compose's Postgres**
(seeded with admin/admin). Only GCS is shared with compose's emulator.

Since the emulator runs with `-public-host=gcs:4443`, connecting directly to
`localhost:4443` returns 404 on object reads. `scripts/gcs-host-proxy.py` rewrites the Host
header to absorb the difference (`make dev-api` starts it automatically).

When hitting the host-side API from the host-side web, credentialed CORS requires the web
origin to be in `TF_ALLOWED_ORIGINS` (not `TF_CORS_ORIGINS`). `make dev-api` defaults this to
the value of `WEB_DEV_PORT`.

```bash
NEXT_PUBLIC_API_URL=http://localhost:8081 API_URL=http://localhost:8081 make dev-web
```

## Verifying in the Browser pane

- **Perform actions with a real click (`computer left_click`).** `javascript_tool`'s
  `element.click()` gets discarded by React 19 / Next 15's selective hydration and looks
  "broken". Limit `javascript_tool` to reading state (URL, DOM, computed styles).
- **Take one `computer screenshot` before measuring an element's position.** If the tab
  isn't in front, `innerWidth` is 0 and every `getBoundingClientRect()` value comes out
  bogus (this is the state where `read_page` returns `Viewport: 0x0`). Needed after every
  navigation.
- For verifying layout shift, the reliable approach is measuring the same element's `top`
  before/after and checking it matches.
- `read_page` returning `(empty page)` means it's right after navigation. Wait a second and
  retry.
- A screenshot while scrolled doesn't work (comes out blank). For long pages, resize the
  viewport to be tall instead (e.g. 1280x2400).

## E2E

```bash
make test-e2e          # against :8080 once make up has run. Baseline: 37 passed / ~70 seconds
```

- `uv run --locked` resolves the dependencies from `e2e/uv.lock` into `e2e/.venv`, so they
  don't pollute the current python environment.
- Manual uploads via `huggingface_hub` need `HF_HUB_DISABLE_XET=1` (without it, parquet goes
  through the xet path and returns 501; `e2e/conftest.py` uses the same setting).
- The local dev bucket still has objects from the old layout. When checking "X doesn't
  exist", scope it to a repo-specific prefix (don't expect zero across the whole bucket).
