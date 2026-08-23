# Cloud Run Migration Design via the Continuity Approach

Design to replace the GCP production configuration in `docs/thinkingface-design.md` §14 (GKE
Autopilot + StatefulSet + PD) with **a scheme that keeps the WAL on GCS**, so that `api` can run
on Cloud Run.

The idea this is based on is Cursor's Continuity (https://cursor.com/blog/git-at-any-scale).
Three key points:

1. **A push is saved to GCS as a WAL entry, and is not acked until it's durably persisted**
2. **The bare repository on disk is demoted to a warm cache. The GCS WAL is always the source of
   truth**
3. **The linearization point is object storage's conditional write (GCS's `ifGenerationMatch`)**
   — no distributed lock, no consensus protocol, and no relational DB is used for git consistency

## 1. Goals and Non-Goals

### Goals

- Retire GKE Autopilot + PersistentVolume, and run `api` on Cloud Run
- Remove the constraint "`api` is not placed on Cloud Run" (design doc §14)
- Make concurrent pushes across multiple instances safe (currently only an in-process mutex)
- Resolve the unimplemented backup for git data (mentioned in §14 but never implemented) at the
  design level

### Non-Goals

- **The metadata DB is Postgres or SQLite (switched via `DATABASE_URL`).** Repository existence /
  ACLs / the LFS ledger / sync jobs / the experiment index are held in the same table layout
  regardless of engine (type mapping etc. in `docs/thinkingface-design.md` §10). The production
  default is Cloud SQL for PostgreSQL; choosing SQLite means a single Cloud Run instance +
  Litestream setup (same §14). **Whichever is used, the metadata DB plays no part whatsoever in
  the git consistency path** — this is treated as an invariant
- The LFS (`lfs/`) key layout and protocol are unchanged (later,
  `docs/content-addressed-storage-design.md` added `blobs/` and retired `exports/`, but the shape
  of the keys and their relationship to the WAL are the same as they were at the time)
