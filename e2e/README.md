# e2e

`huggingface_hub` / `datasets` compatibility tests for thinkingface. These
exercise the server purely through the public HF client libraries — no
thinkingface-specific code — since the design goal is that those libraries
work unmodified against `HF_ENDPOINT=<thinkingface>` (see
`docs/dev/thinkingface-design.md` §2, §7).

## What's covered

- `test_hf_compat.py`
  - `whoami()` against a thinkingface-issued token
  - dataset repo: `create_repo` → `upload_file` (README, non-LFS) →
    `hf_hub_download` → content matches
  - dataset repo: `upload_file` a Parquet file (LFS path, since `*.parquet`
    is LFS-tracked by default) → `list_repo_files` → `hf_hub_download` →
    row count matches via pyarrow
  - `HfApi().list_repo_tree()` returns both files and directories
  - `datasets.load_dataset("parquet", data_files=...)` reads the downloaded
    file back
  - model repo: `create_repo` → upload a safetensors-named binary →
    download → bytes match
  - `delete_repo` removes the repo (`repo_info` 404s afterwards)
- `test_gcs_export.py` — after a commit, polls fake-gcs-server's JSON API
  directly (not through the thinkingface API) to confirm the pushed bytes land
  at their content-addressed keys (`blobs/{sha}` for a plain git blob,
  `lfs/{oid}` for an LFS object -- there is no per-repo path in the bucket,
  and no `exports/` mirror any more), then drives `GET
  /api/v1/repos/{kind}/{ns}/{name}/gcs/main` to check the listing and the
  generated `gcloud storage cp` script / DuckDB snippet it hands back.
- `test_model_meta.py` — `GET /api/v1/model-meta/{kind}/{ns}/{name}/{rev}/{path...}`,
  the checkpoint-header reader, driven with `requests` (it's not part of the
  `huggingface_hub`-compatible surface). Checkpoint bytes come from
  `fixtures_checkpoints.py`, built without installing `torch` (see that
  module's docstring). Covers: a safetensors file read over LFS (tensor
  count/params, file-offset ordering, dtype normalization to
  `float32`/`bfloat16`, `__metadata__` passthrough); a PyTorch `.bin`
  checkpoint read over LFS (dot-joined `state_dict.*` tensor names, dtype
  normalization including `int64`, a 0-dimensional tensor's `shape: []`,
  and `epoch`/`global_step`/`arch` in `metadata`); the same safetensors file
  read back as a plain git blob (no `*.safetensors` LFS rule) to exercise
  the non-LFS code path; a non-checkpoint extension (`README.md`) rejected
  with 400; and the UI tree endpoint (`GET
  /api/v1/repos/{kind}/{ns}/{name}/tree/{rev}`) flagging checkpoints with
  `preview: "model"` / `is_model: true`.
- `test_web_edit.py` — `PUT /api/v1/edit/{kind}/{ns}/{name}/{rev}/{path...}`,
  the web UI's in-browser file editor, driven with a cookie-authenticated
  `requests.Session` (`POST /api/v1/auth/login`) rather than
  `huggingface_hub`, since that's how the web UI itself calls it. Covers:
  editing an existing file with a matching `base_oid` (200, content visible
  via `GET /api/v1/raw/...`, repo HEAD SHA moves); a stale `base_oid`
  rejected with 409; a no-op edit (identical content) returning 200 without
  minting a new blob `oid`; editing an LFS-tracked path rejected with 400;
  creating a new file by writing to a path that doesn't exist yet, with no
  `base_oid`; targeting a commit SHA instead of a branch rejected with 400;
  an unauthenticated request rejected with 401; and the repo detail API's
  `can_write` flag being `true` for the authenticated owner and `false`
  anonymously.
- `test_orgs.py` — the organization feature
  (`docs/dev/organization-design.md`), through both the UI-only `/api/v1/orgs/...`
  API (`requests` + `Authorization: Bearer`) and `huggingface_hub` for
  everything organizations are meant to unlock. Covers: creating an
  organization makes the creator its admin and shows up in
  `whoami()["orgs"]` (`roleInOrg`); `create_repo` under an org; a freshly
  signed-up `read` member can `hf_hub_download` from an org repo but gets 403
  on `upload_file`, and promoting them to `write` unlocks the push
  (repositories carry no visibility of their own since
  `docs/dev/content-addressed-storage-design.md` §1, so what the roles gate is writing,
  not reading); `list_organization_members` returns every member; demoting or
  removing an organization's last admin is rejected with 409 `last_admin`; and
  deleting an organization is rejected with 409 `has_repositories` until its
  repositories are gone, then succeeds (204).
