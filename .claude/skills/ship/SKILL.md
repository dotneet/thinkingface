---
name: ship
description: 'Self-review changes, open a PR, respond to review feedback, and carry it through to merge. Use when asked to "open a PR", "self-review and open a PR", or "address the review comments and merge if there is nothing left".'
---

# ship — from self-review to PR, review response, and merge

The remote is **GitHub** (`dotneet/thinkingface`). Use the `gh` CLI. Check with
`gh auth status`; if it isn't authenticated, `gh auth login`.

## 0. Satisfy the prerequisites

If the instruction is to go all the way to merge, get the following passing before opening
the PR. If it fails later, every CI run and review round-trip that already happened has to be
repeated.

```bash
make check                 # backend / frontend / python / types
make build-web             # when frontend was touched (CI's build step; not included in make check)
make test-store-pg         # when store was touched (requires make up)
make test-e2e              # when HF-compatible endpoints, LFS, or the git path were touched
```

- `make check-types` looks at `git status --porcelain`, so **the generated file
  (`api.gen.ts`) is only judged after committing**. It always fails before that. If you just
  want to confirm sync, running `make gen-types` twice and getting a zero diff is enough.
- If main has moved forward, catch up first (a merge commit is fine, per §7).

## 1. Self-review (always, before opening a PR)

**Read the diff top to bottom yourself.** Don't leave it to the reviewer to find things
first. Every issue you catch here is one that doesn't cost a review round-trip and a fresh
CI run.

```bash
git fetch origin main
git diff --stat origin/main...HEAD   # get an overview of the range first — any unintended files mixed in?
git diff origin/main...HEAD          # then read all of it
```

**The baseline is `origin/main`, not `main`.** In a worktree setup the local `main` sits
untouched in a different worktree and can be many commits behind; using `main...HEAD` mixes
other people's changes into the diff and blurs the review scope (this actually happened on
the PR that wrote this section: what should have been 3 files became 47 files and 9,141
lines).

Apply these lenses in order:

1. **Apply the 6 patterns from §"Common feedback" to your own diff.** This is the single
   most effective step. "One-sided application of a cross-cutting rule" in particular can
   never be caught by only looking at the side where the rule changed —
   **grep for every path that references the rule and count them**.
2. **Spec** — does it meet the acceptance criteria? Did any part of the instructions get dropped?
3. **Correctness** — boundary values, error paths, nil / empty, timeouts, concurrency.
4. **Readability** — naming, responsibility, early returns. Does it match the style of the surrounding code?
5. **Tests** — do they cover the important branches? Do test names make the cause of a failure clear?
6. **Compatibility and contract** — `docs/api-contract.md`, `apitypes`, generated-file sync. A migration path if it's breaking.
7. **Operations** — is anything sensitive leaking into logs? Is the information needed for debugging present?

**When you add a new directory or file type, check whether it's covered by each gate.**
There's a real case where adding `scripts/` was missed by `make check-python` and CI's ruff
target (the targets are hardcoded in the Makefile's `check-python` and the python job in
`.github/workflows/ci.yml`).

Fix what you find before opening the PR. For anything you decide not to fix, write the
reason in the PR description.

## 2. Open the PR

```bash
git push -u origin HEAD                    # push first; gh prompts interactively otherwise
gh pr create -t "title" -F body.md         # -F - reads the body from stdin
```

- **`gh pr create` opens a normal (non-draft) PR by default.** Pass `--draft` only when you
  actually want a draft, and `gh pr ready` to promote one later.
- Push the head branch yourself before running `gh pr create`. If the branch has no upstream,
  gh asks about it interactively, which stalls a non-interactive session.
- Structure the body as "Summary / Changes / Verification". For verification, write the
  actual commands you ran and their results with numbers. If self-review turned up something
  you decided not to fix, note that decision too.
- Pushing more commits updates the PR in place; there is no extra step after `git push`.

## 3. Wait for CI

```bash
gh pr checks N --watch          # blocks until every check finishes; non-zero exit if any failed
gh pr checks N                  # current status, without waiting
```

CI is GitHub Actions (`.github/workflows/ci.yml`: backend / frontend / python / contract).
A full run takes several minutes, so start `--watch` in the background and work on something
else in the meantime.

- Right after a push there is a short window before the new run is registered. If
  `gh pr checks` reports the previous run as already finished, wait ~15s and ask again.
