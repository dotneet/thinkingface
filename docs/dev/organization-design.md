# Organization Feature: Complete Design

Design for a feature that lets a multi-person team share a single namespace (`{org}/{name}`) to
operate model, dataset, and experiment repositories. Today only part of the DB schema and store
layer exists (`namespaces.kind='org'`, `org_members`, `store.CreateOrg`) — **there is no way to
create an organization, add people, or view one, in either the API or the UI**. This document is
the finalized design to bring that up to a level that can withstand real-world operation.

> **Note on visibility**: at the time this design was written, repositories had a public/private
> distinction, and the meaning of the `read` role was tied to "can read private repos." Later,
> `docs/dev/content-addressed-storage-design.md` §1 abolished the concept of visibility itself as a
> premise, so the permission matrix and role definitions from §4 onward have been rewritten. §2
> "Current state and issues" is left as-is as a record of that time — the `private` concept it
> mentions no longer exists in the current code.

Related documents: `thinkingface-design.md` §10-11 (data model, authorization),
`repo-transfer-design.md` §5 (transfer permissions), `api-contract.md` §1 / §9.

---

## 1. Goals and Non-Goals

### Goals

- Anyone (or only site admins) can create an organization and create repositories like `team/foo`
- Organization admins can add members, change their roles, and remove them; members themselves
  can leave
- The roles `admin` / `write` / `read` consistently control **push / delete / transfer / settings
  changes / viewing the member list** (resolving current inconsistencies such as an org appearing
  in the list but returning 404 on its detail page)
- Works out of the box with `huggingface_hub`: `whoami()["orgs"]` includes the organizations the
  user belongs to, and `create_repo("team/foo")` / `list_models(author="team")` /
  `list_organization_members("team")` all work
- A per-organization profile page and settings screens (profile / members / webhooks / storage
  usage / audit log / deletion)
- Whether the member list is public can be configured per organization (visibility of
  repositories themselves was already abolished in `docs/dev/content-addressed-storage-design.md` §1)
- Who did what, and when, can be traced (a per-organization audit log)

### Non-Goals

- Teams (sub-groups within an organization) and per-repository individual collaborator invites →
  future work. A role applies to the organization as a whole, one per member
- Email invitations (pending invites awaiting acceptance). Since this is self-hosted and every
  user is assumed to have an account on the same instance, we use **direct addition by username**
  instead (rationale in §5)
- Renaming an organization (a non-goal, same as in `repo-transfer-design.md`)
- Syncing with SSO groups, organization-scoped PATs (tokens continue to belong to users;
  permissions flow through membership)
- Avatar image upload (`avatar_url` is provided only as an optional field for an external URL)
- A user profile page (`/{username}`). This design covers only the organization page
  `/orgs/{name}`; the user-side equivalent can be built later from the same parts
  → **Later superseded by `docs/dev/namespace-design.md`**: both users and organizations are unified
  under `/{ns}`, and `/orgs/{name}` becomes a permanent redirect to `/{name}`
  (`/orgs/{name}/settings/*` stays as-is)

---

## 2. Current State and Issues

