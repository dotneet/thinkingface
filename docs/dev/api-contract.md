# thinkingface API Contract

The confirmed specification between the backend (Go) and frontend (Next.js), and between internal packages.
**This document is authoritative.** The implementation must match it. If a change is needed, fix this document first.

- Base URL: `TF_PUBLIC_URL` (default `http://localhost:8080`)
- Referenced from the frontend via `API_URL` (server side) / `NEXT_PUBLIC_API_URL` (browser side)
- Two authentication schemes
  - **Cookie session** `tf_session` (for the Web UI, httpOnly)
  - **Bearer token** `Authorization: Bearer tf_xxx` (for CLI / Python). git uses HTTP Basic (`user:tf_xxx`)
- Error responses uniformly use `{"error": {"message": "...", "type": "..."}}` + an appropriate HTTP status
- **Every error response also carries `X-Error-Message`, equal to `error.message` verbatim**
  (`writeError` in `internal/api/errors.go`). `huggingface_hub.utils._http.hf_raise_for_status`
  reads this header before it ever looks at the body, and this API spells `error` as an *object*
  rather than upstream HF's plain string — without the header, the only text `huggingface_hub` can
  pull out of the body is that object's Python `repr()` (`"{'message': '...', 'type': '...'}"`),
  which is what a caller used to see in place of the actual sentence. **Exception:**
  `POST /api/repos/create`'s duplicate-name 409 keeps HF's own error body
  (`{"error": "You already created this model repo"}`, a plain string, exactly what
  `huggingface_hub` expects there) instead of going through `writeError`, so that one route alone
  carries neither header.
- **Some 404s also carry `X-Error-Code`**, which is what lets `huggingface_hub` raise the specific
  subclass (`RepositoryNotFoundError` / `RevisionNotFoundError` / `EntryNotFoundError`) instead of a
  bare `HfHubHTTPError` — and, for `RepositoryNotFoundError` specifically, what makes
  `repo_exists()` / `file_exists()` / `revision_exists()` answer `False` instead of raising, since
  those three only catch that one exception:
  - `RepoNotFound` — the repository does not exist. That is the whole of it: there is no
    private/gated visibility in this system, so no repository is ever hidden from a caller who
    could otherwise reach the server, and an authenticated caller never sees a repository an
    anonymous one does not (`docs/dev/thinkingface-design.md` §11). Applies uniformly across every
    HF-compatible read *and* write on a repository (repo-info, tree, paths-info, resolve, commits,
    preupload, commit, LFS batch) and to the equivalent Web UI route, whether the miss is the
    repository or the whole namespace — see `backend/internal/api/errors_signal_test.go`. **Not**
    used for a 403/409/401 that is about permission or archival state or a genuine name conflict —
    an archived-but-readable repository or a name collision must not look like `RepoNotFound`, or
    `repo_exists()` would answer `False` for a repository the same client can still list.
  - `RevisionNotFound` — the repository exists but `{rev}` does not resolve on it.
  - `EntryNotFound` — the repository and `{rev}` both resolve, but the path inside doesn't (e.g.
    `tree/{rev}/{path}` for a path that isn't there). `HfFileSystem.glob` — and so
    `datasets.push_to_hub`, which globs before uploading — depends on this one specifically.
  A 200 never carries either header.
- **Security headers attached to every response** (`internal/api.securityHeaders`):
  `X-Content-Type-Options: nosniff` / `X-Frame-Options: DENY` /
  `Referrer-Policy: strict-origin-when-cross-origin`
- **CORS uses an allowlist approach.** Only an `Origin` matching `TF_ALLOWED_ORIGINS`
  (comma-separated) gets `Access-Control-Allow-Origin` and `Access-Control-Allow-Credentials: true`
  back. A non-matching `Origin` gets no CORS headers at all (the request itself still goes through).
  When unset, the default is the origin of `TF_PUBLIC_URL`, plus, if `TF_PUBLIC_URL` is not https,
  `http://localhost:3000` / `http://127.0.0.1:3000`. `Vary: Origin` is always attached.
  `Access-Control-Expose-Headers` names what browser JS is allowed to read back: `ETag`,
  `X-Repo-Commit`, `X-Linked-Etag`, `X-Linked-Size`, `Content-Length`, `Content-Range`,
  `Accept-Ranges`, `Location`. `Content-Range` / `Accept-Ranges` are on the list so a
  cross-origin range read of `resolve` can tell which bytes it was given — a header left off
  it is invisible to the caller, not merely unadvertised.
