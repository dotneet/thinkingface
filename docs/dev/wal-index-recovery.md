# WAL Index Recovery

Operational procedure for `docs/dev/continuity-design.md` §13's single point of failure and
§16 open issue 5: **a corrupted or deleted `wal/{storage_path}/index.json`**. Read
`docs/dev/continuity-design.md` (especially §3, §4, §9, §13) before running any of this — this
document assumes that background and does not repeat it.

This only applies when `TF_WAL_MODE=authoritative` (production; see `infra/main.tf`). In `off`
or `shadow` mode the bare repository on disk is still authoritative and an `index.json` problem
is, at worst, an annoyance for the migration tooling — see §15 of the design doc.

## 0. Why this is urgent, not just important

Read `backend/internal/wal/materialize.go`'s `Materialize` before doing anything else. The
behavior for a **missing** index is not an error:

```go
if gen == 0 {
    // No index object exists: this repository has never been written
    // through the WAL. Rebuilding from an empty index would delete every
    // local ref, so leave the copy untouched and let the first write
    // create the index.
    ...
    return nil
}
```

`wal.ReadIndex` maps `storage.ErrNotFound` on `index.json` to `(NewIndex(), 0, nil)` — an empty
index at generation 0, indistinguishable from "this repository has simply never been pushed
through the WAL yet." A **deleted** index therefore does not surface as an error anywhere by
itself. It surfaces only on the *next write*:

- A push/commit to a ref the deleted index used to know about is validated against
  `ix.Refs[ref]`, which is now empty, so `checkPreconditions` in `backend/internal/wal/wal.go`
  sees the client's `<old>` as mismatched and returns `ErrStaleRef` → the client gets "stale
  info, fetch and retry." Confusing, but **not data loss**.
- A push/commit that creates a *new* ref (a new branch, a new tag, the HF commit API creating a
  ref that did not exist) has no old value to conflict with. `PutIndex` with `generation == 0`
  means "create if absent" (`backend/internal/storage/gcs.go`'s `PutIfGeneration`), and the
  object genuinely does not exist any more, so **this write succeeds** and creates a brand-new
  index whose `refs` contains only that one ref, `base == ""`, `entries == []`.
- The moment anything else materializes from that new, nearly-empty index — another push, a
  clone, LRU eviction reclaiming the tmpfs cache and re-fetching, a fresh Cloud Run instance —
  `Materialize`'s `rebuild` branch fires (`local.Base != idx.Base`), `recreateBare` does
  `os.RemoveAll` on the bare repository, and `writeRefs` deletes every ref not present in the
  new index. **This is the actual data-loss event**, and it happens on a completely unrelated,
  ordinary-looking request — not on the delete itself.

The corresponding entry in `backend/cmd/thinkingface/main.go` / `materialize.go`'s own comment
flags this as a known gap: "a missing index on a repository that should have one is the §13
'index corrupted / missing' failure and deserves an alarm" — there is currently no alarm. You
will most likely learn about this from an operator or `wal-verify` finding a repository with
surprisingly few refs, or from a user reporting branches/tags that used to exist are gone, not
from an exception at the moment of deletion.

A **corrupted** (present but unparsable JSON) index is the louder, safer case: `ReadIndex`'s
`json.Unmarshal` fails and returns a real error, which propagates up as a request failure
(materialize fails, `git fsck`/clone/push against that repository start erroring). Grep server
logs for `"read wal index"` or `"parse wal index"` (the error strings in
`backend/internal/wal/wal.go`) scoped to the affected `storage_path`.

**Because the deleted case can look identical to "nothing has ever been pushed here" right up
until it silently discards history, treat any suspicion of a missing or corrupted index with the
same urgency as an active incident, even before you have confirmed data was actually lost.**

## 1. Contain first: archive the repository

Before inspecting or restoring anything, stop further writes to the affected repository so a
new, near-empty index cannot get created out from under you while you work (see §0):

```bash
curl -X POST "$TF_PUBLIC_URL/api/v1/repos/{kind}/{namespace}/{name}/archive" \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

(`backend/internal/api/server.go`: `POST /api/v1/repos/{kind}/{ns}/{name}/archive`, or the
"Archive" action in the repository's Settings in the Web UI.) Every write path — push, the HF
commit API, the web edit endpoint, transfer, experiment ingest — refuses an archived repository
(`backend/internal/api/repos.go`, `auth.go`). Reads (clone/fetch, browsing) still work, which you
will need for validation later. Unarchive only after §5's validation passes.

Archiving does not stop `compact`/`gc` from running against *other* repositories, and does not by
itself stop a concurrent `compact` run on *this* repository from racing you — see §6 for that.

## 2. Find the repository's `storage_path`

The WAL key is keyed by `repositories.storage_path`, not by name (renames/transfers never move
it — `docs/dev/repo-transfer-design.md` §3-4). Look it up:

```sql
SELECT r.kind, n.name AS namespace, r.name, r.storage_path
  FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
 WHERE n.name = '<namespace>' AND r.name = '<repo-name>';
```

New repositories get `repos/{ulid}`; repositories that predate `storage_path` keep the shape of
their old physical location, `{models|datasets}/{namespace}/{name}` (§3 of the design doc). The
index object's key is:

```
wal/{storage_path}/index.json
```

under `gs://{GCS_BUCKET}/{GCS_PREFIX}` (`infra/main.tf`'s `google_cloud_run_v2_service.api` env;
`GCS_PREFIX` is empty in the Terraform default, so normally just
`gs://{GCS_BUCKET}/wal/{storage_path}/index.json`). Confirm `GCS_PREFIX` for your deployment
before running any command below — if it is non-empty, prepend it to every key.