- Multi-region and repository sharding are out of scope (only Cloud Run's horizontal scaling)
- The external shape of HF-compatible endpoints is not changed at all

## 2. Overview

```
git client ──HTTP/2(h2c)──> Cloud Run: api (min-instances=1 / CPU always-on)
                               │
                               │  ① in-process mutex (serializes within one instance)
                               │  ② GCS CAS         (linearizes across instances)
                               │
                               ├─> GCS  wal/…/index.json    ← CAS via ifGenerationMatch [linearization point]
                               ├─> GCS  wal/…/base/*.pack   ← compacted snapshot
                               ├─> GCS  wal/…/entries/*.pack← WAL entry per push
                               ├─> GCS  lfs/ , blobs/       ← unchanged
                               │
                               └─> tmpfs {GIT_ROOT}         ← materialized cache. safe to lose

Cloud Run Job: WAL compaction (periodic)
Metadata DB (Cloud SQL for PostgreSQL or SQLite+Litestream): metadata only. Not part of git consistency
```

The two existing entry points remain as they are:

- `gitserver` (execs the `git` binary) → clone / fetch / push
- `gitrepo` (go-git) → the HF commit API, tree reads, resolve

Both **operate against an already-materialized local copy**. The only difference is that writes
are always finalized through the WAL.

## 3. GCS Key Layout

Adds `wal/` alongside the existing `lfs/` `blobs/`.

```
wal/{storage_path}/index.json                 ← CAS target. one per repository
wal/{storage_path}/base/{ulid}.pack           ← compaction output
wal/{storage_path}/entries/{seq}-{ulid}.pack  ← one push = one entry
```

`{storage_path}` is `repositories.storage_path` (`docs/repo-transfer-design.md` §3-4) — the
repository's **physical location**, immutable and independent of the logical name
`(kind, namespace, name)`. New repositories get `repos/{ulid}`; repositories that predate the
introduction of `storage_path` keep the shape of their old physical location via backfill
(`{models|datasets}/{ns}/{name}`, where `{kind}` is `models` / `datasets`, same as the existing
`storage.ExportKey`). Both shapes share a single code path, which is the reason renaming and
transferring (same design doc) never touch the WAL at all.

`{seq}` is a zero-padded 6-digit sequence number; `{ulid}` is unique per write attempt.
**Multiple entries can share the same seq** — an attempt that lost the CAS race still leaves
behind the pack it already uploaded. Anything the index doesn't reference is an orphan and a GC
target (§11). The ULID is there so this orphan and the winning pack never collide on their key.

## 4. WAL Index Structure

```json
{
  "version": 1,
  "seq": 42,
  "base": "base/01JAV3K5V7Q2N8XCF0S9M4E6BR.pack",
  "entries": [
    "entries/000041-01JAV3M0J3C8T5RD2W1Y7HKQPX.pack",
    "entries/000042-01JAV3N9B6F4G7HZ8K2L5MNPQR.pack"
  ],
  "refs": {
    "refs/heads/main": "3f7a…",
    "refs/tags/v1.0":  "9c21…"
  },
  "updated_at": "2026-08-21T10:23:45Z"
}
```

| Field | Meaning |
|---|---|
| `seq` | The last entry number assigned. The next push uses `seq+1` |
| `base` | The compacted snapshot. Empty string means no base |
| `entries` | Packs to apply on top of base, **in this order**. Order matters |
| `refs` | **The source of truth for refs.** A local copy's refs are merely a projection of this |

The key point is keeping `refs` inline in the index. This means:

- ref consistency is concentrated into the CAS of a single index object
- Deciding "is this local copy up to date?" reduces to a plain generation comparison

The index object's **generation number** (assigned by GCS) directly becomes the repository's
version.

## 5. New Invariants

In addition to the existing invariants in CLAUDE.md, the following must hold.

1. **WAL index updates always go through `ifGenerationMatch`.** Never write an unconditional PUT
2. **Never update the index before a WAL entry (pack) has fully finished writing to GCS.** Doing
   it in reverse order leaves the index pointing at a pack that doesn't exist
3. **Never delete a pack the index no longer references without a grace period.** There may be an
   instance mid-materialize that read the old index
4. **Ack the client only after the index's CAS succeeds.** This is the core of Continuity
5. **Never rewrite a local copy's refs directly.** Always go through the index; the local copy is
   materialized from the index
6. **The pre-receive hook never touches Postgres.** Keep the DB out of the git consistency path

## 6. Push Flow (`git push`)

Git's **quarantine** mechanism is used for linearization as-is. This is the mechanism where
receive-pack isolates the objects it received into a temporary directory, and moves them into the
repository proper only after the pre-receive hook succeeds. It guarantees that **if a push fails,
no data is left on disk at all**.

```
POST /git-receive-pack
  │
  ├─ 0. loadRepoForWrite (existence + write permission, in Postgres)       * only place that touches the DB
  ├─ 1. materialize(repo)  → bring the local copy up to the index's generation G
  │
  ├─ 2. exec: git -c core.hooksPath=/opt/thinkingface/hooks \
  │             receive-pack --stateless-rpc {dir}
  │        │
  │        ├─ unpacks the client's pack into quarantine
  │        │
  │        └─ pre-receive hook = `thinkingface hook pre-receive`
  │             stdin: "<old> <new> <ref>" × N
  │
  │             a. pack the new objects in quarantine
  │                git pack-objects --revs --stdout --delta-base-offset
  │                  stdin: each <new>, and "--not" + the existing refs
  │                (no --thin: the pack must be self-contained)
  │
  │             b. PUT to GCS: wal/…/entries/{G.seq+1}-{ulid}.pack
  │                → wait until fully written
  │
  │             c. CAS the index:
  │                 new_index = contents of G + b appended to entries
  │                                       + refs updated <old>→<new>
  │                                       + seq++
  │                 PUT index.json  ifGenerationMatch=G.generation
  │
  │                 success → exit 0
  │                 412     → re-read the index (goes to the §6.1 conflict check)
  │
  ├─ 3. exit 0 → receive-pack moves quarantine into the repository proper and updates local refs
  │              → sends the client "ok refs/heads/main"
  │    exit≠0 → receive-pack rejects it. quarantine is discarded. nothing is left on disk
  │
  └─ 4. Enqueue a sync job from the before/after ref diff (unchanged)
```

**Ack timing**: receive-pack only returns `ok` to the client after pre-receive exits 0 — that is,
**after both durable GCS persistence and CAS success**. Invariant 4 is enforced structurally.

### 6.1 Resolving CAS Conflicts

When 412 is returned, a naive retry is wrong. receive-pack has already validated the value of
`<old>` **against the local copy**, but another instance may have pushed in the meantime.

```
Read the new index (generation G2, refs R2)
  For each <old>:
    R2[ref] == <old>  → our update is still a fast-forward. retry the CAS against G2
    R2[ref] != <old>  → another instance advanced the same ref.
                        exit 1 + "stale info, fetch and retry"
                        → the client fetches and pushes again (git's normal behavior)
```

The retry cap is around 5. Beyond that, exit 1.
**Skipping this check causes non-fast-forward overwrites.** This is the single most important
branch in the design.

### 6.2 Where the Hook Script Lives

Point `core.hooksPath` at a **fixed directory baked into the image**. Never write a
`hooks/pre-receive` per repository.

```sh
# /opt/thinkingface/hooks/pre-receive (included in the image)
#!/bin/sh
exec /usr/local/bin/thinkingface hook pre-receive
```

`git -c core.hooksPath=…` propagates to child processes via `GIT_CONFIG_PARAMETERS`.

The current code already made a call along these lines — "we don't want a post-receive hook on
disk, so we compare refs before/after on the Go side" (`api/git.go:67`). That intent — *don't
have a mutable per-repository script* — is preserved here too. The hook is part of the image, not
repository state.

## 7. HF Commit API Flow (the path that doesn't use the `git` binary)

`huggingface_hub`'s `POST /api/{type}s/{ns}/{name}/commit/{rev}` has `gitrepo.Repo.Commit()` build
the commit server-side with go-git. quarantine isn't available here, so the same steps are
followed explicitly.

```
1. materialize(repo) → generation G
2. Repo.Commit(req)  → writes new objects locally, yielding newHash / oldHash
                       * it's fine to advance the local ref (it gets overwritten from the index later)
3. git pack-objects --revs --stdout   (newHash, --not the existing refs)
4. PUT to GCS: entries/{G.seq+1}-{ulid}.pack
5. CAS the index (ifGenerationMatch=G.generation)
     success → return 200
     412     → same check as §6.1:
              the target branch's head hasn't moved → retry against G2
              it has moved → re-materialize and start over from step 2 (up to 3 times)
              exceeded → 409 Conflict
6. Enqueue a sync job
```

If step 5 fails, the local objects written in step 2 remain as orphans, but since the local copy
is just a cache this is harmless (they disappear on the next materialize / recreate).

## 8. Clone / Fetch Flow

```
GET /info/refs?service=git-upload-pack
  → loadRepoForRead
  → materialize(repo)
  → git upload-pack --advertise-refs (unchanged)

POST /git-upload-pack
  → loadRepoForRead
  → materialize(repo)
  → git upload-pack --stateless-rpc (unchanged, streaming)
```

materialize is a single GET of the index (a few ms to a few dozen ms if the cache is already
current). The index is read on every request so that reads also get freshness guarantees.

> **Future optimization (not done in the first pass)**: building the advertisement directly from
> the index's `refs` would let `info/refs` be answered without materializing at all. But it would
> require reproducing capabilities / symref HEAD / protocol v2, which conflicts with the current
> policy of "delegate pack negotiation to git." Worth revisiting once Cloud Run's cache-miss rate
> becomes a problem.

## 9. materialize (Catching the Local Copy Up)

```go
// Pseudocode. Runs under gitrepo.Manager's lock.
func materialize(storagePath string) error {
    idx, gen := readIndex(storagePath)              // GCS GET

    local := readLocalState(dir)                    // {generation, applied[]}
    if local.generation == gen {
        return nil                                  // hit
    }
    if !exists(dir) {
        gitInitBare(dir)
    }

    // rebuild if base changed (after a compaction)
    if idx.base != local.base {
        recreate(dir)
        applyPack(dir, idx.base)
    }
    for _, e := range idx.entries[len(local.applied):] {
        applyPack(dir, e)                           // git index-pack --stdin
    }
    writeRefs(dir, idx.refs)                        // git update-ref --stdin (including deletions)
    writeLocalState(dir, gen, idx)
    return nil
}
```

### Why This Order

**Objects are applied first, refs are written last.** If it crashes partway through, only extra
objects are left behind — refs never end up pointing at an object that doesn't exist. Doing it in
reverse order produces a broken repository.

`writeRefs` also **deletes** refs that aren't in the index, using `git update-ref --stdin`'s
`delete`. Without this, branches deleted by another instance would stick around.

### Validating the Pack

Ingested with validation via `git index-pack --stdin --strict`. Silently ingesting a corrupted
pack would leave that materialize permanently broken from then on.

### Cache Eviction

`{GIT_ROOT}` is tmpfs (i.e., RAM). Left unattended, it OOMs. The same LRU scheme as
`viewer/cache.go` (mtime-based, `ensureCached` / `evict`) is also added to `gitrepo`. The cap is
`TF_GIT_CACHE_BYTES` (default 2 GiB).

An evicted repository is simply re-materialized on its next access, so this is safe.

## 10. Compaction

WAL entries grow with every push. Since materialize's cost is proportional to the number of
entries, they are folded down periodically.

**Cloud Run Job** (triggered by Cloud Scheduler, the `thinkingface compact` subcommand):

```
Selecting targets: repositories where entries count > threshold (default 50), or total size > threshold

For each repository:
  1. materialize (generation G)
  2. git repack -a -d --depth=50 --window=250
  3. PUT the resulting single pack to GCS: base/{ulid}.pack
  4. CAS the index (ifGenerationMatch=G.generation):
       base    = the new pack from step 3
       entries = []
       refs    = G.refs (unchanged)
       seq     = G.seq (unchanged)
     412 → a push happened in the meantime. give up this round and defer to the next one (no retry)
  5. record the old base + old entries in a **pending-deletion list** (don't delete immediately)

Deletion is a separate pass: only packs the index no longer references AND that are more than
24 hours past their last update are deleted
```

**Step 5's grace period is invariant 3.** An instance may be mid-materialize having read the old
index and still be referencing it. This matches the same philosophy as the existing
`thinkingface gc` subcommand (judged by orphan age).

The reason step 4 doesn't retry on 412 is that compaction isn't urgent — push should be
prioritized.

## 11. Extending the Storage Interface

Adds two CAS-related methods to `storage.Storage` (11 methods).

```go
// ErrPreconditionFailed is a generation mismatch (GCS's 412).
var ErrPreconditionFailed = errors.New("storage: precondition failed")

type Storage interface {
    // …existing 11 methods unchanged…

    // GetWithGeneration returns the body and generation. The read side of CAS.
    GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error)

    // PutIfGeneration writes only when generation matches.
    // generation == 0 means "only if the object doesn't exist."
    // ErrPreconditionFailed on mismatch.
    PutIfGeneration(ctx context.Context, key string, generation int64,
        r io.Reader, contentType string) error
}
```

The GCS implementation uses `cloud.google.com/go/storage`'s
`ObjectHandle.If(storage.Conditions{GenerationMatch: gen})` / `DoesNotExist: true` directly.
`Generation int64` is added to `ObjectInfo`.

> **Needs verification**: whether fake-gcs-server correctly implements `ifGenerationMatch`. Local
> development and E2E depend on this. If unsupported, prepare a fallback implementation for the
> emulator using a Postgres advisory lock (not included in the production path). **The first
> thing to confirm before starting work.**

## 12. Cloud Run Configuration

| Item | Value | Reason |
|---|---|---|
| Execution environment | **Gen 2** | `git`'s fork/exec and filesystem compatibility |
| HTTP/2 end-to-end | **enabled (h2c)** | Avoids HTTP/1's 32 MiB request cap. Required for large pushes |
| Memory | 4-8 GiB | tmpfs repository cache + git processes. **tmpfs shares the memory limit** |
| CPU | 2 vCPU | `pack-objects` is CPU-hungry |
| CPU allocation | **always-on** | the in-process syncer / webhook workers run even outside a request |
| Concurrency | 20-40 | the default of 80 is too many for git processes. Memory-bound |
| min-instances | 1 | keeps the cache warm. Avoids cold starts |
| Request timeout | 3600s | large clones |
| `GIT_ROOT` | `/tmp/git` | tmpfs |
| `TF_VIEWER_CACHE_DIR` | `/tmp/cache` | existing. Already an emptyDir today, so unchanged |

`backend/Dockerfile`'s `CGO_ENABLED=1` is **unnecessary** (DuckDB isn't used; the viewer is
pure-Go parquet-go). A static binary makes the image smaller and cold starts faster.

