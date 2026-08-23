# Namespace Design

The definitive design for treating usernames and organization IDs as **one and the same concept,
"namespace,"** and showing the resources owned by that namespace (user or organization) through a
single URL shape, `/{ns}`. It also covers the sign-up UX that makes explicit that "the username
can't be changed and is used as the namespace," the API for querying namespace existence, user
profiles, and consolidating reserved names into a single source of truth.

Related documents: `thinkingface-design.md` §10-12, `organization-design.md` (organization roles,
members, settings; this document **replaces** that document's §13 decision that "the organization
page is `/orgs/{name}`"), `repo-transfer-design.md` (repository transfer; an alternative path to
namespace renaming), `api-contract.md` §1.

---

## 1. Goals and non-goals

### Goals

- **Username = personal namespace, organization ID = organization namespace.** Both are
  `namespaces.name`, and neither can be changed once created. Make this explicit in the UI
  (sign-up, organization creation, settings screens)
- **`/{ns}` is a page that shows the resources owned by that namespace** (Models / Datasets /
  Experiments, and members for organizations). Users and organizations share the same URL shape
  and the same screen components, with the kind shown via a badge
- Provide an API that returns a namespace's existence, kind, profile, and counts, so that an
  "empty namespace" doesn't 404
- Give users `display_name` / `description` / `website` / `avatar_url` as well, editable from
  `/settings/profile`. **The username field is read-only, with an explanation of why it can't be
  changed**
- Consolidate the reserved-name list into a single source of truth, and mechanically check it
  against the routes directly under `app/`
- Have `huggingface_hub`'s `get_user_overview()` / `get_organization_overview()` /
  `whoami()["fullname"]` reflect the profile

### Non-goals

- **Renaming namespaces** (changing usernames / organization IDs). Rationale and alternative path
  in §5.4
- Deleting / deactivating user accounts (a future item that inherits the constraints in
  `organization-design.md` §5)
