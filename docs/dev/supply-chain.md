# Supply chain policy

How third-party code gets into this repository, and what stops a compromised
upstream release from reaching a developer machine, CI, or a production image.

## What this defends against

The attack this is shaped around is the one that keeps happening to public
registries: a maintainer account is taken over, a malicious version is
published under an existing package name, and it spreads through everyone who
resolves dependencies in the next few hours. Variants of the same idea are a
git tag moved onto different code, and a `:latest`-style image tag repointed
at a new build.

Three properties block that:

1. **Nothing resolves versions implicitly.** Every install reads a lockfile, a
   digest, or a commit SHA. A build never asks the registry "what is newest?".
2. **New releases are quarantined.** When versions *are* resolved, only
   releases that have been public for at least 7 days are candidates, so a
   malicious publish usually gets yanked before it can be picked up.
3. **What is already pinned is scanned.** Pinning freezes the good and the bad
   alike, so `make audit` runs weekly against advisory databases.

It does not defend against a compromised release that survives quarantine
undetected, a malicious commit in this repository, or a build machine that is
already compromised. Those need review discipline and the GitHub-side settings
at the end of this document.

## The rules, per ecosystem

| Ecosystem | Source of truth | Install command | Release quarantine |
|---|---|---|---|
| Go (`backend/`) | `go.mod` + `go.sum`, toolchain pinned by the `toolchain` directive | `go build` / `go test` (checksums verified against `sum.golang.org`) | Renovate |
| Frontend (`frontend/`) | `bun.lock` | `bun install --frozen-lockfile` | `bunfig.toml` + Renovate |
| E2E (`e2e/`) | `pyproject.toml` + `uv.lock` | `uv run --locked` | Renovate |
| Docs / lint tooling | `docs/requirements.in` → `docs/requirements.txt`, `requirements-lint.in` → `requirements-lint.txt` (pinned + hashed) | `pip install --require-hashes -r ...` | manual bump + `pip-audit` |
| Docker base images | tag **and** `@sha256:` digest in the Dockerfiles and compose files | `docker build` / `docker compose` | Renovate |
| GitHub Actions | commit SHA in `uses:`, version in the trailing comment | — | Renovate |
| Terraform providers | `infra/.terraform.lock.hcl` | `terraform init` | Renovate |

### Minimum release age

`frontend/bunfig.toml` sets `minimumReleaseAge = 604800` (7 days). It changes
*resolution* only — `bun add`, `bun update`, a cold install with no lockfile —
so a fresh publish cannot be pulled in on the day it appears. Installs from an
existing lockfile (CI, the Docker build) are unaffected: they take exactly what
`bun.lock` records.

`renovate.json` sets the same 7 days as `minimumReleaseAge` for every manager
it handles, with `internalChecksFilter: "strict"` so the age applies to
transitive updates too. The one exception is `vulnerabilityAlerts`, which sets
`minimumReleaseAge: null`: a fix for a known CVE should not wait a week.

To take a fresh release deliberately: `bun update <pkg> --minimum-release-age=0`.

### Pinning, and how to move a pin

- **GitHub Actions.** `uses: owner/action@<40-hex-sha> # vX.Y.Z`. Resolve a tag
  to its SHA with `gh api repos/<owner>/<action>/commits/<tag> --jq .sha`.
- **Docker images.** `image:tag@sha256:<digest>`, keeping the tag for
  readability. Resolve with
  `docker buildx imagetools inspect <image>:<tag> | head -2`. Use the top-level
  index digest, not a per-architecture one, or arm64 builds break.
- **Go toolchain.** `backend/go.mod`'s `toolchain` line, kept in step with the
  `golang:` image in `backend/Dockerfile`. Without it CI builds with whatever
  `go 1.25.0` resolves to and inherits every standard-library CVE fixed since.

Renovate keeps all of these current once it is enabled; the commands above are
for doing it by hand.

### Regenerating locks

```bash
cd frontend && bun update      # bun.lock (respects bunfig.toml's quarantine)
cd backend  && go get -u ./... && go mod tidy
make lock-python               # docs/requirements.txt, requirements-lint.txt, e2e/uv.lock
```

`make lock-python` compiles the `.in` files with `uv pip compile --generate-hashes`
and refreshes `e2e/uv.lock`. CI's python job runs `uv lock --check`, so a
`pyproject.toml` edit without a regenerated lockfile fails the PR.

## Auditing

```bash
make audit
```

Runs the three scanners `.github/workflows/security.yml` runs — govulncheck
(Go), `bun audit` (frontend), `pip-audit` (all three Python sets) — at pinned
versions, so a local run and CI agree. The workflow fires weekly, on any PR
that touches a dependency manifest, and on demand; the weekly run is the one
that matters, because an advisory published today applies to code that has not
changed in months.

### Accepted advisories

`make audit` passes `--ignore` for advisories that are transitive, unfixable
from here, and not reachable in this codebase. The list lives in the Makefile
next to `BUN_AUDIT_IGNORES`, each entry with its reason. The security workflow
also runs `bun audit` with no filtering as a non-blocking step, so the full
picture stays visible in the logs.

**Re-check the list whenever Next.js is upgraded** — both current entries exist
only because `next` pins the vulnerable versions.

## Settings that live in GitHub, not in this repository

Nothing in this directory can set these; they need a repository admin.

| Setting | Why | Where |
|---|---|---|
| Renovate app | `renovate.json` does nothing until the app can see the repository | <https://github.com/apps/renovate> |
| Dependabot alerts | Advisory notifications for the dependency graph. Complements `make audit` rather than duplicating it — it sees the graph GitHub builds, and it notices things between weekly runs | Settings → Advanced Security |
| Branch protection / ruleset on `main` | Required review and required status checks. Without it a single compromised credential can push straight to `main`, and none of the above matters | Settings → Rules |
| Require SHA-pinned actions | Enforces mechanically what the workflows do by convention | Settings → Actions → General |
| Private vulnerability reporting | Gives a reporter somewhere to go that is not a public issue | Settings → Advanced Security |

Dependabot *security updates* (the automatic PRs) should stay off while
Renovate is in use: both would open PRs for the same advisory.

## Deliberately not done

- **No SBOM, no image signing.** There is no release pipeline yet — images are
  built by whoever deploys. Worth revisiting when one exists.
- **No CodeQL / SAST.** Out of scope here; this document is about dependencies,
  not about defects in our own code.
- **No `--ignore-scripts` flag.** bun does not run dependency lifecycle scripts
  at all unless they are listed in `trustedDependencies`, and `package.json`
  has no such list. The npm `postinstall` attack surface is already closed.