## 13. Failure Modes and Behavior

| Event | Behavior | Response |
|---|---|---|
| CAS 412 (concurrent push to a different ref) | re-read the index and retry the CAS | automatic |
| CAS 412 (concurrent push to the same ref) | pre-receive exits 1, push rejected | client fetches and pushes again |
| Crash after the pack is PUT but before the CAS | an orphan pack is left. index is untouched | age-based GC |
| Crash after CAS succeeds, before the response | the client sees a failure, but it's actually already committed | re-pushing is idempotent (refs are already at the target value, objects are the same) and is a no-op |
| An instance's tmpfs disappears | materialized on the next request | automatic. **This is exactly what the design aims for** |
| A pack is corrupted | detected by `index-pack --strict`, materialize fails | returns 500. Ops alert |
| The index is corrupted / deleted | the repository can't be read | **A single point of failure.** GCS versioning is enabled, so recovery from an older generation is possible |
| Concurrent compaction | one wins the CAS, the other backs off | automatic |
| Postgres is down | authorization can't happen, all requests fail | git consistency is untouched. Resumes as-is after recovery |

The bucket holding `index.json` already has `versioning { enabled = true }` (`infra/main.tf`).
Deletion of `lfs/` `blobs/` is delegated entirely to reference-counted GC (`thinkingface gc`,
`docs/content-addressed-storage-design.md`) rather than an age-based lifecycle rule, so the bucket
has no rule that automatically prunes noncurrent versions in the first place. `wal/`'s index
automatically gets the same treatment, so old generations are retained until explicitly deleted.
**This is called out here as an intentional setting.**