- On failure, read the failing job with `gh run view <run-id> --log-failed` (get the id from
  the `gh pr checks` output) and reproduce it locally with the corresponding `make` target
  rather than guessing from the log alone.

## 4. Read the review feedback

```bash
gh pr view N --comments      # PR body + every review and comment
gh pr diff N                 # the diff as the reviewer sees it
```

Inline review threads live in GitHub's review API; `gh` has no first-class subcommand for
listing or resolving them (that needs `gh api graphql`). Don't build the workflow around
thread state — read everything with `gh pr view N --comments` and reply with
`gh pr comment N -b "..."`, naming the file and line you're responding to so the mapping is
unambiguous.

## 5. Verify each finding before fixing it

**Don't take a review comment at face value** — this applies equally to a human reviewer and
to any automated one. For each finding, first read the relevant code and confirm the
reproduction path. Most findings turn out to be real, but fixing without confirming leads to
off-target fixes.

- Confirmed real → fix it. **Show the reproduction and the effect with numbers when
  possible** (e.g. `author=admin` returns 41 results while `author=ADMIN` returns 0 / peak
  RSS for a 100MB round trip went from 157MB to 27MB).
- False positive → don't fix it. Write the reasoning as a reply so the reviewer can see why.

After fixing:

```bash
git push                             # the PR updates in place; CI re-runs
gh pr comment N -b "what I did"      # one comment summarizing the responses is fine
```

## 6. Expect your own fix to create the next finding

**A fix for a one-sided rule usually leaves another side behind.** The real example this
lesson comes from: a "make matching case-insensitive" change was reported, and fixing the
forward direction left the author facet and the reverse lookup behind; fixing those left the
facet and the cycle-detection key behind. Three rounds for what looked like a one-line change.

So **when you fix a finding, grep for other leftovers of the same nature yourself and kill
them in the same push.** That is the only thing that keeps the next round from being a repeat
of this one. Don't schedule the PR as if review will be a single round-trip.

## 7. Merge

```bash
git fetch origin main && git merge origin/main   # if there's a conflict, a merge commit to catch up is fine
make check                                        # run again after catching up
git push
gh pr merge N --merge                             # main is operated with merge commits — not squash/rebase
```

- **`gh pr merge` fails when the PR isn't mergeable** (conflicts with main, or required
  checks not green). For conflicts, catch up with `git fetch origin main && git merge
  origin/main`, re-run `make check`, and push — then merge again. Since main is operated with
  merge commits, there's no need to rebase.
- `gh pr merge N --merge --auto` merges automatically once the requirements are met, which is
  handy when CI is still running.

Cleanup after merging **depends on where you're working**:

- Regular clone: `git checkout main && git pull --ff-only origin main && git branch -d <branch>`
- **git worktree (`.claude/worktrees/…`): do no cleanup.** main is checked out in a different
  worktree so you can't `checkout` it, and the working branch is in use by this worktree so
  it can't be deleted either. If you want to bring the main side up to date, only suggest
  running `git -C <main's path> pull --ff-only origin main` there, after confirming no other
  session is using it.

## Common feedback (catch these in self-review first)

The review findings accumulated on this repo so far converge onto essentially these 6
patterns.

1. **One-sided application of a cross-cutting rule** (most common). When you change a
   matching rule, token, or naming convention, **grep every path that references that rule
   and sweep them all**. The classic case: fixing only the forward direction while reverse
   lookup, facets, and cycle detection stay on raw comparison.
2. **Write order and atomicity**. Don't break the "back up the object → update the index →
   delete the row" order. Deleting the row before enqueueing a job leaves an unrecoverable
   orphan if the enqueue fails. Design for idempotency assuming concurrent execution
   (`SKIP LOCKED`).
3. **UI state transitions**. Conflating empty, zero, and failure (`DESIGN.md` §9).
4. **Contract/implementation mismatch**. The behavior documented in `docs/api-contract.md`
   diverges from the handler. Also includes timezone gaps like the DB's `CURRENT_DATE` vs.
   Go's UTC date, and **a new directory missing from lint / CI targets**.
5. **Missing authorization**. Forgetting to run a new read or delete path through ownership
   verification (`ownedLFSKey` / `canAdmin`).
6. **Not accounting for resource limits**. **This repo deals with GB-scale models and
   datasets.** Reading a whole body into memory, fetching everything in one shot, or an
   unbounded cache — none of these pass, even for a dev-only tool
   (`scripts/gcs-host-proxy.py` was actually flagged for this). Write with streaming, paging,
   and explicit limits.