- **CSRF**: For requests other than GET/HEAD/OPTIONS authenticated via cookie session,
  if the `Origin` (or the `Referer`'s origin if absent) doesn't match the allowlist, the response
  is **403 `forbidden`**. Requests with neither `Origin` nor `Referer` are allowed through
  (calls from `huggingface_hub` / git / curl / Server Component forwarding don't come from a browser).
  Requests authenticated via Bearer / Basic are exempt.

`{kind}` is `dataset` | `model`. The plural forms in the URL are `datasets` / `models`.
`{rev}` is a branch name, tag name, or commit SHA (a single segment) — but a branch or tag name is
itself allowed to contain `/` (e.g. `feature/tokenizer`), which `huggingface_hub` always sends
percent-encoded (`quote(revision, safe="")`), so `{rev}` arrives in the request path as
`feature%2Ftokenizer`, not as two path segments. This applies everywhere `{rev}` appears — resolve,
tree, paths-info, commit, preupload, branch/tag writes, commits, repo-info's `/revision/{rev}` —
not only the branch/tag routes. chi routes on the raw (still-escaped) path, so every handler that
reads `{rev}` unescapes it itself rather than relying on the router to have decoded it; a handler
that skipped this would see the escaped form itself where the client meant a plain revision string.
`{path...}` is everything remaining (including slashes).

---

## 1. Authentication & Accounts

### `POST /api/v1/auth/login`
req: `{"username": "admin", "password": "admin"}`
res 200: `{"user": User}` + `Set-Cookie: tf_session=...`
- 401 `unauthorized`: `username or password is incorrect` (the same message and the same processing
  time are used even when the user doesn't exist — a dummy bcrypt pass runs for nonexistent users
  too, so accounts can't be enumerated)
- **429 `rate_limited`** + `Retry-After`: on repeated failures. By default `TF_AUTH_RATE_LIMIT_PER_MIN`
  (default 10) attempts/minute from the same IP, and half that rate per minute for the same username.
  **Only failures are counted**; a success resets the counter.
  The counter is process-local (per replica when there are multiple; SQLite mode is single-process
  by design anyway). The same limit also applies to **HTTP Basic password authentication, accepted
  on every route**. Once the threshold is exceeded, bcrypt is not run and the request is simply
  treated as "unauthenticated" (the response doesn't change).
  `Authorization: Bearer tf_...` and Basic passwords that are tokens starting with `tf_` are exempt,
  since those go through a single SHA-256 pass.
- **503 `overloaded`** + `Retry-After`: when concurrently running bcrypt calls hit their cap and no
  slot freed up even after waiting. **The password was never checked**, so this says nothing about
  the credentials. It's kept separate from 401 because telling a user with correct credentials that
  they were "wrong" — and burning their failure counter under server load — would be misleading.
  This response is not counted as a failure.

### `POST /api/v1/auth/signup`
req: `{"username","email","password"}` → res 200: `{"user": User}` + cookie
(403 when `TF_ALLOW_SIGNUP=false`)
- `password` must be **8-72 bytes**. 72 bytes is bcrypt's limit; exceeding it returns
  400 `bad_request` (it used to return 500). A Japanese passphrase of roughly 24 characters
  already reaches 72 bytes.
- `email` is required. It must be at most 254 bytes and contain `@` and a `.` in the domain part.
  Violations return 400.
- Duplicate detection for the username (= personal namespace name) is **case-insensitive**.
  `Alice` and `alice` are treated as the same namespace, and whichever signs up second gets 409.
  Display and URLs keep the spelling as registered (it is not re-saved via `lower()`).
- The same rate limit as login (per IP) applies.

### `POST /api/v1/auth/logout`
res 204. Invalidates the cookie and **also invalidates all of that user's existing sessions
server-side** (increments `users.session_epoch`). `tf_session` is a signed value of the form
`<userID>.<epoch>.<expiry>.<hmac>`; any value whose `epoch` doesn't match the current DB value
is rejected. Session lifetime is `TF_SESSION_TTL` (default 7 days).

### `GET /api/v1/me`
res 200: `{"user": User}` / 401 if unauthenticated

```ts
type User = {
  id: number
  username: string
  email: string
  is_admin: boolean
  display_name: string          // Profile from the user's own namespace row. "" if unset
  avatar_url: string            // same as above
  namespaces: { name: string; kind: "user" | "org"; role: string }[]
}
```

`display_name` / `avatar_url` are looked up from the `namespaces` row (not held on `users`; §1.2).
Both are `""` right after signup. Edited via `PATCH /api/v1/me/profile`.

`namespaces[].role` means different things depending on the namespace kind: for an organization
(`kind: "org"`) it's `org_members.role` (`admin` / `write` / `read`); for a user's own namespace
(`kind: "user"`) it's always `"admin"` since the owner is implicitly an admin
(`docs/dev/organization-design.md` §3).

### `GET /api/whoami-v2`  (HF-compatible)
res 200:
```json
{ "type": "user", "id": "1", "name": "admin", "fullname": "admin",
  "email": "admin@example.com", "emailVerified": true, "canPay": false,
  "isPro": false,
  "orgs": [
    { "type": "org", "id": "12", "name": "team", "fullname": "Team Inc.",
      "email": null, "canPay": false, "periodEnd": null, "avatarUrl": "",
      "isEnterprise": false, "roleInOrg": "admin" }
  ],
  "auth": { "type": "access_token",
            "accessToken": { "displayName": "cli", "role": "write" } } }
```
`orgs` lists the organizations the authenticated user belongs to (excludes implicit membership as
site admin — only rows that exist in `org_members`). `roleInOrg` is `admin` / `write` / `read`
(a subset of HF's enum `admin|write|contributor|read`; `contributor` is not used).

`fullname` is the profile's own `display_name`, or the username if unset. `avatarUrl` is
`avatar_url` (`""` if unset). Same convention already used for `orgs[].fullname` / `avatarUrl`
against organizations (`docs/dev/namespace-design.md` §5.3). What `hf auth whoami` displays follows this.

### `GET /api/organizations/{org}/members`  (HF-compatible)
Called by `HfApi.list_organization_members()`. Authorization is the same as the UI-facing
`GET /api/v1/orgs/{org}/members` (§1.1). res 200 (an array; HF doesn't return a role either):
```json
[ { "user": "alice", "fullname": "alice", "avatarUrl": "", "type": "user", "isPro": false } ]
```

### `GET /api/users/{username}/overview`  (HF-compatible)
Called by `HfApi.get_user_overview()`. Anyone can call it (even unauthenticated). res 200:
```json
{ "user": "alice", "fullname": "Alice A.", "avatarUrl": "https://cdn.example/a.png",
  "details": "Short bio", "type": "user",
  "numModels": 3, "numDatasets": 2, "numSpaces": 0, "numLikes": 0,
  "numFollowers": 0, "numFollowing": 0, "isPro": false,
  "orgs": [ /* same shape as whoami-v2's orgs. That user's memberships */ ] }
```
Passing an organization name returns 404 (the endpoint is split by kind, matching HF's behavior).
Returns 404 if it doesn't exist.
`numLikes` / `numFollowers` / `numFollowing` / `numSpaces` are fixed at 0 since they aren't modeled.

### `GET /api/organizations/{org}/overview`  (HF-compatible)
Called by `HfApi.get_organization_overview()`. Anyone can call it (even unauthenticated). res 200:
```json
{ "name": "team", "fullname": "Team Inc.", "avatarUrl": "", "details": "",
  "numUsers": 12, "numModels": 8, "numDatasets": 3, "numSpaces": 0,
  "numFollowers": 0, "isVerified": false }
```
Passing a username returns 404. `numUsers` returns the member count regardless of
`members_visibility` (that setting hides the roster, not the headcount).

In both endpoints, `numDatasets` **includes experiment repositories** — from HF's point of view an
experiment repository is a dataset, and it also appears in `GET /api/datasets`. The UI-facing
`GET /api/v1/namespaces/{ns}`, conversely, splits `num_datasets` and `num_experiments` (§1.2).

### Token management
- `GET /api/v1/tokens` → `{"items": [{id, name, scope, created_at, last_used_at, expires_at}]}`.
  Includes expired tokens (`expires_at` in the past) -- the list never filters on expiry, so the
  owner can see why a token stopped working and still delete it.
- `POST /api/v1/tokens` req `{"name","scope":"read"|"write","expires_in_days"?: number | null}` →
  `{id, name, scope, expires_at, token}` (`token` is returned only at creation time, with a `tf_`
  prefix)
- `DELETE /api/v1/tokens/{id}` → 204. Works on an expired token the same as a live one.

`expires_in_days` omitted, `null`, or `0` means the token never expires (`expires_at` is `null`).
Otherwise it must be a positive integer up to **365**; anything negative or over that cap is 400
`bad_request`. The expiry is resolved to an absolute UTC instant in Go at request time
(`time.Now().UTC().AddDate(0, 0, days)`) rather than left to the database's own date arithmetic,
so PostgreSQL and SQLite cannot disagree about it. `LookupToken` (used by every authenticated
request) rejects a token once `expires_at` has passed, indistinguishably from a token that never
existed -- an unauthenticated caller learns nothing about whether a given token string used to be
valid.

### 1.1 Organizations

The confirmed design is `docs/dev/organization-design.md` (permission matrix in §4, behavior in §5).
The type source of truth is `apitypes` (the `// --- organisations` section of
`backend/internal/apitypes/apitypes.go`); what follows is a summary of it.

```ts
type OrgRole = "admin" | "write" | "read"          // "" means not a member
type MembersVisibility = "members" | "public"

type Org = {
  name: string
  display_name: string
  description: string
  website: string
  avatar_url: string
  members_visibility: MembersVisibility
  num_members: number
  num_repos: number
  created_at: string               // RFC3339
  viewer_role: OrgRole             // "admin" for a site admin, "" when not logged in
}

type OrgMember = {
  username: string
  email: string        // "" when a non-member views this under members_visibility="public"
  role: OrgRole
  created_at: string
}

type OrgAuditEntry = {
  id: number
  actor: string         // The acting user's username. "" if the account was deleted
  action: string        // e.g. "org.created" | "member.added" | "repo.deleted"
  target: string        // Target username / repository full_name / webhook URL
  details: Record<string, unknown>
  created_at: string
}
```

| Endpoint | Permission | req | res |
|---|---|---|---|
| `GET /api/v1/orgs` | Anyone | query `search?` `limit?` `offset?` | 200 `{"items": Org[], "total": number}` (`viewer_role` is attached to each row) |
| `POST /api/v1/orgs` | Anyone when `TF_ORG_CREATION=anyone` (default); site admin only when `=admin` | `{"name","display_name?","description?"}` | 201 `{"org": Org}` / 400 `reserved_name` / 403 `org_creation_disabled` / 409 (name collision — checked case-insensitively; must not collide with a user namespace or another org either) |
| `GET /api/v1/me/orgs` | Auth required | – | 200 `{"items": Org[], "total": number}` (only the caller's own memberships, with role) |
| `GET /api/v1/orgs/{org}` | Anyone (200 even for non-members; `viewer_role` is `""`) | – | 200 `{"org": Org}` / 404 (a `kind=user` name, or not yet created) |
| `PATCH /api/v1/orgs/{org}` | admin | `OrgUpdateRequest` (a partial update where every field is optional: `display_name` `description` `website` `avatar_url` `members_visibility`) | 200 `{"org": Org}` |
| `DELETE /api/v1/orgs/{org}` | admin | – | 204 / 409 `has_repositories` (if even one repository remains) |
| `GET /api/v1/orgs/{org}/members` | A member, or anyone if `members_visibility=public` | – | 200 `{"items": OrgMember[]}` |
| `POST /api/v1/orgs/{org}/members` | admin | `{"username","role?"}` (`role` defaults to `read` when omitted) | 201 `{"member": OrgMember}` / 404 (user doesn't exist) / 409 `already_member` |
| `PATCH /api/v1/orgs/{org}/members/{username}` | admin | `{"role"}` | 200 `{"member": OrgMember}` / 409 `last_admin` (attempted to demote the last admin) |
| `DELETE /api/v1/orgs/{org}/members/{username}` | admin, or the member themself (leaving) | – | 204 / 409 `last_admin` |
| `GET /api/v1/orgs/{org}/audit-log` | admin | query `before?` (cursor) `limit?` | 200 `{"items": OrgAuditEntry[], "next_before": number}` (0 marks the end) |

The four profile fields on `POST /api/v1/orgs` and `PATCH /api/v1/orgs/{org}`
(`display_name` / `description` / `website` / `avatar_url`) go through the **same validation**
as `PATCH /api/v1/me/profile` ("Profile field validation" in §1.2). In particular, `website` /
`avatar_url` reject anything other than `http://` / `https://` with 400 `bad_request`
(previously unvalidated, allowing `javascript:` to be saved; `docs/dev/namespace-design.md` §10).

Error `type` values (`{"error": {"type": ...}}`, with their corresponding HTTP status):
`org_creation_disabled` (403), `reserved_name` (400), `last_admin` (409), `has_repositories` (409),
`already_member` (409).

The uniqueness of `namespaces.name` itself **ignores case**
(a `LOWER(name)` unique index in
`backend/internal/store/migrations/{postgres,sqlite}/*_namespace_name_ci_unique.sql`).
Lookups also resolve via `LOWER(n.name) = LOWER($1)`, so a namespace in a URL reaches the same
repository regardless of the spelling used to hit it. The reserved-name check has always run
through `toLowerCase()`, so it's already consistent with this policy.

Reserved names (rejected at creation time for both organization and user names; existing data is
unaffected): the source of truth is `reservedNamespaceNames` in `backend/internal/api/names.go`
(not enumerated here; it's mirrored in `frontend/lib/validation.ts`, and `bun run check:ui` checks
that the two match and that every top-level route under `app/` is covered.
`docs/dev/namespace-design.md` §9).

Instance-wide setting: the environment variable `TF_ORG_CREATION` (`anyone` (default) / `admin`).

### 1.2 Namespaces

The confirmed design is `docs/dev/namespace-design.md`. A username and an organization ID share
**a single namespace**, and both can be looked up through one endpoint regardless of kind.
The type source of truth is `apitypes` (the `// --- namespaces` section).

```ts
type NamespaceProfile = {
  name: string                    // Canonical spelling (matching is case-insensitive)
  kind: "user" | "org"
  display_name: string
  description: string
  website: string
  avatar_url: string
  created_at: string              // RFC3339
  num_models: number
  num_datasets: number            // Does not include experiment repositories
  num_experiments: number         // kind=dataset and is_experiment
  num_members: number             // Organizations only. 0 for a user namespace
  members_visibility: MembersVisibility | ""   // Organizations only. "" for a user namespace
  viewer_role: OrgRole            // "admin" for a site admin and for one's own namespace, "" when not logged in
  can_edit: boolean               // viewer_role === "admin"
}
```

| Endpoint | Permission | req | res |
|---|---|---|---|
| `GET /api/v1/namespaces/{ns}` | Anyone (unauthenticated OK) | – | 200 `{"namespace": NamespaceProfile}` / 404 `not_found` |
| `PATCH /api/v1/me/profile` | Auth required (session, or a token with **write** scope; a read token gets 403) | `{"display_name?","description?","website?","avatar_url?"}` (partial update) | 200 `{"namespace": NamespaceProfile}` / 400 `bad_request` / 401 / 403 |

- `GET` is **kind-agnostic**. It returns 200 for both users and organizations; only the
  organization-specific fields come back as 0 / `""` for a user namespace. Use
  `GET /api/v1/orgs/{org}` as before when you specifically want an organization.
- Namespace matching uses `LOWER(name) = LOWER($1)`. `/api/v1/namespaces/Alice` returns 200, and
  `name` is the spelling as registered (e.g. `"alice"`). The frontend compares this against the
  URL and redirects to the canonical form.
- Only the name's **grammar** (`validateName`) is checked before hitting the DB. Since the
  reserved-name list is a creation-time guard, an existing account that happens to collide with a
  reserved name still returns 200, while a reserved name nobody holds returns a normal 404. The
  signup availability check treats 200 as "taken" / 404 as "available (or reserved — the client
  distinguishes using the same list)".
- A namespace's existence is public information (it appears in repository URLs), so this answers
  even when unauthenticated. It is exempt from the auth-related rate limit.
- The three counts are mutually exclusive, and their sum across tabs matches that namespace's
  repository count (an experiment repository is a dataset, but it isn't counted in `num_datasets`).
- `PATCH /api/v1/me/profile` edits **only the caller's own namespace**. There is no path — not even
  for a site admin — to edit someone else's profile. The namespace name (username / organization
  ID) cannot be changed (there is no rename API; the alternative is creating a new account plus
  transferring repositories — see the transfer section in §2).

**Profile field validation** (shared by `PATCH /api/v1/me/profile` and organization
creation/update. Violations return 400 `bad_request`, with a message safe to display as-is):

| Field | Constraint |
|---|---|
| `display_name` | Up to 96 characters (runes) |
| `description` | Up to 1,024 characters (runes). Treated as plain text (not rendered as Markdown) |
| `website` / `avatar_url` | Either empty (= clear) or starts with `http://` / `https://` and is at most 2,048 bytes |

### SSH public key management

Public keys used to authenticate git over SSH (§8). Unlike tokens there's no secret value, so the
key itself is returned as-is.

- `GET /api/v1/me/ssh-keys` → `{"items": SSHKeyItem[]}` (auth required)
- `POST /api/v1/me/ssh-keys` req `{"title","key"}` → `SSHKeyItem` (write scope required)
- `DELETE /api/v1/me/ssh-keys/{id}` → 204 (write scope required)

```ts
type SSHKeyItem = {
  id: number
  title: string
  key_type: string       // e.g. "ssh-ed25519"
  public_key: string     // "<type> <base64>" (canonical form with the comment stripped)
  fingerprint: string    // "SHA256:..." (same notation as `ssh-keygen -lf`)
  created_at: string
  last_used_at: string | null
}
```

- `key` is a single authorized_keys line. Accepted types are `ssh-ed25519` /
  `ecdsa-sha2-nistp{256,384,521}` / `ssh-rsa` (2048 bits or more), plus the corresponding
  `sk-*@openssh.com` (FIDO). `ssh-dss` is rejected.
- An authorized_keys line with options (e.g. `command=`), multiple lines, or a pasted private key
  all return 400. The error message is worded so it can be shown to the user as-is.
- When `title` is omitted, the public key's comment (e.g. `you@example.com`) is used; if that's
  also absent, the key type is used.
- The fingerprint is unique instance-wide. A key that's already registered returns 409 (without
  revealing which account holds it). Because public-key authentication resolves the user from the
  key alone, the same key can't be shared across two accounts.
- `last_used_at` is the time the key was used to authenticate an SSH session.

### 1.3 Passwords and site administration

`TF_ADMIN_PASSWORD` only ever applies to the *first* boot of an empty instance (`seedAdmin`
returns immediately once `CountUsers() > 0`), so these endpoints are the only way to change a
password after that — and the only way to appoint a second site administrator.

```ts
type AdminUser = {
  id: number
  username: string
  email: string
  is_admin: boolean        // the instance-wide flag, not an organization role
  disabled: boolean        // suspended: authenticates on no path at all
  created_at: string
}
```

`AdminUser` deliberately has no password field of any kind. The stored bcrypt hash lives on
`store.User` and is never copied onto a wire type.

#### Every `/api/v1/admin` endpoint accepts the session cookie only

`requireSiteAdmin` refuses every credential except `tf_session`. An access token — of either
scope — and HTTP Basic are **403 `session_required`**, whose message names the browser session as
the way in. `PATCH /api/v1/me/password` and everything outside `/admin` are unaffected.

A `write` token is a coarse instrument here: there are no fine-grained scopes, so before this
check one leaked administrator token was enough to create accounts, force-reset anyone's password
and register an SSH key that outlives the token's own revocation. Confining site administration to
a browser someone actually signed into costs an operator nothing — the screens are in the web UI —
and takes automation out of the blast radius entirely. Scripted administration, if it is ever
wanted, should arrive as a token scope rather than by widening this.

Two ordering details follow from where the check sits, and both are deliberate:

- The `is_admin` check runs **first**, so a caller who is not an administrator gets 403
  `forbidden` whatever credential they hold. Answering `session_required` there would be
  misleading (signing in would not help them) and would confirm that the account behind the token
  is an administrator.
- On a state-changing route the read/write scope check runs **before** the session check, so an
  administrator's *read-scoped* token is refused as 403 `forbidden` ("this token is read-only")
  rather than `session_required`. Both are 403 and neither reaches the handler; only the sentence
  differs.

A session always carries the write scope, so there is no read-only session and the `write` flag on
`requireSiteAdmin` no longer distinguishes anything for a caller who gets that far.

The three password-writing endpoints below (`PATCH /api/v1/me/password` and the administrator's
create and reset) run bcrypt, so all three answer the two throttling responses §1's login endpoint
documents, and they mean the same things here: **429 `rate_limited`** when the caller's own
per-address failure budget is spent, and **503 `overloaded`** when no bcrypt slot came free. The
second is the server's capacity, not the caller's conduct, and must not be reported as the first.

Writing a password is atomic with revoking that account's sessions — one `UPDATE` sets the hash and
increments `session_epoch` together, so no failure can leave old cookies valid against a new
password. `PATCH /api/v1/me/password` re-signs the caller's own cookie from the epoch that write
returns.

#### `PATCH /api/v1/me/password`

Write scope required. req `{"current_password": string, "new_password": string}` → **204**.

- `current_password` is always verified, even though the caller is already authenticated:
  holding a session is not on its own permission to replace the credential it was minted from.
  A wrong one is **401** (`unauthorized`), and the failure counts against the same per-address
  and per-username buckets as the login form.
- `new_password` is validated by exactly the rule sign-up uses — at least 8 bytes, at most
  `auth.MaxPasswordBytes` (72). Both call the same `validatePassword` helper, so a password
  that could not be registered cannot be reached by changing to it either. **400** otherwise.
- On success every session is revoked (`BumpSessionEpoch`) **and the calling browser's
  `tf_session` cookie is re-issued at the new epoch**, so changing your password does not sign
  you out of the tab you changed it in. A token-authenticated caller holds no cookie and is
  issued none.
- **Access tokens are deliberately *not* revoked.** A token is an independent credential with
  its own lifecycle and its own revocation UI (§1 "Token management"); a password change is
  evidence about the password, not about any token minted from the account. Revoking every
  token on a routine rotation would break unattended clients (CI, `git`, the `tf` CLI) for a
  threat the change does not address — someone who wants a token gone deletes that token.

#### `GET /api/v1/admin/users`

Site administrators only (`users.is_admin`); **403** for anyone else, said out loud rather than
hidden behind a 404. Session cookie only (403 `session_required` for a token or HTTP Basic).

Query: `search` (case-insensitive substring of the username *or* the email), `limit` (default
50, capped at 200), `offset`. res 200:

```json
{ "items": [ /* AdminUser */ ], "total": 12 }
```

`total` counts every account matching `search`, ignoring the page window.

#### `POST /api/v1/admin/users`

Site administrators only, session cookie, write scope. req:

```json
{ "username": "dana", "email": "dana@example.com", "password": "…", "is_admin": false }
```

res **201** `{"user": AdminUser}`.

- **`TF_ALLOW_SIGNUP` is deliberately not consulted.** That flag closes the public
  `/api/v1/auth/signup` form; an instance run that way still has to be able to gain accounts,
  and this is the route that does it. Gating it on the same flag would make
  `TF_ALLOW_SIGNUP=false` a one-way door with no way to add a colleague short of editing the
  database.
- Validation is shared with `handleSignup` down to the helper: `validateNamespaceName` (an
  account is also a namespace, so the reserved list of `docs/dev/organization-design.md` §6.3
  applies — a reserved name answers 400 `reserved_name`), `validatePassword`, `validateEmail`.
  All of it runs before `CreateUser`, so a rejected request creates neither an account nor a
  namespace.
- `is_admin` is optional and defaults to false. Setting it is how an instance gets its second
  site administrator without a two-step create-then-promote.
- A username already held by an account **or an organization** is 409 `conflict` —
  `CreateUser` inserts the user and its namespace in one transaction.
- **No session cookie is issued.** `handleSignup` mints one because the caller *is* the new
  account; here the caller is somebody else, and replacing the administrator's own session with
  the new user's would sign them out of their own browser.
- There is no email round trip and no invitation flow: the administrator hands the password
  over out of band, and the new user changes it at `PATCH /api/v1/me/password`.

#### `PATCH /api/v1/admin/users/{username}`

Site administrators only, session cookie, write scope. req — every field optional, an absent one
is left unchanged:

```json
{ "password": "…", "is_admin": true, "disabled": true }
```

res 200 `{"user": AdminUser}`. Errors:

| Status | Type | When |
|---|---|---|
| 400 | `bad_request` | No field set (a body that changes nothing is refused rather than answered 200), or an invalid `password` |
| 400 | `self_demote` | An attempt to clear **your own** `is_admin`. Its own type so the UI can translate it; the web UI leaves the control off your own row, so it answers a race or a hand-made request |
| 400 | `self_disable` | An attempt to set `disabled` on **your own** account. Same reasoning, one step stronger: the write revokes the session the request arrived on, so the mistake would lock you out mid-click with nothing left to undo it with |
| 403 | `forbidden` | The caller is not a site administrator, or holds a read-only token |
| 403 | `session_required` | The caller presented a token or HTTP Basic rather than a session cookie |
| 404 | `not_found` | No account with that username |
| 409 | `last_admin` | Clearing `is_admin`, or setting `disabled`, would leave the instance with no usable site administrator |

Everything that can fail on its own terms — validation, the target lookup, the self-demotion rule,
and the bcrypt hash — completes before the first durable write, so a refused request cannot have
granted administrator rights on its way out. The two writes are not wrapped in a single
transaction: `SetUserAdmin` goes first because it is the one that can still refuse (409), the
password write carries its own revocation, and what remains between them is an infrastructure
failure that an identical retry converges from.

Setting `password` revokes the *target's* sessions (their tokens, again, survive). The
self-demotion 400 makes the 409 unreachable through this endpoint in practice — any other
account the caller could demote implies a second administrator exists — so `store.ErrLastSiteAdmin`
is a guard against a concurrent demotion rather than an everyday answer. `self_disable` does the
same for suspension, and the disabled-administrator count is what makes it true: a suspended
administrator does not count towards the last-administrator total, since an account that cannot
authenticate cannot administer anything.

##### `disabled`: the offboarding switch

Setting `disabled` is what actually cuts an account off. It is the answer to a problem the rest of
this section deliberately creates: a password reset does not revoke access tokens, so before this
existed the only thing an administrator could do to a departed colleague was change a password
that colleague no longer needed.

- Suspension stops **every** identity path at once — session cookie, password login (403
  `account_disabled` from `POST /api/v1/auth/login`, only ever reachable with the *correct*
  password, so it enumerates nothing), HTTP Basic, access token, and registered SSH key. The check
  lives at the single exit of credential resolution (`resolveIdentity`) rather than in each
  branch, precisely because the usual way such a switch fails is by covering three ways in out of
  four. The two paths that do not pass through there carry it themselves: `store.LookupSSHKey`
  filters on `disabled_at IS NULL` (`internal/sshserver` authenticates before this package runs)
  and `handleLogin` answers `passwordDisabled`. `ServeGit` re-checks it as well, because it takes
  its user from another package.
- `session_epoch` is incremented in the same statement, so cookies already issued stop working
  immediately rather than at their TTL.
- **Nothing is destroyed.** Restoring clears `disabled_at` and the account is exactly what it was:
  its password, tokens and SSH keys all work again. What does *not* come back is anything revoked
  separately (below), and the sessions cut off by the epoch bump — a sign-out has to be permanent
  or it means nothing.
- Setting the state an account is already in is a no-op, so a retried request does not bump the
  epoch a second time.
- A credential presented for a suspended account is logged at `slog.Warn` (naming the account, the
  auth method, the client IP and the path) and the request simply proceeds as anonymous.

#### `POST /api/v1/admin/users/{username}/revoke-credentials`

Site administrators only, session cookie, write scope. No body. res **204**.

Deletes every access token and every registered SSH key the account holds, and bumps its session
epoch. 204 rather than the counts: reporting "3 tokens, 1 key" invites reading it as a result when
the meaningful state afterwards is simply "none", and a retried request would then report zero for
work it genuinely completed.

| Status | Type | When |
|---|---|---|
| 400 | `self_revoke` | Your own account. The session revocation would include the cookie the request arrived on, and re-issuing it here would half-undo what was asked for; your own tokens and keys are managed from `/settings` |
| 403 | `forbidden` | The caller is not a site administrator, or holds a read-only token |
| 403 | `session_required` | The caller presented a token or HTTP Basic rather than a session cookie |
| 404 | `not_found` | No account with that username |

It is a separate endpoint rather than another field on the PATCH because the two actions differ in
kind. Suspension is reversible and destroys nothing; this deletes credentials permanently, and an
irreversible deletion must not be reachable as a side effect of a partial update somebody meant to
use for a password reset.

Neither implies the other, in either direction. Suspending is what stops access; this is for the
case where the credentials themselves are suspected — a laptop lost, a token pasted into a build
log — on an account that is meant to keep working once new ones are issued. A restore after both
brings the account back **without** the revoked tokens and keys.

The two deletions are separate statements rather than one transaction: each is idempotent on its
own and a partial failure leaves strictly fewer credentials than before, so an identical retry
converges.

**Audit**: there is no site-wide audit table. `org_audit_log` is keyed by namespace and these
actions have none, and a handful of verbs do not justify a migration, so every account created,
every password reset and every `is_admin` change is recorded as a structured `slog.Info` line
naming the actor, the target, and what changed — never the password itself, in either direction.
Suspension changes and credential revocations are logged the same way at `slog.Warn`, the latter
carrying counts rather than identifiers and never a token value or a prefix of one. A requeued
sync job (below) is logged as `slog.Info`, naming the actor and the job id.

#### Failed sync jobs

Post-push work — publishing a ref's blobs into `blobs/` and re-indexing its metadata — is queued
in `sync_jobs`. A job that keeps failing is retried with a growing backoff and, after
`store.SyncMaxAttempts` (5) claims, parks as `status = 'failed'` and is never picked up again.

That state is quiet and consequential: the repository keeps serving its previous push, so the
instance looks healthy while that repository's **file listing, search entry and `blobs/` export
stay frozen**. Before these two endpoints the only trace was one log line, and the only way back
was an `UPDATE` against the database by hand.

```ts
type SyncJob = {
  id: number
  repo: string          // "datasets/acme/imdb-ja" — includes the kind segment
  ref: string           // "refs/heads/main"
  attempts: number      // claims made before it parked
  last_error: string    // the final attempt's error, verbatim
  updated_at: string
}
```

##### `GET /api/v1/admin/sync-jobs`

Site administrators only (**403** for anyone else, **401** when anonymous). Session cookie only,
like every `/admin` endpoint: a token of either scope is 403 `session_required`.

Query: `limit` (default 50, capped at 200), `offset`. res 200:

```json
{ "items": [ /* SyncJob */ ], "total": 3 }
```

- **Only `failed` rows are listed.** A job still retrying is not an operator's problem yet, and
  `pending` / `running` / `done` are high churn — a listing dominated by ordinary traffic would
  bury the one row that matters.
- Ordered newest failure first (`updated_at DESC, id DESC`); `total` counts every failed job,
  ignoring the page window.
- `repo` carries the kind segment so the listed name can be opened directly, rather than leaving
  the reader to guess which of the two kinds it is.

##### `POST /api/v1/admin/sync-jobs/{id}/retry`

Site administrators only, session cookie, **write scope** — this changes state, so a read-scoped
token is 403. No body. res **204**.

| Status | Type | When |
|---|---|---|
| 400 | `bad_request` | `{id}` is not an integer |
| 403 | `forbidden` | The caller is not a site administrator, or holds a read-only token |
| 403 | `session_required` | The caller presented a token or HTTP Basic rather than a session cookie |
| 404 | `not_found` | No job with that id **that is still `failed`** |

- The update matches `status = 'failed'` only, and puts the row back to `pending` with
  `attempts = 0`, `last_error` cleared and both `next_attempt_at` and `lease_expires_at` unset.
  Restricting it to parked rows is what keeps a stray retry from resetting the attempt counter of
  work already in flight, and it is why a second retry of the same id — a double click, or a
  colleague on the same screen — is a 404 rather than a silent 204.
- The attempt budget is deliberately reset rather than nudged: a job requeued with its counter
  intact would park again on its first claim without having been given a real chance.
- **Nothing is signalled to the worker.** The sync loop polls every ten seconds as a safety net
  beside its wake channel, so a requeued job is claimed within that window; the response says the
  job was requeued, not that it has since succeeded. If it fails again it parks again and
  reappears in the listing.

---

## 2. Repositories

```ts
type RepoSummary = {
  id: number
  kind: "dataset" | "model"
  namespace: string
  namespace_kind: "user" | "org"   // Whether namespace is a user namespace or an organization
  name: string
  full_name: string      // "ns/name"
  description: string
  tags: string[]
  license: string        // "" if unset
  downloads: number
  total_size: number     // bytes
  num_files: number
  is_experiment: boolean
  default_branch: string
  head_sha: string
  created_at: string     // RFC3339
  updated_at: string
  archived: boolean      // Read-only (archived)
  archived_at: string | null
}

type RepoDetail = RepoSummary & {
  card: Record<string, unknown>   // README front matter
  readme: string                  // README body (front matter stripped), 256KB max
  readme_too_large: boolean       // README.md exists but exceeds 256KB, so readme stays empty (card is unaffected since it's sourced from the index)
  clone_url: string               // "http://localhost:8080/datasets/ns/name.git"
  branches: string[]
  tags_refs: string[]
  parquet_files: ParquetSummary[] // This repository's parquet listing (default branch)
  indexing: boolean               // A sync job from the latest push hasn't finished yet
  can_write: boolean              // Whether the current viewer can commit to this repository (always false once archived)
  can_admin: boolean              // Whether transfer / archive / delete are allowed (namespace admin / site admin; still true while archived)
  downloads_last_30_days: number  // Hit count on the resolve endpoint over the last 30 days (the running total is downloads)
}

type ParquetSummary = {
  path: string
  num_rows: number
  num_row_groups: number
  num_columns: number
  size: number
}
```

### `GET /api/v1/repos`
query:
- `kind` (optional, dataset|model)
- `q` (partial match, ILIKE against name/namespace/description; unchanged existing behavior)
- `search` (full-text search. A tsquery-based prefix-match AND search. Targets
  `repositories.search_vector` = name + the card's description/short_description/summary/license/
  tags/pipeline_tag/task_categories. The README body itself isn't stored in the DB, so it's outside
  the search scope)
- `author` (namespace)
- `tag` / `tags` (multiple allowed, ANDed. `tag` is singular for backward compatibility with old
  links; `tags` is for the facet sidebar. When both are given they're merged)
- `license` (exact match against the card's `license`)
- `task` (matches either the card's `pipeline_tag` or `task_categories`)
- `base_model` (`ns/name`. Only derivatives of that base model; follows the `base_model` edge from
  §12)
- `relation` (`finetune`|`adapter`|`quantized`|`merge`, or any value the card wrote)
- `dataset` (`ns/name`. Only repositories trained on that dataset. Targets only the `dataset` edge;
  the `run` edge is not included — a dataset repository that experiment logs were written to is
  not a "training source")
- `base_only` (`true`|`false`. Only repositories that carry no `base_model` edge at all. Equivalent
  to HF's "Base only". Logically exclusive with `base_model` / `relation` — combining them yields
  0 results)
- `sort` (`updated`|`created`|`name`|`downloads`, default `updated`)
- `archived` (`true`|`false`. When omitted, archived repositories are included too. Since the badge
  is meant to distinguish them, removing them from the listing would make them look deleted, so
  the default doesn't filter them out)
- `limit` (default 30, max 100) / `offset`

Ground rules for the lineage filters (`base_model` / `relation` / `dataset` / `base_only`):

- **Matching is per repository.** Both the edge's `@rev` and the parameter's `@rev`
  (`base_model=ns/name@v1`) are ignored. Requiring an exact revision match would leave almost
  nothing in practice.
- `base_model` and `relation` filter **the same single edge**. `base_model=a/b&relation=quantized`
  means "a quantized version of a/b", not "something that is both a derivative of a/b and a
  quantized version of something".
- A `base_model` edge with an empty `relation` (a row indexed before this feature existed) is
  treated as `finetune` — the same default used for Model Tree grouping (§12).
- A value that can't be parsed as `ns/name` (e.g. `base_model=garbage`) returns **0 results**. The
  condition is never silently dropped in favor of returning everything.

res: `{"items": RepoSummary[], "total": number, "facets": RepoFacets}`

```ts
type RepoFacets = {
  tags: { value: string; count: number }[]
  licenses: { value: string; count: number }[]
  tasks: { value: string; count: number }[]
  relations: { value: string; count: number }[]   // The relation kind of the base_model edge
}
```

`facets` gives counts under the current filter conditions. However, each facet's own dimension
(e.g. the `tags` facet excludes the `tags=` condition) is excluded from its own aggregation, so it
represents "how many results if this were additionally selected". The HF-compatible listing
endpoints (`GET /api/models` `/api/datasets`) do not compute this `facets` (always empty).

Notes on the `relations` facet:

- Its own dimension is only `relation=`. `base_model=` is kept, so it can answer "of this base
  model's derivatives, how many are quantized versions".
- A repository with no `base_model` edge falls into no bucket ("base model only" is the
  `base_only=true` filter, not a relation kind).
- What's counted is the **number of repositories**, not the number of edges. A model that merges
  two base models counts as 1 under `merge`.
- Combined with `base_only=true`, everything is naturally 0, and `relations` comes back as an
  empty array.

### `GET /api/models` and `GET /api/datasets`  (HF-compatible)

What `HfApi.list_models` / `list_datasets` call. The response is a bare JSON array of listing
entries (`_id` `id` `author` `sha` `lastModified` `createdAt` `private` `tags` `downloads` `likes`,
plus `modelId` for models), not the `{items, total, facets}` envelope the Web UI endpoint uses.
`private` is always `false` and `likes` always `0`: neither concept exists here, and both fields
are read unconditionally off every entry by the client.

query:

- `search` (substring match over name / namespace / description — HF's own `search`, and the same
  matching as `q=` on the Web UI endpoint. The full-text `search=` of `GET /api/v1/repos` is a
  different filter and is not reachable from here)
- `author` (namespace)
- `filter` (repeatable, **ANDed**, matched against the card's `tags`. This is where
  `huggingface_hub` puts `library` / `language` / `task_categories` / `size_categories` and the
  rest — it folds them into prefixed tags such as `task_categories:summarization` before sending.
  `tags=` is accepted as a synonym and merged with it)
- `pipeline_tag` (matches the card's `pipeline_tag` or `task_categories`, i.e. the same filter as
  `task=` on the Web UI endpoint)
- `sort` + `direction`, `limit`, `offset`, `full` / `cardData` — below.

**Sorting.** `sort=` is mapped onto the listing's own orderings, and `direction=-1` means
descending exactly as it does on the Hub (any other value, including an absent one, means
ascending). The store's orderings each run in one direction only, so a request for the other one
is a **400** rather than a page silently sorted the wrong way — "most downloaded" and "least
downloaded" are different questions, and a listing that answers the wrong one looks like data.

| `sort` | Direction it is available in | Ordering |
|---|---|---|
| `downloads` | `direction=-1` | `downloads` DESC |
| `lastModified` | `direction=-1` | `updated_at` DESC (the default when `sort` is absent) |
| `createdAt` | `direction=-1` | `created_at` DESC |
| `id` / `name` / `author` | ascending (no `direction`) | `(namespace, name)` ASC |

Keys are matched case-insensitively with `_` ignored, so `lastModified` and `last_modified` are
the same key. `sort=likes` and `sort=trending_score` are a **400** naming the metric: this server
tracks neither, and answering them with the default order would hand back a page that looks
ranked and is not.

**Pagination.** `limit` (default 30, max 100) is the page size, and a page with more behind it
carries a `Link: <...>; rel="next"` header — the same mechanism as the commit listing, and the
only one `huggingface_hub` knows: its `paginate()` follows that header, so before it existed
every repository past the 30th looked as though it did not exist. The cursor in that URL is
`offset=`, which is not part of HF's protocol and is meant to be reached only by following the
link. `limit` is *also* the client's total cap (`list_models` slices the concatenated pages to
it), so `limit=500` walks five pages of 100.

**`full=true` / `cardData=true`** add `cardData` (the README front matter) to every entry. Either
parameter is enough, and their values are read Python-style (`True` counts). Without them the key
is absent rather than `null`.

**Refused parameters.** `expand`, `gated`, `inference` and `emissions_thresholds` return
**400**. Each of them narrows a listing in a way this server cannot reproduce, so ignoring one
would answer with a *superset* of what was asked for — `gated=True` would come back full of
repositories that are not gated, `expand=[...]` missing exactly the fields the caller asked to be
given — and a wrong result set is indistinguishable from a right one at the call site. `facets`
are never computed for these endpoints.

`config` (`list_models(fetch_config=True)`) is the one parameter that is accepted and does
nothing: it only ever *adds* a field, and no entry carries a model `config` either way, so the
client sees `None` rather than a result set that is quietly wrong. It is not rejected because
doing so would break an otherwise perfectly serviceable call.

### `GET /api/v1/repos/{kind}/{ns}/{name}`
res: `{"repo": RepoDetail}`

A nonexistent repository returns 404 (`{"error":{"type":"not_found",...}}`), indistinguishable
from a repository the caller lacks access to (equivalent to `loadRepoForRead` in `auth.go`). The
frontend follows this too and, on 404, shows wording that doesn't hint at whether the repository
exists ("not found or you don't have access"). However, for **a viewer who isn't logged in** it
adds a login affordance (a login link with `?next=`) — because they may simply have followed a
shared link while logged out of an account that does have access ([S15],
`components/repo/repo-not-found.tsx`). If a logged-in viewer still gets 404, it falls back to the
normal not-found page.

### `POST /api/v1/repos`  (creation from the Web UI)
req: `{"kind","namespace","name","description":""}` → `{"repo": RepoDetail}`
On success, if the namespace is an organization, records a `repo.created` audit log entry
(`docs/dev/organization-design.md` §5).

### `POST /api/repos/create`  (HF-compatible)
req: `{"type":"dataset"|"model","name":"foo" | "ns/foo","organization":null}`
(Also **accepts and ignores** the `"visibility": "public"|"private"` sent by `huggingface_hub` 1.x
and the `"private": bool` sent by < 1.0. Repositories have no notion of visibility, so either one
produces the same result — it's decoded purely for client-side compatibility.)
res 200: `{"url":"http://localhost:8080/datasets/ns/foo","repo_id":"ns/foo","name":"ns/foo","type":"dataset"}`
409 if it already exists (`exist_ok` is handled client-side).

### `DELETE /api/v1/repos/{kind}/{ns}/{name}`  (deletion from the Web UI)
res 204. **Namespace admin** only (the owner of a personal namespace, or an org admin) and site
admin. Write members cannot do this (the same line drawn for transfer and archive — a write member
can push and undo, but deletion takes the history and LFS associations with it as a one-way
operation, so it isn't handled at the same permission level).
Deletion is allowed even while archived (archiving protects content, it doesn't prevent disposal).
Since this is irreversible, the Web UI interposes a dialog requiring the repository ID (`ns/name`)
to be typed in.
Server-side cleanup:
- The `repositories` row (`repo_files` / `repo_lfs_objects` / `exp_*` / `repo_redirects` /
  `repo_transfers` / `sync_jobs` / webhook subscriptions are removed via FK ON DELETE CASCADE)
- The bare git directory (`{root}/{storage_path}.git`)
- WAL-staged objects (`wal/{storage_path}/`; a no-op when `TF_WAL_MODE=off`)

The LFS objects under `lfs/` and the non-LFS blob objects under `blobs/` are **not deleted at this
point**. Both are content-addressed and may be shared with other repositories, so reclaiming
objects that are no longer referenced is the job of `thinkingface gc` (§13) (`lfs/` uses
`repo_lfs_objects`, and `blobs/` uses `repo_files.blob_sha` as its reference count).

### `DELETE /api/repos/delete`  (HF-compatible)
req: `{"type","name","organization"}` → 200 `{}`. Cleanup and permissions are the same as above
(**namespace admin only** — so that going through `huggingface_hub` isn't a permission loophole).
For an organization-owned repository, only the organization's **admin** role can do this (write
gets 403). In a user namespace the owner is admin, so behavior doesn't change
(`docs/dev/organization-design.md` §4). `DELETE /api/v1/repos/{kind}/{ns}/{name}` carries the same
constraint.

### Archiving (making read-only)
```
POST   /api/v1/repos/{kind}/{ns}/{name}/archive   → 200 {"repo": RepoDetail}
DELETE /api/v1/repos/{kind}/{ns}/{name}/archive   → 200 {"repo": RepoDetail}
```
Archiving is a soft freeze that doesn't move a single byte of data (it only sets
`repositories.archived_at` / `archived_by`). `updated_at` is left unchanged (otherwise it would
jump to the top of "recently updated").

- Permission: namespace admin (the owner of a personal namespace, or an org admin) and site admin
  only. Write members cannot do this (the same line drawn for transfer and delete). Both endpoints
  are idempotent.
- Effect: every write operation is rejected with 403 `{"error":{"type":"repository_archived",...}}`.
  This covers git receive-pack (starting from the advertisement stage of
  `info/refs?service=git-receive-pack`), HF's `preupload` / `commit`, the LFS batch `upload`,
  `PUT /api/v1/edit/...`, starting a transfer (`POST /api/repos/move` and the Web UI version), and
  experiment ingest (`log` / `finish`, and PATCH/DELETE of a run).
- Not affected: reads in general (`resolve` / clone / tree / parquet / HF's repo info), repository
  **deletion**, viewing and canceling a pending transfer (`GET` / `DELETE .../transfer`), and
  unarchiving.
- `RepoDetail.can_write` becomes false while archived (all edit affordances disappear at once).
  `can_admin` stays true — the owner can unarchive.
- Webhook: delivers `repo.archived` / `repo.unarchived`.

### Changing the default branch
```
PATCH /api/v1/repos/{kind}/{ns}/{name}   req RepoUpdateRequest {default_branch?: string}
      → 200 {"repo": RepoDetail}
```
A partial update over repository configuration; `default_branch` is the only field today, and the
request must set it (400 `bad_request` otherwise). Fixes the common "pushed to `master`, meant
`main`" mistake without a re-push; before this the column was only ever set by `CreateRepo`, so
there was no way to correct it short of editing the database. `RepoUpdateRequest` is shaped for
future fields: every field is a pointer and absent ones are left alone.

- Permission: namespace admin (the owner of a personal namespace, or an org admin) and site admin
  only — the same line drawn for archive/transfer/delete, not a plain write member. Unlike
  archive/unarchive, this is **not** allowed while the repository is archived (403
  `repository_archived`) — switching branches changes what clone/tree/card/lineage read, which is
  a content-adjacent change, not the unarchive escape hatch.
- 404 `not_found` if `default_branch` names a branch that does not exist in the repository
  (checked against `refs/heads/{branch}`, not merely "some ref with this name" — a tag of the same
  name does not count). Idempotent: setting it to its current value returns 200 and does nothing
  else described below.
- Effect:
  - The bare repository's `HEAD` symref is repointed at `refs/heads/{branch}` (`gitrepo.Repo.SetHead`,
    the same mechanism `--initial-branch=` sets at creation time) — this is what a plain `git clone`
    of the repository checks out afterwards.
  - `repositories.default_branch` is updated (`store.SetRepoDefaultBranch`), bumping `updated_at`
    like an ordinary push does (unlike archiving, which leaves it alone).
  - A sync job is enqueued for the new default branch with `old_sha == new_sha` (its current tip):
    `head_sha` / the parsed README card / lineage / `is_experiment` are only refreshed by the sync
    worker for `job.Ref == repositories.default_branch` (`internal/syncer/syncer.go`), so a branch
    pushed to before it became the default — the case this endpoint exists for — still carries the
    *previous* default branch's metadata until this runs; `repo_files` and the parquet index are
    refreshed unconditionally by the same job, so re-running them when the branch was already
    indexed is harmless. Because `old_sha == new_sha`, the resulting `repo.push` webhook (below)
    reports `changed_files: 0` — a subscriber can tell this apart from an actual push to the same
    ref. This also means the response's `head_sha` / `card` / `parquet_files` can be momentarily
    stale until that job finishes (`RepoDetail.indexing` reports it, same as right after repository
    creation).
  - The reindex is queued **even when the branch is already the default**, where nothing else is
    written. That is what makes retrying the request a repair: if an earlier attempt updated
    `HEAD` and the row and then failed to queue the job (500), repeating it queues the job and
    the metadata catches up.
  - A failed enqueue is **not** rolled back. `HEAD` and the row already agree on the new branch
    by that point, so the repository is consistent and only its index is stale. Undoing the
    switch would mean writing to the store that just refused a write, and the outage that failed
    the enqueue fails the undo too — leaving `HEAD` and the row naming different branches, which
    is worse than a stale index. If the **row** write fails, `HEAD` is put back, since that is the
    one pair that must never disagree: a clone follows `HEAD` while every listing reads the row.
  - **Not atomic across the two stores.** `HEAD` (a file) and `default_branch` (a row) are
    separate writes with no lock spanning them, so two concurrent `PATCH`es naming *different*
    branches can interleave and leave the two disagreeing. It is bounded and self-healing — any
    later `PATCH` (including a same-value one) puts them back in step — and it is not worth a
    cross-store lock: under `TF_WAL_MODE=authoritative` the bare repository is per-instance local
    state materialised from the WAL, so "the" `HEAD` is not a single shared object to begin with,
    which is the same reason `alignHEAD` below cannot resolve it either.
  - Webhook: no dedicated event was added for this. `repo.push` fires once the reindex above
    completes (payload as documented in §9), which is enough signal for a mirroring consumer that
    the default branch's effective content may have changed; a purpose-built event would carry
    marginal value over that plus the response's own `default_branch`.
  - Not touched: `wal.materialize.alignHEAD`'s cache-rebuild heuristic (`internal/wal/materialize.go`)
    still does not know about `repositories.default_branch` — it keeps `HEAD` if its current target
    still exists in the index, else prefers `refs/heads/main`, else the alphabetically first branch.
    That is a pre-existing limitation (its own `TODO(continuity-design)`), not something this
    endpoint changes, but it is more likely to be visibly wrong now that a repository's default
    branch can deliberately be something other than `main`: a WAL cache rebuild of such a
    repository can reset its bare-repo `HEAD` to the heuristic's answer rather than the configured
    default until the next push realigns it. Fixing this needs the symref recorded in the WAL index
    itself (a format change), which is out of scope here.

### `POST /api/repos/move`  (HF-compatible: `HfApi.move_repo`)
req: `{"fromRepo":"alice/foo","toRepo":"team/foo","type":"model"|"dataset"}`
- 200 `{"url":"http://localhost:8080/team/foo"}`: completes immediately (the actor has write on
  both the source and destination, or is a site admin)
- 202 `{"url":..., "pending":true, "transfer_id":12}`: pending approval by the destination (an
  extension not present in HF; `move_repo` treats any 2xx as success)
- 403 (no permission on the source — for an org-owned source only `admin` may start a transfer) /
  404 (destination namespace doesn't exist) / 409 (a repo of the same name already exists at the
  destination, or a transfer is already pending)
Renaming (within the same namespace) uses the same endpoint. No actual data (LFS / non-LFS blobs /
git history / WAL) moves — not a single byte (`docs/dev/repo-transfer-design.md`). Keys on GCS are
content-addressed and independent of namespace, so there's no asynchronous relocation job tied to
a transfer.

### Transfer (for the Web UI)
Types are `RepoTransferRequest` / `RepoTransfer` / `RepoTransferResponse` / `MyTransfersResponse`
(`apitypes`).
```
POST   /api/v1/repos/{kind}/{ns}/{name}/transfer   req RepoTransferRequest {namespace, name?}
        → 200 RepoTransferResponse (repo present = completed) / 202 RepoTransferResponse (repo absent = pending approval)
GET    /api/v1/repos/{kind}/{ns}/{name}/transfer   → 200 RepoTransferResponse (pending only) / 404
DELETE /api/v1/repos/{kind}/{ns}/{name}/transfer   → 204 (canceled by the source)
GET    /api/v1/me/transfers                        → MyTransfersResponse {incoming, outgoing}
POST   /api/v1/transfers/{id}/accept               → 200 RepoTransferResponse (repo present)
POST   /api/v1/transfers/{id}/reject               → 200 RepoTransferResponse
```
Accept/reject require write permission on the destination namespace; cancel requires write
permission on the source. A pending transfer expires after 7 days.
If a user without permission calls accept / reject, the response is **404 `not_found`
(`transfer not found`)**. Returning 403 with the destination namespace name would let someone
brute-force numeric IDs to enumerate pending destinations, so the response is unified to be
indistinguishable from a nonexistent ID.

### Accessing the old name (redirect)
The old `{ns}/{name}` after a transfer or rename redirects to the new name, until a new repository
is created under the old name.
- HF-compatible API / `resolve` / LFS: **308** (preserves method and body; `huggingface_hub` follows
  it)
- git smart HTTP `info/refs`: **301** (git's default `http.followRedirects=initial` follows it)
- Web UI-facing API (`/api/v1/...`): **404** +
  `{"error":{"type":"repo_moved","message":...,"moved_to":{"namespace","name"}}}` (the frontend
  does a `permanentRedirect`)
- `create` under the old name: allowed (the redirect disappears). `DELETE /api/repos/delete` under
  the old name: 404

### `GET /api/{datasets|models}/{ns}/{name}` and `.../revision/{rev}`  (HF-compatible)
res 200:
```json
{ "_id": "1", "id": "ns/name", "modelId": "ns/name", "author": "ns",
  "sha": "<commit sha>", "lastModified": "2026-08-21T00:00:00.000Z",
  "private": false, "disabled": false, "gated": false,
  "tags": ["parquet"], "downloads": 0, "likes": 0,
  "cardData": { ... }, "config": {},
  "siblings": [ { "rfilename": "data/train.parquet", "size": 123,
                  "blobId": "<sha1>", "lfs": {"oid":"<sha256>","size":123,"pointerSize":130} } ],
  "createdAt": "2026-08-21T00:00:00.000Z" }
```
(models uses `modelId`; datasets don't need it, but returning it does no harm)

- A repository that does not exist at all (or isn't visible to the caller) is **404 +
  `X-Error-Code: RepoNotFound`**, distinct from the `RevisionNotFound` case just below — see the
  header contract in the preamble at the top of this document. This is what `repo_exists()` /
  `file_exists()` / `revision_exists()` rely on to answer `False` instead of raising.
- A `{rev}` that does not resolve is **404 + `X-Error-Code: RevisionNotFound`**, matching the
  branch/tag and commits endpoints below — the header is what makes `huggingface_hub` raise
  `RevisionNotFoundError` instead of a bare `HfHubHTTPError` (or, without it, silently returning
  as if the revision existed). **Exception:** a repository whose git directory holds zero commits
  — the brief state between `gitexec.InitBare` and the seeded "Initial commit" during
  `createRepo` (`backend/internal/api/repos.go`) — returns 200 for any `{rev}`, including the
  default, rather than 404: `create_repo` → `repo_info` is an ordinary `huggingface_hub` flow and
  must not break on a repository that has nothing in it yet.

### `GET /api/{datasets|models}/{ns}/{name}/tree/{rev}/{path...}`  (HF-compatible)
query: `recursive=true|false`, `expand=true|false`, `limit`, `cursor`
res 200 (an array):
```json
[ {"type":"directory","oid":"<sha1>","size":0,"path":"data"},
  {"type":"file","oid":"<sha1>","size":1234,"path":"README.md"},
  {"type":"file","oid":"<sha1>","size":130,"path":"data/a.parquet",
   "lfs":{"oid":"<sha256>","size":98765,"pointerSize":130}} ]
```
`size` is the actual file size for LFS entries (not the pointer's size).

- Same `{rev}` handling as `repo-info` above: 404 + `X-Error-Code: RevisionNotFound` when the
  repository has commits but `{rev}` doesn't resolve; 200 (empty array) for a repository with
  zero commits.
- `{path...}` naming something that doesn't exist at a `{rev}` that *does* resolve is a different
  case: **404 + `X-Error-Code: EntryNotFound`**, not `RevisionNotFound`. `HfFileSystem.glob` — and
  so `datasets.push_to_hub`, which globs `data/*` before uploading — depends on being able to tell
  the two apart.

### `POST /api/{datasets|models}/{ns}/{name}/paths-info/{rev}`  (HF-compatible)
req: `{"paths": ["a.txt", "data/"], "expand": false}` → res 200: `hfTreeEntry[]` (same shape as `tree`)

- **Two request encodings are accepted, and the `Content-Type` picks between them.**
  `application/x-www-form-urlencoded` is parsed as a form (`paths` repeated once per element);
  anything else is parsed as JSON. The form branch is not a nicety — it is the encoding
  `huggingface_hub` actually uses: `get_paths_info` posts `data={"paths": [...], "expand": ...}`,
  which `requests` sends form-encoded. While only JSON was understood, every real client's batch
  decoded to zero paths and the endpoint answered 200 `[]` to all of them, which
  `HfFileSystem.info()` / `.exists()` turn into a `FileNotFoundError` (breaking `datasets` and
  `pandas.read_parquet("hf://...")`) and `CommitOperationCopy` into an unresolvable source.
- `expand` is accepted in both encodings and ignored: the extra fields it selects (`lastCommit`,
  `securityFileStatus`) aren't produced here, and `huggingface_hub`'s `RepoFile` / `RepoFolder`
  read both with a default, so omitting them is safe.
- `paths` allows **at most 1000 elements**; each element must be at most 4096 bytes and contain no
  NUL. Violations return 400 (each element triggers a commit resolution plus a tree walk, and a
  public repository can be hit without authentication). Both limits apply to the form branch too.
- The overall body size limit is 8MiB, in both encodings. Exceeding it returns 413
  `payload_too_large`.
- A body that can't be read in the encoding its `Content-Type` names is treated as "`paths` is
  empty" and returns 200 rather than 400. This tolerance is now only about genuinely malformed
  bodies — it is no longer what a normal `huggingface_hub` request runs into.
- Same `{rev}` handling as `repo-info` / `tree` above: 404 + `X-Error-Code: RevisionNotFound` when
  the repository has commits but `{rev}` doesn't resolve; 200 for a repository with zero commits.
  The revision is checked ahead of the batch, so it answers the same way in either encoding.

### `GET /api/{datasets|models}/{ns}/{name}/refs`  (HF-compatible)
res:
`{"branches":[{"name":"main","ref":"refs/heads/main","targetCommit":"<sha>"}],"tags":[],"converts":[],"pullRequests":[]}`

`converts` and `pullRequests` are **always present and always empty**. Neither feature exists on
this server, but `huggingface_hub`'s `list_repo_refs` subscripts them rather than reading them
with `.get()`, so `include_pull_requests=True` raised `KeyError` while `pullRequests` was absent —
a crash for asking a question whose honest answer is "there are none". They are lists, never
`null`, for the same reason.

### Branch and tag writes  (HF-compatible)

The four routes `huggingface_hub`'s `create_branch` / `delete_branch` / `create_tag` /
`delete_tag` call. The URL shapes — including the asymmetry in the tag routes — come from the
client and are not ours to tidy: `{rev}` is **the revision being tagged** on `POST /tag/{rev}`
and **the tag name** on `DELETE /tag/{rev}`.

Names arrive percent-encoded (`quote(name, safe="")`), so `feature/x` is `feature%2Fx` in the
path; chi routes on the escaped path, so the handlers unescape the parameter themselves.

| Route | Body | Success |
|---|---|---|
| `POST /api/{datasets\|models}/{ns}/{name}/branch/{branch}` | `{"startingPoint": "<rev>"}` (optional; defaults to the repository's default branch) | 201 |
| `DELETE /api/{datasets\|models}/{ns}/{name}/branch/{branch}` | — | 200 |
| `POST /api/{datasets\|models}/{ns}/{name}/tag/{rev}` | `{"tag": "<name>", "message": "<annotation>"}` (`message` optional) | 201 |
| `DELETE /api/{datasets\|models}/{ns}/{name}/tag/{rev}` | — | 200 |

The success body is `{"name","ref","targetCommit"}`. `huggingface_hub` ignores it (these calls
return `None`); it is there for callers driving the API by hand.

Errors — **the status codes are a compatibility contract, not a style choice**:

- **409** for a ref that already exists, **and for nothing else**. `exist_ok=True` swallows
  every 409 by status alone — the client never reads the body — so 409 has exactly one meaning
  here: *the ref you asked for is there*. Using it for anything else tells a client that
  tolerates duplicates that a ref exists when it does not.
- **409** for `DELETE .../branch/{default branch}`. The default branch is what HEAD, the
  metadata index and every revision-less read depend on. `huggingface_hub` documents this case
  ("`main` cannot be deleted") and raises `HfHubHTTPError` for it. (Safe as a 409: `exist_ok`
  exists only on the create calls, so nothing swallows this one.)
- **503 `overloaded` + `Retry-After`** when a concurrent writer wins the WAL index CAS on the
  same ref (`TF_WAL_MODE=shadow` / `authoritative`). **Deliberately not 409**: nothing was
  written — the local ref is rolled back — so a client told "already exists" would go on to use
  a ref that does not exist. Unlike a commit there is nothing to rebuild, so it is never retried
  server-side; the caller retries. 503 falls through `hf_raise_for_status` to a plain
  `HfHubHTTPError`, and these four calls use `get_session()` directly rather than `http_backoff`,
  so the client surfaces it rather than silently retrying.
- **404 + `X-Error-Code: RevisionNotFound`** for a `startingPoint` / `{rev}` that does not
  resolve, and for deleting a ref that is not there. The header is what makes `huggingface_hub`
  raise `RevisionNotFoundError` instead of a bare `HfHubHTTPError`.
- **400** for a name that is not a valid git reference — `..`, control characters, whitespace,
  `~^:?*[`, `\`, `@{`, a leading or trailing `/`, a component starting with `.`, a `.lock` or
  `.` suffix, `HEAD`, or more than 255 bytes.
- Authorization is `loadRepoForWrite`, exactly as for a commit: a write-scoped token held by
  someone with at least `write` in the namespace, and **403 `repository_archived`** on an
  archived repository.

`message` on a tag produces a real annotated tag object (what `git tag -m` makes), so
`refs` reports the *tag object* as `targetCommit` for it while every revision lookup peels it to
the tagged commit. Without a message the tag is lightweight.

The tag object and the ref that names it are written against **one** repository handle
(`createTagRefThroughWAL`). A fresh tag object is loose, local, and not yet in the WAL, so it
only exists inside the materialisation that produced it; any `gitrepo.Manager.Open` in between
runs `EnsureLocal`, whose rebuild path begins with `os.RemoveAll(gitDir)` (reached after a
compaction changes the base, after cache eviction reclaims the directory, or whenever the local
state file cannot be trusted). Splitting the two writes across two opens leaves a window in
which `pack-objects` fails with `bad object <tag>` and a valid tag comes back as a 500.

Server-side effects:

- **Creating a branch enqueues a sync job for it** (`repo_files` is keyed by
  `(repo_id, ref, path)`, so a branch with no job would have no file index and its GCS access
  script would be empty). The job fires the existing `repo.push` webhook — there is no new
  webhook event for branch or tag writes.
- **Creating a tag enqueues nothing**, matching `git push v1.0`: the indexing worker is driven
  by branch tips (`HeadsAfterPush` lists branches only).
- **Deleting a ref enqueues nothing and leaves its `repo_files` rows behind**, matching
  `git push --delete`. The rows are unreachable once the revision stops resolving, and go with
  the repository.
- In `TF_WAL_MODE=shadow` / `authoritative` the ref update goes through the WAL exactly as a push
  does (`docs/dev/continuity-design.md` §6): authoritative mode acknowledges only after the CAS
  is won, and rolls the on-disk ref back otherwise. A ref written to disk alone would be deleted
  by the next materialisation.

### `GET /api/{datasets|models}/{ns}/{name}/commits/{rev}`  (HF-compatible)

`huggingface_hub.HfApi.list_repo_commits()`. Read authorization, the same as every other
HF-compatible GET on a repository.

query: `limit` (default 50, max 100), `after` (a full commit hash cursor), `path` (restrict to
commits that changed that file or directory)

res 200 (an array, newest first):
```json
[ {"id": "<sha>",
   "authors": [{"user": "alice"}],
   "date": "2026-08-21T00:00:00.000Z",
   "title": "Add notes",
   "message": "A longer explanation of the change."} ]
```

- `authors` elements are **objects**: the client reads `author["user"]` from each one.
- `title` is the commit's subject line, `message` everything after it (`""` for a one-line
  message). `huggingface_hub` indexes both keys directly, so neither may be omitted.
- `date` must end in a literal `Z`. The client parses it with `%Y-%m-%dT%H:%M:%S.%fZ`, so a
  numeric `+00:00` offset raises `ValueError` there.
- Paging follows GitHub's `Link` header convention, which is what the client's `paginate()`
  helper understands: a further page is advertised as
  `Link: <{PublicURL}/api/…/commits/{rev}?…&after={sha}>; rel="next"`, absolute because the
  client follows the URL verbatim. The last page carries no `Link` header.
- A revision that does not resolve is 404 + `X-Error-Code: RevisionNotFound`.
- **`after` errors are 400, never `RevisionNotFound`.** The revision is resolved before the walk
  starts, so by then a failure is the cursor's: one that is not 40 lowercase hex is rejected
  outright, and one that names no commit in this repository is 400 as well. Answering those with
  `RevisionNotFound` would send the client looking for a `revision=` problem it does not have.
  Anything else is a 500. (The UI's own `GET /api/v1/repos/{kind}/{ns}/{name}/commits/{rev}` is
  unchanged and keeps its own `{commits, next_cursor}` shape.)

### `GET /api/v1/repos/{kind}/{ns}/{name}/tree/{rev}/{path...}`  (for the UI)
res:
```ts
{
  path: string
  entries: {
    type: "file" | "directory"
    name: string
    path: string
    size: number
    lfs: boolean
    oid: string
    is_parquet: boolean
    is_model: boolean
    preview: "parquet" | "model" | "text" | "image" | "markdown" | "binary"
    last_commit: CommitInfoUI | null   // The commit that last changed this entry
    gcloud_command: string             // A shell-quoted `gcloud storage cp '<gs://…>' './<name>'`.
                                        // LFS files always use a lfs/ URI; a non-LFS file gets a
                                        // blobs/ URI only when its blob is in the rev's index
                                        // (repo_files) — i.e. already published under blobs/.
                                        // "" for a file not yet indexed right after a push, a bare
                                        // commit sha, or a directory
  }[]
  readme: string | null        // The body of README.md if this directory has one
  readme_too_large: boolean    // This directory's README.md exists but exceeds 256KB, so readme stays null
  latest_commit: CommitInfoUI | null   // The commit rev points to (null for an empty repository)
}
```
`is_model` / `preview:"model"` is set when the extension is `.safetensors` / `.bin` / `.pt` /
`.pth` / `.ckpt` (the target of the checkpoint viewer; see §6).

`CommitInfoUI` is `{ oid, message, author, date }` (`message` is the subject line only).
`last_commit` is resolved by walking history in first-parent order; an entry not found within the
cap (1000 commits) becomes `null`.

- `{rev}` handling matches the HF-compatible `tree` above, and for the same reason spelled out in
  `frontend/DESIGN.md` §9 (never conflate empty, zero and failure): a repository that **has**
  commits but whose `{rev}` doesn't resolve is **404 + `X-Error-Code: RevisionNotFound`**, not a
  200 with `entries: []` — a typo'd or deleted branch must not render as "this repository has no
  files". A repository with **zero** commits keeps its 200 with `entries: []`, `readme: null` and
  `latest_commit: null`; the file browser's empty state depends on it.

### `GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}`  (for the UI: direct GCS fetch script)
Built from `repo_files` (the index for that ref, which the sync worker rebuilds on every push).
Since `repo_files` — not git itself — is the source of truth, the response exactly matches what has
actually been published to object storage. Authorization is the same as an ordinary read, which is
to say none: reads are open to anonymous callers (§11 of the design doc). Note that the script this
returns still needs GCS credentials to run — bucket IAM, not this endpoint, is what gates the bytes.

```ts
type RepoGCSFile = {
  path: string
  size: number
  lfs: boolean
  uri: string   // "gs://bucket/lfs/…" | "gs://bucket/blobs/…"
}

type RepoGCSResponse = {
  ref: string
  files: RepoGCSFile[]
  gcloud_script: string     // The full generated sh script text (format below)
  duckdb_snippet: string    // Non-empty only when at least one .parquet is included; "" otherwise
}
```

- `files` is sorted lexicographically by path.
- `rev` is looked up by an exact match against `repo_files.ref`. A push always indexes under the
  branch or tag name as `ref`, so in practice you're passing a branch or tag name. A ref that git
  knows about but that's not yet in `repo_files` (not indexed) or an empty repository returns
  200 + `files: []`; only a `rev` that can't be resolved as any branch, tag, or commit returns 404
  (passing a bare commit sha also normally returns an empty array, since there's usually no
  matching row in `repo_files`).

The format of `gcloud_script` (must stay consistent between the generator, the docs, and the E2E tests):

```sh
#!/bin/sh
# thinkingface: datasets/team/imdb-ja @ main -- 4 files, 123456789 bytes
# Objects are content-addressed; this script lays them out under DEST.
# DEST may be a local directory or a gs:// prefix.
set -eu
DEST="${DEST:-./imdb-ja}"
cp_one() {
  case "$DEST" in gs://*) ;; *) mkdir -p "$(dirname "$2")" ;; esac
  gcloud storage cp "$1" "$2"
}
cp_one 'gs://bucket/blobs/ab/cd/abcd…' "$DEST"/'README.md'
cp_one 'gs://bucket/lfs/ab/cd/abcd…' "$DEST"/'data/train-00000-of-00004.parquet'
```

- Files follow the same lexicographic path order as `files`. A `'` inside a path is escaped as `'\''`.
- Even with zero files, the header + `set -eu` + `DEST` + the `cp_one` definition are still emitted (there just are no copy calls).

The format of `duckdb_snippet` (only when there is at least one .parquet):

```sql
-- DuckDB: INSTALL httpfs; LOAD httpfs; then CREATE SECRET for GCS (HMAC) before running.
SELECT * FROM read_parquet([
  'gs://bucket/lfs/ab/cd/…',
  'gs://bucket/lfs/ef/01/…'
]);
```

The Web UI's "GCS access" dialog displays this response as-is (`RepoDetail.gcs_uri` /
`gcloud_command` have been removed; see `docs/dev/thinkingface-design.md` §4). The file tree's
`TreeEntryUI.gcloud_command` is also generated with the same quoting rule (`shellSingleQuote`).

### `GET /api/v1/repos/{kind}/{ns}/{name}/refs`  (for the UI)
res:
```ts
{
  branches: { name: string, target_oid: string }[]
  tags: { name: string, target_oid: string }[]
  default_branch: string
}
```

### `GET /api/v1/repos/{kind}/{ns}/{name}/commits/{rev}`  (for the UI)
query: `after` (the last oid scanned on the previous page; when given, results continue from that
commit's first parent), `limit` (default 50, max 100), `path` (restrict to commits that last
changed this file/directory)
res:
```ts
{
  commits: CommitInfoUI[]      // Newest first (a first-parent walk)
  next_cursor: string | null   // The after value for the next page. null once root is reached
}
```
When `path` is given, at most 1000 commits are scanned per request; once that cap is hit, the page
is returned with fewer than `limit` entries (possibly zero) along with a `next_cursor` to continue.

- Same `{rev}` handling as the UI `tree` above: 404 + `X-Error-Code: RevisionNotFound` when the
  repository has commits but `{rev}` doesn't resolve; 200 with `commits: []` for a repository with
  zero commits.

---

## 3. Upload / Commit

### `POST /api/{datasets|models}/{ns}/{name}/preupload/{rev}`  (HF-compatible)
req:
```json
{"files":[{"path":"data/a.parquet","sample":"<base64, first several KB>","size":98765}]}
```
res:
```json
{"files":[{"path":"data/a.parquet","uploadMode":"lfs","shouldIgnore":false,"oid":null}]}
```
`uploadMode` is `lfs` | `regular`. Determined by `.gitattributes` patterns, known binary
extensions, or `size >= 10MiB` (`gitrepo.LFSInlineThreshold`; the comparison is inclusive, so a
file of exactly 10MiB is `lfs`). Every write path decides this with the same
`LFSRules.ShouldUseLFS` call — preupload, the Web UI upload endpoint, and the fallback used when a
repository's own `.gitattributes` cannot be read.

`oid` reports the hash of the file *currently* at that path in the repository at `rev`, in
whichever form matches `uploadMode`, so `huggingface_hub` can tell an unchanged file from a new
one without transferring it:
- `uploadMode: "regular"` → the git blob sha1 of the existing file (what `git hash-object` would
  compute for it).
- `uploadMode: "lfs"` → the sha256 of the existing file's content (the same value that becomes its
  LFS pointer's `oid`).
- `null` — the safe default — whenever there is nothing meaningful to compare against: the path
  does not exist at `rev`, the path is a directory rather than a file, the entry is a symlink
  (whose blob holds the target path, not the bytes the client hashes), or the existing entry's
  storage form doesn't match the freshly computed `uploadMode` (e.g. a path that used to be a
  regular file but would now be routed through LFS, or vice versa).

`huggingface_hub` stores this value as `CommitOperationAdd._remote_oid` and compares it against
the oid it computes locally for the file being uploaded (git blob sha1 or content sha256,
matching the two cases above). A match means the file is byte-identical to what's already
committed, so the operation is dropped from the commit before it is sent; `oid: null` always
means "upload it", since there is no basis for comparison. This is what lets a repeated
`upload_folder` / `create_commit` over an unchanged tree add no new commit — see
`e2e/test_hf_compat.py::test_reupload_of_unchanged_folder_adds_no_commit` and the two
`test_reupload_after_changing_*` tests alongside it, which also confirm a genuinely modified file
is never skipped by mistake.

### `POST /api/{datasets|models}/{ns}/{name}/commit/{rev}`  (HF-compatible)
Content-Type: `application/x-ndjson`. One operation per line.

```jsonl
{"key":"header","value":{"summary":"Upload data","description":"","parentCommit":"<sha>"}}
{"key":"file","value":{"path":"README.md","content":"<base64>","encoding":"base64"}}
{"key":"lfsFile","value":{"path":"data/a.parquet","algo":"sha256","oid":"<sha256>","size":98765}}
{"key":"deletedFile","value":{"path":"old.txt"}}
{"key":"deletedFolder","value":{"path":"old/"}}
{"key":"copyFile","value":{"path":"new.bin","srcPath":"old.bin","srcRevision":null}}
```

Those six keys are the whole set. **A line naming any other `key` is a 400**, and so is a `header`
that will not parse. There used to be no such check, and an unknown operation was skipped in
silence: a commit that mixed one supported operation with one unsupported one applied half of what
was sent and still answered `200 {"success": true}`, leaving the caller no way to find out. Same
trade as `create_pr` below — an error the caller can act on beats a partial write they cannot see.
res 200:
```json
{"success":true,"commitUrl":"http://.../commit/<sha>","commitOid":"<sha>",
 "hookOutput":"","pullRequestUrl":null}
```
The `oid` of an `lfsFile` is accepted **only when it's already linked to this repository** (i.e.
there is a row in `repo_lfs_objects`). Because LFS objects are content-addressed and shared across
the whole instance, accepting an oid based solely on "does it exist in the bucket" would let
someone commit a pointer to another repository's object into their own repository and then fetch
it any time via `resolve`. Both an oid with no link and an oid that's linked but has since
disappeared from the bucket return the same
`400 bad_request: lfsFile {path}: object {oid} has not been uploaded` message. The normal flow
(preupload → LFS batch upload → verify, or a git-lfs push) always creates the link first, so this
doesn't affect it.

The `size` of an `lfsFile` is optional, and the pointer always carries the **stored object's** size
rather than the declared one. A `size` that's present and disagrees returns
`400 bad_request: lfsFile {path}: size {n} does not match the uploaded object's {m} bytes`.
It can't be taken on trust: the pointer's size is what `resolve` declares as `Content-Length`
before streaming the object, so a declared `1` would hand every downloader a one-byte file that
looks completely downloaded (`net/http` truncates the body at the declared length), an over-large
one would hang the connection, and `repo_files.size` and the repository's total size are indexed
from the same number.

`copyFile` is `huggingface_hub`'s `CommitOperationCopy`: it puts the file at `srcPath` (as of
`srcRevision`, defaulting to the revision being committed to) at a second path, without the client
uploading anything. The copy is done by blob hash rather than by bytes, so it costs the same for a
2KB README and for the pointer of a 40GB LFS file — and copying an LFS file copies its *pointer*,
which is what makes the copy resolve to the same object. The source's executable bit is preserved.
Failure modes, none of which commit anything:

- `srcRevision` does not resolve → **404** with `X-Error-Code: RevisionNotFound`.
- `srcPath` does not exist at that revision → **404** with `X-Error-Code: EntryNotFound`
  (`EntryNotFoundError` / `RevisionNotFoundError` in the client, rather than a bare HTTP error).
- `srcPath` is a directory, or a symlink (whose blob holds a path, not content, so copying it
  elsewhere would silently change what it resolves to), or either path is missing → **400**.

`parentCommit` in the `header` is the optimistic lock behind
`create_commit(..., parent_commit=<sha>)`: the commit applies only if the branch's head is that
commit. It accepts a full hash or its first 7 or more characters (the shorthand the client
documents); anything else is a **400**, since a typo is the caller's mistake rather than a branch
that moved. A parent that does not match — including *any* value on a branch that does not exist
yet — is **412 `stale_parent`**, and the message names the head the branch is actually at.

412 rather than the **409 `conflict`** a lost WAL race answers with, because the two ask for
different things next. 409 means another writer won a race this request could still win: send it
again. 412 means the branch is not where the caller believed it was, so re-sending the identical
request can only fail again — they have to look at what landed in between and decide whether their
change still applies. `huggingface_hub` raises `HfHubHTTPError` for both and reads the sentence out
of `X-Error-Message` either way, so the distinction costs the client nothing.

`{rev}` is a **branch name**. A full commit SHA returns 400, and a `{rev}` that resolves to
something in this repository that is *not* a branch — a tag, an abbreviated SHA, `HEAD` — returns
**409 `conflict`** (`{rev} is a tag, not a branch; commits must target a branch`). Without this,
the write would create `refs/heads/{rev}` as a parentless root commit while every read of `{rev}`
kept resolving `refs/tags/{rev}` first (go-git's `RefRevParseRules`), so the commit would be
reported as successful and then never be visible again. A `{rev}` that resolves to nothing is
still accepted and creates the branch — that's the first commit on a new branch. The same rule and
status apply to `PUT`/`DELETE /api/v1/edit/...` and to `POST /api/v1/upload/...`.

An NDJSON line that can't be parsed returns 400 `bad_request`. The message is one of the fixed
strings `commit body must be newline-delimited JSON` / `invalid header entry` /
`invalid file entry` / `invalid lfsFile entry` / `invalid copyFile entry`; the decoder's internal
error text is never returned (the same policy as `decodeJSON`).

**Pull requests are not implemented.** `huggingface_hub`'s `create_commit(..., create_pr=True)`
(and therefore `upload_file` / `upload_folder` / `delete_file` with the same flag) sends this as a
`?create_pr=1` query parameter on this same endpoint, rather than as a field in the NDJSON body.
The server rejects it outright — **400 `bad_request`** — instead of silently committing straight to
`main` as if the flag had been ignored: doing that would surprise a caller who explicitly asked for
a review step before their change becomes visible, and it would leave `pullRequestUrl` in the
response with nothing behind it. The exact wording of the 400 is not part of the contract, only
that the request fails and nothing is committed as a side effect of the attempt.

### `PUT /api/v1/edit/{kind}/{ns}/{name}/{rev}/{path...}`  (editing from the Web UI)

A shortcut for editing and committing a single Markdown/text file from the Web UI. This is
separate from the NDJSON commit above (HF-compatible) and can't be used for batch operations
across multiple files or for deleting files.

req:
```ts
{
  content: string      // New file content (UTF-8)
  message: string      // Defaults to "Update {path}" when omitted
  description: string  // Optional. Appended to the body of the commit message
  base_oid: string     // Optional. The blob SHA at the time editing started
}
```
res 200:
```ts
{
  path: string
  commit_oid: string  // The new commit's SHA
  oid: string          // The file's blob SHA after the commit
  size: number
}
```

Constraints and status codes:

| Condition | Behavior |
|---|---|
| Body size | Capped at 2MiB (`maxEditBytes`). Exceeding it returns **413 `payload_too_large`** |
| JSON parse failure | 400 `bad_request` (`request body must be JSON with a content field`). The decoder's internal message is never returned |
| Character encoding | `content` must be valid UTF-8. Invalid input returns 400 |
| LFS-managed path | 400 for a path that is (or would become) LFS-managed per `.gitattributes`. Also 400 for an existing file that's already committed as LFS |
| `rev` | Branch name only. Passing a commit SHA returns 400; a `rev` that resolves to something other than a branch (a tag, an abbreviated SHA, `HEAD`) returns **409 `conflict`**, as on the commit API and for the same reason — reads resolve a tag before a branch of the same name, so the write would land on a branch nobody reads. A `rev` that resolves to nothing still creates the branch. Defaults to the repository's default branch when omitted |
| Optimistic locking | When `base_oid` is given and it doesn't match the current blob SHA (or the file no longer exists), returns **409 `conflict`** |
| No change | If the saved content is identical to the current content, no new commit is created and the current HEAD and file state are returned as-is |
| Sync | When a commit is created, a sync job to GCS is enqueued just like any other path (`Enqueue`) |
| Auth | 401 when unauthenticated; 403 without write permission (a read-scope token also gets 403) |

### `DELETE /api/v1/edit/{kind}/{ns}/{name}/{rev}/{path...}`  (deleting from the Web UI)

Removes one file in a commit of its own. Same URL as the `PUT` above, same rules, and the same
response shape — it is the deletion half of the Web UI's file editing, not a separate protocol.
For deleting many files at once, or a whole folder, use the NDJSON commit API's `deletedFile` /
`deletedFolder` operations.

req (body optional — an empty body is valid and means "no message, no staleness check"):
```ts
{
  message: string      // Optional. Defaults to "Delete {path}"
  description: string  // Optional. Appended to the body of the commit message
  base_oid: string     // Optional. The blob SHA the caller last saw at this path
}
```
res 200: `EditFileResponse`, as for the `PUT`. `oid` is `""` and `size` is `0` — the file they
would describe is gone; `commit_oid` is the deletion commit.

Constraints and status codes:

| Condition | Behavior |
|---|---|
| Body size | Capped at 64KiB (`maxMetaBody`). Exceeding it returns **413 `payload_too_large`**; a body that will not parse is 400 |
| Missing path | **404 `not_found`** — a delete of something that is not there is not an empty commit |
| Directory | 400. Delete a directory's files individually, or use `deletedFolder` on the commit API |
| **LFS-managed path** | **Allowed**, unlike the `PUT`. Editing an LFS file would mean the browser writing pointer bytes it cannot produce; deleting one only drops the pointer from the tree. **The object itself is not deleted from the bucket** — it is content-addressed, immutable and shared instance-wide, so the deletion merely removes a reference and the bytes stay until `thinkingface gc` finds nothing refers to them any more (`docs/dev/content-addressed-storage-design.md` §5) |
| `rev` | Branch name only. Passing a commit SHA returns 400; a `rev` that resolves to something other than a branch (a tag, an abbreviated SHA, `HEAD`) returns **409 `conflict`**, as on the commit API and for the same reason — reads resolve a tag before a branch of the same name, so the write would land on a branch nobody reads. A `rev` that resolves to nothing still creates the branch. Defaults to the repository's default branch when omitted |
| Optimistic locking | When `base_oid` is given and it doesn't match the current blob SHA, returns **409 `conflict`**. For an LFS file this is the SHA of the *pointer* blob, which is what the tree listing reports |
| Sync | A sync job is enqueued exactly as for any other commit (`Enqueue`), so the index is updated and the `repo.push` webhook fires |
| Auth | 401 when unauthenticated; 403 without write permission (a read-scope token also gets 403); 403 `repository_archived` for an archived repository |

### `POST /api/v1/upload/{kind}/{ns}/{name}/{rev}`  (uploading from the Web UI)

Commits one or more files picked in the browser. Content-Type: `multipart/form-data`. Every part
lands in a **single commit**, so uploading three files adds one entry to the history, not three.

This is the browser's counterpart to the preupload → LFS batch → commit sequence
`huggingface_hub` performs: the server does the LFS routing and the object upload on the client's
behalf, because a browser cannot run git-lfs. Programmatic clients should keep using the
HF-compatible endpoints, which are resumable and parallel.

req parts (order matters — the body is read as a stream):

| Part | Repeatable | Meaning |
|---|---|---|
| `file` | yes | The file's bytes |
| `path` | yes | Repository path for the **next** `file` part. A `path` field must precede the file it names; when none is left over, the part's own `filename` is used |
| `message` | no | Commit message. Defaults to `Upload {path}` for one file, `Upload {n} files` for several |
| `description` | no | Optional. Appended to the body of the commit message |

Any other field is ignored, so a form may carry parts this endpoint has no opinion about.

res 200:
```ts
{
  commit_oid: string  // The new commit's SHA
  paths: string[]     // What landed, in the order the parts arrived
}
```

**Routing to LFS reuses the preupload decision** (`gitrepo.LFSRules.ShouldUseLFS`): a
`.gitattributes` pattern wins outright, and otherwise anything at or above
`gitrepo.LFSInlineThreshold` (10MiB) goes to LFS. A multipart part carries no length, so a file
with no matching pattern is read up to the threshold to find out which side of it it falls on:
filling that buffer means `size >= threshold`, so a file of **exactly** 10MiB goes to LFS, the
same side `ShouldUseLFS` puts it on for every other client. A pattern match needs no read at all. LFS parts are streamed to object storage as they arrive
— **the server never buffers a whole file in memory**, which matters because the same endpoint is
what a 30GB checkpoint would arrive through.

An LFS part is streamed to `storage.LFSIncomingKey(repoID, <random>)` first — the digest, and
therefore the object's only legitimate key, is not known until the last byte, which is the one
thing this path does differently from every other LFS upload (there the client declares the oid in
the batch request). That key lives under `storage.LFSStagingPrefix`, so a request that dies
mid-upload leaves an object `thinkingface gc`'s staging pass sweeps by age, exactly like an
abandoned signed-URL PUT.

From there it hands over to `lfs.PromoteStagedFrom`, so the size check, the server-side copy onto
`storage.LFSKey(oid)`, the link into `lfs_objects` / `repo_lfs_objects`, the staging cleanup and
their ordering are the sequence the batch/verify and emulator-proxy paths run rather than a second
copy of it. Bytes first, index afterwards, as everywhere else in the storage layer: a failure in
between leaves an object nothing references, whereas the reverse order would leave a row promising
bytes that are not there.

Limits (constants in `internal/api/upload.go`):

| Limit | Value | Why |
|---|---|---|
| `maxUploadFiles` | 64 files per request | A browser upload is a handful of files someone picked in a dialog; larger sets belong to `git push` / `upload_folder`, which are resumable and parallel |
| `maxUploadFileBytes` | 10GiB per file | LFS itself caps nothing, but a single non-resumable HTTP request is not the same thing as a resumable transfer |
| `maxUploadInlineBytes` | 32MiB per file **not** routed to LFS | That path really does pass through memory on its way into a git blob. Only reachable when `.gitattributes` negates LFS for the pattern (`*.csv -filter=lfs`) |
| `maxUploadInlineTotalBytes` | 128MiB of non-LFS files per request | The per-file limit alone would still allow 64 x 32MiB resident at once. This is the number that actually bounds the handler's memory; LFS parts never contribute to it |
| `maxUploadFieldBytes` | 8KiB per text field | `message` / `description` / `path` are prose, not payload |

Constraints and status codes:

| Condition | Behavior |
|---|---|
| Not multipart | 400 `bad_request` |
| No `file` part | 400 — an upload with nothing in it is a mistake, not an empty commit |
| Path validation | Every path goes through `gitrepo.ValidatePath` **before any bytes are stored**: a `..` segment, a `.git` component (case-insensitively, at any depth) or a NUL byte is 400 |
| Missing directories | Created by the commit itself, here and on `PUT /api/v1/edit/...` alike — a path may name directories that do not exist yet |
| Over a size limit | **413 `payload_too_large`**, naming the file |
| Too many files | 400, naming the limit |
| `rev` | Branch name only. Passing a commit SHA returns 400; a `rev` that resolves to something other than a branch (a tag, an abbreviated SHA, `HEAD`) returns **409 `conflict`**, as on the commit API and for the same reason — reads resolve a tag before a branch of the same name, so the write would land on a branch nobody reads. A `rev` that resolves to nothing still creates the branch. Defaults to the repository's default branch when omitted |
| Concurrent push | The commit is rebuilt on the moved head (`retryOnStale`), as for the HF commit API; only exhausted retries surface as 409 `conflict` |
| Concurrent GC | A garbage collection that removed the object between the copy and the link answers **409 `conflict`** (`store.ErrLFSObjectGone` / `lfs.ErrNotStaged`), not 500: re-uploading the same bytes succeeds. The emulator's LFS proxy upload answers the identical condition the same way |
| Sync | One sync job for the commit (`Enqueue`), so the index is updated and the `repo.push` webhook fires |
| Auth | 401 when unauthenticated; 403 without write permission (a read-scope token also gets 403); 403 `repository_archived` for an archived repository |

---

## 4. File retrieval

### `GET|HEAD /{ns}/{name}/resolve/{rev}/{path...}`  (model)
### `GET|HEAD /datasets/{ns}/{name}/resolve/{rev}/{path...}`  (dataset)

- A regular file: the body is returned as-is.
- **Always attaches `Content-Disposition: attachment; filename="..."; filename*=UTF-8''...` and
  `X-Content-Type-Options: nosniff`.** Same policy as the LFS signed-URL path on GCS, which attaches
  `response-content-disposition=attachment` (`internal/storage/gcs.go`). This prevents a `.html` /
  `.svg` file inside a repository from being rendered on the API origin via a top-level navigation.
  Subresource loads like `<img src>` are unaffected by `Content-Disposition`, so displaying images
  inside a README still works as before.
- **Content-Type is downgraded to `application/octet-stream` for types that could execute a
  script** (`text/html` / `application/xhtml+xml` / `application/xml` / `text/xml` /
  `application/rdf+xml` / `application/mathml+xml` / `text/vtt`). `image/svg+xml` is not downgraded
  since it's needed for display via `<img>` (the attachment header above covers it).
- An LFS file: **302** to a signed URL (proxied and returned as body content in emulator
  environments).
- **LFS is only returned when the pointer's oid is linked to this repository (`repo_lfs_objects`).**
  If unlinked, 404 `object not found` (no headers are emitted at all, so nothing leaks even via
  HEAD). The object itself is content-addressed and shared across the whole instance
  (`lfs/objects/{oid}`), so "is this repository readable" doesn't by itself authorize the object. A
  pointer is just text that anyone can commit, so without this check someone could read another
  repository's content just by committing a pointer that names its oid into their own repository.
  Same check as LFS batch / commit (`lfsObjectOwned` in `internal/api/resolve.go`). Same for
  `/api/v1/raw/...`.
- Headers (referenced by `hf_hub_download`):
  - `ETag: "<blob sha1>"`, or `"<sha256>"` for LFS
  - `X-Repo-Commit: <commit sha>`
  - `X-Linked-Etag` / `X-Linked-Size` (for LFS)
  - `Content-Length`, `Content-Type`, `Accept-Ranges: bytes`
- Range requests are supported on both paths: a regular file streams the requested slice straight
  out of the git blob (never buffered — these run to gigabytes), LFS uses a storage range read. One
  range per request (`bytes=a-b` / `bytes=a-` / `bytes=-n`); a satisfiable one answers 206 with
  `Content-Range`, while an unparseable, multi-range or unsatisfiable one falls back to 200 with the
  whole body rather than 416. A HEAD ignores `Range` and always reports the full `Content-Length`,
  which is what `hf_hub_download` reads the size from.
- Download stats: once a request is known not to be for a directory, it counts as 1 request = 1
  count, and **the same single count advances both counters** — the running total
  `repositories.downloads` and today's row in `repo_download_stats(repo_id, date, count)`
  (UPSERTed). One rule governs both, so the last-30-days figure is always a window over the same
  total the UI shows it beside, and can never exceed it. Both HEAD and an LFS 302 count as one
  (range splitting, or a client's actual fetch from the GCS location a 302 pointed to, isn't
  counted — that traffic doesn't pass through this server, so there's no way to observe it).
  Recording happens in a goroutine decoupled from the request's response path; a failure on either
  write is only logged and doesn't affect the response (`Server.recordDownload` →
  `store.IncrementDownloads` + `store.RecordDownload`).

### `GET /api/v1/raw/{kind}/{ns}/{name}/{rev}/{path...}`  (for the UI preview)
res: `{"path","size","truncated":bool,"content":"...","encoding":"utf-8"|"base64"}`
Capped at 512KB. Content past that returns `truncated: true`.
The body comes back as a JSON string, so there's no need to worry about Content-Type /
Content-Disposition the way `resolve` does (this is what the Web UI preview uses).
When returning the content of an LFS pointer's target, it goes through the same ownership check as
`resolve`.

<a id="single-file-read-bad-rev"></a>
**A `{rev}` that doesn't resolve is a 404** here, in `model-meta` (§6) and in both `parquet`
endpoints (§5) — the three single-file reads that map their errors through `handleStoreError`.
A repository with zero commits is a 404 on these three as well: each names one file, and a file
that isn't there is not there whichever of the two reasons applies. (They differ from the listing
endpoints on that point, which answer 200 for a zero-commit repository because "there is nothing
to list" is a real answer.) These are Web UI routes, so no `X-Error-Code` distinction is made —
nothing here picks a `huggingface_hub` exception type off the header.

---

## 5. Parquet viewer

Both endpoints below resolve one named file, so a `{rev}` that doesn't resolve — and a repository
with zero commits — is a 404, exactly as described for `raw` in §4.

### `GET /api/v1/parquet/{kind}/{ns}/{name}/schema/{rev}/{path...}`
res:
```ts
{
  path: string
  size: number
  num_rows: number
  num_row_groups: number
  compression: string
  columns: Column[]
  columns: Column[]
}

Column:
{
  name: string
  type: string           // Physical type: "INT64" / "BYTE_ARRAY"; nested types are "GROUP"
  logical_type: string   // "STRING" / "JSON" / "TIMESTAMP(MICROS)" / "LIST" / "MAP"; "" if none
  optional: boolean
  repeated: boolean
  feature: string        // The HF datasets feature type, lowercased ("image" / "audio" / "classlabel" ...); "" if none
}
```

`feature` is a rendering hint. It first uses the `_type` from the Parquet key-value metadata
`huggingface` (the `{"info":{"features":{...}}}` that `datasets` writes); columns without that are
filled in from the README's `dataset_info.features` (`name` / `dtype`, taking the first config when
there are several) at the same `rev`. The shape of the value itself is never changed: an `image`
column comes back either as a `{bytes: base64, path: string}` struct or as raw bytes (a base64
string), unmodified. The Web UI uses this to switch to things like an image thumbnail display
(a column with `logical_type: "JSON"` gets a JSON tree display).

### `GET /api/v1/parquet/{kind}/{ns}/{name}/rows/{rev}/{path...}`
query: `offset` (default 0) / `limit` (default 50, max 500) / `columns` (comma-separated; all
columns when omitted)
res:
```ts
{
  path: string
  offset: number
  limit: number
  num_rows: number       // The row count of the whole file
  columns: Column[]      // Only the columns returned
  rows: Record<string, unknown>[]   // JSON-encoded values. Byte strings become base64 strings
}
```

---

## 6. Model checkpoint viewer

### `GET /api/v1/model-meta/{kind}/{ns}/{name}/{rev}/{path...}`

Targets `.safetensors` / `.bin` / `.pt` / `.pth` / `.ckpt`. Any other extension returns 400.
A `{rev}` that doesn't resolve — and a repository with zero commits — is a 404, as for `raw` in §4.

No weights are ever downloaded. For safetensors, only the leading `<u64 header length><JSON
header>` is range-read; for PyTorch, only the `data.pkl` member inside the zip is range-read.
Unlike `torch.load`, the pickle is never executed — it's read with a safe decoder that only
interprets `torch._utils`'s tensor-rebuild functions (`_rebuild_tensor_v2`, etc.).

The result is cached in memory server-side, keyed by LFS OID / git blob SHA (content-addressed, so
it never goes stale; capped at `DefaultCacheEntries`=256 entries, with singleflight preventing
duplicate parsing of concurrent requests).

res 200:
```ts
{
  path: string
  size: number
  format: "safetensors" | "pytorch"
  num_tensors: number
  num_parameters: number
  tensor_bytes: number
  dtypes: { dtype: string; num_tensors: number; num_parameters: number; size_bytes: number }[]
  metadata: Record<string, string>   // safetensors' __metadata__ / PyTorch's scalar values (epoch, global_step, etc.)
  header_bytes: number                // Byte count of the header read (the JSON header or data.pkl)
  tensors: { name: string; dtype: string; shape: number[]; num_parameters: number; size_bytes: number }[]
  truncated: boolean                 // tensors is truncated to the first 4096 entries (aggregate values cover the whole file)
  warnings: string[]                 // Notes for when only part of it could be read
}
```

dtype names are normalized to a shared vocabulary across safetensors and PyTorch (`float32` /
`float16` / `bfloat16` / `float64` / `int64` / `int32` / `int16` / `int8` / `uint8` / `uint16` /
`uint32` / `bool` / `float8_e5m2` / `float8_e4m3fn`, etc.).

404 if the LFS object doesn't exist in storage. 400 if the header is corrupt and can't be parsed.

---

## 7. Experiment tracking

```ts
type ExpProject = { name: string; num_runs: number; updated_at: string }
type ExpRun = {
  name: string
  status: "running" | "finished" | "failed"
  last_step: number
  num_points: number
  started_at: string | null
  updated_at: string
  config: Record<string, unknown>
  metric_keys: string[]
  summary: Record<string, number>   // The final value of each metric
  group: string                     // The name of the sweep it belongs to ("" if unspecified)
  job_type: string                  // Its role within the sweep (e.g. "train" / "eval"; "" if unspecified)
  tags: string[]                    // Manually attached labels
  archived: boolean                 // Hidden from the default listing (not deleted)
  is_baseline: boolean              // The comparison baseline. At most 1 run per project
  note: string                      // A handwritten note (Markdown). "" if not written
  models: ExpRunModelRef[]          // Models this run declared it produced (below)
}

type ExpRunModelRef = {
  repo_id: string    // "ns/name" (a model repository)
  revision: string   // The revision the run recorded. "" if it couldn't be resolved
  exists: boolean    // Whether it's actually a model repository the viewer can see. If false, don't render as a link
}
```

`tags` / `archived` / `is_baseline` / `note` / `models` are annotations attached by a human (or a
training script); ingest and the parquet indexer never overwrite them (they survive
re-indexing). The run-listing API returns archived runs too — hiding them by default is a Web UI
filter.

`group` / `job_type` are not annotations — they come **from ingest**
(`trackio.init(group=..., job_type=...)`) — but **omitting them or sending an empty string is
treated as "keep the current value"**. This means (1) a run doesn't fall out of its group just
because a later batch omits it, and (2) they aren't wiped out by re-indexing through the parquet
indexer (route A), which doesn't know about `group`. `group: ""` means "no group" — an ordinary
flat run as before — and the Web UI only groups runs that have a `group` into a collapsible row.
Sending an empty string cannot remove a run from a group (a rerun with the group removed is a
different run).

Two keys of `config` are reserved, and the Web UI's run detail page shows them in dedicated
sections:

| Key | Content |
|---|---|
| `_meta` | A snapshot of the execution environment (`git.commit` / `git.branch` / `git.dirty` / `cmdline` / `python` / `platform` / `hostname` / `gpu.name` / `gpu.count` / `gpu.cuda` / `requirements_sha256`). Collected by the Python shim at `init()` time |
| `_args` | HF Trainer's `TrainingArguments` |

Via the ingest path this arrives as a nested object; via the parquet path, the configs file's
columns go straight in (as flat keys like `_meta.git.commit`). Both shapes may be treated as the
same dot-separated key.

An experiment repository is a dataset repository, so per-repository archive and delete (§2) apply
to it directly. While archived, both ingest (`log` / `finish`) and PATCH/DELETE of a run return
403 `repository_archived`.

- `GET /api/v1/experiments` → `{"items": {namespace, name, full_name, num_projects, updated_at}[], "total": number}`
  query `author?` (filter by namespace, case-insensitive) / `search?` (the same full-text search as
  `store.RepoFilter.Search`; targets name, the card's description, etc.) / `limit?` (default 100,
  cap 100) / `offset?`. `total` is the total match count regardless of limit/offset. A call with no
  arguments returns the same results as before (only `total` is added). This is used by the
  namespace page's Experiments tab and by the `/experiments` listing (for the latter, search and
  paging are mandatory since this endpoint caps out at 100 entries).
- `GET /api/v1/experiments/{ns}/{repo}` → `{"repo": RepoSummary, "projects": ExpProject[]}`
- `GET /api/v1/experiments/{ns}/{repo}/{project}/runs` → `{"runs": ExpRun[]}`
- `PATCH /api/v1/experiments/{ns}/{repo}/{project}/runs/{run}` (write permission required)
  A partial update of a run's annotations. An omitted field is left unchanged.
  req: `{"tags":["lr-sweep"],"archived":false,"is_baseline":true,"note":"# lr sweep\n...","models":[{"repo_id":"team/bert-ja","revision":"a1b2c3d"}]}` (at least one of these)
  res 200: `{"run": ExpRun}`
  - `tags` is saved after trimming surrounding whitespace, dropping empty elements, and
    deduplicating (at most 32 entries, 64 bytes each)
  - `is_baseline: true` clears `is_baseline` on every other run in the same project. The "one per
    project" rule is also enforced by a partial unique index.
  - `note` is free-form Markdown (up to 16384 bytes). Newlines and tabs are allowed; any other
    control character returns 400. Trailing whitespace is stripped before saving, and sending
    `""` clears the note.
  - `models` is **replaced wholesale** (sending `[]` clears it). At most 32 entries per run;
    `revision` is at most 256 bytes. If `repo_id` can't be parsed as `ns/name`, returns 400 (since
    this is a program-written value, it's not left dangling). **Pointing at a repository that
    doesn't exist is allowed**, and it stays with `exists: false`. `revision` is recorded but not
    validated (consistent with the dangling policy in §12).
  - 400 if not even one field is given; 404 if the run doesn't exist.
- `GET /api/v1/experiments/{ns}/{repo}/{project}/runs/{run}/artifacts`
  A run's list of artifacts. The actual content lives in the git tree of the dataset repository's
  default branch; there's no dedicated store (see "Run artifacts" below).
  res 200:
  ```ts
  {
    path: string        // "{project}/artifacts/{run}"
    rev: string         // Always the default branch
    artifacts: {
      name: string      // Path relative to the artifacts directory
      path: string      // The full path within the repository
      size: number      // For LFS, the size of the actual object
      lfs: boolean
      preview: "parquet" | "model" | "text" | "image" | "markdown" | "binary"   // Same as the tree in §2
    }[]
  }
  ```
  - A run whose directory doesn't exist returns `artifacts: []`, not 404 ("no artifacts" is a valid
    answer).
  - 400 if `project` / `run` contains `..` or otherwise escapes into a parent directory.
- `DELETE /api/v1/experiments/{ns}/{repo}/{project}/runs/{run}` (write permission required)
  Deletes the run's index row and its live metric points (`exp_points`). res 204; 404 if the run
  doesn't exist. Note the distinction from `archived: true` (which only hides it and is
  reversible): this one is not reversible. However, for a run sourced from trackio's parquet
  export, the export is the true source of record, so it comes back on the next index (since the
  git history isn't rewritten). To remove it permanently, delete the whole repository.
- `GET /api/v1/experiments/{ns}/{repo}/{project}/metrics`
  query: `runs` (comma-separated; all runs when omitted) / `keys` (comma-separated; all keys when
  omitted) / `x` (`step`|`time`, default `step`) / `max_points` (default 1000)
  res:
  ```ts
  { series: { run: string; key: string; points: [number, number][] }[] }
  ```
- `POST /api/v1/experiments/{ns}/{repo}/{project}/log`  (ingest, Bearer write required)
  req:
  ```json
  {"run":"run-1","config":{"lr":0.001},"status":"running",
   "group":"lr-sweep","job_type":"train",
   "points":[{"step":1,"timestamp":"2026-08-21T00:00:00Z","metrics":{"loss":0.5}}]}
  ```
  res 200: `{"ok":true,"run":"run-1","accepted":1}`
  - `group` / `job_type` are optional. Empty or omitted means "keep the current value" (as above).
    Values follow the same constraint as run names (1-256 bytes, no control characters); anything
    outside that returns 400.
  - **`summary` and `metric_keys` are merged across batches.** A batch that sends only `loss`
    doesn't wipe out an existing `accuracy`, and a batch with `points: []` (a status notification)
    doesn't empty out the summary. The same key is overwritten with the new value.
  - Metric names follow the same constraint as run names (1-256 bytes, no control characters) and
    additionally must not be one of the **structural columns** of the metrics parquet — `id`,
    `log_id`, `space_id`, `run_id`, `run_name`, `run`, `step`, `_step`, `global_step`, `timestamp`,
    `_timestamp`, `created_at`, `project`, `_ingest_id`. Those describe the row rather than a
    measurement, so a metric named after one would overwrite it when the buffer is flushed into
    the parquet (a run renamed to `"0.5"`, a broken x axis). 400.
  - `{project}` must also be usable as a directory inside the repository, since the flush writes
    `{project}/metrics.parquet`: `.`, `..` and `.git` (matched case-insensitively) return 400
    rather than being buffered into points that could never be committed.
- `POST /api/v1/experiments/{ns}/{repo}/{project}/finish`
  req `{"run":"run-1","status":"finished","group":"lr-sweep","job_type":"eval"}` → 200
  - `group` / `job_type` are handled the same as in `log` (optional, empty means keep). A run that
    never sent a single point gets created by this call, so these are accepted here too, so that
    such a run can also be placed into a group.
  - `{project}` is validated exactly as in `log`, since this call creates the project too.

### Run artifacts

Files saved by `trackio.log_artifact(path, name=None)` are placed **inside the same dataset
repository** as the metrics. No dedicated artifact store is created.

```
{project}/artifacts/{run}/{name}
```

- `name` defaults to the file's base name. When a directory is passed, its relative structure
  underneath is preserved under `{name}/`.
- Upload uses the HF-compatible preupload / commit (§3) as-is. There's no dedicated endpoint.
  Large files get routed to LFS by the repository's `.gitattributes`.
- Artifacts are therefore version-controlled by git, included in `git clone`, and also retrievable
  via `gcloud storage cp` through the script `GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}`
  returns (§17).
- The Python shim **buffers these during the run rather than sending them immediately, and bundles
  them into a single commit at `finish()`** (commit message:
  `chore(trackio): artifacts for {project}/{run}`) — so a run that saves 20 figures doesn't turn
  into 20 commits.

**The `artifacts/` segment exists to avoid colliding with parquet layout detection.** The indexer
(`backend/internal/experiments.DetectLayouts`) discovers a project from `{project}/metrics.parquet`
or `{project}.parquet`, so an ordinary artifact (e.g.
`{project}/artifacts/{run}/foo.parquet`) is never mistakenly detected as a project. The one
exception is when an artifact's name is **exactly `metrics.parquet`**, which would create a
fictitious project named `{project}/artifacts/{run}`. So the Python shim rejects this name
(forcing you to pass an alternate `name=`). The equivalent of `aux/configs.parquet` is harmless,
since configs alone don't become a project.

### Run-to-produced-model links

`trackio.log_model("ns/name", revision=None)` records "this run produced this model" on the run
side. When `revision` is omitted, it resolves to the HEAD right after the push (the `sha` from
`GET /api/models/{ns}/{name}`).

- The save target is the same **annotation path** as `note` (the `models` field of
  `PATCH .../runs/{run}`). `UpsertExpRun` never touches it, so it survives parquet re-indexing.
- **No edge is added to `repo_lineage`.** That index is wholesale-replaced from the repository card
  on every push to the default branch (§12), so a row written from the run side would be wiped out
  by the next push. To keep there being a single writer, the model page instead reverse-looks-up
  and reads the run-side record (the `produced_by` field of
  `GET /api/v1/repos/model/{ns}/{name}/lineage`, §12).
- This is shown alongside the card's `lineage.run:` as a **separate thing**. The former is "the
  provenance the model's card claims"; the latter is "the artifact the training script claims to
  have produced" — the claims come from different sources.
- The record persists even when it points at a repository that doesn't exist; the UI shows it as
  `exists: false` with warning text (not rendered as a link). `revision` is not validated: a link
  to a revision that's since been rewritten becomes an error on the file browser side.

### trackio integration (batch path)
The sync worker detects and indexes parquet files inside a dataset repository. Recognized layouts:

| File | Project name |
|---|---|
| `metrics.parquet` | The repository name |
| `{project}/metrics.parquet` | The directory name |
| `{project}.parquet` (no `_system` / `_configs` etc. suffix) | The base name |

Metrics parquet columns: `run_name` (required) / `step` / `timestamp`; `id`, `log_id`, `space_id`
are ignored; the remaining numeric columns are the metrics. Config is read from `run_name` plus
flattened config columns in `aux/configs.parquet` or `{project}_configs.parquet`.

**Route A currently doesn't pick up `group` / `job_type` columns.** Even if the configs parquet has
a `group` column, it just lands in `config.group` as an ordinary config value and is never written
to `exp_runs.group_name` (because the indexer doesn't pass `Group` / `JobType` to
`store.UpsertExpRunWith` — not passing them means NULL means keep, so a group set via the ingest
path is never wiped out by re-indexing).

### Parquet flush of ingest points (native path)

Points received via `POST .../log` are only staged in `exp_points`; the source of truth remains
the parquet inside the dataset repository. The sync worker periodically
(`TF_EXP_FLUSH_INTERVAL`, default 1 minute; immediately once a run becomes `finished` / `failed`)
commits the buffer to `{project}/metrics.parquet` (or that file, if a metrics parquet with a
different name already exists) and deletes it from `exp_points`. See `docs/dev/thinkingface-design.md`
§8 for details.

- The commit message is `chore(trackio): flush {project} metrics`. This does not fire the
  `repo.push` webhook.
- The columns written are the same `run_name` / `step` / `timestamp` + metric columns as the table
  above, plus an `_ingest_id` (`exp_points.id`) column — this exists for retry idempotency and,
  like `id`, is ignored as a structural column rather than treated as a metric.
- `GET .../metrics` merges the parquet with any not-yet-flushed points. `_ingest_id` prevents
  double-counting a point right after a flush (before it's deleted). So the series a client sees
  doesn't change across a flush.

---

## 8. Git / LFS

- `GET  /{ns}/{name}.git/info/refs?service=git-upload-pack|git-receive-pack`
- `POST /{ns}/{name}.git/git-upload-pack`
- `POST /{ns}/{name}.git/git-receive-pack`
- datasets accept the same at `/datasets/{ns}/{name}.git/...`
- `POST /{...}.git/info/lfs/objects/batch` — the LFS Batch API
  - upload: `actions.upload.href` (a signed PUT, or a proxy URL in the emulator) and
    `actions.verify` are returned only for oids not already held. "Already held" means an object
    at that oid's key whose length is **exactly** the `size` the request declared: deduplication's
    only evidence is the client's own declaration, so an object declared as 0 bytes that is not 0
    bytes gets an upload action rather than a link to bytes the caller never showed it had. A
    genuinely empty object (declared 0, stored 0) deduplicates like any other.
  - download: `actions.download.href`, **only when that oid is linked to this repository in
    `repo_lfs_objects`.** An unlinked oid returns a per-object error
    `{"code":404,"message":"object <oid> not found"}` (this doesn't fail the whole batch). Whether
    it exists in the bucket is only checked once it's linked — so "does another repository have
    this oid" can never be inferred from the response.
  - The link is created by the upload path's deduplication branch / `verify` / the emulator's
    proxy upload (all of these via `store.RecordLFSObject`), and by the post-push indexer
    (`store.LinkLFSObjects`).
- `PUT  /api/v1/lfs/{repo_id}/{oid}` — proxy upload for the emulator
- `GET  /api/v1/lfs/{repo_id}/{oid}` — proxy download for the emulator. On the fallback path used
  when an href's signature doesn't validate, both read permission on the repository and **the
  oid's ownership** are checked. The response carries `X-Content-Type-Options: nosniff` and
  `Content-Disposition: attachment`.
- `POST /api/v1/lfs/{repo_id}/verify`
- `POST /{...}.git/info/lfs/objects/verify`

When none of the three `/api/v1/lfs/{repo_id}/...` endpoints pass either the signature or the
permission check, they return **404 `not_found` (`object not found`)** regardless of whether the
repository ID exists. Distinguishing 401 from 404 here would let scanning numeric IDs reveal how
many repositories the instance has.

### git over SSH

When `TF_SSH_ENABLED=true`, the same repositories are also accessible over SSH (default `:2222`).
Rather than URL routing like HTTP, it interprets SSH exec commands.

```
git-upload-pack  '<path>'      # clone / fetch
git-receive-pack '<path>'      # push
```

- `<path>` is `ns/name` / `models/ns/name` / `datasets/ns/name`. A leading `/` and trailing `.git`
  are optional. Each segment must match the same naming rule as the REST API
  (`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`); anything that doesn't is rejected outright.
- Any command other than the two above, a shell, a PTY, a subsystem (sftp), and port forwarding are
  all rejected.
- Authentication is public key only (no password authentication). The SSH username is ignored; the
  user is resolved from the key presented (by convention `git@` is used).
- Repository resolution, write permission, archive checks, and post-push sync job enqueueing all go
  through the same implementation as the HTTP path.
- On failure, one line is returned to the client's stderr and it exits 1. Internal error details
  are never returned.

---

## 9. Webhooks

When an event occurs, an HTTP POST is sent to a registered URL. Registration can be per-namespace
(`repo_id` is null) or per specific repository; the permission required is write/admin on the
target namespace (`CanWriteNamespace`, or site admin). Every endpoint requires auth and write
scope. **When the namespace is an organization, the admin role is required** (write gets 403). In
a user namespace the owner is admin, so behavior doesn't change (`docs/dev/organization-design.md`
§4).

```ts
type WebhookEvent = "repo.push" | "repo.created" | "repo.deleted" | "repo.moved" | "repo.transfer_requested" | "repo.archived" | "repo.unarchived" | "run.finished" | "run.failed"
type WebhookDeliveryStatus = "pending" | "success" | "failed"

type Webhook = {
  id: number
  namespace: string
  repo_full_name: string   // "" means the entire namespace; "ns/name" means only that repository
  url: string
  events: WebhookEvent[]
  active: boolean
  created_at: string
  updated_at: string
}

type WebhookDelivery = {
  id: number
  event: WebhookEvent
  payload: Record<string, unknown>
  status: WebhookDeliveryStatus
  attempts: number
  last_attempt_at: string | null
  response_status: number | null   // null when the target was unreachable
  response_body: string            // The first few KB of the response body
  created_at: string
}
```

### `GET /api/v1/namespaces/{ns}/webhooks`
res: `{"items": Webhook[]}` (includes both namespace-wide webhooks and per-repository webhooks
belonging to it)

### `POST /api/v1/namespaces/{ns}/webhooks`
req: `{"repo": "dataset/my-metrics" | undefined, "url": "https://...", "events": WebhookEvent[], "active": true}`
res: `{"webhook": Webhook, ..., "secret": "whsec_..."}` (`secret` is returned only here)
Only http/https URLs are allowed. Local/private addresses (`localhost`, `127.0.0.0/8`, `10/8`,
`172.16/12`, `192.168/16`, `169.254/16`, `::1`, etc.) are rejected by default, and allowed only
when `TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS=true`.

### `GET /api/v1/webhooks/{id}`
res: `{"webhook": Webhook}`

### `PUT /api/v1/webhooks/{id}`
req: `{"url": "...", "events": [...], "active": false, "rotate_secret": true}` (every field
optional; omitted fields keep their current value)
res: `{"webhook": Webhook, ..., "secret": "whsec_..."}` (`secret` is returned only when
`rotate_secret:true`)

### `DELETE /api/v1/webhooks/{id}`
res: 204

### `GET /api/v1/webhooks/{id}/deliveries`
query: `limit` (default 30, max 100) / `offset`
res: `{"items": WebhookDelivery[], "total": number}` (newest first)

### `POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver`
Re-enqueues a new delivery with the same event/payload as an existing one. res: `WebhookDelivery`

### How delivery works

- A lightweight worker (`internal/webhooks`, the same pattern as `sync_jobs` /
  `internal/syncer`) uses the `webhook_deliveries` (PG table) as its queue. The POST timeout is
  10 seconds.
- Headers: `Content-Type: application/json` / `X-Thinkingface-Event: <event>` /
  `X-Thinkingface-Delivery: <delivery id>` /
  `X-Thinkingface-Signature: sha256=<hex of HMAC-SHA256(secret, body)>`
- On failure (non-2xx or unreachable), it retries with exponential backoff (30s, 1m, 2m, 4m,
  8m... capped at 15m) up to 5 times; if it still fails, it's finalized with `status: "failed"`.
- Events fired, and their payloads:
  - `repo.push` (when post-push processing completes): `{namespace, repo, full_name, kind, ref, old_sha, new_sha, changed_files}`
  - `repo.created` / `repo.deleted`: `{namespace, name, kind, full_name}`
  - `repo.moved` (when a transfer/rename completes; delivered to subscriptions on the **new**
    namespace): `{kind, from: {namespace, name}, to: {namespace, name}, full_name}`
  - `repo.transfer_requested` (when a transfer becomes pending approval; delivered to
    subscriptions on the **destination** namespace):
    `{transfer_id, kind, from: {namespace, name}, to: {namespace, name}, requested_by, expires_at}`
  - `repo.archived` / `repo.unarchived` (making read-only / undoing that — lets a mirroring
    consumer know no changes are coming while archived): `{namespace, name, kind, full_name, archived}`
  - `run.finished` / `run.failed` (fired only when a run's status transitions to that value, to
    prevent duplicate sends): `{namespace, repo, full_name, project, run, status}`

---

## 10. Miscellaneous

- `GET /healthz` → `{"status":"ok"}`
- `POST /api/validate-yaml` — Validates README front matter (HF-compatible). **Auth required**
  (returns 401 otherwise). `huggingface_hub` calls this from `create_commit` with a token attached
  for the commit, so this doesn't affect compatibility. Body size cap is 1MiB (exceeding it returns
  413 `payload_too_large`).
- `GET /api/v1/stats` → `{"datasets":n,"models":n,"experiments":n,"total_size":n}`
  (instance-wide totals, identical for every caller: there is no repository visibility to filter
  on, so signing in does not bring more repositories into the count — see §11 of the design doc)
- CORS / CSRF / security headers: see the shared specification at the top of this document
  (the `TF_ALLOWED_ORIGINS` allowlist approach)

### `GET /api/v1/usage`  (auth required)

GCS storage usage for the namespaces the caller can access (the same set as `NamespacesForUser`).
`lfs_size` is the sum of `repo_lfs_objects` × `lfs_objects.size` (since it's content-addressed, an
LFS object shared across multiple repositories is counted separately in each repository's
breakdown). Plain git blobs (like README) never reach GCS, so they're not included.

```ts
type UsageNamespace = { namespace: string; lfs_size: number; num_files: number; num_repos: number }
type UsageRepo = {
  namespace: string; name: string; kind: "dataset" | "model"; full_name: string
  lfs_size: number; num_files: number
}
type UsageResponse = {
  namespaces: UsageNamespace[]
  repos: UsageRepo[]  // Sorted by lfs_size descending
}
```

---

## 11. Internal package contracts

### `internal/storage`
```go
type Storage interface {
    SupportsSignedURL() bool
    SignedGetURL(ctx context.Context, key string, ttl time.Duration, downloadName string) (string, error)
    SignedPutURL(ctx context.Context, key string, ttl time.Duration, size int64) (string, error)
    Put(ctx context.Context, key string, r io.Reader, contentType string) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
    Stat(ctx context.Context, key string) (ObjectInfo, error)
    Copy(ctx context.Context, srcKey, dstKey string) error
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]ObjectInfo, error)
    PublicURI(key string) string
}
type ObjectInfo struct { Key string; Size int64; ContentType string; Updated time.Time }
var ErrNotFound = errors.New("storage: object not found")

func LFSKey(oid string) string                                  // lfs/ab/cd/abcd...
func BlobKey(sha string) string                                 // blobs/ab/cd/abcd... (a non-LFS git blob)
```

### `internal/viewer`
```go
package viewer

type Column struct {
    Name        string `json:"name"`
    Type        string `json:"type"`          // "INT64", "BYTE_ARRAY", ...
    LogicalType string `json:"logical_type"`  // "STRING", "TIMESTAMP(MICROS)", "", etc.
    Optional    bool   `json:"optional"`
    Repeated    bool   `json:"repeated"`
}

type Schema struct {
    Columns      []Column `json:"columns"`
    NumRows      int64    `json:"num_rows"`
    NumRowGroups int      `json:"num_row_groups"`
    Compression  string   `json:"compression"`
    SizeBytes    int64    `json:"size_bytes"`
}

type Rows struct {
    Columns []Column         `json:"columns"`
    Rows    []map[string]any `json:"rows"`
    NumRows int64            `json:"num_rows"`
    Offset  int64            `json:"offset"`
}

// New returns a reader that reads parquet from GCS. It keeps a local LRU cache under cacheDir.
func New(st storage.Storage, cacheDir string, maxCacheBytes int64) *Reader

func (r *Reader) Schema(ctx context.Context, key string) (*Schema, error)
func (r *Reader) Rows(ctx context.Context, key string, offset int64, limit int, columns []string) (*Rows, error)
// Scan passes every row to fn in order. Used by the experiment indexer. Aborts if fn returns an error.
func (r *Reader) Scan(ctx context.Context, key string, columns []string, fn func(row map[string]any) error) error
```
Value JSON-encoding rules: INT32/INT64 → `float64`/`int64` (a JSON number), BOOLEAN → bool,
FLOAT/DOUBLE → float64 (NaN/Inf become null), a BYTE_ARRAY with the STRING logical type → string,
any other BYTE_ARRAY → a base64 string, null → `nil`, LIST/MAP → `[]any` / `map[string]any`.

### `internal/modelmeta`
```go
package modelmeta

type Format string

const (
    FormatSafetensors Format = "safetensors"
    FormatPyTorch     Format = "pytorch"
)

type Tensor struct {
    Name string `json:"name"`
    // DType is a framework-independent name (e.g. "float32", "bfloat16").
    DType string  `json:"dtype"`
    Shape []int64 `json:"shape"`
    // NumParameters is the product of Shape (1 for a scalar tensor).
    NumParameters int64 `json:"num_parameters"`
    // SizeBytes is NumParameters * the dtype's width. 0 if the width is unknown.
    SizeBytes int64 `json:"size_bytes"`
}

type DTypeStat struct {
    DType         string `json:"dtype"`
    NumTensors    int    `json:"num_tensors"`
    NumParameters int64  `json:"num_parameters"`
    SizeBytes     int64  `json:"size_bytes"`
}

type Info struct {
    Format Format `json:"format"`
    // NumTensors, NumParameters, TensorBytes cover the whole file even when Tensors is truncated.
    NumTensors    int         `json:"num_tensors"`
    NumParameters int64       `json:"num_parameters"`
    TensorBytes   int64       `json:"tensor_bytes"`
    DTypes        []DTypeStat `json:"dtypes"`
    // Metadata is the file's own metadata (safetensors' __metadata__ /
    // PyTorch's scalar values).
    Metadata map[string]string `json:"metadata"`
    // HeaderBytes is the size of the header read (safetensors' JSON header, or the pickled data.pkl).
    HeaderBytes int64    `json:"header_bytes"`
    Tensors     []Tensor `json:"tensors"`
    // Truncated indicates that Tensors holds only the first maxTensors(=4096) entries.
    Truncated bool `json:"truncated"`
    // Warnings covers recoverable issues (e.g. only part of the structure could be read).
    Warnings []string `json:"warnings"`
}

// Fetcher returns the bytes of [off, off+n) from the file under inspection.
// It may return fewer than n bytes only at EOF.
type Fetcher func(ctx context.Context, off, n int64) ([]byte, error)

// FormatFor determines the format from the path's extension. Returns "" for an unrecognized extension.
func FormatFor(filePath string) Format

// Inspect reads a checkpoint's metadata for the given format.
// size is the file's actual size (the LFS object's size, not the pointer's size).
func Inspect(ctx context.Context, format Format, size int64, fetch Fetcher) (*Info, error)

// Cache memory-caches Inspect's result, keyed by a content-addressed key (LFS OID / git blob hash).
// Concurrent requests for the same key share a single parse (singleflight). Safe for concurrent use.
func NewCache(max int) *Cache
func (c *Cache) Inspect(ctx context.Context, key string, format Format, size int64, fetch Fetcher) (*Info, error)
```

---

## 12. Lineage

A mechanism where "which dataset and run this model was produced from" is written into the
repository card (the YAML front matter of README.md), and the sync worker indexes it so it can be
looked up bidirectionally.

### Card convention

```yaml
---
license: apache-2.0
base_model_relation: quantized   # HF-compatible. Inferred from the repository's contents when omitted
lineage:
  datasets:
    - team/imdb-ja@a1b2c3d      # ns/name[@rev]. rev is a branch, tag, or commit SHA
    - team/wiki-ja
  base_model: team/bert-base@main
  eval_datasets:
    - team/jglue@v1             # The dataset used for evaluation (distinct from the training source)
  run: team/trackio-metrics/sentiment/run-42   # ns/repo/project/run
  new_version: team/bert-ja-v2  # This repository's successor
---
```

- Five edge kinds: `datasets` (training-source datasets) / `base_model` (base model) /
  `eval_datasets` (datasets used for evaluation) / `run` (the training run) / `new_version` (the
  successor version)
- Each key accepts **both singular and plural forms** (`dataset`/`datasets`,
  `base_model`/`base_models`, `eval_dataset`/`eval_datasets`, `run`/`runs`,
  `new_version`/`new_versions`). The value may be a single string or a list.
- When `lineage:` doesn't have that key, it falls back to the HF-card-compatible top-level field
  (so a card written for the Hub works unmodified). If `lineage:` does specify it, that wins.

  | Top-level field | Falls back to edge | Notes |
  |---|---|---|
  | `datasets:` | `datasets` edge | |
  | `source_datasets:` | `datasets` edge | **Only for a dataset repository.** See below |
  | `base_model:` / `base_models:` | `base_model` edge | |
  | `model-index:` / `eval-results:` | `eval_dataset` edge | See below |
  | `new_version:` | `new_version` edge | |

- What a reference resolves to depends on the edge kind. `datasets` / `eval_datasets` / `run` point
  at a **dataset** repository, while `base_model` points at a **model** repository (experiment logs
  live in a dataset repository — see §7). Only `new_version` points at **the same kind as the
  declaring repository** (a model's successor is a model, a dataset's successor is a dataset).
- Up to 64 references per list. Duplicates and empty strings are dropped.
- **A reference that can't be resolved is still kept as a raw string** (dangling). A nonexistent
  repository and one that exists but has never been pushed are treated the same way — the UI shows
  text plus a note instead of a link.

The index is updated only on a push to the default branch. An edge removed from the card is also
removed from the index on that push (wholesale replacement).

### Relation to the base model (`base_model_relation`)

A `base_model` edge carries a single value describing its relationship to that base model. The
same vocabulary as HF Hub's `base_model_relation`:

| Value | Meaning |
|---|---|
| `finetune` | Trained further from the base model's weights (the default) |
| `adapter` | A LoRA / PEFT adapter. Doesn't run on its own |
| `quantized` | A lower-precision version of the same model (GGUF / AWQ / GPTQ / bitsandbytes, etc.) |
| `merge` | A merge of two or more base models |

- An explicit value is set via the top-level **`base_model_relation:`** (HF-compatible, so a card
  written for the Hub works as-is). `relation:` / `base_model_relation:` inside the `lineage:`
  block are also read, and when both are present the block-level one wins (same precedence as
  `base_model:`).
- Case is ignored and the value is normalized to one of the four (`Quantized` → `quantized`).
- **A value outside the four is kept as a raw string** (same policy as a dangling reference). The
  UI puts it in an "other" group and attaches the written value as-is, truncated to 64 characters.
- Only the `base_model` edge carries a relation kind. The `dataset` / `run` edges are always `""`.
  If the card doesn't declare any `base_model`, it's `""`.

For a card that doesn't specify it, the sync worker **infers it from the repository's contents**.
The determination only looks at file paths and the card's tags — it never reads a checkpoint's
header or its blob (`backend/internal/repocard.InferBaseModelRelation`, a pure function with unit
tests). Rules are checked top to bottom, and the first match wins:

1. Two or more `base_model` entries → `merge`
2. An `adapter_config.json` exists anywhere in the tree → `adapter`
3. Signs of quantization exist → `quantized`
   - A `.gguf` / `.ggml` file
   - `quantize_config.json` / `quant_config.json` / `quantization_config.json`
   - A quantization token in a filename or card tag (matched as an alphanumeric token:
     `gguf` `awq` `gptq` `exl2` `int4` `int8` `nf4` `4bit` `8-bit` `q4`, etc.)
   - The card has `quantized_by:`
4. Otherwise → `finetune`

### A dataset card's `source_datasets`

An HF dataset card declares its derivation source via the top-level `source_datasets:`. This is
read as a fallback for the `datasets` edge **only for a dataset repository** (ignored for a model
repository — on the model side, `base_model:` plays the same role).

- Precedence is as before: `lineage.datasets` > top-level `datasets` > `source_datasets`.
- HF's vocabulary mixes classification words (`original` / `extended` / `crowdsourced`, ...) with
  Hub IDs in the same list. **Only values readable as a reference are taken**: after stripping an
  `extended|` prefix, only a value containing a slash (= a namespaced reference) becomes an edge.
  `original`, and a bare canonical ID with no namespace (`squad`), are dropped (this server can
  never resolve them anyway).

### Evaluation datasets (`model-index` / `eval-results`)

The `datasets` edge means only "used for training". A dataset that was **used solely for
evaluation** is recorded as the separate `eval_dataset` kind. Only the reference is read — metric
values are out of scope here (that's the responsibility of experiment tracking; §7).

Two formats are recognized:

```yaml
model-index:                    # HF standard
  - name: bert-ja
    results:
      - task: { type: text-classification }
        dataset: { type: team/imdb-ja, name: IMDb }   # type is the Hub ID
        metrics: [{ type: accuracy, value: 0.93 }]

eval-results:                   # huggingface_hub's EvalResult laid out directly
  - task_type: text-classification
    dataset_type: team/imdb-ja
    metric_type: accuracy
```

- `model-index` takes `results[].dataset.type`, falling back to `.name` if absent.
- `eval-results` / `eval_results` tries `dataset_type` → `dataset` → `dataset_name`, in that order.
- If `lineage.eval_datasets` is present, that wins.
- The UI shows this under **a separate heading** from the training source (`datasets`) — upstream
  "evaluation data" / downstream "evaluated with this".

### Successor version (`new_version`)

The successor relationship when a new version is cut into a separate repository, e.g.
`team/foo-v1` → `team/foo-v2`. Compatible with HF Hub's top-level `new_version:`; `lineage.new_version:`
is also read (when both are present, `lineage:` wins — the same existing precedence).

- **Only one per repository.** Even if the card writes a list with several entries, only the first
  in the list is taken (to avoid introducing branches into chain resolution).
- Can be declared on **either** a model or a dataset. The reference resolves to the same kind as
  the declaring repository.
- Not included in the upstream edges (`upstream`) — it's returned as the `new_version` field
  instead, since chronologically it points "forward", not "back".
- **The chain is walked to its final destination and returned** (same as HF). For v1 → v2 → v3,
  v3 shows up on v1's page.
- The depth cap is **8 hops** (`store.MaxNewVersionChainDepth`). If the cap is exceeded, or a cycle
  is detected, `truncated: true` is set and only **the direct successor** is returned (`hops: 1`,
  `latest == direct`). The UI notes this and doesn't say "latest version".
- A self-reference (declaring itself as its own successor) is treated the same as no declaration:
  `new_version: null`.
- If the direct successor can't be resolved (doesn't exist / not pushed yet / no view permission),
  `hops: 0` and `direct.exists == false`. The UI shows text rather than a link (the same existing
  dangling policy).
- The resolution logic is the pure function `store.ResolveNewVersionChain` (DB access happens via a
  callback); cycles, self-reference, and the depth cap all have unit tests.

The reverse direction ("this is {old version}'s successor") appears in `downstream` as a
`new_version` edge. The reverse lookup is **restricted to the same kind** (a model and a dataset
with the same name never appear as each other's successor).

The UI shows an alert banner at the top of the repository page
(`frontend/components/repo/new-version-banner.tsx`).

### Types

```ts
type LineageEdgeKind = "dataset" | "base_model" | "eval_dataset" | "run" | "new_version"

// The known four values. When a card writes something else, the raw string is kept as-is.
type LineageRelation = "finetune" | "adapter" | "quantized" | "merge"

type LineageRef = {          // Upstream (a reference declared by this repository's card)
  kind: LineageEdgeKind
  raw: string                // The exact string as written in the card. Only this carries meaning when dangling
  target_kind: "dataset" | "model"
  namespace: string          // The normalized target namespace. "" if it couldn't be parsed
  name: string
  full_name: string          // "ns/name", "" if it couldn't be parsed
  rev: string                // The revision given via "@rev". "" if none
  project: string            // run edge only
  run: string                // run edge only
  relation: string           // base_model edge only. A LineageRelation or a raw string. "" otherwise
  exists: boolean            // Whether it actually exists to this viewer. If false, don't render as a link
}

type LineageDependent = {    // Downstream (the side pointing at this repository)
  repo: RepoSummary
  kind: LineageEdgeKind
  raw: string
  rev: string
  project: string
  run: string
  relation: string           // base_model edge only. The grouping key for the Model Tree
}

type LineageSuccessor = {    // Successor version (the resolved result of the new_version edge)
  direct: LineageRef         // The successor the card directly specified. Only this carries meaning when dangling
  latest: LineageRef         // The chain's final destination. Same as direct when truncated
  hops: number               // Number of edges walked. 1 = direct successor, 0 = couldn't be resolved
  truncated: boolean         // Cut off by a cycle or the depth cap. The UI doesn't say "latest version"
}
```

### `GET /api/v1/repos/{kind}/{ns}/{name}/lineage`
res 200: `{"upstream": LineageRef[], "downstream": LineageDependent[], "new_version": LineageSuccessor | null, "produced_by": ExpRunProducer[]}`

- `upstream` is in the order written in the card (kind → written order). The `new_version` edge
  doesn't go in here — it's returned in the `new_version` field.
- `downstream` is the reverse lookup. For a dataset repository, the `dataset` / `eval_dataset` /
  `run` edges are the target; for a model repository, the `base_model` edge is the target. Both
  also include the reverse lookup of `new_version` (older versions that declare this repository as
  their successor). Newest-updated first, at most 100 entries.
- `downstream` is grouped by the UI (equivalent to HF's Model Tree; `groupDependents` in
  `frontend/lib/lineage.ts`). The order is `new_version` (older versions) → `finetune` → `adapter`
  → `quantized` → `merge` → other → `eval_dataset` → no kind. A `base_model` edge with an empty
  `relation` (a row indexed before this feature existed) is treated as `finetune`.
- The graph is the same for every viewer. There is no repository visibility to filter on (§11 of
  the design doc), so `exists: false` means the target genuinely is not there, never that the
  viewer lacks permission.
- `produced_by` only has content **for a model repository** (`[]` otherwise). It's the list of runs
  that pointed at this repository via `trackio.log_model` — a reverse lookup of the run-side record
  (§7), not `repo_lineage`. Newest-updated first, at most 100 entries.

```ts
type ExpRunProducer = {
  repo: RepoSummary   // The *experiment dataset* repository the run lives in
  project: string
  run: string
  revision: string    // The revision of this model that the run recorded. "" if none
}
```

This is kept separate from the `run` edge in `upstream` (which the model's card declares via
`lineage.run:`). Even though both describe a "run and model" relationship, who declared it
differs — the model side or the run side — and the latter doesn't disappear just because the card
gets rewritten on a push.

### `GET /api/v1/experiments/{ns}/{repo}/{project}/lineage`
query: `run` (all runs in the project when omitted)
res 200: `{"items": [{"run": string, "models": LineageDependent[]}]}`

When `run` is given explicitly, an entry for that run is always returned even if it produced
nothing (with an empty `models`).

---

## 13. Storage operations (CLI subcommands)

In addition to `serve` / `migrate` / `seed`, `backend/cmd/thinkingface` has the following
operational subcommands (like the existing three, each runs `db.Migrate` first before branching).

### `thinkingface gc`
For each of the two content-addressed store layers on GCS (`lfs/` `blobs/`), detects and deletes
objects that no repository references anymore.

- **LFS side**: detects `lfs_objects` referenced by no repository via `repo_lfs_objects`, and
  deletes both the GCS `lfs/{oid...}` object and the `lfs_objects` row.
- **blobs side**: detects a sha referenced by no repository's ref via `repo_files.blob_sha`, and
  deletes the GCS `blobs/{sha...}` object (there's no DB table with a corresponding row, so this
  side only does the GCS deletion). Objects updated within the last 24 hours are excluded (a grace
  period to avoid racing with a push whose `repo_files` commit hasn't landed yet; see
  `docs/dev/content-addressed-storage-design.md` §5 for details).
- `--dry-run` (default `true`): only shows the oids/shas targeted for deletion and their total
  size.
- `--yes`: allows the actual deletion. Passing `--dry-run=false` alone is treated the same way.
- The scan is a snapshot. To guard against a push / LFS verify adding to `repo_lfs_objects` between
  the scan and the deletion, the actual deletion goes through
  `store.DeleteOrphanedLFSObject`, which does a `FOR UPDATE` on the `lfs_objects` row and proceeds
  only after confirming there's still no reference at that point.
- Deletion order: while holding the lock, the GCS object is deleted first, and only for an oid that
  succeeded is the `lfs_objects` row then deleted. An oid whose GCS-side deletion failed keeps its
  row (since it's still unreferenced, the next `gc` run picks it up again). An oid whose reference
  came back right before deletion is skipped, not treated as an error.
- The upload batch locks the same row via `RecordLFSObject` after confirming existence in GCS. If
  GC grabs that lock first and deletes the bytes, `RecordLFSObject` re-checks storage under the
  lock, and if the bytes are gone, rolls back and returns `ErrLFSObjectGone`. The batch then treats
  it as nonexistent and issues an upload action (without this re-check, only the DB row would come
  back while the client never re-uploads, permanently losing the bytes).
- The core reference-detection logic is `store.OrphanedLFSObjects` (a pure function, no DB
  required, with unit tests).

### `thinkingface resync`
The consistency check `docs/dev/thinkingface-design.md` §17 promises against the risk that a sync
failure lets git and the index drift apart. It is the inverse of `gc`: `gc` starts from the bucket
and asks what nothing references any more, `resync` starts from the database and asks whether what
it promises is actually there. Three cross-checks, run per repository and per branch:

1. **`repo_files` ⇄ `blobs/`** — every indexed non-LFS row must have its `blobs/{sha…}` object.
   The sync worker publishes a revision's blobs *before* it writes that revision's rows, so a row
   whose object is absent means the publish was undone or never happened.
2. **`repo_files` (LFS rows) ⇄ `lfs/`** — every indexed LFS row must have its `lfs/{oid…}` object.
   `repo_lfs_objects` is the reference table `gc` counts with; what this pass verifies is the file
   the user would download.
3. **git tree ⇄ `repo_files`** — the ref's real tree, converted exactly the way
   `syncer.runPushPipeline` converts it, against the indexed listing. A non-empty difference
   (`missing` / `extra` / `changed` paths) is the shape a failed sync job leaves behind: the ref
   moved and the index froze at the previous push.

- `--dry-run` (default `true`): report only. `--yes`: allow repairs. Passing `--dry-run=false`
  alone is treated the same way, exactly as in `gc`.
- `--repo {ns}/{name}` or `--repo {kind}/{ns}/{name}` restricts the run to one repository (the kind
  is accepted in the singular the database stores and the plural the URLs use). Without it, every
  repository is checked. A selector matching nothing is an error, not an empty run.
- **Repairs are only ever regeneration from something that still exists.** A missing blob is
  re-published from the git object database through `gitrepo.PublishBlob` — the same single write
  path of the `blobs/` layer the sync worker uses. Re-enqueuing the job would *not* fix it:
  `Syncer.publishBlobs` skips every sha the ref's index already covers, which is exactly the broken
  set. A drifted index is repaired by enqueuing an ordinary sync job (`store.EnqueueSync`, with an
  empty `old_sha` so the whole tree counts as changed) rather than by writing rows from the CLI —
  the worker is the pipeline that also re-reads the repo card and re-indexes parquet, lineage and
  experiments.
- **A missing LFS object is never repaired, in any mode.** Its bytes only ever lived in the bucket
  (git holds a pointer), so nothing on the instance can regenerate them; the command names the
  repository, ref, path and oid and leaves the file for a human to re-upload.
- Both storage layers are listed once, before any repository is read, and reduced to a set keyed by
  the content identifier (`storage.BlobKey` / `storage.LFSKey` shard by identifier and end in it).
  That ordering can only produce a false *report* — of an object published while the scan ran —
  never a false repair: re-publishing an object that is already there is a no-op, and staleness is
  decided against git, not against the listings.
- Output is one line per finding plus progress every 25 repositories, closing with a summary
  (repositories, refs, missing blobs, missing LFS objects, stale ref indexes, and what was
  repaired). **The exit status is the verdict**, as with `wal-verify`: non-zero when findings
  remain unrepaired, so the command can be scheduled.
- The decision logic is pure and unit tested (`parseRepoSelector`, `storedSet`, `missingObjects`,
  `diffIndex`); the run itself does no git state changes at all — it only reads trees and writes
  content-addressed objects that are byte-identical to what is already promised.