## 14. Changes to Existing Code

| File | Change |
|---|---|
| `internal/storage/storage.go` | Adds `GetWithGeneration` / `PutIfGeneration` / `ErrPreconditionFailed` / `ObjectInfo.Generation` |
| `internal/storage/gcs.go` | Implements the above via `storage.Conditions` |
| `internal/wal/` (new) | Index read/CAS, entry pack generation/upload, materialize, compaction |
| `internal/gitrepo/repo.go` | Injects the WAL dependency into `Manager`. Materializes before `Open`. Adds LRU eviction |
| `internal/gitserver/gitserver.go` | Adds `-c core.hooksPath=…` to the receive-pack exec. Adds GCS-related environment variables to `gitEnv()` (**`DATABASE_URL` is not passed**) |
| `internal/api/commit.go` | WAL-entry creation + CAS + retry after `Repo.Commit` |
| `cmd/thinkingface/main.go` | Branches the `hook` subcommand **before `config.Load()` / `store.Open()`** (hooks never open the DB; today `config.Load` requires `DATABASE_URL` and would error). Adds the `compact` subcommand |
| `Dockerfile` | Bundles `/opt/thinkingface/hooks/pre-receive`. Changes to `CGO_ENABLED=0` |
| `infra/` | Removes the GKE cluster / StatefulSet / PVC; adds a Cloud Run service (api) + Cloud Run Job (compact) |
| `docs/thinkingface-design.md` | Rewrites §14 |