| Layer | Current state | Problem |
|---|---|---|
| DB | `namespaces(kind user\|org, owner_user_id)`, `org_members(role admin\|write\|read)` | No columns for organization description, display name, creator, etc. `owner_user_id` still points at the founder even for organizations |
| store | `CreateOrg` (adds the creator as admin), `NamespacesForUser`, `CanWriteNamespace`, `RoleInNamespace` | No listing/adding/changing/removing members, and no updating/deleting organizations |
| API | Only `GET /api/v1/me`'s `namespaces[]` (with role) | **Zero endpoints to create or operate on organizations**. `whoami-v2`'s `orgs` is hardcoded to `[]` |
| Authorization | `canRead`: private repos are only readable via `CanWriteNamespace` (owner or admin/write). `RepoFilter.ViewerID` / `visibilityClause`: allowed if there's a row in `org_members` | **The `read` role sees private repos in listings, but detail/resolve return 404**. The meaning of the `read` role is undefined |
| Authorization | `n.owner_user_id = $1` is treated as "equivalent to admin" everywhere | An organization's founder retains full privileges even after being removed from `org_members`. If the founding user is deleted, `ON DELETE CASCADE` **deletes the entire organization** |
| Authorization | Webhook management and repository deletion are allowed at write | These are operations that should be admin-only for organizations (only transfer is already admin-only) |
| Names | `validateName` only checks character classes | Namespaces can be created that collide with routes like `datasets` / `models` / `api` / `settings` / `orgs` (same for usernames) |
| UI | Only the namespace select in the new-repo form (derived from `user.namespaces`) | No organization page, settings screen, member management, or creation flow |
| E2E | None | Compatibility via organizations (whoami's orgs, create_repo into an org) is unverified |

---

## 3. Conceptual Model

```
users ──< org_members >── namespaces(kind='org')
                              │
                              └──< repositories (namespace_id)
                              └──< webhooks (namespace_id)
                              └──< org_audit_log
```

- **The namespace is a single naming table**: usernames and organization names are UNIQUE on
  `namespaces.name` (unchanged from today). If `alice` is a user, an organization with the same
  name cannot be created
- **A role is one per organization as a whole** (`admin` > `write` > `read`). There is no
  per-repository override
- **In a user namespace, the user themself is implicitly admin** (`owner_user_id = user`).
  Organization namespaces don't use `owner_user_id` at all — `org_members` is the sole source of
  authority
- **Site admins (`users.is_admin`) are treated as admin-equivalent for every organization**
  (though they don't have a row in `org_members`, so they don't appear in the member list or
  `whoami.orgs`)

### 3.1 Effective Role

Consolidate all authorization decisions into a single function:

```go
// internal/api/authz.go
type Role int
const (
    RoleNone Role = iota
    RoleRead
    RoleWrite
    RoleAdmin
)

// roleIn is the caller's effective role for ns.
//   site admin                       → RoleAdmin
//   ns.kind == user && owner == user → RoleAdmin
//   ns.kind == org  && org_members   → that role
//   otherwise                        → RoleNone
func (s *Server) roleIn(ctx context.Context, user *store.User, ns string) (Role, error)
```

`canRead` / `canWrite` / `canAdmin` are rewritten in terms of this (§4). The SQL in the store
layer (listing visibility, transfer permissions) keeps its existing shape of
"`owner_user_id = $1` OR an `org_members` role condition," but **setting an organization's
`owner_user_id` to NULL** (§6) removes founder privilege, so the SQL's meaning becomes
consistently "the user themself in a user namespace, OR a member of the organization."

---

## 4. Permission Matrix

| Operation | Non-member | read | write | admin | site admin |
|---|---|---|---|---|---|
| View the organization page (name, description, repo list) | ○ | ○ | ○ | ○ | ○ |
| View the member list | ×(*1) | ○ | ○ | ○ | ○ |
| View / clone / resolve a repository, view experiments | ○(*2) | ○ | ○ | ○ | ○ |
| Create a repository | × | × | ○ | ○ | ○ |
| push / commit / web edit / experiment ingest | × | × | ○ | ○ | ○ |
| Accept a repository transfer (organization as destination) | × | × | ○ (unchanged) | ○ | ○ |
| Change repository settings (rename, description, default branch — `PATCH /api/v1/repos/{kind}/{ns}/{name}`) | × | × | × | ○ | ○ |
| Archive / unarchive a repository | × | × | × | ○ | ○ |
| Delete a repository | × | × | **×** (changed) | ○ | ○ |
| Transfer a repository out of the organization | × | × | × (unchanged) | ○ | ○ |
| Manage webhooks | × | × | **×** (changed) | ○ | ○ |
| View storage usage | × | ○ | ○ | ○ | ○ |
| Add members / change roles / remove members | × | × | × | ○ | ○ |
| Leave on one's own | – | ○ | ○ | ○ (not allowed for the last admin) | – |
| Change profile / policy | × | × | × | ○ | ○ |
| View the audit log | × | × | × | ○ | ○ |
| Delete the organization | × | × | × | ○ (when there are 0 repositories) | ○ (same) |

*1 In organizations with `members_visibility = 'public'`, anyone can view the member list
(default is `members`).

*2 Repositories have no concept of visibility (`docs/dev/content-addressed-storage-design.md` §1).
Reading is open to everyone; role only controls the write side.

**Changes from existing behavior** (two points that narrow write-role privileges):

- Deleting an organization-owned repository (UI `DELETE /api/v1/repos/...`, HF
  `DELETE /api/repos/delete`) is admin-only. No effect for user namespaces, since the user
  themself is already admin
- Managing an organization's webhooks (`/api/v1/namespaces/{ns}/webhooks` and
  `/api/v1/webhooks/{id}`) is admin-only