## 3. List the available generations

The bucket has `versioning { enabled = true }`, and — as of the lifecycle rule added alongside
this document (`infra/main.tf`) — `wal/` is deliberately excluded from the noncurrent-version
retention rule, so every generation `index.json` has ever had is still there:

```bash
gcloud storage ls -a "gs://${GCS_BUCKET}/wal/${STORAGE_PATH}/index.json"
```

`-a` (`--all-versions`) lists every version, each as
`gs://.../index.json#<generation>`, oldest first. Get per-version metadata (size, `timeCreated`,
`updated`) for each candidate:

```bash
gcloud storage objects describe "gs://${GCS_BUCKET}/wal/${STORAGE_PATH}/index.json#<generation>"
```

If the object is deleted outright (not just superseded), it will not appear in a plain `ls`
either — GCS versioning still lists it as long as **soft delete** hasn't expired, via the same
`-a`/`describe` calls plus (for something soft-deleted rather than merely noncurrent) `gcloud
storage objects list --soft-deleted` (`bucket_soft_delete_retention_days`, default 30 days —
`infra/variables.tf`). A live-object *delete* and a *replace* (a new generation overwriting the
old one) both show up the same way in `-a`: as one more entry in the version history.

## 4. Read a candidate generation without touching anything live

```bash
gcloud storage cat "gs://${GCS_BUCKET}/wal/${STORAGE_PATH}/index.json#<generation>"
```

