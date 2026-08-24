# Authentication

This page covers every way to prove who you are to a thinkingface instance: signing in to
the web UI, issuing and using access tokens, registering an SSH key, and the security
controls around all of it. It's aimed at anyone using an existing instance; for the
environment variables that configure these behaviors on the server side, see
[Configuration](../self-hosting/configuration.md).

## Signing in to the web UI

The web UI's login page (`/login`) has **Log in** and **Sign up** tabs. Logging in
successfully sets a `tf_session` cookie: signed, `HttpOnly`, `SameSite=Lax`, and marked
`Secure` whenever the instance is served over HTTPS. The cookie carries no readable
information itself — the server verifies its signature and checks it against the account's
current session state on every request, so signing out or changing your password
invalidates it immediately even though the cookie value itself hasn't expired yet.

By default the cookie is valid for 7 days after issue (configurable by the instance operator
via `TF_SESSION_TTL`). Signing out clears it; the browser session then falls back to
anonymous access, which still works for any repository that's public.

### Self-service signup

Whether the **Sign up** tab actually creates an account depends on the instance: operators
can disable self-service signup (`TF_ALLOW_SIGNUP=false`), leaving only accounts an admin
creates directly. When signup is open, a new account needs a username, an email address, and
a password of at least 8 characters (and, because passwords are hashed with bcrypt, at most
72 bytes).

Every instance also seeds one admin account on first boot, from `TF_ADMIN_USERNAME` /
`TF_ADMIN_PASSWORD` / `TF_ADMIN_EMAIL` (defaulting to `admin` / `admin` /
`admin@example.com` if the operator hasn't changed them — change these before exposing an
instance to anyone else).

## Changing your password

Change your own password at **Settings → Account** (`/settings/account`). The form asks for your
current password, the new one, and the new one again. The current password is always verified:
being signed in isn't on its own permission to replace the credential the session was minted
from. A new password must be at least 8 characters and at most 72 bytes — the same rule signup
applies.

Changing it **signs out every other browser**. The session cookie carries a revocation counter
that the change bumps, so anything still holding an old cookie is rejected on its next request;
the tab you changed it in is re-issued a fresh one and stays signed in.

Your **access tokens keep working**. A token is a separate credential with its own list and its
own delete button, and a routine password change says nothing about any of them — revoking them
all would break unattended clients (CI jobs, `git`, the `tf` CLI) for no gain. If a token itself
leaked, delete that token at **Settings → Access tokens**.

## When an admin resets a password

`TF_ADMIN_PASSWORD` only ever creates the very first account, on an instance whose user table is
empty; changing it afterwards has no effect. So a forgotten password is fixed by an
administrator, not by editing an environment variable.

Accounts with site administrator rights get a **Settings → Users** entry
(`/settings/admin/users`) listing every account on the instance, with a search box over
usernames and email addresses. Each row offers:

- **Reset password** — set a new password for that account. It signs out every browser they have
  open; their access tokens are untouched, exactly as for a self-service change. There is no
  email round trip, so hand the new password over out of band and let them change it themselves
  at **Settings → Account**.
- **Make admin** / **Revoke admin** — grant or remove site administrator rights. This is how an
  instance gets a second administrator, and therefore how it stays recoverable if the first one
  loses their password.
- **Suspend** / **Restore** — turn an account off and back on. A suspended account authenticates
  on *nothing*: not the web UI, not its password over HTTP Basic, not its access tokens, not its
  registered SSH keys. This is what an offboarding needs, because a password reset on its own
  leaves the departing account's tokens and keys working for `git push`. Suspending destroys
  nothing, so restoring brings the account back as it was — minus its sessions, which stay
  revoked.
- **Revoke credentials** — delete every access token and SSH key the account holds, and sign it
  out. Unlike suspension this cannot be undone, and it does not stop the account working: it is
  for credentials you think have leaked (a lost laptop, a token in a build log) on an account
  that should keep going once new ones are issued.

**Add user** at the top of the same screen creates an account outright: a username, an email
address, a password, and optionally the administrator flag. It works **whether or not
self-service signup is open** — that is the point of it. On an instance running
`TF_ALLOW_SIGNUP=false` this is the only way anyone new gets an account, so closing signup is
not a one-way door. The username becomes that account's namespace (`dana/*`) and can never be
changed, so it goes through the same rules signup applies, reserved names included. As with a
reset, there is no invitation email: pass the password on out of band and have them change it
at **Settings → Account**.

Two rules stop an instance from locking itself out: you can't revoke your own administrator
access or suspend your own account (ask another administrator), and the last administrator who
is still active can't be demoted or suspended at all. A suspended administrator does not count
towards that last one — an account that cannot sign in is no more able to recover the instance
than a missing one. The restriction is enforced by the server for every one of these actions,
not merely hidden by the UI.

!!! note "Site administration is browser-only"
    Every `/api/v1/admin` endpoint accepts the session cookie and nothing else. An access token
    or HTTP Basic credentials are refused with 403 `session_required`, even when the account
    behind them is an administrator. A single leaked write-scoped token used to be enough to
    create accounts, reset anyone's password, and register an SSH key that outlives the token's
    revocation; requiring a browser session keeps automation out of that blast radius. Use the
    screens under **Settings**.

## Failed background jobs