The `read` role is defined as **"a member of the organization, who can view the member list and
storage usage, but cannot write."** Reading a repository is open to everyone regardless of role,
so what `read` grants is membership itself and the viewing scope that comes with it. HF's
`contributor` (can only write to repositories they created) is not introduced.

### 4.1 Policy (Organization Settings)

| Setting | Value | Where it applies |
|---|---|---|
| `members_visibility` | `members` (default) / `public` | Member list API, the members section of the org page |

Instance-wide setting:

| Environment variable | Value | Meaning |
|---|---|---|
| `TF_ORG_CREATION` | `anyone` (default) / `admin` | Whether any user can create an organization, or only site admins |

---

## 5. Behavior Details

### Creating an Organization

- The name goes through `validateName` + a **reserved-name check** (§6.3). It cannot collide with
  a username either (UNIQUE)
- The creator automatically becomes `admin`
- With `TF_ORG_CREATION=admin`, anyone other than a site admin gets 403

### Adding Members

- **Added directly by username** (no invitation). Rationale: in a self-hosted setup, (a) everyone
  already has an account on the same instance, (b) we cannot assume an email-sending
  infrastructure, and (c) GitHub/HF's "invite → accept" flow exists to protect external users who
  are harmless unless they leave — in a closed internal environment it's only friction. Being
  added is reflected immediately in the person's own `GET /api/v1/me` and recorded in the audit
  log
- A nonexistent username returns 404. An existing member returns 409 (role changes go through
  PATCH)
- The role at add time is one of `read` / `write` / `admin`. Unspecified defaults to `read`

### Role Changes / Removal / Leaving

- **An organization must always have at least one admin.** Demoting, removing, or having the last
  admin leave returns 409 `last_admin`
- A removed user's token remains valid, but loses permission on the organization's repositories
  starting with the next request (permissions are evaluated per request; not cached)