`api/git.go`'s post-push before/after ref comparison and sync Enqueue, `syncer`, `lfs`, `viewer`,
and `store` are **unchanged**. `syncer` already has a ticker that picks up "jobs inserted by other
replicas," so it works as-is across multiple instances.

## 15. Staged Migration

Each phase can be deployed independently and rolled back at any time.

Implementation status (2026-08-21): the code for Phase 0-4 and 6 is complete, and E2E has passed
locally (docker compose + fake-gcs-server) in both shadow and authoritative modes. Phase 5/6's
Terraform has passed `terraform validate`; only applying it to production GCP and switching
traffic remain.

| Phase | Content | Completion criteria |
|---|---|---|
| **0** | Verify fake-gcs-server's `ifGenerationMatch` | Conclusion on whether it's supported. **If this fails, the design must be revisited** |
| **1** | `storage`'s CAS extension + `internal/wal`'s index read/write | Unit tests |
| **2** | **Shadow write**: writes the WAL on push, but PD remains the source of truth | All existing E2E passes |
| **3** | materialize implementation + **cross-check verification**: does PD's content match what's restored from the WAL | Matches for all repositories |
| **4** | Switch the source of truth to the WAL. Demote PD to a cache (still on GKE) | `make test-e2e` passes |
| **5** | Move to Cloud Run (HTTP/2, tmpfs, min-instances=1) | Stable under production traffic (**Terraform code completed 2026-08-21**: `google_cloud_run_v2_service.api` in `infra/main.tf`. Applying it and running stably under production traffic are not yet done) |
| **6** | compaction Job + GKE teardown | PD removal (**Terraform code completed 2026-08-21**: `google_cloud_run_v2_job.compact` + Cloud Scheduler; `infra/k8s/` already removed. The `thinkingface compact` subcommand itself is not yet implemented — see `cmd/thinkingface/main.go`. Actual PD removal via apply has not been done) |

