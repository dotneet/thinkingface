# Webhooks

A webhook tells an endpoint you control that something happened on this instance: a push landed,
a repository was created or archived, a transfer needs approval, an experiment run finished. The
server sends an HTTP `POST` with a JSON body and a signature you can verify, retries on failure,
and keeps a delivery history you can inspect and replay.

## Where to configure them

| Namespace | Screen |
|---|---|
| Your own | `/settings/webhooks` (<http://localhost:3000/settings/webhooks>) |
| An organization | `/orgs/{org}/settings/webhooks` |

Both screens are the same manager: the personal one lets you pick from every namespace you
administer, the organization one is pinned to that organization.

**Webhooks are admin-only.** In an organization that means the `admin` role, not `write` — a
webhook carries a namespace secret to an external URL, which is treated as an administrative act
rather than a content change. A `write` member gets a 403 from the API and does not see the
namespace offered in the picker. In your own namespace you are the admin, so nothing changes.

Each webhook has a **scope**: leave it blank and it fires for every repository in the namespace,
or pick a single repository and it fires only for that one. It can also be created (or later
toggled) inactive, which parks it without deleting it.

## Events { #events }

There are nine events. All nine are offered in the UI and accepted by the API; anything else is
rejected as an unknown event rather than silently stored.

| Event | Fires when |
|---|---|
| `repo.push` | Post-push processing for a ref finished (blobs published, file/parquet indexes refreshed). Creating a branch through the API schedules the same work, so it also fires this — there is no separate branch-created event. Creating a *tag* does not. |
| `repo.created` | A repository was created |
| `repo.deleted` | A repository was deleted. Only namespace-wide subscriptions can receive it: a repository-scoped webhook is deleted along with its repository |
| `repo.moved` | A transfer or rename completed. Delivered to subscriptions on the **new** namespace |
| `repo.transfer_requested` | A transfer is waiting for the destination's approval. Delivered to the **destination** namespace |
| `repo.archived` | A repository was frozen read-only |
| `repo.unarchived` | An archived repository was thawed |
| `run.finished` | A tracked experiment run transitioned into `finished` |
| `run.failed` | A tracked experiment run transitioned into `failed` |

`run.finished` / `run.failed` fire on the **transition** into that status, so a run that keeps
logging after finishing — or a retried finish call — does not send a fresh delivery each time.

## What a delivery looks like

The request is a `POST` with a JSON body:

```http
POST /your-endpoint HTTP/1.1
Content-Type: application/json
User-Agent: thinkingface-webhooks/1.0
X-Thinkingface-Event: repo.push
X-Thinkingface-Delivery: 4127
X-Thinkingface-Signature: sha256=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

### Payloads { #payloads }

Each event's body is a flat JSON object. `kind` is `"dataset"` or `"model"`; `full_name` is
`"{namespace}/{name}"`.

`repo.push`:

```json
{
  "namespace": "acme",
  "repo": "sentiment-base",
  "full_name": "acme/sentiment-base",
  "kind": "model",
  "ref": "main",
  "old_sha": "0a1b2c3…",
  "new_sha": "4d5e6f7…",
  "changed_files": 3
}
```

`repo.created` and `repo.deleted`:

```json
{ "namespace": "acme", "name": "sentiment-base", "kind": "model", "full_name": "acme/sentiment-base" }
```

`repo.archived` and `repo.unarchived` — the same fields plus the resulting state, so a consumer
does not have to infer it from the event name:

```json
{
  "namespace": "acme", "name": "sentiment-base", "kind": "model",
  "full_name": "acme/sentiment-base", "archived": true
}
```

`repo.moved`:

```json
{
  "kind": "model",
  "from": { "namespace": "admin", "name": "sentiment-base" },
  "to": { "namespace": "acme", "name": "sentiment-base" },
  "full_name": "acme/sentiment-base"
}
```

`repo.transfer_requested`:

```json
{
  "transfer_id": 12,
  "kind": "model",
  "from": { "namespace": "admin", "name": "sentiment-base" },
  "to": { "namespace": "acme", "name": "sentiment-base" },
  "requested_by": "admin",
  "expires_at": "2026-01-31T09:00:00Z"
}
```

`run.finished` and `run.failed`:

```json
{
  "namespace": "acme",
  "repo": "training-metrics",
  "full_name": "acme/training-metrics",
  "project": "sentiment",
  "run": "run-2026-01-30-a",
  "status": "finished"
}
```

## Verifying the signature { #verifying-the-signature }

When you create a webhook the server generates a secret and shows it **once**, prefixed
`whsec_`. Copy it then — no later response ever returns it. If you lose it, edit the webhook and
rotate the secret, which shows a new one once and invalidates the old.

`X-Thinkingface-Signature` is `sha256=` followed by the lowercase hex HMAC-SHA256 of the **raw
request body**, keyed by the secret:

```text
X-Thinkingface-Signature: sha256=<hex(HMAC_SHA256(secret, raw_body))>
```

Two things matter for getting this right:

- **Hash the raw bytes**, before any JSON parse-and-reserialize. Re-encoding changes whitespace
  and key order, and the signature will not match.
- **Compare in constant time** (`hmac.compare_digest` in Python, `hmac.Equal` in Go, and so on),
  never with `==`.

The secret is used as the HMAC key verbatim, including its `whsec_` prefix — pass the whole
string exactly as it was shown to you.

A minimal receiver, using Flask:

```python
import hashlib
import hmac
import os

from flask import Flask, request

app = Flask(__name__)
SECRET = os.environ["THINKINGFACE_WEBHOOK_SECRET"].encode()  # the whole "whsec_..." string


@app.post("/thinkingface")
def receive():
    body = request.get_data()  # raw bytes, not request.json
    expected = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
    sent = request.headers.get("X-Thinkingface-Signature", "")
    if not hmac.compare_digest(sent, expected):
        return "bad signature", 401

    event = request.headers["X-Thinkingface-Event"]
    delivery = request.headers["X-Thinkingface-Delivery"]
    payload = request.get_json()
    app.logger.info("%s delivery=%s %s", event, delivery, payload)
    return "", 204  # any 2xx counts as success
```

## Retries and delivery guarantees { #retries }

Firing an event only writes delivery rows; a background worker pool does the actual sending, so
nothing on the server waits for your endpoint.

- **One attempt has a 10-second timeout.** Anything outside 2xx, and any transport failure,
  counts as a failed attempt.
- **Up to 5 attempts in total** (the first plus four retries), so four waits: 30s, then 1m, 2m,
  4m — each double the last, and capped at 15 minutes should the limits ever be raised. After
  the fifth failure the delivery is parked as `failed` and is not retried again on its own.
- **A disabled webhook parks its deliveries instead of failing them.** While a webhook is
  inactive its pending deliveries are left untouched — no attempts are burned — and they go out
  once you re-enable it.
- **Delivery is at-least-once, and unordered.** A worker that dies mid-delivery releases its
  claim after a couple of minutes and the delivery is retried, which can produce a duplicate of
  a request your endpoint already processed. Nothing guarantees that two events fired close
  together arrive in the order they happened, either. Make your handler idempotent and
  deduplicate on `X-Thinkingface-Delivery`.
- **Your response body is recorded.** The first 4 KB of it is stored with the delivery, along
  with the status code (null when the endpoint could not be reached at all), which is what makes
  the history below useful for debugging.

## Delivery history and redelivering { #delivery-history }

Expanding a webhook in the settings screen shows its deliveries, newest first: the event, the
payload, the status (`pending` / `success` / `failed`), how many attempts it took, when the last
attempt was, and the response the endpoint gave.

**Redeliver** re-enqueues the same event and payload as a **new** delivery. The original row is
left exactly as it was, so the history keeps showing what really happened, and the replay gets
its own delivery id — which is another reason to deduplicate on that header rather than assume
one id means one arrival.

## Which URLs are allowed { #ssrf-guard }

Only `http` and `https` URLs are accepted, and by default the server refuses to deliver to local
or private addresses. That covers `localhost`, `127.0.0.0/8`, `10/8`, `172.16/12`, `192.168/16`,
link-local ranges such as `169.254/16` (which is how cloud instance metadata is reached), the
unspecified address, and `::1`.

The check runs twice, deliberately: once when the webhook is created or edited, and again on the
actual TCP connection each delivery opens, against the address it really resolved to. The second
check is what stops a hostname that only resolves to a private address later — DNS rebinding, or
an operator's DNS changing after the webhook was created.

For local development, where the receiver legitimately lives on `localhost`, an operator can set
`TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS=true`, which disables both checks. Leave it off in production:
it lets anyone with admin on any namespace point a webhook at the instance's own internal network.

## See also

- [Organizations](organizations.md) — roles, and the admin-only settings screens
- [Tracking Experiments](experiments.md) — where `run.finished` / `run.failed` come from
- [Configuration](../self-hosting/configuration.md) — `TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS` and the
  other server-side settings