- Leaving uses the same endpoint as `DELETE /orgs/{org}/members/{self}` (allowed for non-admins
  too, as long as it's themself)

### Deleting an Organization

- Can be deleted **only when it has 0 repositories** (409 `has_repositories`). Each repository
  must be deleted or transferred first. This is deliberate friction so a single click can't take
  dozens of repositories down with it
- On deletion, `org_members` / `webhooks` / `org_audit_log` CASCADE. The `namespaces` row is
  removed, so the same name can be reused (since there are no repositories, no redirect
  (`repo_redirects`) is left behind; pending transfers addressed to the old organization name are
  removed via the `to_namespace_id` CASCADE)
- Site admins are bound by the same constraint (force-delete is not provided as an ops command;
  deleting repositories first is sufficient)

### Relationship to User Deletion

A user-deletion endpoint doesn't currently exist. We fix the constraint in advance for when one
is added later: if the target user is **the last admin of any organization, return 409** (a
different admin must be set up first). Organizations do not depend on their founder (§6).

### Role Does Not Affect Reading

Since repositories have no concept of visibility, detail / tree / resolve / raw / parquet /
model-meta / reading experiments / LFS batch download / search / lineage do not consult role at
all (`docs/dev/content-addressed-storage-design.md` §1). Role only affects write and admin
operations, plus viewing the member list and storage usage.

### Audit Log

The following is recorded per organization (`org_audit_log`, §6). A repository-level audit log is
a separate concern; here we cover only **organization admin operations and repository
creation/deletion**:

| action | Accompanying info |
|---|---|
| `org.created` / `org.updated` / `org.deleted` | Changed fields |
| `member.added` / `member.role_changed` / `member.removed` / `member.left` | target user, old/new role |
| `repo.created` / `repo.deleted` / `repo.transferred_in` / `repo.transferred_out` | repo kind/name, from/to |
| `webhook.created` / `webhook.updated` / `webhook.deleted` | webhook id, url |

Retention is indefinite (the row count is small since it tracks admin operations). The UI
paginates through the most recent 200 entries. Not delivered as a webhook event (adding `org.*`
to webhook `events` is future work; it can be added by extending the `WebhookEvent` enum).

---

## 6. Data Model

### 6.1 Migration `postgres/0010_organizations.sql` / `sqlite/0004_organizations.sql`

```sql
-- Organization profile and policy. Columns exist for user namespaces too but go unused (NULL / default).
ALTER TABLE namespaces ADD COLUMN display_name           TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN description            TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN website                TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN avatar_url             TEXT NOT NULL DEFAULT '';
ALTER TABLE namespaces ADD COLUMN members_visibility     TEXT NOT NULL DEFAULT 'members'
    CHECK (members_visibility IN ('members', 'public'));
ALTER TABLE namespaces ADD COLUMN created_by             BIGINT REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE namespaces ADD COLUMN updated_at             TIMESTAMPTZ NOT NULL DEFAULT now();

-- Add history info to membership.
ALTER TABLE org_members ADD COLUMN added_by   BIGINT REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE org_members ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE org_members ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX IF NOT EXISTS org_members_user_idx ON org_members (user_id);

-- Organizations no longer depend on their founder: guarantee the founder's admin row, then drop owner_user_id.
-- This means (a) removing the founder doesn't leave them with privileges, and (b) deleting the founding user
-- no longer CASCADE-deletes the organization.
INSERT INTO org_members (namespace_id, user_id, role)
    SELECT id, owner_user_id, 'admin' FROM namespaces
    WHERE kind = 'org' AND owner_user_id IS NOT NULL
ON CONFLICT (namespace_id, user_id) DO NOTHING;
UPDATE namespaces SET created_by = owner_user_id WHERE kind = 'org';
UPDATE namespaces SET owner_user_id = NULL        WHERE kind = 'org';

CREATE TABLE IF NOT EXISTS org_audit_log (
    id             BIGSERIAL PRIMARY KEY,
    namespace_id   BIGINT NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    actor_user_id  BIGINT REFERENCES users (id) ON DELETE SET NULL,
    actor_name     TEXT NOT NULL,              -- denormalized so it's still readable after the user is deleted
    action         TEXT NOT NULL,
    target_user_id BIGINT REFERENCES users (id) ON DELETE SET NULL,
    target_name    TEXT NOT NULL DEFAULT '',   -- target username / repo full_name / webhook URL
    details        JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS org_audit_log_ns_idx ON org_audit_log (namespace_id, id DESC);
```

The SQLite version follows the type mapping (`thinkingface-design.md` §10):
`BIGSERIAL`→`INTEGER PRIMARY KEY AUTOINCREMENT`, `TIMESTAMPTZ`→`DATETIME DEFAULT (strftime(...))`,
`JSONB`→`TEXT`, `BOOLEAN`→`INTEGER`. SQLite allows `CHECK` on `ALTER TABLE ... ADD COLUMN` (with
`DEFAULT`), so it's syntax differences only. `ON CONFLICT DO NOTHING` becomes `INSERT OR IGNORE`.

### 6.2 Store Layer (new `internal/store/orgs.go`; org-related code moves out of `users.go`)

```go
type Org struct {            // org view of a namespaces row
    ID int64; Name, DisplayName, Description, Website, AvatarURL string
    MembersVisibility string
    CreatedBy *int64; CreatedAt, UpdatedAt time.Time
}
type OrgMember struct { UserID int64; Username, Email, Role string; AddedBy *int64; CreatedAt, UpdatedAt time.Time }
type OrgSummary struct { Org; Role string; NumMembers, NumRepos int64 }   // for listings
type OrgUpdate  struct { DisplayName, Description, Website, AvatarURL *string; MembersVisibility *string }
type AuditEntry struct { ID int64; ActorName, Action, TargetName string; Details json.RawMessage; CreatedAt time.Time }

CreateOrg(ctx, name string, creator int64, in OrgUpdate) (*Org, error)            // extend existing (created_by, owner_user_id=NULL)
GetOrg(ctx, name string) (*Org, error)                                             // ErrNotFound for anything other than kind='org'
UpdateOrg(ctx, id int64, u OrgUpdate) (*Org, error)
DeleteOrg(ctx, id int64) error                                                     // ErrConflict if repositories exist (count → delete in the same Tx)
ListOrgsForUser(ctx, userID int64) ([]OrgSummary, error)                           // with role
ListOrgs(ctx, search string, limit, offset int) ([]OrgSummary, int64, error)       // public directory (role empty)
CountOrgRepos(ctx, id int64) (int64, error)

ListOrgMembers(ctx, id int64) ([]OrgMember, error)
AddOrgMember(ctx, id, userID int64, role string, addedBy int64) (*OrgMember, error) // ErrConflict if already a member
UpdateOrgMemberRole(ctx, id, userID int64, role string) (*OrgMember, error)        // ErrLastAdmin when demoting the last admin
RemoveOrgMember(ctx, id, userID int64) error                                       // ErrLastAdmin when removing the last admin
AppendOrgAudit(ctx, id int64, e AuditEntry) error                                  // also a variant that can join the caller's own Tx
ListOrgAudit(ctx, id int64, beforeID int64, limit int) ([]AuditEntry, error)
```

- `ErrLastAdmin` is a new sentinel added to `store`. The "last admin" check is done as
  **`SELECT count(*) ... role='admin'` → change, within the same transaction**; on PostgreSQL, the
  target organization's `namespaces` row is serialized with `FOR UPDATE` (the existing `forUpdate`
  dialect helper; a no-op on SQLite)