Phases 2-3 are the essence of this. **Write the WAL while PD remains the source of truth, confirm
the two match, and only then** switch over. Don't skip this dual-write period.

## 16. Open Issues

1. ~~fake-gcs-server's precondition support~~ **Verified (2026-08-21, Phase 0 complete)**:
   v1.55.1 correctly implements `ifGenerationMatch` / `DoesNotExist` (confirmed with a race test —
   sequential plus 8-way concurrent × 30 rounds). Versions below v1.55.0 have a bug where the
   precondition is ignored on the resumable-upload path (fixed in PR #2260) → docker-compose.yml
   is pinned to `1.55.1`. Errors come back as `googleapi.Error` Code 412, same as real GCS
2. ~~`git pack-objects --revs`'s quarantine behavior~~ **Resolved (2026-08-21)**:
   the hook was implemented to use the WAL index's refs as the exclude list, rather than
   `--not --all` (`internal/wal/push.go`). New objects inside quarantine are visible through the
   environment pass-through of GIT_QUARANTINE_PATH / GIT_OBJECT_DIRECTORY /
   GIT_ALTERNATE_OBJECT_DIRECTORIES. Confirmed with both a `git push`-based integration test and
   unit tests
3. ~~The right size for the tmpfs cache~~ **Measured (2026-08-21, local dev environment)**:
   9 repositories total 1.9 MiB, largest is 340 KiB. Since LFS pushes the actual content out, a
   bare repository is roughly pointer files + README, so the tmpfs-cache assumption holds. In
   production too, the `TF_GIT_CACHE_BYTES` default of 2 GiB is expected to fit thousands of
   repositories
4. materialize frequency due to the lack of cache locality across Cloud Run instances. Tuning
   min-instances and concurrency comes after real measurements
5. `index.json`'s single point of failure. Recoverable via GCS versioning, but the recovery
   procedure needs to be documented for operations
6. **A known residual risk (confirmed in the 2026-08-21 review, mitigated but not fully
   resolved)**: no repository lock is held while smart HTTP's `upload-pack` / `receive-pack` is
   running. Because of this, while one is in flight, (a) LRU eviction, or (b) a full materialize
   rebuild triggered by another request (only when the base changes right after a compaction),
   could `RemoveAll` the same directory. As a mitigation, eviction only happens when "idle for 10+
   minutes AND TryLock succeeds," and since every request updates lastUse at the start, in actual
   operation (where LFS keeps repositories to a few hundred KiB and transfers take seconds) the
   race essentially never occurs. The WAL side is already committed, so this never causes data
   loss — the only impact is the failure of that one transfer. The full fix is to convert the
   per-repo lock to an RWMutex (with the read path holding a shared lock), tracked as a
   follow-up
