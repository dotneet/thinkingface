.DEFAULT_GOAL := help
COMPOSE := docker compose

# Put mise-managed bun / node at the front of PATH.
#
# On a bare PATH, `node` can resolve to the v18 at /usr/local/bin, in which
# case vitest (rolldown) requires `node:util`'s styleText and check-frontend
# fails with a SyntaxError -- not a code problem, but you end up fixing PATH
# by hand every time. `mise which` looks at the real binary rather than a
# shell function, so it gives the same answer even in execution environments
# that never source ~/.zshrc (the launch.json launcher, hooks, CI containers).
# In an environment without mise (CI), nothing happens and the bun / node
# already on PATH are used as-is.
MISE ?= $(firstword $(wildcard /opt/homebrew/bin/mise /usr/local/bin/mise $(HOME)/.local/bin/mise))
ifneq ($(MISE),)
MISE_BUN := $(shell $(MISE) which bun 2>/dev/null)
MISE_NODE := $(shell $(MISE) which node 2>/dev/null)
ifneq ($(MISE_BUN),)
export PATH := $(dir $(MISE_BUN)):$(PATH)
endif
ifneq ($(MISE_NODE),)
export PATH := $(dir $(MISE_NODE)):$(PATH)
endif
endif
BUN ?= $(if $(MISE_BUN),$(MISE_BUN),bun)

# Ports for the host-side dev servers. Kept separate from docker compose's
# web(3000) / api(8080) (see the dev-web / dev-api entries below). If another
# session is already holding a port, override it, e.g.
# `make dev-web WEB_DEV_PORT=3111`.
WEB_DEV_PORT ?= 3100
API_DEV_PORT ?= 8081
GCS_PROXY_PORT ?= 14443

# Pick up POSTGRES_* from .env if it exists (it's KEY=VALUE, so a Makefile can
# include it as-is). Otherwise fall back to the same defaults as compose
# (tf/tf/thinkingface). Used by test-store-pg and psql.
-include .env
POSTGRES_USER ?= tf
POSTGRES_PASSWORD ?= tf
POSTGRES_DB ?= thinkingface
POSTGRES_PORT ?= 5432

# Use `ruff` if it actually runs, otherwise fall back to `uvx ruff`.
# (There are cases like pyenv shims where the binary is on PATH but can't
#  actually run, so this checks by whether --version succeeds rather than
#  command -v.)
define RUFF
	@if ruff --version >/dev/null 2>&1; then \
		ruff $(1); \
	elif uvx ruff --version >/dev/null 2>&1; then \
		uvx ruff $(1); \
	else \
		echo "  !! SKIPPED: ruff / uvx not found (pip install ruff). CI still runs it" >&2; \
	fi
endef

.PHONY: build-web help up down up-sqlite down-sqlite logs rebuild psql check check-backend check-frontend check-python \
        check-types gen-types test test-backend test-frontend test-store-pg test-e2e fmt lint clean tf \
        dev-web dev-api gcs-proxy dev-stop docs docs-build

# The full SQLite override set. See the comment at the top of
# docker-compose.sqlite.yml.
COMPOSE_SQLITE := $(COMPOSE) -f docker-compose.yml -f docker-compose.sqlite.yml

help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*##' $(MAKEFILE_LIST) | sort | awk -F ':.*##' '{printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

up: ## Start all services in the background (postgres, gcs, api, web)
	$(COMPOSE) up -d

down: ## Stop all services and remove containers (volumes are kept)
	$(COMPOSE) down

up-sqlite: ## Start in SQLite mode (no postgres; api/web/gcs only, see docker-compose.sqlite.yml)
	$(COMPOSE_SQLITE) up -d api web gcs

down-sqlite: ## Stop the SQLite-mode services and remove containers (volumes are kept)
	$(COMPOSE_SQLITE) down

logs: ## Tail logs from all services
	$(COMPOSE) logs -f

rebuild: ## Rebuild the api/web images from scratch and restart
	$(COMPOSE) build --no-cache api web
	$(COMPOSE) up -d

psql: ## Open a psql shell against the postgres service
	$(COMPOSE) exec postgres psql -U $${POSTGRES_USER:-tf} -d $${POSTGRES_DB:-thinkingface}

