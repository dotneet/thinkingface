# infra

Terraform for the GCP production deployment described in
`docs/dev/thinkingface-design.md` §14 ("GCP production configuration") and
`docs/dev/continuity-design.md` (the Cloud Run / WAL migration). Compose (repo
root `docker-compose.yml`) and this differ only in environment variables and
the storage driver (`gcs-emulator` locally vs `gcs` here) — that parity is a
deliberate design goal.

## What this provisions

- `google_storage_bucket.main` — the single bucket holding `lfs/` and
  `blobs/` (both content-addressed and immutable — `lfs/` for LFS objects,
  `blobs/` for every other file the sync worker publishes off a pushed ref;
  no per-repository, human-readable layout exists in the bucket itself, only
  in the destination side of the `gcloud storage cp` script
  `GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}` generates), and `wal/` (the
  git write-ahead log — primary persistence for git data, see
  `docs/dev/continuity-design.md` §3; noncurrent versions are kept indefinitely,
  since old `index.json` generations are the recovery path for the WAL's
  single point of failure, §13/§16). Object lifecycle for `lfs/`/`blobs/` is
  reference-counted GC (`thinkingface gc`), not a bucket lifecycle rule —
  versioning and soft delete stay purely as a safety net against operator
  error
- `google_artifact_registry_repository.images` — Docker repo for
  backend/frontend images
- `google_sql_database_instance.main` — Cloud SQL for PostgreSQL 17, private
  IP only (via `google_service_networking_connection`), automated daily
  backups + PITR. Holds metadata only (repos / ACL / LFS ledger / jobs /
  experiments) — it is never on the git consistency path
  (`docs/dev/continuity-design.md` §1, §5 invariant 6)
- `google_service_account.api` — the api workload identity, scoped to:
  - `roles/storage.objectAdmin` on the bucket only (not project-wide)
  - `roles/iam.serviceAccountTokenCreator` on itself (keyless `signBlob` for
    LFS signed URLs)
  - `roles/cloudsql.client`
  - `roles/secretmanager.secretAccessor` on each of the three secrets below
- `google_cloud_run_v2_service.api` — the Go backend (HF-compatible REST +
  git smart HTTP + LFS batch + Parquet viewer), gen2 execution environment,
  h2c end-to-end, `min_instance_count = 1`, CPU always-allocated, Direct VPC
  egress to reach Cloud SQL's private IP. See "Cloud Run settings" below for
  the reasoning behind each setting
- `google_cloud_run_v2_job.compact` — runs `thinkingface compact` (WAL
  compaction, `docs/dev/continuity-design.md` §10), same image/SA/VPC egress/
  secrets as `api`
- `google_service_account.compact_scheduler` +
  `google_cloud_run_v2_job_iam_member.compact_scheduler_invoker` +
  `google_cloud_scheduler_job.compact` — triggers the compact Job once a day
  via the Cloud Run Admin API's `jobs.run` method (OAuth token, scoped SA
  that can only invoke this one job)
- `google_cloud_run_v2_job.gc` — runs `thinkingface gc` (reference-counted GC
  of `lfs/`/`blobs/`), same image/SA/VPC egress/secrets as `api`/`compact`,
  but its own timeout and schedule (see "Cloud Run Job settings"
  below). **Reports only by default** — see "Reference-counted GC" below for
  how to turn on actual deletion
