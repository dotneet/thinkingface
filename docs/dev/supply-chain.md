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
| Python client (`clients/python/`) | `pyproject.toml` + `uv.lock` | `uv run --locked` | Renovate |
| Docs / lint tooling | `docs/requirements.in` → `docs/requirements.txt`, `requirements-lint.in` → `requirements-lint.txt` (pinned + hashed) | `pip install --require-hashes -r ...` | manual bump + `pip-audit` |
| Docker base images | tag **and** `@sha256:` digest in the Dockerfiles and compose files | `docker build` / `docker compose` | Renovate |
| GitHub Actions | commit SHA in `uses:`, version in the trailing comment | — | Renovate reports, a human bumps — see [Running Renovate](#running-renovate) |
| Renovate itself | `renovatebot/github-action` by commit SHA, and the Renovate image by tag **and** digest in the `renovate-version` input | `docker run`, from inside the action | Renovate reports (via `customManagers`), a human bumps |
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

That exception only relaxes Renovate's own choice of version — it does not lift
bun's quarantine. Renovate refreshes `bun.lock` by running bun in `frontend/`,
where `bunfig.toml` applies, and bun rejects a too-fresh version even when it is
the exact version being asked for (`error: Version "<pkg>@<version>" was
published within minimum release age of 604800 seconds`). So a security PR for a
frontend package whose fix is less than a week old arrives with `package.json`
bumped and the lockfile update failed, and CI's `bun install --frozen-lockfile`
fails with it. Regenerate the lockfile on the PR branch:

```bash
cd frontend && bun install --minimum-release-age=0
```

`install`, not `update`: Renovate has already written the version it chose into
`package.json`, and `bun update <pkg>` would walk to the newest release in the
range instead of resolving what is written there.

To take a fresh release deliberately when *you* are the one choosing it, the
command is `bun update <pkg> --minimum-release-age=0`. Either way it has to be
the CLI flag: on bun 1.4.0 `BUN_CONFIG_MINIMUM_RELEASE_AGE` has no effect at all
(setting it to `0` fails identically to not setting it).

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
  The two are a pair because the official golang images set
  `GOTOOLCHAIN=local`: the Docker build uses the Go in the image and ignores
  the `toolchain` line entirely, so an image that lags the pin would ship a
  binary built with an older Go while CI's govulncheck — which does honour the
  line — stayed green. The builder stage asserts the two match and fails the
  build otherwise, so this cannot drift silently.

Renovate keeps all of these current once it is enabled — except the action
SHAs, which it can only report on (see [Running Renovate](#running-renovate)).
The commands above are for doing it by hand.

### Regenerating locks

```bash
cd frontend && bun update      # bun.lock (respects bunfig.toml's quarantine)
cd backend  && go get -u ./... && go mod tidy
make lock-python               # docs/requirements.txt, requirements-lint.txt,
                               # e2e/uv.lock, clients/python/uv.lock
```

`make lock-python` compiles the `.in` files with `uv pip compile --generate-hashes`
and refreshes both `uv.lock` files. CI's python job runs `uv lock --check` on
each, so a `pyproject.toml` edit without a regenerated lockfile fails the PR.

`clients/python/uv.lock` covers a package rather than a bare requirement set,
so it locks the project's own runtime dependencies *and* the `dev` dependency
group the unit tests run under. The `transformers` / `lightning` extras are
declared but left out of that group on purpose: the tests assert that the
autolog integrations import with neither installed, so pulling them in would
quietly void the contract and drag two very large trees into every PR.

That project also sets `[tool.uv] package = false`, which is what keeps its
*build* dependencies out of the picture. `uv run` would otherwise build the
package before running anything, and a build resolves `[build-system] requires`
from PyPI at that moment -- a lockfile records runtime dependencies, not build
ones, so every `make check` and every CI run would fetch an unpinned hatchling.
The tests import the package from the source tree beside them
(`[tool.pytest.ini_options] pythonpath`), so nothing needs building to run
them; `[build-system]` still describes how a wheel is produced for
distribution.

## Auditing

```bash
make audit
```

Runs the three scanners `.github/workflows/security.yml` runs — govulncheck
(Go), `bun audit` (frontend), `pip-audit` (the docs / lint requirement files
plus both `uv.lock` trees, exported with `uv export --frozen`) — at pinned
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
| Allow GitHub Actions to create and approve pull requests | Renovate runs as a workflow (`.github/workflows/renovate.yml`) and opens its PRs with the job's `GITHUB_TOKEN`. Without this, GitHub refuses to create them | Settings → Actions → General → Workflow permissions |
| Dependabot alerts | Advisory notifications for the dependency graph. Complements `make audit` rather than duplicating it — it sees the graph GitHub builds, and it notices things between weekly runs | Settings → Advanced Security |
| Branch protection / ruleset on `main` | Required review and required status checks. Without it a single compromised credential can push straight to `main`, and none of the above matters | Settings → Rules |
| Require SHA-pinned actions | Enforces mechanically what the workflows do by convention | Settings → Actions → General |
| Private vulnerability reporting | Gives a reporter somewhere to go that is not a public issue | Settings → Advanced Security |

Dependabot *security updates* (the automatic PRs) should stay off while
Renovate is in use: both would open PRs for the same advisory.

## Running Renovate

Renovate is **self-hosted**: `.github/workflows/renovate.yml` runs it as an
ordinary job with [`renovatebot/github-action`], and `renovate.json` configures
it. Nothing is delegated to the Mend-hosted app. That is the same argument as
the rest of this document — the bot with write access to every lockfile here is
itself a dependency, so it is pinned by digest, its logs are ours to read, and
its version moves when we move it.

The two files split the job cleanly:

- **`renovate.json`** — *what* may be updated, grouped how, inside which
  window (`schedule`, `minimumReleaseAge`, `packageRules`), and what happens to
  the pull request afterwards (`automerge`, with `platformAutomerge` off:
  Renovate does the merging itself, as a merge commit, rather than handing the
  decision to GitHub's auto-merge — which is switched off on this repository
  and would need branch protection to mean anything).
- **`.github/workflows/renovate.yml`** — *how often* Renovate looks, and *as
  whom*. It runs every three hours; a run outside the window `renovate.json`
  allows simply finds nothing to do. That is the point: `vulnerabilityAlerts`
  is scheduled `at any time`, so a security update lands within hours rather
  than waiting for Monday, and ticking an update's checkbox on the Dependency
  Dashboard takes effect on the next run instead of the next week. Impatient?
  Actions → *renovate* → *Run workflow*.

The workflow itself is pinned twice over: the action by commit SHA like every
other `uses:`, and the Renovate image by tag *and* digest in the
`renovate-version` input. The `github-actions` manager only reads `uses:`
lines, so the image is covered by the `customManagers` entry in
`renovate.json` — which is what notices when the bot's own version has gone
stale.

To check a `renovate.json` change before pushing it, run the validator out of
the very image the workflow pins:

```bash
docker run --rm -v "$PWD:/repo:ro" -w /repo \
  "ghcr.io/renovatebot/renovate:$(sed -n 's/.*renovate-version: //p' .github/workflows/renovate.yml)" \
  renovate-config-validator renovate.json
```

[`renovatebot/github-action`]: https://github.com/renovatebot/github-action

### What it costs to run on `GITHUB_TOKEN`

Renovate authenticates as the workflow itself, with the job's `GITHUB_TOKEN`.
There is no secret to create, nothing to rotate, and no personal credential
whose theft would hand someone write access to this repository. GitHub charges
two things for that, and both are consequences to work with rather than bugs to
fix:

1. **It cannot write to `.github/workflows/`.** The `workflows` permission
   simply does not exist for `GITHUB_TOKEN`, and a push that touches a workflow
   file is refused. So `renovate.json` holds *everything under that directory*
   behind `dependencyDashboardApproval` — the action SHAs, and the Renovate
   image pinned in `renovate.yml` itself. They are *listed* on the Dependency
   Dashboard under "Pending Approval", and a human applies them: the SHA and
   its trailing `# vX.Y.Z` comment moved together, the image tag and its digest
   moved together. Ticking the checkbox there will fail on push — the list is a
   to-do, not a button.
2. **Its pull requests do not start CI.** Nothing done with `GITHUB_TOKEN`
   triggers another workflow, so a Renovate PR arrives with no checks. Each one
   carries an **Approve and run** button; press it. A dependency PR without a
   green CI run has not been verified, whatever its diff looks like.

   This is also the whole of the human step, because `renovate.json` sets
   `automerge`. Approve the run and walk away: if CI comes back green, Renovate
   merges the PR itself on one of its next runs, within three hours. If it
   comes back red, nothing happens and the PR waits for you. Nothing merges
   without checks having actually run — a branch with no check runs reports as
   *pending*, not as passing, which is the behaviour this depends on.

   The same rule applies on the way out: the merge is a `GITHUB_TOKEN` push to
   `main`, so it starts none of `main`'s push-triggered workflows — **`ci`
   included**, not just the `docs` deploy and the `security` audit. An
   automerged update is verified on its own branch and never on `main`. When
   that matters for a particular update, dispatch the run by hand (Actions →
   *CI* / *docs* / *security* → *Run workflow*).

   What keeps that from being a stale-branch problem: `rebaseWhen` defaults to
   `auto`, which Renovate resolves to `behind-base-branch` *because* automerge
   is on, so a branch that falls behind `main` is rebased instead of merged as
   it stands. The rebase is itself a `GITHUB_TOKEN` push, and it moves the head
   commit — which discards the checks that were approved on the old one. A
   branch that goes stale needs its **Approve and run** pressed again before it
   can merge.

Both disappear the moment Renovate is given a credential of its own — a classic
personal access token with `repo` + `workflow`, or a GitHub App token minted by
`actions/create-github-app-token` with Contents, Pull requests, Issues and
Workflows write. That is a change to two things: the `token:` input in the
workflow (plus `RENOVATE_GIT_AUTHOR`, which currently has to name
`github-actions[bot]` because that is who the pushes come from), and deleting
the `.github/workflows/**` packageRule from `renovate.json`. If keeping action SHAs
fresh ever matters more than avoiding a stored credential, that is the trade to
make.

### Turning Renovate on

1. **Settings → Actions → General → Workflow permissions**: tick *Allow GitHub
   Actions to create and approve pull requests*. Without it Renovate runs,
   finds its updates, and fails at the last step.
2. Run it by hand once with **Dry run** ticked (Actions → *renovate* → *Run
   workflow*). It reports what it would open and creates nothing. Read the log,
   then run it again with dry run off. Set the log level to `debug` on the same
   form if a run does something surprising.
3. There is no onboarding pull request to merge: `renovate.json` already
   exists, so Renovate treats the repository as configured and goes straight to
   work. The first sign it is alive is the **Dependency Dashboard** issue.
4. Expect that first real run to be the noisiest one — `pinDigests` fills in
   image digests across the Dockerfiles and compose files.
   `prConcurrentLimit: 5` keeps it to five PRs at a time.
5. Confirm Dependabot security updates are off (the table above), or the two
   will race on the same advisory.

## Deliberately not done

- **No SBOM, no image signing.** There is no release pipeline yet — images are
  built by whoever deploys. Worth revisiting when one exists.
- **No CodeQL / SAST.** Out of scope here; this document is about dependencies,
  not about defects in our own code.
- **No `--ignore-scripts` flag.** bun does not run dependency lifecycle scripts
  at all unless they are listed in `trustedDependencies`, and `package.json`
  has no such list. The npm `postinstall` attack surface is already closed.