# ---- host-side dev servers -------------------------------------------------
#
# docker compose's web(:3000) runs `next start` (a production build), so
# editing the source on the host has no effect on it. To check a UI change
# for real, always bring up `make dev-web` on a separate port. Likewise for
# the API: use `make dev-api` to try out a branch's backend without
# rebuilding the docker image.

dev-web: ## Run the Next.js dev server on the host, against the compose api (WEB_DEV_PORT, default 3100)
	@cd frontend && [ -d node_modules ] || $(BUN) install
	@cd frontend && $(BUN) scripts/copy-duckdb-assets.mjs
	@echo "==> next dev on http://localhost:$(WEB_DEV_PORT) (api: $${NEXT_PUBLIC_API_URL:-http://localhost:8080})"
	cd frontend && $(BUN) node_modules/next/dist/bin/next dev -p $(WEB_DEV_PORT)

# Invoke next directly instead of `bun run dev`: via the launcher, `bun run`
# spawns another shell internally, which loses PATH and ends up with
# "bun: command not found". The duckdb asset copy (the step that precedes the
# dev script in package.json) is already done above.

dev-api: ## Run the Go API on the host with SQLite + the compose GCS emulator (API_DEV_PORT, default 8081)
	scripts/dev-api.sh

gcs-proxy: ## Host-rewriting proxy so a host-side API can read the compose fake-gcs (GCS_PROXY_PORT, default 14443)
	scripts/gcs-host-proxy.py --port $(GCS_PROXY_PORT)

# Does not touch docker compose services (that's `make down`'s job). This
# only stops the dev servers started on the host.
dev-stop: ## Stop the host-side dev servers started by dev-web / dev-api / gcs-proxy
	@for port in $(WEB_DEV_PORT) $(API_DEV_PORT) $(GCS_PROXY_PORT); do \
		pids="$$(lsof -ti tcp:$$port 2>/dev/null | tr '\n' ' ' || true)"; \
		if [ -n "$$pids" ]; then \
			echo "==> stopping $$port ($$pids)"; kill $$pids 2>/dev/null || true; \
		else \
			echo "==> $$port: nothing listening"; \
		fi; \
	done

tf: ## Build the tf CLI into backend/bin/tf
	cd backend && go build -trimpath -ldflags "-s -w -X github.com/dotneet/thinkingface/backend/internal/tfcli.Version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/tf ./cmd/tf

# ---- documentation site ----------------------------------------------------

# docs/users/ is the published site (mkdocs.yml points docs_dir there);
# docs/dev/ holds the internal design docs and is deliberately excluded.
# Run in a disposable uv environment so MkDocs never lands in the ambient
# python environment (same approach as make test-e2e).
MKDOCS := uv run --isolated --with-requirements docs/requirements.txt mkdocs
DOCS_PORT ?= 8123

docs: ## Serve docs/users/ locally with live reload (DOCS_PORT, default 8123)
	@echo "==> mkdocs on http://localhost:$(DOCS_PORT)/thinkingface/ (site_url sets the base path)"
	$(MKDOCS) serve -a 127.0.0.1:$(DOCS_PORT)

docs-build: ## Build the docs site into site/ the same way CI does
	$(MKDOCS) build --strict

# ---- quality gates ---------------------------------------------------------

check: check-backend check-frontend check-python check-types ## Run every quality gate (run this after any code change)
	@echo "==> all checks passed"

gen-types: ## Regenerate frontend/types/api.gen.ts from backend/internal/apitypes (tygo)
	backend/scripts/gen-types.sh

# `git status --porcelain` rather than `git diff --exit-code`: diff only sees
# *tracked* files, so an api.gen.ts that was regenerated but never `git add`ed
# would sail through the check. --porcelain reports modified, untracked and
# deleted alike, which is what "is the committed file in sync" really means.
check-types: ## Verify frontend/types/api.gen.ts is in sync with the Go wire structs
	@echo "==> types: tygo generate + diff"
	backend/scripts/gen-types.sh
	@status="$$(git status --porcelain -- frontend/types/api.gen.ts)"; \
	if [ -n "$$status" ]; then \
		echo "$$status"; \
		git --no-pager diff -- frontend/types/api.gen.ts || true; \
		echo "frontend/types/api.gen.ts is out of sync with the Go types (including uncommitted/untracked changes). Commit the result of 'make gen-types'"; \
		exit 1; \
	fi

