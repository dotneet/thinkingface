# Contributing to thinkingface

Thanks for taking the time to contribute. Bug reports, documentation fixes, compatibility
findings against `huggingface_hub` / `datasets` / `git`, and feature work are all welcome.

## Before you start

- **Bugs and ideas** go in [GitHub Issues](https://github.com/dotneet/thinkingface/issues).
  For a bug, include the client you used (`huggingface_hub` version, `git`, the web UI, the
  `tf` CLI, ...), the server setup (Compose / SQLite / GCP), and what you expected to happen.
- **Larger changes** — a new endpoint, a storage layout change, anything touching the
  HF-compatible surface — are worth an issue first so the approach can be agreed on before
  you invest in code. The design documents in [`docs/dev/`](docs/dev/) explain the current
  decisions and the reasoning behind them.

## Development setup

The [development guide](docs/dev/development.md) covers everything: prerequisites, running the
stack, the host-side dev servers, and the toolchain quirks. The short version:

```bash
cp .env.example .env
make up          # full local stack
make check       # every quality gate — run this after any change
```

## Pull requests

1. Branch from `main` and keep the change focused.
2. Run `make check` (it mirrors CI). If you touched an HF-compatible endpoint, also run
   `make test-e2e` against a running `make up` stack.
3. Keep the generated and mirrored artifacts in sync — `frontend/types/api.gen.ts`
   (`make gen-types`), both migration directories, and `docs/dev/api-contract.md`. See
   ["Things that must stay in sync"](docs/dev/development.md#things-that-must-stay-in-sync).
4. If the change is user-visible, update the relevant page under `docs/users/` (new pages must
   be added to the `nav:` in `mkdocs.yml`).
5. Use [Conventional Commits](https://www.conventionalcommits.org/) in English for commit
   messages and the PR title: `feat:` / `fix:` / `docs:` / `refactor:` / `chore:`.
6. Open the PR against `main` and describe what changed, why, and how you verified it.

CI runs the backend, frontend, Python, type-contract, and Terraform checks on every PR, and the
docs workflow builds `docs/users/` with `mkdocs --strict` when it changes. The Terraform job is
static only (`fmt -check`, `init -backend=false`, `validate`) — it has no GCP credentials, so a
green run does not mean `infra/` will apply cleanly.

## Code style

Formatting is mechanical — `make fmt` rewrites Go, TypeScript, Python, and Terraform sources,
and `make lint` runs the static checks. The frontend additionally follows
[`frontend/DESIGN.md`](frontend/DESIGN.md) (UI primitives, semantic color tokens, loading /
empty / error states, i18n dictionaries), enforced by `bun run check:ui`.

## License

By contributing, you agree that your contributions are licensed under the project's
[MIT License](LICENSE).