Compare against the shape documented in `docs/dev/continuity-design.md` §4
(`backend/internal/wal/wal.go`'s `Index` struct):

```json
{
  "version": 1,
  "seq": 42,
  "base": "base/....pack",
  "entries": ["entries/000041-....pack", "entries/000042-....pack"],
  "refs": { "refs/heads/main": "3f7a...", "refs/tags/v1.0": "9c21..." },
  "updated_at": "..."
}
```

Sanity-check the JSON itself: `version` must be `1` (`wal.IndexVersion`; a higher value is a
schema this codebase's `ReadIndex` refuses to read — `ErrIndexVersion`), `refs` should look like
what you expect the repository's branches/tags to be, and `entries` should be in increasing
`{seq}` order matching the `-{seq}-` prefix embedded in each filename.

## 5. Judgment: which generation to restore

In roughly this priority order:

1. **The most recent generation whose `refs` you can positively confirm are correct** — e.g. it
   matches what a user/operator remembers, or matches an independent copy (a recent `git clone`
   someone has locally, a CI checkout, a fork). Prefer recent over exact-at-incident-time: WAL
   entries are additive, so a slightly older-but-verified generation loses at most the last few
   pushes, which is far better than restoring something wrong.
2. If nothing can be positively confirmed, the **generation immediately before the
   delete/corruption** (the last one in the `-a` listing before the bad state) is the least-bad
   default — it is what the repository actually looked like right before the incident.
3. **Never guess a generation that predates it just because it "looks more complete."** A larger
   `entries` list is not evidence of correctness; an attacker or a bug could just as easily have
   produced a bad generation with a long history.

Whatever you pick, before restoring it, confirm every object it depends on still exists — a
generation can name packs that a later `compact` run has since collected as orphans (§10 of the
design doc; `wal.GCOrphans`'s 24-hour grace period, `backend/internal/wal/gc.go`):

```bash
# repeat for idx.base (if non-empty) and every entry in idx.entries
gcloud storage objects describe "gs://${GCS_BUCKET}/wal/${STORAGE_PATH}/<base-or-entry-path>"
```

If a referenced pack is missing as a *live* object, it may still exist as a **noncurrent
version** of that same key — the noncurrent-version lifecycle rule in `infra/main.tf` is scoped
to `lfs/`/`blobs/` and deliberately never matches `wal/` base/entries objects either, so the only
thing that ever removes one is `GCOrphans` explicitly deleting it after 24 hours, and a
version-enabled bucket keeps a deleted object's prior generations around indefinitely absent a
lifecycle rule. Look it up the same way
as the index (`gcloud storage ls -a` on that exact key) and fetch the specific generation with
`gcloud storage cat gs://.../<path>#<generation>`. **This is not a code path the server ever
reads** — `Materialize` and `applyPack` always read the live object — so a pack that only exists
as a noncurrent version must be restored (copied back to live, §6 below) alongside the index, not
left as-is.

If you cannot find a generation whose full dependency chain (`base` + every `entries[]`) is
intact, restoring that generation will make `Materialize` fail loudly (`applyPack`'s `fetch %s:
%w` wrapping `storage.ErrNotFound`) rather than silently corrupt anything further — pick an
earlier generation and check again.

## 6. Restore

**Do not delete anything.** Restoring means making the chosen old generation the new live
version, which itself becomes one more entry in the version history (nothing is lost, including
the bad generation you are replacing):

```bash
gcloud storage cp \
  "gs://${GCS_BUCKET}/wal/${STORAGE_PATH}/index.json#<chosen-generation>" \
  "gs://${GCS_BUCKET}/wal/${STORAGE_PATH}/index.json"
```

This is a plain, unconditional copy-in-place — it is **not** a CAS write and does not go through
`wal.PutIndex`/`PutIfGeneration`. That is intentional here (there is no code path for "reset the
index out of band" to call instead), but it means none of the WAL package's invariants (§5 of the
design doc) apply to this one write. Do it only while the repository is archived (§1), so no
concurrent push can race it, and only after §5's dependency check. If §5 found any `base`/
`entries[]` pack that only exists as a noncurrent version, restore each of those the same way,
**before** restoring the index (the index must never point at an object that is not live yet —
invariant 2 of §5 in the design doc, and the same ordering it describes for a normal push).

`updated_at` inside the restored JSON will be stale (it reflects when the old generation was
originally written, not now) — this is cosmetic; nothing reads it programmatically outside of
display.

## 7. Validate before unarchiving

1. **Clone test.** With the repository still archived (reads still work), do an ordinary
   `git clone` (or `huggingface_hub` `snapshot_download`) against it through the running `api`
   service. This exercises the real `materialize` path (`GET /info/refs` → `EnsureLocal` →
   `Materialize`) end-to-end, including `git index-pack --strict` on every pack and — because
   `Materialize`'s `rebuild` branch will fire here, since the local cache's state file will not
   match the newly-restored generation — a full rebuild from the restored `base`/`entries`, not a
   cache hit. A clean clone with the expected branches/tags/history is the strongest signal you
   have that the restore is sound.
2. **`thinkingface wal-verify`.** This subcommand
   (`backend/cmd/thinkingface/walops.go` → `wal.Verify`, `backend/internal/wal/verify.go`)
   materializes the WAL into a scratch directory and compares it against the *already-materialized
   on-disk copy* (`{GIT_ROOT}/{storage_path}.git`) ref-by-ref, then object-by-object
   (`git rev-list --objects`), then `git fsck --full --strict` on the reconstruction. Run it
   after step 1 has populated a local cache for this repository, on the same instance/container
   that clone went through, e.g. via a one-off override of the existing `compact` Cloud Run Job
   image:
   ```bash
   gcloud run jobs execute "$(terraform -chdir=infra output -raw compact_job_name)" \
     --region "${REGION}" --args=wal-verify
   ```
   **Known limitation — read before relying on this**: `wal-verify` has no per-repository filter;
   it walks every repository in the DB (`forEachRepo` in `walops.go`) and **silently skips**
   (`slog.Warn("wal-verify: no git directory, skipping", ...)`, not a failure) any repository
   whose `{GIT_ROOT}/{storage_path}.git` does not already exist on the container running the Job.
   Since Cloud Run Jobs start from an empty filesystem, a `wal-verify` run that did not go through
   the exact same container as your clone test will report "all repositories match" having
   actually checked nothing for the repository you care about — that is a false pass, not a real
   one. **Unconfirmed**: whether a `compact` Job execution and an ordinary `git clone` against the
   `api` Service can ever land on the same container/filesystem in this deployment (they are
   different Cloud Run resources) — if they cannot, `wal-verify` as currently written may not be
   usable this way in production at all, and step 1 (the clone test) plus the object-existence
   checks in §5 are the real validation. Treat this bullet as best-effort rather than load-bearing
   until that is confirmed.
3. Only once step 1 (and, if it actually ran against the right data, step 2) look right, unarchive
   the repository (`DELETE /api/v1/repos/{kind}/{ns}/{name}/archive`, or "Unarchive" in the UI).

## What not to do

- **Do not delete the corrupted/bad `index.json` generation.** It costs nothing to keep — this is
  exactly why the noncurrent-version lifecycle rule in `infra/main.tf` deliberately never matches
  `wal/` — and it may be useful evidence for figuring out how it happened.
- **Do not run `wal-seed --force`** (`backend/cmd/thinkingface/walops.go`) as a substitute for
  this procedure. `wal-seed` builds a *fresh* index from whatever is currently on disk in
  `GIT_ROOT` for that repository — in authoritative mode that on-disk copy is only a cache, and
  after the incident described in §0 it may itself already be the rebuilt, history-losing copy.
  Seeding from it would make the loss permanent by writing it back as the new "true" WAL history.
  `wal-seed` is for Phase 3 migration (repositories that never had an index yet), not incident
  recovery.
- **Do not restore by writing a hand-built `index.json`** unless every `base`/`entries[]` path it
  names is verified to exist (§5) — a fabricated index that looks plausible but names missing or
  wrong objects will pass a superficial read and then fail (or worse, silently misbehave) at the
  next materialize.
- **Do not skip §1 (archiving).** A concurrent push landing between "you diagnosed the problem"
  and "you finished restoring" can create the near-empty index described in §0, which then
  competes with the very generation you are trying to restore.
