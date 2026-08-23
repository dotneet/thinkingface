# Repository Transfer (Changing Ownership Between Users / Organizations) Design

The design for a feature that moves ownership of a model or dataset (including experiment
repositories) to a different user or organization. **Never moving the actual data (LFS objects,
git history, WAL) during a transfer** is the top-priority design goal.

Related documents: `thinkingface-design.md` §3-4 (repository model, GCS layout),
`continuity-design.md` §3-5 (WAL key layout, invariants), `api-contract.md`.

---

## 1. Goals and non-goals

### Goals

- Move `alice/foo` to `bob/foo` or `team/foo`. Renaming (`alice/foo` → `alice/bar`) is handled
  through the same mechanism
- A transfer completes in **a single DB transaction**, and the repository is immediately usable
  under its new name for clone / push / the HF API / the Web UI
- LFS objects and git history (both the local bare repo and the WAL on GCS) are **never moved, not
  even one byte**
- Requests to the old name (`alice/foo`) are redirected to the new name (so existing
  `huggingface_hub` / `git` remote configurations don't break)
- Compatible with `huggingface_hub.HfApi.move_repo()` (`POST /api/repos/move`)
- If the destination is a user namespace outside the actor's own permissions, the transfer
  completes only after that user approves it (user-to-user transfers)

### Non-goals

- An audit-log UI for transfer history (future work; a record is still kept in the
  `repo_transfers` table)
- Forking / copying (a transfer reassigns ownership, it isn't a duplication)
- Renaming the namespace itself (user or organization)
- Permanently reserving old names (we follow GitHub's approach, where a redirect can be overwritten
  by a new repository)

---

## 2. Current state: places where the physical location is coupled to `(kind, ns, name)`

| Layer | Current key | Role | Does a transfer need to move it? |
|---|---|---|---|
| LFS (`lfs/{oid[0:2]}/{oid[2:4]}/{oid}`) | Content address | **Authoritative.** Large binary content | **No** (already independent of the name) |
| Non-LFS blob (`blobs/{sha[0:2]}/{sha[2:4]}/{sha}`) | Content address | Content of non-LFS git blobs | **No** (already independent of the name; see `docs/dev/content-addressed-storage-design.md`) |
| WAL (`wal/{models\|datasets}/{ns}/{name}/…`) | ns/name | **Authoritative** (in authoritative mode). Git history pack + index | Currently yes → **this design makes it unnecessary** |
| Local bare repo (`{root}/{models\|datasets}/{ns}/{name}.git`) | ns/name | Authoritative when WAL=off, a cache in WAL mode | Currently yes → **this design makes it unnecessary** |
| Each DB table | `repo_id` | Metadata | **No** (just an update to `repositories.namespace_id` / `name`) |

Reference sites (33 hits via `grep`): `gitrepo.Manager.Dir`,
`storage.WALPrefix/WALIndexKey/WALKey/WALBasePrefix/WALEntriesPrefix`, the pre-receive hook
environment variables in `api/server.go` (`TF_WAL_REPO_{KIND,NS,NAME}`), `wal/*`, `syncer`,
`cmd/thinkingface/{walops,hook}.go`.

> The `exports/` layer has already been removed (`docs/dev/content-addressed-storage-design.md`). Keys
> on GCS are now just the two content-addressed layers `lfs/` and `blobs/`, both fully independent
> of namespace and repository name. There was never a need to relocate GCS objects during a
> transfer in the first place.

---

## 3. Basic approach: separating the logical name from the physical location

Add **`storage_path`** to `repositories` (assigned at creation time, immutable thereafter,
UNIQUE), and **derive the physical location of the git history entirely from `storage_path`**.

```
Logical name   (kind, namespace, name)  ← Mutable. The address used by the UI / API / git URLs
Physical location  storage_path         ← Immutable. The address for the WAL prefix and the local bare dir
```

- New repository: `storage_path = "repos/{ulid}"` (factor `internal/wal/entry.go`'s `newULID()`
  out so `internal/store` can use it)
- Existing repository: a migration fills `storage_path` with **the current physical location
  as-is** (`"{models|datasets}/{ns}/{name}"`). **Zero existing objects are moved or renamed**
- Layout functions just concatenate `storage_path` as-is:

```
wal/{storage_path}/index.json            ← old: wal/datasets/alice/foo/index.json   new: wal/repos/01J…/index.json
wal/{storage_path}/base/{ulid}.pack
wal/{storage_path}/entries/{seq}-{ulid}.pack
{root}/{storage_path}.git                ← old: {root}/datasets/alice/foo.git        new: {root}/repos/01J….git
```

The existing layout just persists as a "legacy form" of the `storage_path` value; the code becomes
a single path. A transferred repository's physical location ends up containing the old owner's
name (the contents of `wal/datasets/alice/foo/` become `bob/foo`), but this is intentional
behavior — `repositories.storage_path` is the one and only mapping. For operations, look it up
with `thinkingface repo-info {kind}/{ns}/{name}` (added in §11).

There's also an option to make `storage_path` `repos/{id}` instead of a ULID, but that would take
two statements (INSERT, learn the id, then UPDATE), and risks a mismatch between the physical
location and the id if the DB is restored and ids are reassigned — so we use a ULID instead.

---

## 4. Data model

Migration `0008_repo_transfer.sql` (both `migrations/postgres/` and `migrations/sqlite/`).

```sql
-- (1) Separate out the physical location
ALTER TABLE repositories ADD COLUMN storage_path TEXT NOT NULL DEFAULT '';
UPDATE repositories r SET storage_path =
    (CASE r.kind WHEN 'model' THEN 'models/' ELSE 'datasets/' END)
    || (SELECT n.name FROM namespaces n WHERE n.id = r.namespace_id) || '/' || r.name
  WHERE storage_path = '';
CREATE UNIQUE INDEX idx_repositories_storage_path ON repositories (storage_path);

-- (2) Redirects from old names to a repository
CREATE TABLE repo_redirects (
    kind           TEXT   NOT NULL CHECK (kind IN ('dataset', 'model')),
    from_namespace TEXT   NOT NULL,            -- namespaces are never renamed later, so store as a string
    from_name      TEXT   NOT NULL,
    repo_id        BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, from_namespace, from_name)
);
CREATE INDEX idx_repo_redirects_repo ON repo_redirects (repo_id);

-- (3) Transfer requests (for the approval flow; even an immediate transfer keeps one row for audit)
CREATE TABLE repo_transfers (
    id               BIGSERIAL PRIMARY KEY,
    repo_id          BIGINT NOT NULL REFERENCES repositories (id) ON DELETE CASCADE,
    from_namespace_id BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    to_namespace_id  BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    to_name          TEXT   NOT NULL,          -- allows a simultaneous rename during transfer; normally the original name
    requested_by     BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status           TEXT   NOT NULL CHECK (status IN ('pending', 'accepted', 'rejected', 'cancelled', 'expired')),
    decided_by       BIGINT REFERENCES users (id) ON DELETE SET NULL,
    decided_at       TIMESTAMPTZ,
    expires_at       TIMESTAMPTZ NOT NULL,     -- expiry for pending state (7 days)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- At most one pending transfer per repository at a time
CREATE UNIQUE INDEX idx_repo_transfers_one_pending ON repo_transfers (repo_id) WHERE status = 'pending';
CREATE INDEX idx_repo_transfers_to_pending ON repo_transfers (to_namespace_id) WHERE status = 'pending';

```

> Since the `exports/` relocation job has been removed, there's no need to add `kind` / `payload`
> to `sync_jobs` (see §10). A transfer is fully self-contained within the DB changes in (1)-(3).

The SQLite version uses `BIGSERIAL` → `INTEGER PRIMARY KEY AUTOINCREMENT`, `TIMESTAMPTZ` → `TEXT`,
`JSONB` → `TEXT`, keeping the partial indexes as-is (SQLite supports partial indexes). This follows
the existing pattern for splitting migrations between the two dialects.

Add `StoragePath string` to `store.Repo`, always SELECTed via `repoColumns`. Also include it in
`store.RepoRef` (`AllRepoRefs`).

---

## 5. Transfer semantics

### What happens

| Target | Handling during transfer |
|---|---|
| `repositories.namespace_id`, `name` | Rewritten (this is the core of the transfer) |
| `repositories.storage_path` | **Unchanged** |
| WAL / local bare repo / LFS | **Unchanged, no operation** |
| `repo_files`, `parquet_files`, `repo_lfs_objects`, `exp_*`, `repo_download_stats`, `sync_jobs`, `repo_lineage` (this repository's own row) | Keyed by `repo_id`, so **they come along automatically with no operation needed** |
| Rows in `repo_lineage` where **another repository** points at this one (`target_namespace`/`target_name`) | The normalized target is UPDATEd to the new name (`raw`, being the card's original text, is left untouched) |
| `repo_redirects` | INSERTs `(kind, old_ns, old_name) → repo_id`. Rows for **other old names this repository previously held** are left as-is (every hop of old names still ends up at the latest name) |
| `webhooks` (scoped to a specific repository via `repo_id`) | **Deleted.** Letting the old owner keep receiving events for a repository they no longer own would be an information leak; the new owner re-creates it if needed |
| `webhooks` (scoped to a namespace, `repo_id IS NULL`) | No operation. The old namespace's subscription naturally stops receiving this repository's events, and the new namespace's subscription starts receiving them |
| `private` / `default_branch` / `card` / `downloads` etc. | Unchanged |
| `repo_transfers` | The `accepted` row is kept (for audit) |
| Webhook events | `repo.moved` is delivered to subscriptions on the **new** namespace (and the matching repository). Payload: `{kind, from: {namespace, name}, to: {namespace, name}, full_name}`. While awaiting approval, `repo.transfer_requested` (§16) goes to subscriptions on the destination namespace |

### Permissions

| Operation | Condition |
|---|---|
| Start a transfer | **Write** access on the source repository (`CanWriteNamespace(actor, from_ns)`: for a user namespace, that user; for an organization, an `admin` / `write` member). Transfers from an organization are restricted to `admin` only (so a `write` member can't walk off with a repository) |
| Complete immediately | In addition, the actor must also be able to create a repository at the **destination** (`CanWriteNamespace(actor, to_ns)`). This covers your own other namespace, an organization where you're an admin/write member, or a server admin (`is_admin`) |
| Becomes pending approval | The destination is a user namespace or organization outside the actor's permissions. The destination's owner (the user themself, or an org admin) approves it to complete |
| Approve / reject | A user with write permission on the destination namespace |
| Cancel | A user with write permission on the source (i.e. whoever could start it) |

The same applies even if the destination is a `private` organization / user. The destination
namespace's existence is checked when the transfer starts (a nonexistent namespace returns 404;
concerns about user enumeration are accepted given this is meant to be self-hosted — a setting to
collapse it to 400 can be added later if needed).

### Conflicts

- If the destination already has the same `(namespace_id, name, kind)`, return 409 (checked
  proactively rather than relying solely on the UNIQUE constraint, though the constraint still
  guards it as a backstop)
- Re-checked again at approval time too (if the other side creates a repo with the same name while
  the transfer is pending, it fails with a `rejected`-equivalent error)
- If a new repository is created at the old name, delete the corresponding `repo_redirects` row
  for that `(kind, ns, name)` (the new repository wins, same as GitHub)
- `create_repo` against a name that currently has a redirect pointing away from it is allowed (per
  the rule above, this removes the redirect). Names are never reserved

---

## 6. API

### HF-compatible: `POST /api/repos/move` (replaces the existing `handleNotImplemented` with a
real implementation)

Called by `huggingface_hub.HfApi.move_repo(from_id, to_id, repo_type)`.

```
POST /api/repos/move
Authorization: Bearer <write token>
{"fromRepo": "alice/foo", "toRepo": "team/foo", "type": "model"}
```

- 200: completed immediately → `{"url": "https://host/team/foo"}` (same shape as HF; datasets get
  `/datasets/team/foo`)
- 202: became pending approval → `{"url": ..., "pending": true, "transfer_id": 12}` (an extension
  not in HF; `move_repo` treats any 2xx as success)
- 401 / 403 / 404 / 409 use the existing error format (`writeError`)

### For the UI (types defined in `apitypes`, then `make gen-types`)

```
POST   /api/v1/repos/{kind}/{ns}/{name}/transfer        RepoTransferRequest  → RepoTransferResponse
DELETE /api/v1/repos/{kind}/{ns}/{name}/transfer        Cancel a pending transfer (by the source)
GET    /api/v1/repos/{kind}/{ns}/{name}/transfer        Returns the pending transfer, if any (for the settings-screen banner)
GET    /api/v1/me/transfers                             List of pending transfers I can approve + pending transfers I've initiated
POST   /api/v1/transfers/{id}/accept                    → RepoTransferResponse (includes the RepoDetail once completed)
POST   /api/v1/transfers/{id}/reject
```

```go
// apitypes
type RepoTransferRequest struct {
    Namespace string `json:"namespace"`      // destination
    Name      string `json:"name,omitempty"` // if omitted, keeps the current name
}

type RepoTransferStatus string // "pending" | "accepted" | "rejected" | "cancelled" | "expired"

type RepoTransfer struct {
    ID            int64              `json:"id"`
    Kind          string             `json:"kind"`
    FromNamespace string             `json:"from_namespace"`
    FromName      string             `json:"from_name"`
    ToNamespace   string             `json:"to_namespace"`
    ToName        string             `json:"to_name"`
    RequestedBy   string             `json:"requested_by"` // username
    Status        RepoTransferStatus `json:"status"`
    ExpiresAt     time.Time          `json:"expires_at"`
    CreatedAt     time.Time          `json:"created_at"`
}

type RepoTransferResponse struct {
    Transfer RepoTransfer `json:"transfer"`
    Repo     *RepoDetail  `json:"repo,omitempty"` // only once completed (immediate / accepted). Reflects the new name
}

type MyTransfersResponse struct {
    Incoming []RepoTransfer `json:"incoming"` // ones I can approve
    Outgoing []RepoTransfer `json:"outgoing"` // ones I (my namespace) initiated
}
```

`RepoDetail` **doesn't** expose `StoragePath` (it's internal information; the UI has no need to
know the physical location).

### Redirects (§9)

After a transfer, requests that arrive at the old name are answered as follows. The redirect
target is the repository's current name in `repositories`.

| Path | Response |
|---|---|
| Web page `/{kind}s/{old-ns}/{old-name}/...` | The Next.js side receives the API's 404+redirect info and calls `redirect()` (308-equivalent). The rest of the path is carried through unchanged |
| HF API `/api/{models\|datasets}/{ns}/{name}/...`, `/{ns}/{name}/resolve/...`, `/api/v1/...` | **308 Permanent Redirect** (preserves method and body; `requests` keeps POST across 307/308, so `upload_file` / `create_commit` also work against the old name) |
| git smart HTTP `/{ns}/{name}.git/info/refs` | **301.** By default (`http.followRedirects=initial`), git follows the redirect on info/refs and swaps in the new base URL for what follows |
| LFS batch `/{ns}/{name}.git/info/lfs/objects/batch` | 308. git-lfs relies on the result of following info/refs, so it normally arrives with the new URL already |
| `POST /api/repos/create` / `POST /api/v1/repos` specifying the old name | **Allows the creation** rather than redirecting, and deletes the matching redirect row |
| `DELETE /api/repos/delete` specifying the old name | 308 would risk accidentally deleting the new repository, so it's safer not to let a delete happen via the old name — but HF follows redirects and deletes anyway. Here we instead return **404** (a destructive operation against the old name must be explicitly retried against the new name) |

---

## 7. Execution flow

### 7.1 Immediate transfer (the actor has permission on both sides / is a server admin)

Everything happens in `store.TransferRepo(ctx, TransferSpec)`'s **single transaction**:

```
BEGIN
  SELECT ... FROM repositories WHERE id = $repo FOR UPDATE          -- serializes concurrent transfers/deletes
  Verify the destination namespace exists; check for a conflict on (to_ns, to_name, kind)  → 409
  UPDATE repositories SET namespace_id = $to, name = $to_name, updated_at = now() WHERE id = $repo
  INSERT INTO repo_redirects (kind, from_namespace, from_name, repo_id) VALUES (...)
     ON CONFLICT (kind, from_namespace, from_name) DO UPDATE SET repo_id = EXCLUDED.repo_id
  DELETE FROM repo_redirects WHERE kind = $kind AND from_namespace = $to_ns AND from_name = $to_name
     -- if the new name used to be someone's old name, that redirect shouldn't keep pointing here (the live repo takes precedence)
  UPDATE repo_lineage SET target_namespace = $to_ns, target_name = $to_name
     WHERE target_namespace = $from_ns AND target_name = $from_name      -- follow the normalized target
  DELETE FROM webhooks WHERE repo_id = $repo                               -- discard repository-scoped webhooks
  INSERT INTO repo_transfers (..., status = 'accepted', decided_by = $actor, decided_at = now())
COMMIT
```

After the commit (outside the transaction):

- Fire the `repo.moved` webhook
- **Nothing touches git / WAL / LFS / GCS.** Since the local bare dir is keyed by the fixed
  `storage_path`, not even `os.Rename` is needed, and GCS's `lfs/` and `blobs/` are content
  addressed and thus independent of namespace/repository name, so no relocation job of any kind is
  required (`docs/dev/content-addressed-storage-design.md`)

### 7.2 Approval flow (destination outside the actor's permissions)

```
Start:    INSERT repo_transfers (status='pending', expires_at = now() + 7d)      → 202
Approve:  BEGIN; SELECT repo_transfers FOR UPDATE; confirm status='pending' and not yet expired;
          same steps as 7.1 (from the repositories UPDATE onward); UPDATE repo_transfers SET status='accepted' ...; COMMIT
Reject:   status='rejected'
Cancel:   status='cancelled' (by the source)
Expiry:   checked against expires_at at approval time and flipped to 'expired' (no background
          cleanup job; listings exclude rows where expires_at < now())
```

The repository remains usable as normal while pending (it isn't locked). If the source repository
is deleted while pending, the transfer row is removed via CASCADE.

### 7.3 Relationship to concurrent pushes / reads

- The git path (`gitserver`), HF commit, materialize, and LFS batch all resolve `ns/name → repo` at
  the start of the request, then operate purely on `repo.StoragePath` from that point on. Even
  spanning a commit of the transfer transaction, they keep operating on the **same physical
  location**, so there's no conflict. The WAL index's CAS (`continuity-design` §5-1) guarantees
  eventual consistency
- The `push` job enqueued after a push completes carries `repo_id` and reads the `repositories` row
  as of when it's processed (the new name, if processed after a transfer), but the publish-target
  key under `blobs/` is determined by the blob's sha, so processing under either the old or new
  name resolves to the same key. There's no conflict window on the GCS side

---

## 8. Migrating to `storage_path` (code changes)

| Location | Change |
|---|---|
| `storage/layout.go` | `WALPrefix/WALIndexKey/WALBasePrefix/WALEntriesPrefix/WALKey(kind, ns, name, …)` → `(storagePath string, …)`. `LFSKey` / `BlobKey` stay unchanged since they're already content-addressed |
| `gitrepo.Manager` | `Dir/Exists/Init/Remove/Open/EnsureLocal/AdoptLocal(kind, ns, name)` → `(storagePath)`. `Dir` becomes `filepath.Join(root, filepath.FromSlash(storagePath) + ".git")`. The `store` layer guarantees `storagePath` never contains `..` and has no leading `/` (only a ULID or `{kindDir}/{ns}/{name}` can end up in it) |
| `gitrepo/wal.go maybeEvict` | Change the fixed 3-level walk over `{root}/{models\|datasets}/{ns}/{name}.git` into a `WalkDir` across `root` that picks up any directory ending in `.git` (`repos/{ulid}.git` is only 2 levels) |
| `wal/*` | Replace `kind, ns, name` arguments with `storagePath`. Entry names inside the index are repository-relative, so **the index format itself is unchanged** (`continuity-design` §4's `version: 1` stays as-is) |
| `api/server.go` (pre-receive hook env) | `TF_WAL_REPO_{KIND,NS,NAME}` → a single `TF_WAL_STORAGE_PATH`. Same change in `cmd/thinkingface/hook.go` |
| `api/repos.go` `handleCreateRepo` | Assign a `storage_path` and pass it to `store.CreateRepo`. The "stale WAL before create" cleanup (l.186-195) can be removed, since a ULID can never collide (the legacy form is never used for new creations) |
| `api/repos.go` `handleDeleteRepo` / `purgeWAL` | Delete by `repo.StoragePath`. The GCS side (`lfs/` `blobs/`) is left to reference-counted `gc` (never touched immediately by transfer or delete) |
| `syncer` | `gitrepo.Open(repo.StoragePath)`. The publish target under `blobs/` is determined by the sha, so it doesn't depend on `repo.Kind/Namespace/Name` |
| `cmd/thinkingface/walops.go` (gc) | Include `StoragePath` in `AllRepoRefs`; the WAL-orphan scan lists all of `wal/` and cross-references it against the set of `storage_path` values in the DB (`models/…`, `datasets/…`, `repos/…` are all treated uniformly) |
| `api/git.go` `routeGit` etc. | Pass the resolved `repo.StoragePath` to gitserver |
| `store` | `Repo.StoragePath`, `RepoRef.StoragePath`, `CreateRepo(..., storagePath)`, `TransferRepo`, CRUD for `repo_redirects` / `repo_transfers`, `ResolveRepoRedirect(kind, ns, name)`, `DeleteRepoRedirect` |
| `docs/dev/thinkingface-design.md` §4-5 / `continuity-design.md` §3 | Update the key layout to `wal/{storage_path}/`, and add a note explaining the legacy form |

Existing `_test.go` files follow the signature changes. The WAL-related tests in
`storage/layout_test.go` get rewritten to take `storagePath` as input.

---

## 9. Resolving redirects

Put a shared resolution function in `api/auth.go`'s `loadRepoForRead` / `loadRepoForWrite` (and the
git routing's `GetRepoAnyKind`):

```go
// resolveRepo returns the repository for (kind, ns, name). When the name has
// been moved it returns ErrRepoMoved carrying the current full name, so the
// handler can answer 308 / 301 instead of 404.
func (s *Server) resolveRepo(ctx, kind, ns, name) (*store.Repo, error)
```

- `repo_redirects` is only consulted when `store.GetRepo` returns `ErrNotFound` (so we don't add a
  JOIN to the hot path)
- A handler that receives `ErrRepoMoved{Kind, Namespace, Name}` returns a `Location` built by
  **substituting the `{ns}/{name}` portion of the current request path** (preserving the query
  string too). Shared helper: `redirectMoved(w, r, moved)`
- Web UI (Next.js): `lib/api.ts`'s `apiFetch` doesn't set `redirect: "manual"`, so fetch would
  follow automatically. Since the page needs to end up at the correct URL, the UI-facing API
  instead returns **`404` + `{"error":"repo_moved","moved_to":{namespace,name}}`** rather than 308;
  `lib/repos.ts`'s `getRepo` surfaces this as `{ ok: false, status: 404, movedTo }` in the
  `ApiResult`, and the page calls `redirect()`. `apiFetch`'s no-throw design (CLAUDE.md invariant
  3) stays intact
- Typing an old URL directly into a web page uses Next.js's `permanentRedirect()` (308)

No caching is kept (transfers are rare, and `repo_redirects` is a single primary-key lookup).

---

## 10. GCS never moves during a transfer

With the removal of the `exports/` layer (`docs/dev/content-addressed-storage-design.md`), keys on GCS
are now just the two content-addressed layers `lfs/{oid...}` and `blobs/{sha...}`. Both are
completely independent of namespace and repository name, so **there was never any GCS-side
relocation job needed for a transfer in the first place**. The old design needed a job
(`sync_jobs.kind = 'relocate_exports'`) that would "server-side copy to the new prefix and delete
the old prefix on every transfer," because the export layout used human-readable `(ns, name)` keys
— but since that premise no longer holds, neither the design nor the implementation is needed
anymore.

Right from the moment a transfer completes, the Web UI / API / git / direct GCS fetch (the script
returned by `GET .../gcs/{rev}`) all work under the new name. There's no delay, no window.

Past alternatives that were considered and **not adopted** (from back when `exports/` was still
assumed; kept here as a decision record):

- **Key `exports` by `storage_path` too**: this loses the human-readable path, defeating the point
  of `gcloud` / BigQuery external tables. → The approach adopted in this design — fully decoupling
  GCS keys from namespace, and expressing the human-readable path on the destination side of the
  script instead — is the natural evolution of this idea
- **Leave the old prefix in place and don't create the new prefix until the next push** / **leave
  behind a `_MOVED.json` marker**: both were stopgap measures that assumed the exports layer still
  existed; once that layer was removed entirely, there was nothing left to consider

---

## 11. Operational commands

- `thinkingface repo-info {kind}/{ns}/{name}`: displays `repo_id` / `storage_path` / the WAL
  index's generation / the list of redirect sources. Also accepts `--storage-path repos/01J…` to
  reverse-lookup "which repository does this WAL prefix belong to" after a transfer
- The existing `thinkingface gc` gets `storage_path` support as described in §8 (the reference-
  counted GC of `lfs/` / `blobs/` itself is unrelated to `storage_path`; see
  `docs/dev/content-addressed-storage-design.md`)

---

## 12. Web UI

- Add a **Settings tab** to the repository page (`/{kind}s/{ns}/{name}/settings`, shown only to
  those with write access; added to `repo-tabs.tsx`)
  - A "Transfer ownership" card: destination namespace (a select for your own namespace / your
    organizations, a free-text username / org-name field otherwise), an optional new name, and a
    confirmation `Dialog` (`ui/Dialog`) using the GitHub-style pattern of typing `ns/name` to
    confirm
  - While pending, the same card shows a banner (destination, expiry, cancel button)
  - The same tab will eventually also hold "Delete repository" (a TODO delete feature)
- `/settings/transfers`: pending transfers addressed to you (approve / reject) and pending
  transfers you've initiated (cancel). A count badge in the header's user menu
- Accessing the old URL uses `permanentRedirect()`. We don't show a "moved from `alice/foo`" notice
  at the top of the repository page right after a transfer (HF doesn't either; history lives in
  Settings)
- All copy comes from the en / ja dictionaries in `lib/i18n/`. Only `ui/` primitives are used. The
  3 states (loading / empty / error) apply

---

## 13. Existing data and backward compatibility

- **Existing repositories migrate with zero downtime**: the migration just fills `storage_path`
  with the current physical location, without touching any object or directory. During a rolling
  deploy, the old binary (deriving from `kind/ns/name`) and the new binary (deriving from
  `storage_path`) see the same key
- The WAL index format and the pre-receive hook's WAL protocol are unchanged. Only the hook's
  environment variable name changes, so **the hook script and the server binary must be updated
  together in the same image** (which is already the case today, both being bundled in the same
  image)
- Behavior is identical across `TF_WAL_MODE=shadow` / `authoritative` / `off` (in `off` mode the
  local bare dir is authoritative, but since that's also keyed by the fixed `storage_path`, it
  doesn't move during a transfer either)
- SQLite mode: the same migration goes into `migrations/sqlite/`. Since it's single-process,
  `FOR UPDATE` is a no-op for that dialect (the existing `forUpdate` helper)
- API behavior is unchanged apart from `huggingface_hub`'s `move_repo`. Add `POST /api/repos/move`
  and the UI-facing endpoints to `api-contract.md` §2, and `repo.moved` to §9

---

## 14. Test plan

- `store` (SQLite always, plus `make test-store-pg`): `TransferRepo`'s happy path / 409 /
  serialization of concurrent transfers, overwrite/delete of `repo_redirects`, following of
  `repo_lineage` targets, deletion of repository-scoped webhooks, uniqueness of `storage_path`,
  the migration's backfilled values
- `gitrepo` / `wal`: Dir / WALPrefix behave as expected for `storagePath` in both the
  `repos/{ulid}` and `datasets/ns/name` forms; the eviction walk picks up both forms
- `api`: `POST /api/repos/move` returns 200 / 202 / 403 / 409; a GET against the old name returns
  308; git `info/refs` returns 301; creation against the old name succeeds and removes the
  redirect row; the UI-facing API's `repo_moved` response
- E2E (`make test-e2e`, **invariant 5**):
  - After `HfApi.move_repo`, `hf_hub_download` / `upload_file` succeed under the old id, and
    `repo_info(new_id)` matches
  - After `git clone http://…/alice/foo.git` followed by a transfer, `git pull` / `git push`
    succeed via the redirect (`-c http.followRedirects=initial` is the default)
  - The set of `gs://` URIs returned by `GET .../gcs/{rev}` is unchanged before and after the
    transfer (confirms the design never moves GCS objects)
  - Consistency of a push spanning a transfer under WAL authoritative-mode compose (if the compose
    used by `make up` has a WAL configuration)

---

## 15. Implementation phases

> **Implementation status (2026-08-22)**: Phases 1-3 have been implemented on the
> `feat/repo-transfer` branch (introducing `storage_path`, `POST /api/repos/move` and the
> UI-facing transfer API, redirects, the `repo.moved` / `repo.transfer_requested` webhooks, the
> Settings tab, `/settings/transfers`, and E2E coverage in `e2e/test_repo_transfer.py`).
> Phase 4's `thinkingface repo-info` is not yet implemented.
> The `relocate_exports` job and `sync_jobs.kind`/`payload` that existed at the time have since
> been removed, following the removal of the `exports/` layer
> (`docs/dev/content-addressed-storage-design.md`) (§10).

1. **Separating out the physical location** (ship this first without the transfer feature itself —
   ship the highest-risk change on its own)
   - Migration (`storage_path` column + backfill + UNIQUE), `store.Repo.StoragePath`
   - Signature changes across `storage/layout.go`, `gitrepo`, `wal`, `gitserver`, `syncer`, `cmd`,
     and the hook env change
   - Assign `repos/{ulid}` for new creations
   - `make check` + `make test-e2e` + regression checks under WAL mode
2. **The transfer feature itself**
   - `repo_redirects` / `repo_transfers`, `store.TransferRepo`, redirect resolution,
     `POST /api/repos/move`, the UI-facing API, the `repo.moved` webhook
   - `apitypes` + `make gen-types` + `api-contract.md`
   - Additional E2E coverage
3. **UI**
   - Settings tab / Transfer card / `/settings/transfers` / `permanentRedirect` for old URLs
4. **Operations**
   - `thinkingface repo-info`, updates to design doc §4-5 and `continuity-design.md` §3

---

## 16. Decisions and remaining work

The following were settled during design review (2026-08-22).

| Question | Decision |
|---|---|
| Who can start a transfer of an organization-owned repository | **Org `admin` only** (`write` members cannot). For a user namespace, the user themself; a server admin (`is_admin`) can do it from anywhere |
| Notifying about a pending approval | **A UI badge (header) + `/settings/transfers`**, plus delivering **`repo.transfer_requested`** to the destination namespace's namespace-scoped webhooks. Payload: `{transfer_id, kind, from: {namespace, name}, to: {namespace, name}, requested_by, expires_at}`. Only the final `repo.moved` (on approval) is sent when it's resolved — reject / cancel / expiry emit no event |
| Redirects to the old name | **Stand indefinitely until a new repository is created under the old name** (GitHub's approach). Old names are never reserved |

Remaining work (to be decided during implementation):

- For a transferred `is_experiment` repository (trackio's `space_id`), confirm whether the
  `clients/python` shim follows the ingest API's 308; if it doesn't, have the shim read `Location`
  and resubmit