- `RoleInNamespace` / `CanWriteNamespace` / `NamespacesForUser` are kept as-is (only
  `owner_user_id` becomes NULL for organizations). `NamespacesForUser`'s result already includes
  `kind`, so the UI select can keep using it unchanged

### 6.3 Reserved Names

Among the places that call `validateName`, the **two that create a new namespace** (username at
signup, organization creation) add an extra check (this does not apply to repository names —
`alice/models` is legal):

```
api, apis, datasets, models, spaces, experiments, orgs, organizations, settings, new, login, logout, signup,
styleguide, healthz, static, _next, assets, raw, resolve, lfs, info, git, webhooks, transfers, me, whoami-v2
(later unioned with `docs/dev/namespace-design.md` §9. `admin` was excluded since it's the default seed username)
```

Reason: these collide with the frontend's `app/` routes, the backend's `/{ns}/{name}` routes, and
the HF-compatible `/datasets/{ns}/{name}`. Defined in exactly one place,
`backend/internal/api/names.go` (also mirrored in `frontend/lib/validation.ts`, with a comment
directing readers to keep them in sync). Existing users with a matching name are not removed
(only future creation is rejected).

---

## 7. API

### 7.1 For the UI (types defined in `apitypes`, `make gen-types`, add an "Organizations" section
to `api-contract.md` §1)

```
GET    /api/v1/orgs                         Public directory. ?search= &limit= &offset= → OrgListResponse
POST   /api/v1/orgs                         req OrgCreateRequest {name, display_name?, description?} → 201 OrgResponse
GET    /api/v1/me/orgs                      List of the caller's own organizations (with role) → OrgListResponse
GET    /api/v1/orgs/{org}                   → OrgResponse (with viewer_role. 200 even for non-members. 404 if the name is kind=user)
PATCH  /api/v1/orgs/{org}                   admin. req OrgUpdateRequest (partial update) → OrgResponse
DELETE /api/v1/orgs/{org}                   admin. 204 / 409 has_repositories
GET    /api/v1/orgs/{org}/members           Members (or anyone, if members_visibility=public) → OrgMembersResponse
POST   /api/v1/orgs/{org}/members           admin. req OrgMemberAddRequest {username, role} → 201 OrgMemberResponse / 404 user / 409 already_member
PATCH  /api/v1/orgs/{org}/members/{username}  admin. req {role} → OrgMemberResponse / 409 last_admin
DELETE /api/v1/orgs/{org}/members/{username}  admin or self. 204 / 409 last_admin
GET    /api/v1/orgs/{org}/audit-log         admin. ?before=<id>&limit= → OrgAuditLogResponse
```

```go
type OrgRole string // "admin" | "write" | "read". "" means non-member
type MembersVisibility string // "members" | "public"

type Org struct {
    Name, DisplayName, Description, Website, AvatarURL string
    MembersVisibility    MembersVisibility
    NumMembers, NumRepos int64        // NumRepos only counts what the viewer can see
    CreatedAt time.Time
    // ViewerRole is the caller's effective role (site admin gets "admin"). "" when not logged in.
    ViewerRole OrgRole
}
type OrgMember struct { Username, Email string; Role OrgRole; CreatedAt time.Time }   // Email is only returned to members (empty "" for public viewing)
type OrgAuditEntry struct { ID int64; Actor, Action, Target string; Details map[string]any; CreatedAt time.Time }
type OrgListResponse { Items []Org; Total int64 }
type OrgResponse { Org Org }
type OrgMembersResponse { Items []OrgMember }
type OrgMemberResponse { Member OrgMember }
type OrgAuditLogResponse { Items []OrgAuditEntry; NextBefore int64 }   // 0 marks the end
type OrgCreateRequest / OrgUpdateRequest / OrgMemberAddRequest / OrgMemberUpdateRequest
```