- `google_service_account.gc_scheduler` +
  `google_cloud_run_v2_job_iam_member.gc_scheduler_invoker` +
  `google_cloud_scheduler_job.gc` — triggers the gc Job once a week (offset
  from `compact`'s schedule so the two never run at once), same pattern as
  the compact scheduler resources above
- `google_cloud_run_v2_service.web` — stateless Cloud Run service for the
  Next.js frontend (deployed with a placeholder image on first apply; CI
  owns the image afterwards, see `lifecycle.ignore_changes`)
- Three Secret Manager secrets: the assembled `DATABASE_URL`, the seeded
  admin password, and the session secret — none of these are ever written
  to a `.tf` state-adjacent file in plaintext outside Secret Manager itself
  (`terraform.tfstate` will contain them, though — treat state as
  sensitive, e.g. a GCS backend with encryption + restricted IAM)

Not provisioned here: a custom domain / TLS front for `api` and `web`. Cloud
Run terminates TLS itself and serves each service on its own `*.run.app`
URL, which is enough to get started; add a
`google_cloud_run_domain_mapping` (or an External HTTPS LB + Cloud Armor/IAP
in front of both services, per the old §14 diagram) once you've decided on a
domain strategy for your environment.

## Cloud Run settings (api)

| Item | Value | Reason |
|---|---|---|
| Execution environment | gen2 (`EXECUTION_ENVIRONMENT_GEN2`) | Filesystem compatibility for `git`'s fork/exec |
| Container port | `h2c` (named port, container_port 8080) | Avoids HTTP/1's 32 MiB request limit — required for large pushes / LFS batch |
| CPU allocation | Always on (`cpu_idle = false`) | The in-process syncer / webhook workers keep running outside of request handling |
| Resources | 2 vCPU / 8 GiB | `pack-objects` is CPU-hungry and shares the memory budget with the tmpfs cache |
| Concurrency | 40 (`max_instance_request_concurrency`) | The default of 80 is too many for git processes — memory is the limiting factor |
| min/max instances | 1 / `var.api_max_instances` (default 4; always 1 when `database_backend = "sqlite"`) | min=1 keeps the cache warm and avoids cold starts; max is also pinned to 1 for sqlite since Litestream assumes a single writer |
| Request timeout | 3600s | For large clones |
| `GIT_ROOT` | `/tmp/git` | tmpfs. **A warm cache, not the source of truth** — the source of truth is the WAL in GCS |
| `TF_VIEWER_CACHE_DIR` | `/tmp/cache` | tmpfs. Scratch space for WAL compaction only — the parquet viewer no longer caches to disk (it reads via storage range requests, see `TF_VIEWER_METADATA_CACHE_BYTES`) |

### Why Direct VPC egress (instead of a Serverless VPC Connector / Cloud SQL Auth Proxy sidecar)

- A **Serverless VPC Connector** needs a separate resource (an extra VM-based standing
  component), and its bandwidth cap and cost scale with the number of connector instances.
  Direct VPC egress is entirely a Cloud Run-side setting — no extra resource and no
  throughput ceiling (GA on gen2).
- The **Cloud SQL Auth Proxy sidecar** was used in the GKE StatefulSet, but on Cloud Run it
  would only add complexity for "run a second container permanently just to tunnel port
  5432." Cloud SQL is already private-IP-only (`google_sql_database_instance.main.ip_configuration`),
  so reaching the private IP directly via Direct VPC egress makes the proxy unnecessary.
  The `DATABASE_URL` secret was already assembled by embedding
  `google_sql_database_instance.main.private_ip_address` directly (the GKE version's
  comments assumed the Auth Proxy, but the actual secret-generation code was
  private-IP-direct from the start), so **the secret's format needs no change**.
- `egress = "PRIVATE_RANGES_ONLY"` routes only Cloud SQL-bound traffic (RFC1918) through the
  VPC, while calls to GCS / Secret Manager stay on Cloud Run's normal path (no VPC billing,
  no extra hop).

## Cloud Run Job settings (compact vs gc)

| Item | `compact` | `gc` | Reason for the difference |
|---|---|---|---|
| Resources | 2 vCPU / 8 GiB | 2 vCPU / 8 GiB (same) | `gc` needs the 8 GiB for the same reason `api`/`compact` have it: each pass in `backend/cmd/thinkingface/gc.go` holds an *entire* prefix listing plus the matching reference set in memory at once (`gcBlobs`: all of `blobs/` and every referenced `blob_sha`; `gcLFS`: all of `lfs/` and every `lfs_objects` row) — both scale with total bucket object / row count, not with any one repository. The 2 vCPU is then forced rather than chosen: Cloud Run permits at most 4 GiB per vCPU, so 8 GiB requires ≥ 2 vCPU. `gc` would otherwise be happy with less, since unlike `compact` it never forks `git`/pack-objects and is dominated by sequential GCS List/Delete and Postgres round trips |
| Timeout | 3600s (1h) | 21600s (6h) | `compact` walks one repository's WAL at a time; `gc` does a single unpaginated listing of each of `lfs/`, `blobs/` and `tmp/uploads/lfs/` (`backend/internal/storage.GCS.List` has no page cap) and then deletes orphans one HTTP call at a time (no GCS batch-delete API). A first run against a deployment with a large accumulated backlog needs real headroom; a caught-up bucket finishes in minutes |
| Schedule | daily, 03:00 UTC (`compact_schedule`) | weekly, Sunday 05:00 UTC (`gc_schedule`) | Offset so the two never load Postgres/GCS at the same time. Weekly is enough for `gc` because an orphaned object has no urgency the way an uncompacted WAL does — it just sits there costing storage until the next run |

