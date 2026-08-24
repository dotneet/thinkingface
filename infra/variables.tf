variable "project_id" {
  description = "GCP project id to deploy thinkingface into."
  type        = string
}

variable "region" {
  description = "Primary region for all regional resources (bucket, Artifact Registry, Cloud SQL, Cloud Run)."
  type        = string
  default     = "asia-northeast1"
}

variable "environment" {
  description = "Short environment name, used as a resource-name suffix (e.g. \"prod\", \"staging\")."
  type        = string
  default     = "prod"
}

variable "bucket_name" {
  description = "GCS bucket name. Defaults to \"{project_id}-thinkingface\" per docs/dev/thinkingface-design.md §4 when left empty."
  type        = string
  default     = ""
}

variable "artifact_registry_repository_id" {
  description = "Artifact Registry repository id for the backend/frontend container images."
  type        = string
  default     = "thinkingface"
}

variable "bucket_soft_delete_retention_days" {
  description = <<-EOT
    Soft-delete retention for the whole bucket: how long a deleted object --
    including a deleted noncurrent version -- stays restorable. Independent of
    object versioning, so it also covers a delete that versioning cannot undo.
    GCS accepts 7-90 days, or 0 to disable (not recommended: lfs/ and blobs/
    are each the only copy of their content, and normal object lifecycle here
    is reference-counted GC (`thinkingface gc`), not age -- this retention
    window exists purely as a safety net against operator error, e.g. a stray
    `gcloud storage rm` or a misapplied IAM policy).
  EOT
  type        = number
  default     = 30
}

variable "tmp_uploads_retention_days" {
  description = <<-EOT
    Days before an object under tmp/uploads/ is deleted. The LFS proxy upload
    path removes its staging object as soon as the copy succeeds, so this rule
    exists purely to collect the orphans left by uploads that failed midway.
  EOT
  type        = number
  default     = 1
}

variable "lfs_blobs_noncurrent_retention_days" {
  description = <<-EOT
    Days after an object under lfs/ or blobs/ becomes a noncurrent version
    (i.e. days since `thinkingface gc` deleted it) before that noncurrent
    version itself is deleted. This bucket has versioning enabled, so a gc
    delete alone never frees storage -- it just makes the object noncurrent,
    and the bytes keep being billed until something removes the noncurrent
    version too. This is that removal step.

    Deliberately does not apply to wal/: an old generation of
    wal/{storage_path}/index.json is the only recovery path for a corrupted
    or deleted WAL index (docs/dev/continuity-design.md §13, open issue 5;
    procedure in docs/dev/wal-index-recovery.md) and must be kept
    indefinitely, so the matching lifecycle_rule in main.tf is scoped to
    lfs/ and blobs/ only.
  EOT
  type        = number
  default     = 30
}

variable "bucket_reader_members" {
  description = <<-EOT
    IAM members granted read-only access to the bucket, for consumers that
    read lfs/ and blobs/ directly: `gcloud storage cp` / `cp -r`, BigQuery
    external tables, DuckDB `read_parquet()` over a `gs://` URI. Each entry is
    a full IAM member string, e.g. "group:ml-team@example.com" or
    "serviceAccount:bq@PROJECT.iam.gserviceaccount.com".

    Use this instead of granting roles/storage.admin, roles/editor or
    roles/owner: those carry storage.objects.delete, and lfs/ / blobs/ are
    each the only copy of their content. Write access belongs to the api
    service account alone.
  EOT
  type        = list(string)
  default     = []
}

# ---- Database backend ------------------------------------------------------

variable "database_backend" {
  description = <<-EOT
    Backing datastore for the api service and compact Job:
      - "postgres" (default): Cloud SQL for PostgreSQL, DATABASE_URL wired
        through Secret Manager. Supports api_max_instances > 1.
      - "sqlite": a single SQLite file on the api container's ephemeral
        filesystem, replicated to GCS via Litestream (backend/entrypoint.sh)
        so it survives restarts. No Cloud SQL resources are created.
        Forces the api service to a single Cloud Run instance (Litestream
        assumes one writer), see google_cloud_run_v2_service.api.
  EOT
  type        = string
  default     = "postgres"

  validation {
    condition     = contains(["postgres", "sqlite"], var.database_backend)
    error_message = "database_backend must be either \"postgres\" or \"sqlite\"."
  }
}

# ---- Cloud SQL -----------------------------------------------------------

variable "db_tier" {
  description = "Cloud SQL machine tier."
  type        = string
  default     = "db-custom-2-7680"
}

variable "db_name" {
  description = "Application database name."
  type        = string
  default     = "thinkingface"
}