- Follow / like / star / activity feeds (HF's `numFollowers` etc. are returned fixed at 0)
- Avatar image upload (external URL only, same as organizations)
- A user directory (a `/users` listing). Organizations keep the existing `/orgs` directory
- Changing the organization admin screen's URL. `/orgs/{name}/settings/*` stays as-is (§4.2)

---

## 2. Current state and problems

| Layer | Current state | Problem |
|---|---|---|
| URL | Two systems: `/{ns}` (`app/[ns]/page.tsx`) and `/orgs/{name}`. `RepoCard` / `RepoBreadcrumb` / `RepoSidebar` branch on `namespace_kind` and send the user to different URLs | Two entry points for the same concept. The screens themselves differ too (`/{ns}` is a card grid with no search or facets, `/orgs/{name}` is `RepoListPage`) |
| Existence check | `/{ns}` fetches the models / datasets / experiments lists, and **calls `notFound()` if there are zero hits** | A freshly registered user or an empty organization 404s. There's no API to ask whether a namespace exists |
| Experiments tab | `GET /api/v1/experiments` fetches everything (capped at 100) and filters by `namespace` client-side | Anything beyond 100 items goes missing. There's no `author=` filter |
| Sign-up | `auth.usernameHint` only describes the allowed character set | It doesn't say "this can't be changed" or "`{username}/` becomes your namespace" |
| Profile | Organizations only (`display_name` etc.). `whoami-v2.fullname` is just a copy of the username | Users have no display name or bio. The `namespaces` columns already exist as of migration 0010 but are unused |
| Reserved names | Three separate places: `backend/internal/api/names.go`, `frontend/lib/validation.ts`, `frontend/lib/namespace.ts`. Even the contents don't match (`favicon.ico` / `robots.txt` / `duckdb` / `public` are frontend-only, `orgs` / `organizations` / `raw` etc. are backend-only) | Adding a new top-level route is easy to forget in one of the lists |
| Validation | The organization profile's `website` / `avatar_url` have no length or scheme validation | A `javascript:` URL could end up in `<a href>` / `<img src>` (§10) |

---

## 3. Conceptual model

```
                 namespaces (name UNIQUE, case-insensitive)
                 ├── kind = 'user'  ── owner_user_id → users (1:1; users.username == namespaces.name)
                 └── kind = 'org'   ── org_members (role) → users
                        │
                        ├──< repositories   (kind: model | dataset, is_experiment)
                        ├──< webhooks
                        ├──< org_audit_log  (org only)
                        └──  profile columns  display_name / description / website / avatar_url
```

**Invariants**

1. **A namespace name never changes after creation.** `users.username` and `namespaces.name`
   always match, and neither gets an UPDATE path (the store won't provide a method for it)
2. **Namespace names are unique case-insensitively** (`idx_namespaces_name_lower`,
   `thinkingface-design.md` §10). Display and URLs use the spelling recorded at sign-up (the
   canonical form). Lookups use `LOWER(name) = LOWER($1)`
3. **Reserved names are checked only at the two places that create a new namespace** (sign-up and
   organization creation); they don't apply to repository names
4. **The first segment of `/{ns}` is reserved exclusively for namespaces.** When a new static
   route is added directly under `app/`, it must also be added to the reserved-name list
   (mechanically checked in §9)
5. **Profile columns live on `namespaces` regardless of kind.** A user edits their own namespace
   row, not `users`

The grammar for namespace names stays the existing `validateName`
(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`, no trailing `.git`).

---

## 4. URL design

### 4.1 Public surface (viewable by anyone)

| URL | Content |
|---|---|
| `/{ns}` | Namespace page. Models tab (default) |
| `/{ns}?tab=datasets` / `?tab=experiments` | Datasets / Experiments tabs. Each tab accepts the same query params as `RepoListPage` (`q` / `tag` / `license` / `task` / `sort` / `offset`) |
| `/{ns}?tab=members` | **Organizations only.** Member list (follows `members_visibility`). User namespaces don't show the tab at all; if opened directly it falls back to Models |
| `/orgs` | Organization directory (existing). Card links now point to `/{ns}` |
| `/orgs/{name}` | **Permanently redirects to `/{name}`** (`permanentRedirect`). Kept for bookmark / external-link compatibility |

If the spelling in `/{ns}` differs from the canonical form (e.g. `/Alice` → canonical `alice`), it
`permanentRedirect`s to the canonical form (same behavior as GitHub). This gives every namespace a
single canonical URL.

This also matches the shape of HF Hub's `/{user}` and `/{org}`, so HF URLs can be dropped in as-is
by swapping the host for thinkingface's.

### 4.2 Admin surface (requires permission)

| URL | Content | Change |
|---|---|---|
| `/settings/profile` | **New.** Edit your own profile. Username is read-only | New |
| `/orgs/{name}/settings/*` | Organization settings (existing) | No change |

Why we don't use `/{ns}/settings`: on the backend, `/{ns}/{name}` is the route for git transport /
resolve, and `alice/settings` is a valid repository name (this would also clash with HF's
convention where `/{ns}/{name}` means a model repository). Admin surfaces stay at `/settings/*`
(for yourself) and `/orgs/{name}/settings/*` (for organizations); everything under `/{ns}` stays
public-only.

### 4.3 Layout of the `/{ns}` page

```
┌──────────────────────────────────────────────────────────────────────────┐
│ [avatar]  Display Name   [User | Organization]  [Admin]  (if self/admin) │
│           alice                                      [Edit profile]/[Settings]│
│           description …                                                  │
│           🌐 website   👥 12 members(org)   📦 8 models · 3 datasets     │
│                                             Joined 2026-01-12            │
├──────────────────────────────────────────────────────────────────────────┤
│ Models (8) │ Datasets (3) │ Experiments (2) │ Members (12)  ← org only    │
├──────────────────────────────────────────────────────────────────────────┤
│ RepoListPage(kind, author=ns)  … search / tags / license / sort / paging │
└──────────────────────────────────────────────────────────────────────────┘
```

- The heading is `display_name || name`. When `display_name` is present, the namespace name is
  shown below it in `font-mono text-fg-subtle` (same as the existing `OrgHeader`). **The namespace
  name is always visible** (it's the identifier that matches the URL and `repo_id`)
- A kind badge (`User` / `Organization`) is always shown. If the viewer is an organization member,
  `OrgRoleBadge` is also shown (existing)
- Top-right button: **Edit profile** (→ `/settings/profile`) for your own namespace, **Settings**
  (→ `/orgs/{name}/settings`) for an organization admin, nothing otherwise
- Empty state: for your own namespace, or one where you have write access or above, show a "create
  your first repository" CTA (`/new?ns={ns}` pre-selects the namespace; since `/new` currently
  doesn't read query params, add `initialNamespace` to `CreateRepoForm`). Otherwise show "no models
  yet"
- The 3 states (loading / empty / error) use `Skeleton` / `EmptyState` / `ErrorState`
  (`DESIGN.md`)
- `generateMetadata`: `{display_name || name} · 🤔 Thinking Face`, with `description` set to
  `description`

---

## 5. Behavioral details

### 5.1 Sign-up (making explicit that the username is the namespace)

Change the Sign up tab of the login form (`components/auth/login-form.tsx`) as follows:

1. **Label**: `Username` → `Username (your namespace)`
2. **Hint** (directly under the input, always shown):
   > Your username is also your namespace: your profile lives at `/{username}` and every repository
   > you create is named `{username}/<repo>`. **It can't be changed later** — pick a name you're happy
   > to keep. Letters, digits, dot, dash or underscore; 1–96 characters.
3. **Live preview** (updates as the user types, uses `window.location.origin`):
   ```
   Profile        https://hub.example.com/alice
   Repositories   alice/<repo-name>        git clone https://hub.example.com/alice/<repo-name>
   ```
4. **Availability check**: 400ms after typing stops, fetch `GET /api/v1/namespaces/{name}`.
   200 → "`alice` is already taken (case-insensitive)", 404 → "`alice` is available". Reserved
   names / grammar violations are judged instantly client-side (existing `validateNamespaceName`).
   Show a `Spinner` while checking. If the lookup fails, don't block submission (the server makes
   the final call)
5. **Lock notice**: directly above the submit button, with a `Lock` icon, show
   "Usernames are permanent. You can change your display name any time." **No** confirmation
   checkbox or confirmation dialog (the friction isn't worth the reduction in mistaken sign-ups;
   instead, the alternative path from §5.4 is surfaced in the settings screen)

The `signup` API response doesn't change. `User` gains `display_name` / `avatar_url` (§7.1).

### 5.2 Organization creation (making explicit that the organization ID is the namespace)

Apply the same treatment to `/orgs/new` (`components/orgs/create-org-form.tsx`):

- Label `Organization ID (namespace)`, hint "Becomes `/{org}` and `{org}/<repo>`. Can't be changed
  after creation"
- Same live preview and availability check (`GET /api/v1/namespaces/{name}`)
- `Display name` is optional, written in contrast to note that it can be changed later

### 5.3 Profile

| Field | User | Organization | Constraint (shared, §10) |
|---|---|---|---|
| `name` | `users.username`. **Immutable** | Organization ID. **Immutable** | `validateNamespaceName` |
| `display_name` | Optional | Optional | ≤ 96 characters |
| `description` | Optional (bio) | Optional | ≤ 1,024 characters |
| `website` | Optional | Optional | `http://` / `https://` only, ≤ 2,048 bytes |
| `avatar_url` | Optional | Optional | `http://` / `https://` only, ≤ 2,048 bytes |
| `members_visibility` | — | Existing | — |

- Users edit via `/settings/profile` (`PATCH /api/v1/me/profile`). Organizations use the existing
  `/orgs/{name}/settings` (`PATCH /api/v1/orgs/{org}`)
- The username field on `/settings/profile` is `disabled` on the `Input`, with a `Lock` icon and
  explanatory text: "Your username is your namespace (`alice/*`) and can't be changed. To use a
  different name, create a new account and transfer your repositories to it." (linking to
  `/settings/transfers`)
- `whoami-v2`: `fullname` = `display_name || username`, `avatarUrl` = `avatar_url`
- `whoami-v2.orgs[].fullname` / `avatarUrl` are already returned this way by the existing
  `whoamiOrgs`. Align the user object with the same rule

### 5.4 Why we don't rename namespaces, and the alternative path

We won't build renaming (for either kind). Rationale:

- Namespace names are baked directly into `repo_id` (`from_pretrained("alice/x")`), git remote
  URLs, model card `base_model: alice/x`, lineage's `target_namespace` (stored as a string),
  webhook subscription keys, and audit log `target_name`. `repo_redirects` exists for moving
  individual repositories; a namespace-level redirect table and permanent reservation of old names
  would be a separate piece of work
- HF Hub also only supports username changes through support, and doesn't release the old name.
  The operational burden of matching that for a self-hosted instance isn't worth it

Alternative path (surfaced in the UI): **create a new account / organization and transfer
repositories to it** (`repo-transfer-design.md`). Transfer leaves behind a `repo_redirects` entry,
so clone / download using the old `repo_id` keeps working. It works in either direction, org ↔
user.

### 5.5 Existence checks and case handling

- `GET /api/v1/namespaces/{ns}` looks up via `LOWER(name) = LOWER($1)`, and returns the
  **canonical spelling** in the response's `name`
- The frontend's `/[ns]` compares the response's `name` against the URL segment, and issues a
  `permanentRedirect` to the canonical form if they differ. Under the streaming behavior of
  `app/[ns]/loading.tsx`, Next.js returns the shell with a 200 first, so the redirect happens via
  the RSC payload (a client-side transition). The browser's URL ends up canonical, but note that
  `curl` will see a 200 plus a transition instruction rather than an HTTP-level 308 (if an
  HTTP-level 308 is needed later, remove `loading.tsx` or do it in middleware instead)
- Nonexistent → 404 `not_found`. Existence is public information (it's embedded in repository
  URLs), so this returns 200 / 404 even when unauthenticated
- `GET` only checks the name's **grammar** and then queries the DB (the reserved-name list is only
  a guard at creation time). An existing account that happens to collide with a reserved name
  (e.g. the default seed `admin`) still returns 200. A reserved name nobody owns just gets a normal
  404. The availability-check UI cross-references 404s against the client-side reserved-name list
  to distinguish "reserved" from "available"

### 5.6 Experiments tab

Add `author=` (namespace, case-insensitive) and `limit` / `offset` to `GET /api/v1/experiments`,
and return `total`. `/{ns}?tab=experiments` uses this. The existing call site (the `/experiments`
listing) stays compatible with no arguments.

---

## 6. Data model

**No migration needed.** This just starts using the `display_name` / `description` / `website` /
`avatar_url` / `updated_at` columns that migration 0010 already added to `namespaces`, now for user
namespaces too.

Store layer (new `internal/store/namespaces.go`, moving `GetNamespace` / `NamespacesForUser` /
`CanWriteNamespace` there from `users.go`):

```go
type NamespaceProfile struct {
    ID   int64
    Name string        // canonical form
    Kind string        // "user" | "org"
    DisplayName, Description, Website, AvatarURL string
    MembersVisibility string   // only meaningful for orgs
    CreatedAt, UpdatedAt time.Time
}
type NamespaceCounts struct{ Models, Datasets, Experiments, Members int64 }
type NamespaceUpdate struct{ DisplayName, Description, Website, AvatarURL *string }

GetNamespaceProfile(ctx, name string) (*NamespaceProfile, error)           // kind-agnostic. LOWER match
CountNamespaceResources(ctx, id int64) (NamespaceCounts, error)            // single query (FILTER / CASE aggregation)
UpdateNamespaceProfile(ctx, id int64, u NamespaceUpdate) (*NamespaceProfile, error)
```

- `store.UpdateOrg` is rewritten as a thin wrapper around `UpdateNamespaceProfile` plus
  `members_visibility` (so we don't end up with two separate UPDATE code paths both carrying
  `WHERE kind = 'org'`)
- `Experiments` is the count of `repositories.kind = 'dataset' AND is_experiment`. `Members` is
  the count from `org_members` (0 for a user)

---

## 7. API

### 7.1 For the UI (added to `apitypes` → `make gen-types`, add a "Namespace" section to
`api-contract.md` §1)

```
GET    /api/v1/namespaces/{ns}            Public. → NamespaceResponse / 404 not_found
PATCH  /api/v1/me/profile                 Auth required (session or write token). req NamespaceProfileUpdate → NamespaceResponse
GET    /api/v1/experiments?author=&limit=&offset=   Existing endpoint, with args added. → ExpProjectListResponse{items, total}
```

```go
type NamespaceProfile struct {
    Name        string        `json:"name"`          // canonical spelling
    Kind        NamespaceKind `json:"kind"`
    DisplayName string        `json:"display_name"`
    Description string        `json:"description"`
    Website     string        `json:"website"`
    AvatarURL   string        `json:"avatar_url"`
    CreatedAt   time.Time     `json:"created_at"`
    NumModels, NumDatasets, NumExperiments int64
    // NumMembers / MembersVisibility are only meaningful for organizations.
    // For a user namespace, NumMembers = 0, MembersVisibility = "".
    NumMembers        int64             `json:"num_members"`
    MembersVisibility MembersVisibility `json:"members_visibility"`
    // ViewerRole is the caller's effective role (organization-design.md §3.1).
    // "admin" for your own user namespace and for site admins. "" when logged out / unrelated.
    ViewerRole OrgRole `json:"viewer_role"`
    // CanEdit drives whether the UI shows the "Edit profile / Settings" button (ViewerRole == admin).
    CanEdit bool `json:"can_edit"`
}
type NamespaceResponse      struct{ Namespace NamespaceProfile `json:"namespace"` }
type NamespaceProfileUpdate struct{ DisplayName, Description, Website, AvatarURL *string }  // partial update
```

Changes to existing types (additive only, backward compatible):

- `User` (`/me` / `/login` / `/signup`) gains `display_name` / `avatar_url`
- `ExpProjectListResponse` gains `total`
- `OrgUpdateRequest`'s validation is routed through `validateProfileFields`, shared with
  `NamespaceProfileUpdate` (§10)

`PATCH /api/v1/me/profile` also works with token auth (for updating a profile from CI). A `read`
scoped token gets 403.

### 7.2 HF-compatible

| Endpoint | Matching client | Response |
|---|---|---|
| `GET /api/users/{username}/overview` | `HfApi.get_user_overview()` | `{"user": name, "fullname", "avatarUrl", "details": description, "type": "user", "numModels", "numDatasets", "numSpaces": 0, "numLikes": 0, "numFollowers": 0, "numFollowing": 0, "isPro": false, "orgs": [same shape as whoami]}` |
| `GET /api/organizations/{org}/overview` | `HfApi.get_organization_overview()` | `{"name", "fullname", "avatarUrl", "details", "numUsers", "numModels", "numDatasets", "numSpaces": 0, "numFollowers": 0, "isVerified": false}` |
| `GET /api/whoami-v2` | `whoami()` | `fullname` / `avatarUrl` reflect the profile (§5.3) |

`huggingface_hub`'s `User` / `Organization` accept values via `kwargs.pop(..., None)`, so missing
keys are tolerated, but we return the minimal set above regardless. `numSpaces` etc. stay fixed at
0. `get_user_overview("team")` (passing an organization name) 404s, same as HF.

---

## 8. Frontend

### 8.1 Routes and files

| Route / file | Change |
|---|---|
| `app/[ns]/page.tsx` | Fetch `getNamespace()` first → 404 / canonical redirect → pass `profile` into `NamespaceOverview` |
| `components/namespace/namespace-overview.tsx` | Replace the listing with `RepoListPage` (`author=ns`, `basePath=/{ns}`, `showHeading=false`). Experiments use `listExperiments({author})`. The Members tab (org only) reuses the existing `OrgMembersPanel` |
| `components/namespace/namespace-header.tsx` | Absorb `OrgHeader` and generalize it (kind badge, role badge, Edit profile / Settings button, counts). `OrgHeader` is deleted |
| `components/namespace/namespace-avatar.tsx` | Takes `avatarUrl` (folds in `OrgAvatar`; renders `<img>` if there's an image, an initial otherwise) |
| `components/namespace/namespace-tabs.tsx` | Members tab (org only) and counts |
| `app/orgs/[name]/page.tsx` | Reduced to just `permanentRedirect(namespaceHref(name))` |
| `app/orgs/[name]/settings/*` | No change (`OrgSettingsNav` / `OrgDangerZone` / `OrgMembersPanel`'s use of `orgHref(name)` is replaced with `namespaceHref(name)`; paths under `/settings` stay as-is) |
| `app/settings/profile/page.tsx` (new) | `ProfileSettings` client component. Username (read-only, with explanation), display name, bio, Website, Avatar URL, with preview |
| `components/settings/settings-nav.tsx` | Add "Profile" at the top. `/settings`'s default redirect target becomes `/settings/profile` |
| `components/user-menu.tsx` | Add "Your profile" (→ `/{username}`) at the top. The displayed username prefers `display_name` |
| `app/new/page.tsx` / `components/repo/create-repo-form.tsx` | Read `?ns=` and use it as the namespace select's initial value (ignored if the viewer has no permission on that namespace) |
| `components/auth/login-form.tsx` | §5.1 |
| `components/orgs/create-org-form.tsx` | §5.2 |
| `components/repo/repo-card.tsx` / `repo-breadcrumb.tsx` / `repo-sidebar.tsx` / `orgs/org-card.tsx` / `settings/organizations-manager.tsx` | Unify namespace links to `namespaceHref(ns)`. Remove the branching on `namespace_kind` (`RepoCard` keeps `ns` / `name` as separate links even for a user namespace, so clicking the username still goes to `/{ns}`) |
| `lib/namespace.ts` | `namespaceHref(ns)`, `getNamespace(ns, opts)`, `updateMyProfile(req)`, reserved names (§9). `lib/orgs.ts`'s `orgHref` is removed |
| `lib/i18n/dictionaries/{en,ja}/namespace.ts` / `auth.ts` / `settings.ts` / `org.ts` | Add copy (§8.3) |

### 8.2 Shared components

- `NamespaceKindBadge` (`Badge tone="muted"`, `User` / `Organization`). Not shown on `RepoCard`
  hover etc., only in the header
- `NamespaceAvailability` (shared by sign-up and organization creation): input value → debounce →
  `getNamespace` → 3-state display
- `NamespaceUrlPreview` (also shared): `origin + "/" + name`, `name + "/<repo>"`

### 8.3 i18n (en dictionary; ja has the same shape)

```ts
auth.usernameLabel:        "Username (your namespace)"
auth.usernameHint:         "Also your namespace: your profile is /{username} and repositories are {username}/<repo>. It can't be changed later."
auth.usernamePermanent:    "Usernames are permanent. You can change your display name any time."
auth.availability.checking / available / taken / reserved
namespace.kind.user / namespace.kind.org
namespace.tabs.members, namespace.editProfile, namespace.settings
namespace.empty.ownModels: "You haven't created any models yet" + CTA namespace.empty.createFirst
namespace.counts.models / datasets / experiments
settings.profile.title / username / usernameLocked / usernameLockedHint (copy pointing to transfers) / displayName / bio / website / avatarUrl / saved
org.create.idLabel: "Organization ID (namespace)" / org.create.idHint / org.create.idPermanent
```

### 8.4 UI conventions

`components/ui/` primitives only, semantic tokens only, 3 states, sourced from the dictionary
(`DESIGN.md`). The `Lock` icon comes from `lucide-react`. Check `/{ns}` (user / org, empty /
non-empty), `/settings/profile`, and the sign-up preview in both dark and light themes.

---

## 9. Single source of truth for reserved names

Consolidate the currently three separate lists as follows:

1. **The source of truth is `backend/internal/api/names.go`.** Make it the union of the two
   frontend lists (adding `favicon.ico` / `robots.txt` / `sitemap.xml` / `duckdb` / `public` /
   `users` / `namespaces` / `profile` / `search`). **Remove** `admin` (it's the namespace of the
   default seed user `TF_ADMIN_USERNAME=admin`, and there's no `/admin` route)
2. The frontend collapses to a single `RESERVED_NAMESPACE_NAMES` in `lib/validation.ts`;
   `lib/namespace.ts`'s `RESERVED_NAMESPACES` is removed, and `isReservedNamespace` references
   `validation.ts` instead
3. **Mechanical check** (added to `frontend/scripts/check-ui.mjs`, `bun run check:ui` → CI):
   - Every static segment directly under `app/` (directories that don't start with `[`) is
     contained in `RESERVED_NAMESPACE_NAMES`
   - `RESERVED_NAMESPACE_NAMES` matches `names.go`'s `reservedNamespaceNames` (read the Go file
     with a regex, same "zero diff" approach as `check-types`)
4. Existing accounts that collide with a newly reserved name aren't deleted (same as
   `organization-design.md` §6.3). That namespace's `/{ns}` becomes unreachable, losing out to the
   static route, but repository URLs (`/models/{ns}/{name}`) and the API are unaffected. Log the
   count as a `WARN` at startup

---

## 10. Security

- **URL field scheme validation**: `website` / `avatar_url` reject anything other than `http://` /
  `https://` with a 400 (`validateProfileFields`; also applied to the existing
  `PATCH /orgs/{org}` — currently unvalidated, letting `javascript:` through). Rendering also
  keeps `<a rel="nofollow noopener noreferrer" target="_blank">` and
  `<img referrerpolicy="no-referrer">`
- **Length limits**: `display_name` 96 characters, `description` 1,024 characters, URLs 2,048
  bytes. `description` is rendered as plain text (no Markdown rendering)
- **Disclosure of existence**: a namespace's existence is public information. The availability
  check's `GET /api/v1/namespaces/{ns}` is allowed unauthenticated, and is excluded from the
  existing auth rate limit (`TF_AUTH_RATE_LIMIT_PER_MIN`, per-IP) — there's nothing to gain from
  enumerating it
- **Authorization for profile updates**: `PATCH /me/profile` is self-only. There's no path for a
  site admin to edit someone else's profile (moderation is a future item). Organizations keep the
  existing admin-role requirement

---

## 11. Existing data and backward compatibility

- No schema change. This just starts using unused columns already on `namespaces`
- `/orgs/{name}` stays as a permanent redirect. `/orgs/{name}/settings/*` is unchanged
- All API changes are additive (two new fields on `User`, new args and `total` on `experiments`,
  new endpoints)
- This document replaces `organization-design.md` §13's statement that "the organization page is
  `/orgs/{name}`, and users will eventually get `/users/{name}`"
  (the `/[ns]` route already exists, and the cost of maintaining reserved names is already being
  paid)
- `whoami-v2.fullname` now returns `display_name`. This only changes what `hf auth whoami`
  displays; it doesn't affect `huggingface_hub` authentication or uploads

---

## 12. Test plan

- `store` (SQLite always, plus `make test-store-pg`): `GetNamespaceProfile` for both kinds and
  case-insensitivity, `CountNamespaceResources` (models / datasets / experiments / members, all
  zero for an empty namespace), `UpdateNamespaceProfile` (user / org)
- `api` (`httptest`): `GET /namespaces/{ns}` — a freshly registered user returns 200 with zero
  counts, nonexistent → 404, reserved name → 404, `Alice` → `name: "alice"`, `viewer_role` /
  `can_edit` (self / org admin / non-member / site admin / unauthenticated). `PATCH /me/profile` —
  partial update, `javascript:` URL → 400, over-length → 400, read token → 403.
  `GET /experiments?author=`'s case-insensitivity and `total`. Shape and 404 of
  `GET /api/users/{u}/overview` / `organizations/{o}/overview`. Reserved names on signup / org
  creation (after the union)
- `frontend` (vitest): `namespaceHref`, `isReservedNamespace` referencing `validation.ts`,
  `parseNamespaceTab` (`members` only for orgs). `check-ui`'s reserved-name sync check (app/'s
  top level ⊆ the list, Go ⇔ TS match)
- E2E (`e2e/test_namespaces.py`, `make test-e2e`): signup → `GET /api/v1/namespaces/{u}` returns
  200 with zero counts → after `create_repo`, `num_models` becomes 1 →
  after `PATCH /me/profile`, `whoami()["fullname"]` and `get_user_overview(u).fullname` reflect it
  → `get_organization_overview(org).num_users` matches the member count →
  `get_user_overview(org_name)` returns 404
- UI: in both themes, check `/{ns}` (user empty / user non-empty / org), the `/orgs/{name}`
  redirect, `/Alice` normalization, `/settings/profile`, and the sign-up preview and availability
  display

---

## 13. Implementation phases

| Phase | Content | Completion criteria |
|---|---|---|
| **1. API and validation** | `store/namespaces.go`, `GET /namespaces/{ns}`, `PATCH /me/profile`, `validateProfileFields` (applied to organizations too), `experiments?author=`, `User` extensions, union of reserved names, `apitypes` + `make gen-types`, `api-contract.md` | `make check`. §12's api tests |
| **2. Consolidate the namespace page** | Rewrite `/[ns]` starting from the profile, adopt `RepoListPage`, Members tab, `/orgs/{name}` redirect, all links moved to `namespaceHref`, consolidation and removal of `OrgHeader` / `OrgAvatar` / `orgHref`, user menu | `make check` (including `check:ui`). Both themes verified |
| **3. Sign-up / creation UX and profile settings** | Explicit copy, preview, and availability check for sign-up / org creation, `/settings/profile`, en/ja i18n | `make check`. E2E `test_namespaces.py` |
| **4. HF compatibility and docs** | `users/{u}/overview`, `organizations/{o}/overview`, `whoami`'s `fullname` / `avatarUrl`, `thinkingface-design.md` §11-12, a note in `organization-design.md` §13 pointing to the replacement, README | `make test-e2e` shows no regressions |

Phase 1 is worth merging on its own (the URL scheme validation also plugs a hole on the
organization side). Phases 2 onward stack in order.

---

## 14. Decisions, rejected options, and remaining work

Decisions:

- The namespace page is consolidated into `/{ns}`. `/orgs/{name}` redirects
- Usernames and organization IDs can't be renamed. The alternative is creating a new account /
  organization plus a repository transfer
- Profile columns live on `namespaces` for both kinds; users edit theirs via `PATCH /me/profile`
- Sign-up makes it explicit via copy + preview + availability check, without a confirmation
  checkbox
- The source of truth for reserved names is the backend; the frontend has one list; CI checks that
  they stay in sync

Rejected options:

- Keeping the two systems `/users/{name}` and `/orgs/{name}` → leaves two URL shapes for the same
  concept, with branching on the link target scattered everywhere. Also doesn't match HF's URLs
- Putting the admin surface at `/{ns}/settings` → `/{ns}/{name}` collides with the repository URL
  shape (§4.2)
- Adding profile columns to the `users` table → duplicates management with organizations;
  `namespaces` already has the columns
- A dedicated `GET /api/v1/namespaces/{name}/availability` for the availability check → the
  existence query (200 / 404) already covers it
- Namespace renaming plus an old-name redirect table → see §5.4

Remaining work (future):

- User deletion (the last-admin constraint, handling of owned repositories)
- Site-admin moderation editing of other users' profiles
- Avatar upload (carving out an `avatars/{ns}` key in `storage`)
- Showing activity (recent pushes / runs) on `/{ns}`