check-backend: ## gofmt check + go vet + go test
	@echo "==> backend: gofmt"
	@cd backend && unformatted="$$(gofmt -l .)" && \
		if [ -n "$$unformatted" ]; then \
			echo "Files needing gofmt:"; echo "$$unformatted"; exit 1; \
		fi
	@echo "==> backend: go vet"
	cd backend && go vet ./...
	@echo "==> backend: go test"
	cd backend && go test ./...

check-frontend: ## typecheck + lint + format:check + check:ui + test (bun)
	@echo "==> frontend: typecheck / lint / format:check / check:ui / test"
	cd frontend && $(BUN) run typecheck
	cd frontend && $(BUN) run lint
	cd frontend && $(BUN) run format:check
	cd frontend && $(BUN) run check:ui
	cd frontend && $(BUN) run test

# Mirrors the CI `python` job exactly (lint *and* format), so a green
# `make check` cannot be followed by a red CI on formatting alone.
check-python: ## ruff check + ruff format --check for e2e/, clients/python/ and scripts/
	@echo "==> python: ruff check"
	$(call RUFF,check e2e clients/python scripts)
	@echo "==> python: ruff format --check"
	$(call RUFF,format --check e2e clients/python scripts)

test: test-backend test-frontend ## Run backend and frontend unit tests

test-backend: ## Run the Go test suite
	cd backend && go test ./...

test-frontend: ## Run the frontend unit tests
	cd frontend && $(BUN) run test

# `make check` deliberately leaves the production build out (it is the slowest
# gate and CI runs it anyway), but when you do want it locally, run it through
# make: `bun run build` on a bare PATH picks up Node 18 and next refuses to
# start ("Node.js version ^18.18.0 || >= 20.0.0 is required").
build-web: ## Production build of the Next.js app (the CI `build` step)
	cd frontend && $(BUN) run build

# backend/internal/store's integration tests always run against SQLite, but
# the PostgreSQL path only runs when TF_TEST_DATABASE_URL is set. This target
# points it at the postgres already brought up by `make up` (docker-compose.yml
# already exposes ${POSTGRES_PORT:-5432} to the host).
# The suite TRUNCATEs every table between cases, so it gets its own database
# ($(POSTGRES_DB)_test) on the compose instance rather than the one the api
# uses; it is created here on first run.
test-store-pg: ## Run backend/internal/store integration tests against the `make up` postgres (requires `make up` first)
	@$(COMPOSE) exec -T postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -tAc \
		"SELECT 1 FROM pg_database WHERE datname = '$(POSTGRES_DB)_test'" | grep -q 1 || \
		$(COMPOSE) exec -T postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -c "CREATE DATABASE $(POSTGRES_DB)_test"
	cd backend && TF_TEST_DATABASE_URL="postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:$(POSTGRES_PORT)/$(POSTGRES_DB)_test?sslmode=disable" \
		go test ./internal/store/ -count=1

# `uv run --isolated` builds a throwaway environment from requirements.txt, so
# the suite never installs huggingface_hub / datasets / pyarrow into whatever
# interpreter happens to be active. Falls back to plain pip when uv is absent.
test-e2e: ## Run the huggingface_hub compatibility E2E suite (requires `make up` first)
	@if uv --version >/dev/null 2>&1; then \
		cd e2e && uv run --isolated --with-requirements requirements.txt pytest -v; \
	else \
		echo "  uv not found; installing dependencies into the current python environment" >&2; \
		cd e2e && pip install -q -r requirements.txt && pytest -v; \
	fi

# ---- formatting / linting --------------------------------------------------

fmt: ## Format Go, TypeScript, Python and Terraform sources
	@echo "==> gofmt"
	cd backend && gofmt -w .
	@echo "==> biome format"
	cd frontend && $(BUN) run format
	@echo "==> ruff format"
	$(call RUFF,format e2e clients/python scripts)
	@echo "==> terraform fmt"
	cd infra && terraform fmt -recursive

lint: ## Lint Go, Python and Terraform sources
	@echo "==> go vet"
	cd backend && go vet ./...
	@echo "==> golangci-lint"
	@if golangci-lint --version >/dev/null 2>&1; then \
		cd backend && golangci-lint run ./...; \
	else \
		echo "  skip: golangci-lint not found"; \
	fi
	@echo "==> ruff check"
	$(call RUFF,check e2e clients/python)
	@echo "==> terraform validate"
	cd infra && terraform validate

clean: ## Stop services and remove containers, networks and named volumes
	$(COMPOSE) down -v