variable "db_user" {
  description = "Application database user."
  type        = string
  default     = "thinkingface"
}

variable "db_availability_type" {
  description = "Cloud SQL availability type: ZONAL or REGIONAL."
  type        = string
  default     = "ZONAL"
}

# ---- networking (Private IP for Cloud SQL) --------------------------------

variable "network_name" {
  description = "Name of the VPC network created for private services access."
  type        = string
  default     = "thinkingface-network"
}

variable "private_ip_range_prefix_length" {
  description = "Prefix length of the reserved internal range used for Cloud SQL private services access."
  type        = number
  default     = 16
}

# ---- Cloud Run (api) --------------------------------------------------------

variable "api_service_name" {
  description = "Cloud Run service name for the Go backend (HF-compatible REST + git smart HTTP + LFS)."
  type        = string
  default     = "thinkingface-api"
}

variable "api_placeholder_image" {
  description = <<-EOT
    Image used on first apply only, before CI has published a real backend
    image to Artifact Registry. Terraform ignores later drift on this field
    for both google_cloud_run_v2_service.api and google_cloud_run_v2_job.compact
    (see their lifecycle.ignore_changes), so CI/CD owns the deployed image
    afterwards.
  EOT
  type        = string
  default     = "us-docker.pkg.dev/cloudrun/container/hello"
}

variable "api_max_instances" {
  description = "Upper bound for Cloud Run api autoscaling. min_instance_count is fixed at 1 (see main.tf) to keep the tmpfs git cache warm."
  type        = number
  default     = 4
}

variable "compact_job_name" {
  description = "Cloud Run Job name for WAL compaction (`thinkingface compact`, docs/dev/continuity-design.md §10)."
  type        = string
  default     = "thinkingface-compact"
}

variable "compact_schedule" {
  description = "Cloud Scheduler cron expression for the compact Job. Default: once a day at 03:00 UTC."
  type        = string
  default     = "0 3 * * *"
}

variable "gc_job_name" {
  description = "Cloud Run Job name for reference-counted GC (`thinkingface gc`, reclaims lfs/ and blobs/ objects nothing references any more -- see the object store comment near the top of main.tf)."
  type        = string
  default     = "thinkingface-gc"
}

variable "gc_schedule" {
  description = <<-EOT
    Cloud Scheduler cron expression for the gc Job. Default: once a week,
    Sundays at 05:00 UTC -- two hours after the daily compact_schedule
    default and on a coarser cadence, so the two never load Postgres and GCS
    at the same time. Weekly (rather than daily, like compact) is enough
    because gc's own full-bucket listing is far more expensive per run than
    compact's per-repo WAL fold, and orphaned storage has no urgency the way
    an uncompacted WAL does: an orphan just sits there costing storage until
    the next run collects it.
  EOT
  type        = string
  default     = "0 5 * * 0"
}

variable "gc_delete_enabled" {
  description = <<-EOT
    Whether the gc Job actually deletes orphaned lfs/ and blobs/ objects
    (passes `--yes`) or only reports them, which is `thinkingface gc`'s own
    default behavior (`--dry-run=true`) when this is left off.

    Defaults to false: a freshly Terraform-deployed environment should not
    start deleting production storage objects on its first scheduled run
    before an operator has read a few dry-run reports (`gcloud run jobs
    executions list --job <gc_job_name>` / the execution's logs) and
    confirmed they agree with what the deployment actually still
    references. Flip to true once you trust the reports, or run a one-off
    `gcloud run jobs execute <gc_job_name> --args=gc,--yes --region
    <region>` for a supervised first pass instead of waiting for the next
    scheduled run.
  EOT
  type        = bool
  default     = false
}

# ---- Cloud Run (web) --------------------------------------------------------

variable "web_service_name" {
  description = "Cloud Run service name for the Next.js frontend."
  type        = string
  default     = "thinkingface-web"
}

variable "web_placeholder_image" {
  description = <<-EOT
    Image used on first apply only, before CI has published a real
    frontend image to Artifact Registry. Terraform ignores later drift on
    this field (see lifecycle.ignore_changes on google_cloud_run_v2_service.web),
    so CI/CD owns the deployed image afterwards.
  EOT
  type        = string
  default     = "us-docker.pkg.dev/cloudrun/container/hello"
}

variable "api_public_url" {
  description = "Public URL the api is reachable at (TF_PUBLIC_URL), e.g. behind the External HTTPS LB."
  type        = string
  default     = ""
}

variable "labels" {
  description = "Common labels applied to resources that support them."
  type        = map(string)
  default = {
    app = "thinkingface"
  }
}