- `test_namespaces.py` — the namespace feature (`docs/dev/namespace-design.md`),
  which unifies "username" and "organization ID" into one concept exposed at
  `/{ns}`. Covers: a freshly signed-up user's namespace answers 200 with
  every count at 0 (not 404) and `can_edit` only for the account itself; a
  lookup with different casing (`Alice` vs. `alice`) returns the canonical
  spelling; `num_models` reflects `create_repo`; `PATCH /api/v1/me/profile`
  (partial update) shows up in `whoami()["fullname"]` and
  `get_user_overview().fullname`/`.details`, a `javascript:` `website` is
  rejected with 400, and a read-scoped token gets 403; an organization's
  namespace has `kind == "org"` and its member count matches
  `get_organization_overview().num_users`, while `get_user_overview(org_name)`
  404s; a reserved name (`models`) and a name nobody holds both 404; and
  `GET /api/v1/experiments?author=` is case-insensitive and returns `total`.

## Running

Requires a running stack:

```bash
cd /path/to/thinkingface
cp .env.example .env   # first time only
docker compose up -d
# or: make up
```

Then, from this directory:

```bash
uv run --locked pytest -v
```

[uv](https://docs.astral.sh/uv/) is required (the same as for `make lint` and
`make docs`). It builds the environment in `.venv/` here, so `huggingface_hub`
/ `datasets` / `pyarrow` never land in whatever interpreter happens to be
active. `make test-e2e` from the repo root runs exactly this.

`--locked` is the point of the command: the dependency set comes from
`uv.lock`, which pins every transitive package to an exact version and hash,
and the flag makes uv fail rather than silently re-resolve if `pyproject.toml`
and the lockfile have drifted apart. CI runs the same lockfile, and the `python`
job re-checks it with `uv lock --check`. After editing `pyproject.toml`, run
`uv lock` here (or `make lock-python` from the repo root) and commit the result.
See [docs/dev/supply-chain.md](../docs/dev/supply-chain.md).

## Configuration

Environment variables (all optional, default to the local compose setup):

| Variable | Default | Meaning |
|---|---|---|
| `TF_ENDPOINT` | `http://localhost:8080` | thinkingface API base URL |
| `TF_ADMIN_USERNAME` | `admin` | Seeded admin username to log in as |
| `TF_ADMIN_PASSWORD` | `admin` | Seeded admin password |
| `GCS_EMULATOR_URL` | `http://localhost:4443` | fake-gcs-server base URL |
| `GCS_BUCKET` | `thinkingface` | Bucket name to inspect for content-addressed objects |

## Status

The suite passes against the current backend (37 tests). Two things worth
knowing before you read a red run:

- `test_gcs_export.py` used to be **flaky** because of a server-side race, not
  a slow-sync timeout. With `TF_SYNC_WORKERS=2`, `store.ClaimSyncJob` handed
  two workers two jobs for the *same* repo and ref at once -- the
  repo-creation job and the follow-up commit job. `Syncer.publishBlob`
  (`backend/internal/syncer/syncer.go`) walks `OldSHA..NewSHA` diffs rather
  than the whole tree on every push, falling back to the full tree only when
  there is no `OldSHA` (the very first sync) or the diff fails; depending on
  which of the two concurrent jobs finished last, a file outside *that* job's
  diff (e.g. `.gitattributes`, only ever present in the repo-creation commit)
  could end up unpublished at its `blobs/{sha}` key, and nothing republished it
  afterwards. `ClaimSyncJob` now refuses a job whose repo+ref another worker is
  already syncing, so the jobs for one ref run in id order however many workers
  are configured. `TestIntegrationSyncJobs` covers both the sequential and the
  concurrent case; if this test ever flakes again, look there first.
- Every test creates its repo inside a `try:` and deletes it in `finally:`, so
  a failing assertion still cleans up. A killed process (Ctrl-C, CI timeout)
  does not; `make clean` resets the local stack.

The bootstrap token minted in `conftest.py` is revoked in
`pytest_sessionfinish`, so repeated runs do not accumulate write-scoped
credentials on the server.