Administrators also get **Settings → Sync jobs** (`/settings/admin/sync-jobs`). Every push
schedules background work that rebuilds the repository's file listing, search entry and object
storage export. When that work fails repeatedly it parks, and the repository stays indexed at
its previous push — the files you see are stale even though the push succeeded. This screen
lists the parked jobs with the error from their last attempt, and **Retry** puts one back in
the queue with a fresh budget. An empty list is the healthy state.

## Access tokens

An access token (`tf_xxxxxxxxxxxx`) is what every non-browser client uses: `huggingface_hub`,
`git`, the `tf` CLI, and direct API calls. Issue one at **Settings → Access tokens**
(`/settings/tokens`), signed in to the web UI:

![The access token page, showing the create-token form and a list of existing tokens](../images/settings-tokens.png)

Give the token a name, a scope, and an expiration, then create it:

| Scope | Meaning |
|---|---|
| `read` | Can read repositories you have access to; cannot push, create, delete, or manage tokens/SSH keys |
| `write` | Everything `read` can do, plus pushing commits, creating and deleting repositories, and managing your own tokens and SSH keys |

Expiration is one of **No expiration**, 7, 30, 60, 90, or 365 days (365 is the maximum). Once a
token's expiration date passes it stops authenticating requests — the same as if it had been
deleted — though it still appears in the token list, badged as expired, so you can tell why it
stopped working and remove it.

The token's value is displayed exactly once, immediately after creation — copy it before
navigating away, since the server only ever stores its hash and cannot show it to you again.
If you lose it, delete the token and create a new one.

The token list shows each token's name, scope, creation date, last-used date, and expiration
(or **Never** for one with no expiration), and lets you delete a token (with a confirmation
step) at any time — deleting it takes effect immediately, the same as letting it expire.

!!! note "Minting tokens needs a write-scoped credential"
    Creating or deleting a token or an SSH key always requires write scope, even though
    reading the list only requires being signed in. A read-scoped token cannot use itself to
    mint a more powerful one.

## Using a token

**With `huggingface_hub` / `datasets`**: point `HF_ENDPOINT` at your instance and set
`HF_TOKEN` to the token value (also set `HF_HUB_DISABLE_XET=1` — see
[Compatibility](compatibility.md) for why):

```bash
export HF_ENDPOINT=http://localhost:8080
export HF_TOKEN=tf_xxxxxxxxxxxx
export HF_HUB_DISABLE_XET=1
```

**With git over HTTP**: use the token as the password in Basic auth; the username is
ignored, so anything non-empty works:

```bash
git clone http://admin:tf_xxxxxxxxxxxx@localhost:8080/datasets/admin/imdb-reviews.git
```

**With the `tf` CLI**: run `tf login` interactively, pass `--token`, or set
`THINKINGFACE_API_KEY` in the environment. See [tf CLI](tf-cli.md#credential-resolution-order)
for the full precedence order.

**With the raw API**: send it as a bearer token:

```bash
curl -H "Authorization: Bearer tf_xxxxxxxxxxxx" http://localhost:8080/api/whoami-v2
```

Git over HTTP also accepts this same header form; the Basic-auth form above is only needed
where a tool insists on a username/password prompt.

## SSH keys

If the instance operator has enabled git-over-SSH, you can register a public key at
**Settings → SSH keys** (`/settings/ssh-keys`) and clone or push over SSH instead of HTTP,
without a token in the URL:

```bash
git clone ssh://git@localhost:2222/admin/imdb-reviews.git
```

Paste the full line from your `.pub` file (for example `ssh-ed25519 AAAA... you@example.com`).
Accepted key types are Ed25519, ECDSA, and RSA (2048 bits or larger); DSA keys are rejected
with an explanation. Registering a key requires a write-scoped credential, the same as
minting a token. Each fingerprint can only be registered to one account instance-wide.

## Security notes

- **Authentication gates writes, not reads.** thinkingface has no per-repository visibility
  setting, so every repository is readable — including by unauthenticated callers — by
  anyone who can reach the instance. A token controls what its holder may *write*; it is not
  what keeps a repository unread. See
  [Compatibility](compatibility.md#known-incompatibilities-and-limitations).
- **Rate limiting**: failed password attempts (both the web UI's login form and HTTP Basic
  auth, which every route accepts) are throttled per client address and, at half that rate,
  per username — so a shared address failing many times doesn't lock out one account faster
  than the account's own bucket allows. Only failures count; successful logins never consume
  the budget. A throttled request gets `429` with a `Retry-After` header. The instance
  operator controls the base rate via `TF_AUTH_RATE_LIMIT_PER_MIN` (10 per minute by
  default; `0` disables the limiter entirely).
- **Only the smart git protocol is accepted.** A dumb-HTTP git client gets a clear error
  telling it to upgrade rather than failing obscurely.
- **CORS**: state-changing requests authenticated via the session cookie are only accepted
  from an allowlisted origin (the web UI's own origin, plus `localhost:3000` over plain HTTP
  in development). A token-authenticated request isn't subject to this, since it can't be
  triggered ambiently by a browser the way a cookie can.

## See also

- [tf CLI](tf-cli.md) — credential resolution order and the `tf login`/`tf status` commands
- [Compatibility](compatibility.md) — what `HF_TOKEN`, git, and SSH each cover
- [Configuration](../self-hosting/configuration.md) — the environment variables mentioned
  above, from the operator's side