Changes to existing endpoints:

- `GET /api/v1/me`'s `namespaces[].role`: returns `org_members.role` for organizations, and
  `"admin"` for user namespaces (compatible, since it's already `admin` today via the `owner`
  check)
- `DELETE /api/v1/repos/...` / `DELETE /api/repos/delete`: admin required for organization-owned
  repos (new `loadRepoForAdmin`)
- `/api/v1/namespaces/{ns}/webhooks`, `/api/v1/webhooks/{id}`: admin required for organizations
  (`requireNamespaceWrite` → `requireNamespaceAdmin`; unchanged for user namespaces, where the
  user themself is admin)
- Transfers (`transfers.go`): initiating from an organization-owned repo requires admin
  (unchanged). On completion/acceptance, `repo.transferred_in/out` is recorded for both
  organizations

Error codes (`{"error": {"type": ...}}`): `org_creation_disabled` (403), `reserved_name` (400),
`last_admin` (409), `has_repositories` (409), `already_member` (409).

### 7.2 HF Compatibility

- Populate `GET /api/whoami-v2`'s `orgs`. The shape of each entry matches HF's keys:

  ```json
  { "type": "org", "id": "12", "name": "team", "fullname": "Team Inc.",
    "email": null, "canPay": false, "periodEnd": null, "avatarUrl": "",
    "isEnterprise": false, "roleInOrg": "admin" }
  ```

  `roleInOrg` passes through `admin` / `write` / `read` as-is (a subset of HF's enum
  `admin|write|contributor|read`). `huggingface_hub` just returns `orgs` as a list of dicts
  without validating its shape, but `hf auth whoami` displays `name` / `roleInOrg`
- `GET /api/organizations/{org}/members` — supports `HfApi.list_organization_members()`. Matches
  HF's return shape `[{"user": "alice", "fullname": "alice", "avatarUrl": "", "type": "user",
  "isPro": false}]` (HF doesn't return role either). Authorization is the same as the UI's
  `GET /orgs/{org}/members`
- `GET /api/users/{username}/overview` — supports `HfApi.get_user_overview()` and carries an
  `orgs` array of the same shape. It is a **membership disclosure by another route**, so it obeys
  the rule of the member list above rather than `whoami-v2`'s: an organization appears in it only
  when `members_visibility = 'public'`, or when the caller is a member of that organization
  (§4 *1). Organizations that fail the test are omitted from the array; the endpoint itself stays
  public and never answers 403. Without this, the members-only roster the two member-list
  endpoints protect could simply be reassembled username by username.
  `whoami-v2` describes the caller's own account and is not filtered
- `GET /api/organizations/{org}/overview` — supports `HfApi.get_organization_overview()`, and is
  the organization half of the user overview above (`handleHFOrgOverview`, routed in
  `internal/api/server.go`; the response is spelled out in `docs/dev/api-contract.md` §
  "GET /api/organizations/{org}/overview"). It describes the organization itself — display name,
  avatar, counts — and carries no membership, so the disclosure rule that filters the *user*
  overview's `orgs` array has nothing to apply to here. A user namespace of that name is a 404,
  the mirror of what `GET /api/users/{username}/overview` does for an organization
- HF organization APIs beyond the ones listed here are **not implemented** (no public method in
  `huggingface_hub` calls them). Unimplemented endpoints keep returning a JSON 404, as today
- `create_repo(..., organization=)` (a deprecated argument) and `repo_id="org/name"` both work
  through the existing `createRepo`. No changes
- `list_models(author="team")` works through the existing `author=` filter. No changes

---

## 8. Frontend

### 8.1 Routes

```
/orgs                               Public directory (search, create button)
/orgs/new                           Creation form (name / display_name / description). With TF_ORG_CREATION=admin, non-admins see guidance only
/orgs/{name}                        Organization page: header (avatar / display name / description / website / member count)
                                    + Models / Datasets / Experiments tabs (reuse the existing RepoListPage with author=)
                                    + Members section (only when viewable) + a "Settings" button for admins
