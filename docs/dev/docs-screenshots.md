# Regenerating the documentation screenshots

The images embedded in `docs/users/` live in `docs/users/images/` and are captured
mechanically, not by hand. Two things make that necessary:

- They must show **English UI and English sample content**, whatever the locale of the
  machine taking them. The Japanese pages (`*.ja.md`) embed the *same* files — only the alt
  text is translated — so there is no Japanese set to regenerate.
- They must show **only curated demo content**. A developer's compose stack accumulates
  E2E leftovers (`e2e-3e7e83067f`, `uiaudit-model-1`, …), and those must never reach a
  published page.

So the screenshots come from a throwaway instance with its own database, its own bucket
prefix and nothing in it but the content `scripts/docs-demo/seed.py` creates.

## Why a separate GCS emulator

The compose emulator is started with `-public-host=gcs:4443`. That hostname is the one it
embeds in the resumable-upload session URLs it hands back, and a host-side API cannot
resolve `gcs` — so LFS uploads (every `.safetensors` file) fail with
`dial tcp: lookup gcs: no such host`. `scripts/gcs-host-proxy.py` cannot rescue this: the
storage client follows the returned session URL directly, bypassing the proxy.

The fix is an emulator of its own whose public host is reachable from the host.

## Procedure

```bash
docker run -d --name tf-docs-gcs -p 4499:4443 \
  fsouza/fake-gcs-server:1.55.1@sha256:91afded49de804aa61b5f3eb6c7cd65205acf9e5c5e047cf0ba7d9507af806c8 \
  -scheme=http -public-host=localhost:4499 -port=4443 -filesystem-root=/data
curl -X POST 'http://localhost:4499/storage/v1/b?project=test' \
  -H 'Content-Type: application/json' -d '{"name":"thinkingface"}'
```

The image digest is the same one `docker-compose.yml` pins for its `gcs`
service -- keep the two in sync when either is bumped (Renovate bumps the
compose side; see `docs/dev/supply-chain.md`).

Then the API and the web server, each on a port of its own:

```bash
API_DEV_PORT=8091 GCS_PROXY_PORT=4499 DEV_DIR=.dev/docs-demo make dev-api
```

```bash
make dev-web WEB_DEV_PORT=3120 NEXT_PUBLIC_API_URL=http://localhost:8091 API_URL=http://localhost:8091
```

!!! note

    Pass the two URLs as **make command-line variables**, exactly as above. The repository
    root `.env` is loaded into the environment ahead of the recipe and would otherwise put
    `NEXT_PUBLIC_API_URL` back to the compose API on `:8080`, leaving the browser half of
    the app pointed at the wrong instance (server-rendered pages would still be correct,
    which makes this fail confusingly rather than loudly).

Seed, then capture:

```bash
uv run --isolated --with huggingface_hub==1.28.0 --with pandas==2.3.3 --with pyarrow==25.0.1 --with requests==2.34.2 --with ./clients/python scripts/docs-demo/seed.py
```

```bash
uv run --isolated --with playwright==1.55.0 --with pillow==11.3.0 scripts/docs-demo/shots.py
```

The first capture needs `playwright install chromium` once. Pass image names to redo only
some of them (`… shots.py home models-list`).

Tear down with `docker rm -f tf-docs-gcs` and `rm -rf .dev/docs-demo`.

## What the capture guarantees

`shots.py` fixes everything that would otherwise drift between runs: a 1440×900 viewport at
2× scale, the light colour scheme, `tf_locale=en`, a logged-in session (it fails loudly
rather than shooting a logged-out UI), the Next.js dev indicator hidden, and a final
downscale to 1600px wide with uniform bottom whitespace trimmed off.

## Adding an image

1. Add it to `SHOTS` in `shots.py` — name, path, and how far to scroll first.
2. Embed it from the page that needs it, with real alt text.
3. Re-run the capture. `mkdocs build --strict` fails on a reference to an image that is not
   there, so a missing capture cannot ship.
