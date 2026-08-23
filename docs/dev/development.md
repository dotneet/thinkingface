# Development guide

How to build, run, and verify thinkingface from a checkout. This is the contributor-facing
companion to the user documentation in [`docs/users/`](../users/index.md); it covers the
repository layout, the local development loop, the quality gates, and the conventions that
`make check` and CI enforce.

For *what* the system does and *why* it is built this way, read
[`thinkingface-design.md`](thinkingface-design.md). For the Web UI-facing API surface, see
[`api-contract.md`](api-contract.md).

## Prerequisites

| Tool | Used for |
|---|---|
| Docker with the Compose plugin | The full local stack (`make up`) |
| Go 1.25+ | Backend, `tf` CLI |
| [bun](https://bun.sh) and Node.js 20+ | Frontend (the Makefile resolves both to absolute paths via [mise](https://mise.jdx.dev); see "Toolchain notes" below) |
| [uv](https://docs.astral.sh/uv/) | Python checks, E2E tests, and the docs site — all run in disposable environments |
| `git` and `git-lfs` | Exercising the git transport locally |
| Terraform (optional) | `infra/` |

## Repository layout

```
thinkingface/
├── backend/                   # Single Go binary: REST API, git smart HTTP, LFS, Parquet viewer, sync worker
│   ├── cmd/thinkingface/      #   server entry point
│   ├── cmd/tf/                #   `tf` CLI entry point (see tf-cli.md)
│   └── internal/              #   packages (api, auth, gitrepo, gitserver, lfs, store, syncer, viewer, ...)
├── frontend/                  # Next.js 15 (App Router) + React 19 + Tailwind v4 web UI, built with bun
├── clients/python/            # pip package `thinkingface` (login helper + trackio-compatible shim)
├── e2e/                       # pytest compatibility suite driven through huggingface_hub / datasets / git
├── infra/                     # Terraform for GCP (Cloud Run + Cloud SQL or SQLite/Litestream + GCS)
├── scripts/                   # Helper scripts (GCS host proxy, docs screenshot seeding, ...)
├── docs/users/                # User documentation, published to GitHub Pages (mkdocs.yml at the root)
├── docs/dev/                  # Design documents, API contract, this guide — not published
├── docker-compose.yml         # Full local stack: postgres / gcs emulator / api / web
├── docker-compose.sqlite.yml  # Override that drops postgres and runs the API on SQLite (`make up-sqlite`)
└── Makefile                   # Every task below; `make help` lists them
```

The backend's package-by-package responsibilities are listed in `CLAUDE.md` at the repository
root and in [`thinkingface-design.md`](thinkingface-design.md) §15.

## Running the stack

### Full stack with Docker Compose

```bash
cp .env.example .env
make up          # docker compose up -d: postgres, fake-gcs-server, api, web
make logs        # follow logs
make down        # stop (volumes are kept); `make clean` also removes the volumes
```

| What | Where |
|---|---|
| Web UI | <http://localhost:3000> |
| API | <http://localhost:8080> |
| GCS emulator (fake-gcs-server) | <http://localhost:4443> |
| PostgreSQL | `localhost:5432` (`make psql` opens a shell) |
| Default login | `admin` / `admin` (`TF_ADMIN_USERNAME` / `TF_ADMIN_PASSWORD` in `.env`) |

`make up-sqlite` brings up the same stack without the `postgres` container, with the API on
SQLite (`docker-compose.sqlite.yml`). The tradeoffs between the two database backends are
documented in [Deployment](../users/self-hosting/deployment.md#choosing-a-database-backend).

`make rebuild` rebuilds the `api` / `web` images from scratch when a Dockerfile or dependency
changed.

### Hot reload inside Compose

Copy `docker-compose.override.yml.example` to `docker-compose.override.yml`. Compose picks it
up automatically; it bind-mounts `backend/` and `frontend/` into the containers and swaps the
production entrypoints for `go run` / `bun run dev`, so edits on the host show up without an
image rebuild.

### Host-side dev servers (recommended for UI and API work)

`http://localhost:3000` is the production build running in docker — editing source on the host
does not change it. Run the framework dev servers on the host instead, against the compose
services:

```bash
make dev-web     # next dev on :3100, talking to the compose API on :8080
make dev-api     # the Go API on :8081 with SQLite + the compose GCS emulator (via a Host-rewriting proxy on :14443)
make dev-stop    # stop everything the two targets above started (docker is untouched)
```

Login cookies are shared across ports, so a session on :3000 is valid on :3100. If a port is
taken, override it: `make dev-web WEB_DEV_PORT=3111`.

`make dev-api` starts `scripts/gcs-host-proxy.py` (`make gcs-proxy`) automatically. It is
needed because the emulator is started with `-public-host=gcs:4443`, so a host-side process
reading objects through `localhost:4443` directly gets 404s until the `Host` header is
rewritten.

### Backend standalone, without docker

The server only needs a database URL and an object store:

```bash
cd backend
DATABASE_URL=sqlite:///tmp/thinkingface.db \
STORAGE_DRIVER=gcs-emulator STORAGE_EMULATOR_HOST=http://localhost:4443 \
  go run ./cmd/thinkingface serve
```

(`STORAGE_DRIVER=gcs` with a real bucket works too — see
[Configuration](../users/self-hosting/configuration.md).)

### The `tf` CLI

```bash
make tf                      # builds backend/bin/tf, embedding the version from `git describe`
backend/bin/tf login http://localhost:8080
backend/bin/tf up ./some-dataset
```

Command reference and internals: [`tf-cli.md`](tf-cli.md).

## Quality gates

```bash
make check
```

**Run this after every code change.** It is the same set of checks CI runs
(`.github/workflows/ci.yml`) and breaks down into:

| Target | What it runs |
|---|---|
| `make check-backend` | `gofmt` check, `go vet`, `go test ./...` in `backend/` |
| `make check-frontend` | `bun run typecheck`, `lint` (ESLint), `format:check` (Biome), `check:ui` (`frontend/scripts/check-ui.mjs`, the UI conventions from `frontend/DESIGN.md`), `test` (vitest) |
| `make check-python` | `ruff check` + `ruff format --check` for `e2e/`, `clients/python/`, `scripts/` |
| `make check-types` | Regenerates `frontend/types/api.gen.ts` from `backend/internal/apitypes` with tygo and fails on any diff |

Formatting and linting on their own: `make fmt` (Go / TypeScript / Python / Terraform) and
`make lint`.

Optionally install [lefthook](https://github.com/evilmartians/lefthook) for pre-commit
feedback (`lefthook install`; `lefthook.yml` is at the root). It is not required — `make check`
and CI are authoritative — and `git commit --no-verify` skips it for one commit.

## Tests

| Command | Scope | Needs |
|---|---|---|
| `make test` | Go unit tests + frontend unit tests | nothing |
| `make test-store-pg` | `backend/internal/store` integration tests against PostgreSQL (the SQLite path always runs as part of `go test`) | `make up` |
| `make test-e2e` | `e2e/` — `huggingface_hub` / `datasets` / git / GCS compatibility against a running server | `make up` |

The E2E suite talks to an already-running server: it logs in as the admin user from `.env`,
issues a write token, and revokes it at the end of the session (`e2e/conftest.py`). It runs in
a disposable `uv` environment so its dependencies never touch your Python setup. **Run it
whenever you change an HF-compatible endpoint** (whoami / create_repo / preupload / commit /
resolve / tree / LFS batch) — compatibility with the upstream client libraries is the project's
top priority. What the suite covers is listed in [`e2e/README.md`](../../e2e/README.md).

## Things that must stay in sync

- **Web UI-facing API types.** `backend/internal/apitypes` is the single source of truth.
  After changing a struct there, run `make gen-types`, commit the regenerated
  `frontend/types/api.gen.ts` (never edit it by hand), and update
  [`api-contract.md`](api-contract.md). HF-compatible / LFS / ingest endpoints are excluded:
  the external protocol defines those.
- **Database migrations.** Add sequentially numbered SQL files to *both*
  `backend/internal/store/migrations/postgres/` and `backend/internal/store/migrations/sqlite/`.
- **Pure Go only.** Parquet goes through `parquet-go` and SQLite through `modernc.org/sqlite`.
  Do not introduce CGo dependencies (Arrow C++ bindings, `mattn/go-sqlite3`, ...) — they break
  the container build and cross-compilation.
- **Frontend conventions.** `frontend/DESIGN.md` describes the primitives, semantic color
  tokens, the loading / empty / error state rule, and the i18n dictionaries; `bun run check:ui`
  enforces the mechanical parts.

## Documentation

- `docs/users/` is published to <https://dotneet.github.io/thinkingface/> by
  `.github/workflows/docs.yml`. `make docs` serves it locally on
  <http://localhost:8123/thinkingface/> with live reload; `make docs-build` runs the same
  `mkdocs build --strict` CI does, so broken internal links fail early. A new page must be added
  to the `nav:` block of `mkdocs.yml` to appear in the sidebar.
- The site is **bilingual**: `mkdocs-static-i18n` builds English at the root and Japanese
  under `/ja/`. A Japanese page is the same path with a `.ja` suffix
  (`guides/uploading.md` → `guides/uploading.ja.md`); it is *not* added to `nav:`, and a page
  without a translation falls back to English. Nav labels are translated in the `ja` block of
  `mkdocs.yml` (`nav_translations`). Links between pages keep the plain `.md` path — the
  plugin rewrites them per locale — and translated `##` headings repeat the English anchor
  (`## 見出し { #english-anchor }`) so cross-page anchors keep working. `README.md` and
  `README.ja.md` are the same pair at the repository root.
- `docs/dev/` is not published. Design documents and the API contract live here.
- Screenshots under `docs/users/images/` are generated from a seeded throwaway instance by
  `scripts/docs-demo/` — see [`docs-screenshots.md`](docs-screenshots.md). Do not hand-edit or
  capture them from a personal stack.

## Conventions

- **Commits** follow [Conventional Commits](https://www.conventionalcommits.org/) in English:
  `feat:` / `fix:` / `docs:` / `refactor:` / `chore:` + a short summary. `main` is merged with
  merge commits (no squash / rebase).
- **Go**: `gofmt` + `golangci-lint` (`backend/.golangci.yml`); every package carries a
  `// Package xxx ...` doc comment.
- **TypeScript**: Biome (`frontend/biome.json`) for formatting and basic lint, ESLint
  (`frontend/eslint.config.mjs`) for the Next.js rules.
- **Python**: `ruff` (`e2e/pyproject.toml`, `clients/python/pyproject.toml`).
- **Configuration** lives in `.env` (`cp .env.example .env`); never commit `.env`.

## Toolchain notes

- The Makefile resolves `bun` and `node` to absolute paths via mise. On a plain `PATH`, `node`
  may resolve to an older version and vitest / `next build` fail — go through `make`
  (`make test`, `make build-web`) rather than running `bun run test` / `bun run build` directly.
- Inside Compose, web → api is `http://api:8080` while browser → api is
  `http://localhost:8080`; the two are split via `API_URL` / `NEXT_PUBLIC_API_URL`
  (`apiBaseUrl()` in `frontend/lib/api.ts`).
- `frontend/public/duckdb/` holds the DuckDB-WASM assets used by the SQL console. They are
  generated (gitignored) and copied from `node_modules` automatically before `bun run dev` /
  `bun run build`.
