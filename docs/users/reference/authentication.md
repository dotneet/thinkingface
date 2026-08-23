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

## Access tokens

An access token (`tf_xxxxxxxxxxxx`) is what every non-browser client uses: `huggingface_hub`,
`git`, the `tf` CLI, and direct API calls. Issue one at **Settings → Access tokens**
(`/settings/tokens`), signed in to the web UI:

![The access token page, showing the create-token form and a list of existing tokens](../images/settings-tokens.png)

Give the token a name and a scope, then create it:

| Scope | Meaning |
|---|---|
| `read` | Can read repositories you have access to; cannot push, create, delete, or manage tokens/SSH keys |
| `write` | Everything `read` can do, plus pushing commits, creating and deleting repositories, and managing your own tokens and SSH keys |

The token's value is displayed exactly once, immediately after creation — copy it before
navigating away, since the server only ever stores its hash and cannot show it to you again.
If you lose it, delete the token and create a new one.

The token list shows each token's name, scope, creation date, and last-used date, and lets
you delete a token (with a confirmation step) at any time — deleting it takes effect
immediately.

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