### Reference-counted GC (`thinkingface gc`)

`thinkingface gc`'s own default is `--dry-run`: it reports orphaned `lfs/`
and `blobs/` objects without deleting anything. The `gc` Job mirrors that —
`var.gc_delete_enabled` (default `false`) controls whether the Job's `args`
include `--yes`:

- **`false` (default)**: the scheduled Job only prints a report (visible via
  `gcloud run jobs executions list` / the execution's logs). Nothing is ever
  deleted by the schedule. This is deliberate — a freshly Terraform-deployed
  environment should not start deleting production storage objects on its
  first scheduled run before an operator has read a few reports and confirmed
  they agree with what the deployment actually still references.
- **`true`**: the scheduled Job passes `--yes` and actually deletes what it
  finds orphaned.

To turn on real deletion once you've reviewed a few dry-run reports:

```bash
terraform apply -var="project_id=my-gcp-project" -var="gc_delete_enabled=true"
```

Or, for a supervised one-off pass without changing the schedule's default:

```bash
gcloud run jobs execute $(terraform output -raw gc_job_name) \
  --args=gc,--yes --region ${REGION}
```

## Database backend: postgres (default) vs sqlite

`var.database_backend` (`"postgres"` | `"sqlite"`) switches how the api
service and the `compact` Job persist metadata:

- **`postgres`** (default): everything described above — Cloud SQL for
  PostgreSQL 17, `DATABASE_URL` assembled from the instance/user/password and
  stored in Secret Manager, `roles/cloudsql.client` + secret-accessor IAM on
  the api SA, `api_max_instances` instances allowed.
- **`sqlite`**: no Cloud SQL instance, database, user, password, or
  `DATABASE_URL` secret are created at all (`count = 0` on each of those
  resources in `main.tf`; the corresponding `terraform output`s become
  `null`). Instead:
  - `DATABASE_URL` is a plain (non-secret) Cloud Run env var:
    `sqlite:///data/db/thinkingface.db` — a path inside the api container's
    ephemeral filesystem, not durable on its own.
  - `TF_LITESTREAM_REPLICA_URL` is set to
    `gs://<bucket>/litestream/thinkingface.db`. `backend/entrypoint.sh`
    (baked into the image, see `backend/Dockerfile`) uses
    [Litestream](https://litestream.io) to restore this file from GCS on
    container start and continuously replicate writes back to it while
    `serve` runs, using the api SA's Application Default Credentials — no
    extra secret or key needed. The bucket IAM binding that already grants
    the api SA `roles/storage.objectAdmin` (for `lfs/`/`blobs/`) covers
    `litestream/` too, since that role isn't prefix-scoped.
  - `scaling.max_instance_count` on `google_cloud_run_v2_service.api` is
    forced to `1` regardless of `var.api_max_instances`: Litestream assumes
    a single writer to the SQLite file, so a second concurrent instance
    would replicate independently and corrupt/diverge the GCS copy.
  - The `compact` Job also restores the file on each run (read-only —
    `compact` never writes SQLite state, see `backend/entrypoint.sh`), but
    is not wrapped in `litestream replicate` since it's a one-shot process,
    not a long-lived server.

**Known limitation**: because `max_instance_count = 1` bounds steady-state
traffic, not an in-flight deploy, a Cloud Run revision rollout can briefly
run the old and new revision side by side (Cloud Run's default rollout
behavior). In `sqlite` mode that means two writers to the same GCS replica
for a short window during every deploy. This is safe for `postgres` mode
(no shared local file) but is a real risk in `sqlite` mode; mitigate by
deploying with `--no-traffic` + a manual `gcloud run services update-traffic`
cutover, or accept the small window if deploys are infrequent.

## Prerequisites

- Terraform >= 1.9
- `gcloud auth application-default login` (or a service account key via
  `GOOGLE_APPLICATION_CREDENTIALS`) with permission to create the resources
  above in the target project
- A GCS bucket for Terraform state (recommended — this repo ships without a
  configured backend; add one before a real apply, see below)

## Usage

```bash
cd infra
terraform init            # add -backend-config=... once you configure a real backend
terraform plan  -var="project_id=my-gcp-project"
terraform apply -var="project_id=my-gcp-project"
```

Or with a `terraform.tfvars`:

```hcl
project_id  = "my-gcp-project"
region      = "asia-northeast1"
environment = "prod"
```

### Remote state

No backend is configured in `versions.tf` (kept blank so `terraform init
-backend=false && terraform validate` works without any GCP project). Before
a real `apply`, add a backend block, e.g.:

```hcl
terraform {
  backend "gcs" {
    bucket = "my-project-terraform-state"
    prefix = "thinkingface"
  }
}
```

## After `apply`

1. Build and push images to the Artifact Registry repo (`terraform output
   artifact_registry_repository`):
   ```bash
   gcloud auth configure-docker ${REGION}-docker.pkg.dev
   docker build -t ${REGION}-docker.pkg.dev/${PROJECT}/thinkingface/backend:latest ../backend
   docker push ${REGION}-docker.pkg.dev/${PROJECT}/thinkingface/backend:latest
   ```
2. Point Cloud Run's `api` service and the `compact`/`gc` Jobs at the real
   backend image (Terraform deliberately ignores drift on that field after
   the first apply for all three resources):
   ```bash
   gcloud run deploy $(terraform output -raw api_service_name) \
     --image ${REGION}-docker.pkg.dev/${PROJECT}/thinkingface/backend:latest \
     --region ${REGION}

   gcloud run jobs update $(terraform output -raw compact_job_name) \
     --image ${REGION}-docker.pkg.dev/${PROJECT}/thinkingface/backend:latest \
     --region ${REGION}

   gcloud run jobs update $(terraform output -raw gc_job_name) \
     --image ${REGION}-docker.pkg.dev/${PROJECT}/thinkingface/backend:latest \
     --region ${REGION}
   ```
3. Point Cloud Run's `web` service at the real frontend image (same
   ignore-drift pattern):
   ```bash
   gcloud run deploy $(terraform output -raw web_service_name 2>/dev/null || echo thinkingface-web) \
     --image ${REGION}-docker.pkg.dev/${PROJECT}/thinkingface/frontend:latest \
     --region ${REGION}
   ```
4. Set `TF_PUBLIC_URL` (`api_public_url` var, defaults to a placeholder
   `https://api.{environment}.example.com`) to wherever `api` is actually
   reachable — either the `*.run.app` URL from `terraform output api_url`,
   or a custom domain once you've wired up a domain mapping / LB in front of
   it — and re-apply.
5. Sanity-check the compact Job runs on schedule:
   ```bash
   gcloud scheduler jobs run thinkingface-compact-${ENVIRONMENT} --location ${REGION}   # manual trigger
   gcloud run jobs executions list --job $(terraform output -raw compact_job_name) --region ${REGION}
   ```
6. Sanity-check the gc Job's dry-run report before ever setting
   `gc_delete_enabled = true` (see "Reference-counted GC" above):
   ```bash
   gcloud scheduler jobs run thinkingface-gc-${ENVIRONMENT} --location ${REGION}   # manual trigger, dry-run by default
   gcloud run jobs executions list --job $(terraform output -raw gc_job_name) --region ${REGION}
   # then read the execution's logs to see what it would have deleted
   ```

## Notes on the runtime filesystem

`GIT_ROOT` (`/tmp/git`) and `TF_VIEWER_CACHE_DIR` (`/tmp/cache`) are tmpfs —
in-memory, and shared out of the same 8 GiB container memory budget as the
`git`/`pack-objects` processes. Both directories may be evicted or wiped
between requests or instances; that's expected and safe, since the actual
source of truth for git data is the WAL in GCS
(`docs/dev/continuity-design.md` §2/§9), not local disk. `TF_GIT_CACHE_BYTES`
(default 2 GiB, see `backend/internal/config`) bounds how much of that
budget the git repo LRU cache is allowed to use. `TF_VIEWER_CACHE_DIR` itself
is only used as WAL compaction's working directory now — the parquet viewer
reads objects from storage via range requests instead of caching them to
disk, so its budget (`TF_VIEWER_METADATA_CACHE_BYTES`, default 256 MiB) is a
small in-process heap allocation, not part of this tmpfs sizing.

## Validating without a GCP project

This is what CI (and this change) runs — no `apply`, no real project
required:

```bash
cd infra
terraform init -backend=false
terraform validate
```