/orgs/{name}/settings               Profile (display_name / description / website / avatar_url) and the 3 policy items
/orgs/{name}/settings/members       Member table (username, role Select, remove) + add form (username + role)
/orgs/{name}/settings/webhooks      Shows the existing WebhooksManager with namespace=org
/orgs/{name}/settings/storage       Shows the existing StorageUsage scoped to this organization
/orgs/{name}/settings/audit-log     Audit log (a "load more" button, not infinite scroll)
/orgs/{name}/settings/danger        Delete the organization (disabled with a count and link to the repo list if repos remain; a Dialog requires typing the org name)
/settings/organizations             List of the caller's own memberships (role badges, leave button, create link)
```

- `settings/*` is a Server Component: `getOrg()` (via `authHeaders()`) → `notFound()` if
  `viewer_role !== "admin"` (since existence itself is public information, a "you don't have
  permission" `ErrorState` is also fine instead of 404; since non-admins hitting a settings URL
  would mostly be from a bookmark, **we go with `ErrorState`**)
- New `lib/orgs.ts` (`listOrgs` / `getOrg` / `createOrg` / `updateOrg` / `deleteOrg` /
  `listMembers` / `addMember` / `updateMemberRole` / `removeMember` / `listAuditLog`). Follows
  `apiFetch`'s `ApiResult` convention and never throws
- New `lib/i18n/dictionaries/{en,ja}/org.ts`. Adds an `organizations` section to `settings.ts`

### 8.2 Integration into Existing Screens

- "Organizations" (`/settings/organizations`) and "New organization" added to the header's user
  menu
- New repository form: the namespace select shows a `kind` badge (`user` / `org`); when an
  organization is selected (since `user.namespaces` only has kind and role, `getOrg()` for the
  selected organization is called when the form renders, to fetch its policy)
- Repository page sidebar / breadcrumb: links to `/orgs/{name}` when the namespace is an
  organization (`namespace_kind` added to `RepoDetail`)
- Repository Settings tab: when it's organization-owned and the viewer has write, deletion /
  webhooks are shown as admin-only and disabled
- `/settings/transfers`: no changes (approving transfers to an organization stays at
  write-or-above, as today)

### 8.3 UI Conventions

Only `components/ui/` primitives; colors from semantic tokens; every screen carries the 3 states
(loading / empty / error); copy comes from the dictionary (`DESIGN.md` §7). Roles are color-coded
with `Badge` (admin = accent, write = default, read = muted).

---

## 9. Backend Changes (by File)

| File | Change |
|---|---|
| `store/migrations/{postgres/0010,sqlite/0004}_organizations.sql` | §6.1 |
| `store/orgs.go` (new) | §6.2. Moves `CreateOrg` / `RoleInNamespace` out of `users.go` |
| `store/store.go` | Adds `ErrLastAdmin` |
| `api/authz.go` (new) | `Role` type, `roleIn`, `canRead` / `canWrite` / `canAdmin`, `loadRepoForAdmin`, `requireOrgRole(w, r, org, min)` |
| `api/auth.go` | Changes `canRead` to `roleIn >= RoleRead`. Populates `handleWhoami`'s `orgs` |
| `api/orgs.go` (new) | §7.1 handlers + the audit-log recording helper `s.audit(ctx, nsID, action, ...)` |
| `api/hfcompat.go` | `GET /api/organizations/{org}/members` |
| `api/repos.go` | Adds policy checks + audit to `createRepo`; deletion moves to `loadRepoForAdmin` |
| `api/webhooks.go` | `requireNamespaceWrite` → `requireNamespaceAdmin` |
| `api/transfers.go` | Audit log on completion |
| `api/names.go` (new) | The reserved-name list and `validateNamespaceName` |
| `api/server.go` | Adds routes |
| `apitypes/apitypes.go` | §7.1 types. Adds `RepoDetail.NamespaceKind` |
| `config/config.go` | `OrgCreation` (`TF_ORG_CREATION`) |
| `cmd/thinkingface/main.go` | None (`seed` unchanged; no need to seed organizations) |

---

## 10. Existing Data and Backward Compatibility

- The migration only adds columns and nulls out `owner_user_id`; it doesn't touch repositories or
  objects. During a rolling deploy there's a brief window where the old binary still treats the
  founder as admin via `owner_user_id = $1`, but once switched to the new binary the same
  privilege is available through the `org_members` admin row (guaranteed by the migration), so
  the founder's operations never fail partway through
- `GET /api/v1/me`'s shape is unchanged. `whoami-v2` just has `orgs` go from an empty array to
  real data
- The narrowing of the write role (deletion, webhooks) is an **intentional breaking change**. It's
  called out explicitly in the CHANGELOG / PR description. Since there was no API before, any
  existing self-hosted environment using organizations must have done so by editing the DB
  directly
- SQLite mode behaves the same way. `FOR UPDATE` is a no-op (single writer)

---

## 11. Test Plan

- `store` (always on SQLite + `make test-store-pg`): create / update / delete (with repositories
  → `ErrConflict`), member add / change / remove, last-admin protection (demotion, removal), role
  in `ListOrgsForUser`, audit-log pagination, `owner_user_id` nulling and admin-row backfill in
  the migration
- `api` (`httptest`, following the pattern of the existing `transfers_test.go`): covers the §4
  permission matrix with a **role × operation table-driven test**. In particular: write cannot
  touch deletion / webhooks, non-members can still read repositories, reserved names,
  `TF_ORG_CREATION=admin`, `whoami-v2.orgs`
- `frontend` (vitest): `lib/orgs.ts`'s `ApiResult` handling, role-display helpers,
  `validation.ts`'s reserved names
- E2E (`e2e/test_orgs.py`, `make test-e2e`): create an organization via the UI API,
  `create_repo("org/x")` with an admin token → it shows up in `whoami()["orgs"]` → adding another
  user (via signup) with `read` lets them `hf_hub_download` but `upload_file` gets 403 →
  promoting to `write` allows `upload_file` → `list_organization_members` returns both names
- UI: verify the 3 states of `/orgs/{name}` and the settings screens in both dark and light themes

---

## 12. Implementation Phases

| Phase | Content | Completion criteria |
|---|---|---|
| **1. Unify authorization + schema** | Migration, `store/orgs.go`, `api/authz.go`, fixing `canRead`, making deletion / webhooks admin-only, reserved names | `make check` + table-driven permission-matrix tests. Fixes the read-role inconsistency even without a UI |
| **2. Organization API** | All §7.1 endpoints, `whoami-v2.orgs`, HF members, `apitypes` + `make gen-types`, `api-contract.md` update | E2E `test_orgs.py` passes |
| **3. UI** | `/orgs/*`, `/settings/organizations`, the user menu, policy handling in the new-repo form, i18n en/ja | `make check` including `bun run check:ui`. Verify both themes |
| **4. Operational features** | Audit log (recording + UI), policy settings UI, `TF_ORG_CREATION`, updates to `.env.example` / README / design doc §11 & §16 | `make check`, including the documentation updates |

Phase 1 has standalone merge value (a bug fix). Phases 2 onward stack sequentially.

---

## 13. Decisions and Open Items

Decisions:

- Roles are the three: `admin` / `write` / `read`. No `contributor`
- Members are added directly, with no invitation
- Deleting an organization requires 0 repositories. No force-delete
- An organization's `owner_user_id` is retired (NULL). `org_members` is the sole source of
  authority
- ~~The organization page URL is `/orgs/{name}` (`/{name}` is not chosen, since route collisions
  and the cost of managing reserved names would be too high; `/users/{name}` is also recommended
  when a user profile is built in the future)~~
  → **Superseded by `docs/dev/namespace-design.md`**: since the `/[ns]` route already exists and the
  cost of managing reserved names has already been paid, both users and organizations are unified
  under `/{ns}`. `/orgs/{name}` becomes a permanent redirect to `/{name}`;
  `/orgs/{name}/settings/*` is unchanged
- Repository deletion / webhook management are removed from write (admin-only)

Open items (future):

- Per-repository collaborators / teams
- Organization webhook events (`org.member_added`, etc.)
- A user-deletion endpoint (accounting for the last-admin constraint)
- Quota-style policies, such as an "LFS usage cap," in addition to `private_only`
