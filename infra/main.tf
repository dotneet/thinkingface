locals {
  bucket_name    = var.bucket_name != "" ? var.bucket_name : "${var.project_id}-thinkingface"
  api_public_url = var.api_public_url != "" ? var.api_public_url : "https://api.${var.environment}.example.com"
}

# ---------------------------------------------------------------------------
# APIs required by the resources below.
# ---------------------------------------------------------------------------

resource "google_project_service" "required" {
  for_each = toset([
    "storage.googleapis.com",
    "artifactregistry.googleapis.com",
    "sqladmin.googleapis.com",
    "servicenetworking.googleapis.com",
    "run.googleapis.com",
    "secretmanager.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "compute.googleapis.com",
    "cloudscheduler.googleapis.com",
  ])

  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

# ---------------------------------------------------------------------------
# GCS: content-addressed object store (design doc §4)
#
# Every key is content-addressed and immutable: lfs/{oid[0:2]}/{oid[2:4]}/{oid}
# for LFS objects, blobs/{sha[0:2]}/{sha[2:4]}/{sha} for every other file the
# sync worker publishes off a pushed ref, plus wal/ (the git WAL,
# docs/dev/continuity-design.md) and tmp/ (transient upload staging). There is no
# human-readable, per-repository layout in the bucket -- nothing here is named
# after a namespace, repository or path -- and there never was a separate
# rewrite of the LFS bytes into one: an LFS object lives at exactly one key for
# its whole life. `GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}` is what
# reconstructs the human-readable mapping, entirely client-side, as the
# destination side of a generated `gcloud storage cp` script.
#
# Because lfs/ and blobs/ are deduplicated across every repository, deleting a
# repository does not delete its objects -- another repository (or another
# branch of the same one) may still reference them. Reclaiming orphans is
# `thinkingface gc`'s job (reference-counted against repo_lfs_objects /
# repo_files.blob_sha), not a bucket lifecycle rule keyed on age: an
# untouched-for-90-days object is very often still exactly what a stable
# dataset release should keep serving forever.
# ---------------------------------------------------------------------------

resource "google_storage_bucket" "main" {
  name     = local.bucket_name
  project  = var.project_id
  location = var.region

  uniform_bucket_level_access = true
  force_destroy               = false

  versioning {
    enabled = true
  }

  # Soft delete + versioning are kept purely as a safety net against operator
  # error (a stray `gcloud storage rm`, an admin fat-fingering an IAM policy)
  # -- not as part of the normal object lifecycle, which is entirely
  # reference-counted GC now. A deleted object (including a deleted noncurrent
  # version) stays restorable for this long regardless of which prefix it was
  # under.
  soft_delete_policy {
    retention_duration_seconds = var.bucket_soft_delete_retention_days * 86400
  }

  # tmp/uploads/ is the staging key for a proxied LFS upload, deleted by the
  # handler as soon as the object is copied into place. This rule only ever
  # catches the orphans left by an upload that failed midway.
  lifecycle_rule {
    condition {
      age            = var.tmp_uploads_retention_days
      matches_prefix = ["tmp/uploads/"]
    }
    action {
      type = "Delete"
    }
  }

  labels = var.labels

  depends_on = [google_project_service.required]
}

# Read-only access for the humans and analytics workloads that read lfs/ and
# blobs/ directly -- `gcloud storage cp` / `cp -r`, BigQuery external tables,
# DuckDB `read_parquet()` over a `gs://` URI.
#
# Deliberately NOT an IAM condition on a prefix: storage.objects.list is
# evaluated against the bucket, not against an object name, so a
# resource.name condition makes recursive copies and external-table scans
# fail to enumerate anything. Bucket-wide objectViewer is the price of
# keeping `gcloud storage cp -r` working.
#
# What this grant is really for is the roles it lets you stop handing out:
# objectViewer carries no storage.objects.delete, whereas roles/storage.admin,
# roles/editor and roles/owner all do. lfs/ and blobs/ are the only copy of
# their bytes -- a stray `gcloud storage rm` is data loss, recoverable only
# from the soft-delete window and noncurrent versions above. Grant write
# access to the api service account and to nothing else.
resource "google_storage_bucket_iam_member" "bucket_readers" {
  for_each = toset(var.bucket_reader_members)

  bucket = google_storage_bucket.main.name
  role   = "roles/storage.objectViewer"
  member = each.value
}

# ---------------------------------------------------------------------------
# Artifact Registry: backend + frontend container images
# ---------------------------------------------------------------------------

resource "google_artifact_registry_repository" "images" {
  project       = var.project_id
  location      = var.region
  repository_id = var.artifact_registry_repository_id
  format        = "DOCKER"
  description   = "thinkingface backend/frontend container images"

  labels = var.labels

  depends_on = [google_project_service.required]
}

# ---------------------------------------------------------------------------
# Networking: private VPC for Cloud SQL private IP + Cloud Run Direct VPC
# egress (api needs to reach Cloud SQL's private IP; docs/dev/continuity-design.md
# §12/§14 — Direct VPC egress instead of the Serverless VPC Connector or a
# Cloud SQL Auth Proxy sidecar, see infra/README.md for the rationale).
# ---------------------------------------------------------------------------

resource "google_compute_network" "main" {
  project                 = var.project_id
  name                    = var.network_name
  auto_create_subnetworks = false

  depends_on = [google_project_service.required]
}

# Subnet Cloud Run's Direct VPC egress attaches to (google_cloud_run_v2_service.api
# / google_cloud_run_v2_job.compact vpc_access.network_interfaces below). No
# secondary ranges needed here anymore -- those were only for GKE IP-alias pods
# /services, which no longer exist.
resource "google_compute_subnetwork" "vpc_access" {
  project       = var.project_id
  name          = "${var.network_name}-run"
  region        = var.region
  network       = google_compute_network.main.id
  ip_cidr_range = "10.10.0.0/20"

  private_ip_google_access = true
}

resource "google_compute_global_address" "private_services" {
  project       = var.project_id
  name          = "${var.network_name}-private-services"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = var.private_ip_range_prefix_length
  network       = google_compute_network.main.id
}

resource "google_service_networking_connection" "private_services" {
  network                 = google_compute_network.main.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.private_services.name]

  depends_on = [google_project_service.required]
}

# ---------------------------------------------------------------------------
# Cloud SQL for PostgreSQL 17: metadata DB (private IP, automated backups)
#
# Skipped entirely when database_backend == "sqlite" (variables.tf) -- the
# api service uses a local SQLite file replicated to GCS via Litestream
# instead (backend/entrypoint.sh). count + [0]/try() below is how each
# resource opts out; nothing here runs for the sqlite backend.
# ---------------------------------------------------------------------------

resource "random_password" "db" {
  count = var.database_backend == "postgres" ? 1 : 0

  length  = 32
  special = false
}

resource "google_sql_database_instance" "main" {
  count = var.database_backend == "postgres" ? 1 : 0

  project             = var.project_id
  name                = "thinkingface-${var.environment}"
  region              = var.region
  database_version    = "POSTGRES_17"
  deletion_protection = true

  depends_on = [google_service_networking_connection.private_services]

  settings {
    tier              = var.db_tier
    availability_type = var.db_availability_type

    ip_configuration {
      ipv4_enabled                                  = false
      private_network                               = google_compute_network.main.id
      enable_private_path_for_google_cloud_services = true
    }

    backup_configuration {
      enabled                        = true
      start_time                     = "02:00"
      point_in_time_recovery_enabled = true
      transaction_log_retention_days = 7
      backup_retention_settings {
        retained_backups = 14
        retention_unit   = "COUNT"
      }
    }

    maintenance_window {
      day  = 7 # Sunday
      hour = 3
    }
  }
}

resource "google_sql_database" "app" {
  count = var.database_backend == "postgres" ? 1 : 0

  project  = var.project_id
  name     = var.db_name
  instance = google_sql_database_instance.main[0].name
}

resource "google_sql_user" "app" {
  count = var.database_backend == "postgres" ? 1 : 0

  project  = var.project_id
  name     = var.db_user
  instance = google_sql_database_instance.main[0].name
  password = random_password.db[0].result
}

# DATABASE_URL, assembled and stored in Secret Manager so the running
# workload never needs the raw password in a manifest/env var. Points
# directly at Cloud SQL's private IP -- api reaches it over Direct VPC
# egress (google_cloud_run_v2_service.api.template.vpc_access below), no
# Cloud SQL Auth Proxy sidecar involved (see infra/README.md).
resource "google_secret_manager_secret" "database_url" {
  count = var.database_backend == "postgres" ? 1 : 0

  project   = var.project_id
  secret_id = "thinkingface-${var.environment}-database-url"

  replication {
    auto {}
  }

  depends_on = [google_project_service.required]
}

resource "google_secret_manager_secret_version" "database_url" {
  count = var.database_backend == "postgres" ? 1 : 0

  secret = google_secret_manager_secret.database_url[0].id
  secret_data = format(
    "postgres://%s:%s@%s/%s?sslmode=disable",
    google_sql_user.app[0].name,
    random_password.db[0].result,
    google_sql_database_instance.main[0].private_ip_address,
    google_sql_database.app[0].name,
  )
}

resource "google_secret_manager_secret" "admin_password" {
  project   = var.project_id
  secret_id = "thinkingface-${var.environment}-admin-password"

  replication {
    auto {}
  }

  depends_on = [google_project_service.required]
}

resource "random_password" "admin" {
  length  = 24
  special = false
}

resource "google_secret_manager_secret_version" "admin_password" {
  secret      = google_secret_manager_secret.admin_password.id
  secret_data = random_password.admin.result
}

resource "google_secret_manager_secret" "session_secret" {
  project   = var.project_id
  secret_id = "thinkingface-${var.environment}-session-secret"

  replication {
    auto {}
  }

  depends_on = [google_project_service.required]
}

resource "random_password" "session_secret" {
  length  = 48
  special = false
}

resource "google_secret_manager_secret_version" "session_secret" {
  secret      = google_secret_manager_secret.session_secret.id
  secret_data = random_password.session_secret.result
}

# ---------------------------------------------------------------------------
# Service account for the api workload (Cloud Run, attached directly via
# google_cloud_run_v2_service.api.template.service_account -- no Workload
# Identity indirection needed like the old GKE setup).
# ---------------------------------------------------------------------------

resource "google_service_account" "api" {
  project      = var.project_id
  account_id   = "thinkingface-api-${var.environment}"
  display_name = "thinkingface api (${var.environment})"
}

# Read/write access to lfs/, blobs/, wal/ and tmp/, scoped to this bucket
# only. Also covers litestream/ (database_backend == "sqlite", see
# local.api_env below): objectAdmin is bucket-wide, not prefix-scoped, so no
# separate grant is needed for Litestream's restore/replicate traffic.
resource "google_storage_bucket_iam_member" "api_bucket_object_admin" {
  bucket = google_storage_bucket.main.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.api.email}"
}

# Lets the api SA mint its own signed URLs (signBlob) without a JSON key,
# per docs/dev/thinkingface-design.md §6/§14.
resource "google_service_account_iam_member" "api_self_token_creator" {
  service_account_id = google_service_account.api.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${google_service_account.api.email}"
}

resource "google_project_iam_member" "api_cloudsql_client" {
  count = var.database_backend == "postgres" ? 1 : 0

  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.api.email}"
}

resource "google_secret_manager_secret_iam_member" "api_database_url_accessor" {
  count = var.database_backend == "postgres" ? 1 : 0

  secret_id = google_secret_manager_secret.database_url[0].secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.api.email}"
}

resource "google_secret_manager_secret_iam_member" "api_admin_password_accessor" {
  secret_id = google_secret_manager_secret.admin_password.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.api.email}"
}

resource "google_secret_manager_secret_iam_member" "api_session_secret_accessor" {
  secret_id = google_secret_manager_secret.session_secret.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.api.email}"
}

# ---------------------------------------------------------------------------
# Cloud Run: api (Go backend -- HF-compatible REST + git smart HTTP + LFS)
#
# Runs on Cloud Run instead of GKE/StatefulSet now that the git bare
# repositories' primary persistence is the WAL in GCS
# (docs/dev/continuity-design.md), not a PersistentVolume. The local disk is
# just a warm cache that may be tmpfs and may disappear between requests.
# See docs/dev/continuity-design.md §12 for the settings below.
# ---------------------------------------------------------------------------

locals {
  # SQLite backend only (database_backend == "sqlite"): local file path
  # inside the api container and its matching DATABASE_URL, plus the GCS
  # replica backend/entrypoint.sh restores from / replicates to via
  # Litestream. The container filesystem is ephemeral (Cloud Run), so the
  # GCS side -- not this path -- is the durable copy.
  sqlite_db_path         = "/data/db/thinkingface.db"
  sqlite_database_url    = "sqlite://${local.sqlite_db_path}"
  litestream_replica_url = "gs://${google_storage_bucket.main.name}/litestream/thinkingface.db"

  # Non-secret env vars shared by the api service and the compact job.
  # DATABASE_URL is a special case: in postgres mode it's wired via
  # value_source.secret_key_ref (see the dynamic "env" blocks below) and
  # deliberately left out of this map; in sqlite mode there's no secret to
  # protect (it's a fixed local path), so it's a plain value here instead,
  # alongside TF_LITESTREAM_REPLICA_URL which points backend/entrypoint.sh
  # at the GCS replica. TF_ADMIN_PASSWORD / TF_SESSION_SECRET stay
  # secret_key_ref-only regardless of database_backend.
  api_env = merge(
    {
      GIT_ROOT            = "/tmp/git"   # tmpfs cache, not the source of truth (WAL in GCS is)
      TF_VIEWER_CACHE_DIR = "/tmp/cache" # tmpfs, existing emptyDir-equivalent
      STORAGE_DRIVER      = "gcs"
      GCS_BUCKET          = google_storage_bucket.main.name
      GCS_PREFIX          = ""
      TF_PUBLIC_URL       = local.api_public_url
      TF_SIGNED_URL_TTL   = "1h"
      TF_SYNC_WORKERS     = "4"
      TF_ALLOW_SIGNUP     = "false"
      TF_ADMIN_USERNAME   = "admin"
      TF_ADMIN_EMAIL      = "admin@example.com"
      TF_WAL_MODE         = "authoritative"
      TF_GIT_HOOKS_PATH   = "/opt/thinkingface/hooks" # baked into the image, see backend/Dockerfile
      # Both caches live on the memory-backed filesystem and share the 8 GiB
      # instance memory with the git/pack-objects processes: budget explicitly
      # (2 GiB each) instead of trusting the binary's defaults (2 + 4 GiB).
      TF_GIT_CACHE_BYTES    = "2147483648"
      TF_VIEWER_CACHE_BYTES = "2147483648"
    },
    var.database_backend == "sqlite" ? {
      DATABASE_URL              = local.sqlite_database_url
      TF_LITESTREAM_REPLICA_URL = local.litestream_replica_url
    } : {}
  )
}

resource "google_cloud_run_v2_service" "api" {
  project  = var.project_id
  name     = var.api_service_name
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  template {
    service_account                  = google_service_account.api.email
    execution_environment            = "EXECUTION_ENVIRONMENT_GEN2" # git fork/exec needs gen2's fuller syscall/FS support
    max_instance_request_concurrency = 40                           # default 80 is too many concurrent git processes per instance (design §12)
    timeout                          = "3600s"                      # large clone/push

    scaling {
      min_instance_count = 1 # keep the tmpfs cache warm, avoid cold starts on git operations
      # Litestream (database_backend == "sqlite") assumes a single writer to
      # the SQLite file; a second concurrent instance would each replicate
      # independently and corrupt/diverge the GCS replica. Force exactly
      # one instance in that mode regardless of api_max_instances.
      max_instance_count = var.database_backend == "sqlite" ? 1 : var.api_max_instances
    }

    # Direct VPC egress: reaches Cloud SQL's private IP without a Serverless
    # VPC Connector or an Auth Proxy sidecar. PRIVATE_RANGES_ONLY keeps
    # GCS/Secret Manager traffic on Cloud Run's normal path (not billed as
    # VPC egress, no extra hop).
    vpc_access {
      network_interfaces {
        network    = google_compute_network.main.name
        subnetwork = google_compute_subnetwork.vpc_access.name
      }
      egress = "PRIVATE_RANGES_ONLY"
    }

    containers {
      image = var.api_placeholder_image

      # h2c: end-to-end HTTP/2 in cleartext to the container. Required to
      # get past HTTP/1.1's 32MiB request body cap -- large `git push` /
      # LFS batch payloads need it (design §12).
      ports {
        name           = "h2c"
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = "2"
          memory = "8Gi" # tmpfs GIT_ROOT/TF_VIEWER_CACHE_DIR share this budget with the git/pack-objects processes
        }
        cpu_idle = false # CPU always allocated: syncer/webhook workers run in-process outside request handling
      }

      dynamic "env" {
        for_each = local.api_env
        content {
          name  = env.key
          value = env.value
        }
      }

      # DATABASE_URL as a secret_key_ref: postgres mode only. In sqlite
      # mode it's already set as a plain value by the dynamic "env" block
      # above (local.api_env), so this must stay empty then to avoid two
      # "DATABASE_URL" env entries on the same container.
      dynamic "env" {
        for_each = var.database_backend == "postgres" ? { DATABASE_URL = google_secret_manager_secret.database_url[0].secret_id } : {}
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = env.value
              version = "latest"
            }
          }
        }
      }
      env {
        name = "TF_ADMIN_PASSWORD"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.admin_password.secret_id
            version = "latest"
          }
        }
      }
      env {
        name = "TF_SESSION_SECRET"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.session_secret.secret_id
            version = "latest"
          }
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [
      template[0].containers[0].image,
    ]
  }

  depends_on = [
    google_project_service.required,
    google_secret_manager_secret_iam_member.api_database_url_accessor,
    google_secret_manager_secret_iam_member.api_admin_password_accessor,
    google_secret_manager_secret_iam_member.api_session_secret_accessor,
  ]
}

# api is invoked directly by git/git-lfs clients (no separate auth layer in
# front) -- same allUsers-invoker pattern as web. Authn/authz for HF-compatible
# endpoints happens inside the app (personal access tokens / session cookie).
resource "google_cloud_run_v2_service_iam_member" "api_public" {
  project  = var.project_id
  location = google_cloud_run_v2_service.api.location
  name     = google_cloud_run_v2_service.api.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# ---------------------------------------------------------------------------
# Cloud Run Job: WAL compaction (`thinkingface compact`, docs/dev/continuity-design.md §10)
# ---------------------------------------------------------------------------

resource "google_cloud_run_v2_job" "compact" {
  project  = var.project_id
  name     = var.compact_job_name
  location = var.region

  template {
    template {
      service_account = google_service_account.api.email
      timeout         = "3600s"
      max_retries     = 1

      vpc_access {
        network_interfaces {
          network    = google_compute_network.main.name
          subnetwork = google_compute_subnetwork.vpc_access.name
        }
        egress = "PRIVATE_RANGES_ONLY"
      }

      containers {
        image = var.api_placeholder_image
        args  = ["compact"]

        resources {
          limits = {
            cpu    = "2"
            memory = "8Gi"
          }
        }

        dynamic "env" {
          for_each = local.api_env
          content {
            name  = env.key
            value = env.value
          }
        }

        # See the matching block on google_cloud_run_v2_service.api above:
        # secret_key_ref in postgres mode only, plain value via local.api_env
        # otherwise.
        dynamic "env" {
          for_each = var.database_backend == "postgres" ? { DATABASE_URL = google_secret_manager_secret.database_url[0].secret_id } : {}
          content {
            name = env.key
            value_source {
              secret_key_ref {
                secret  = env.value
                version = "latest"
              }
            }
          }
        }
        env {
          name = "TF_ADMIN_PASSWORD"
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.admin_password.secret_id
              version = "latest"
            }
          }
        }
        env {
          name = "TF_SESSION_SECRET"
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.session_secret.secret_id
              version = "latest"
            }
          }
        }
      }
    }
  }

  lifecycle {
    ignore_changes = [
      template[0].template[0].containers[0].image,
    ]
  }

  depends_on = [
    google_project_service.required,
    google_secret_manager_secret_iam_member.api_database_url_accessor,
    google_secret_manager_secret_iam_member.api_admin_password_accessor,
    google_secret_manager_secret_iam_member.api_session_secret_accessor,
  ]
}

# Dedicated SA for Cloud Scheduler -> Cloud Run Job invocation, scoped to
# just this one job (not the broader api SA) so a compromised scheduler
# config can't do anything beyond "run the compact job".
resource "google_service_account" "compact_scheduler" {
  project      = var.project_id
  account_id   = "thinkingface-compact-${var.environment}"
  display_name = "thinkingface compact scheduler (${var.environment})"
}

resource "google_cloud_run_v2_job_iam_member" "compact_scheduler_invoker" {
  project  = var.project_id
  location = google_cloud_run_v2_job.compact.location
  name     = google_cloud_run_v2_job.compact.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.compact_scheduler.email}"
}

# Triggers the compact Job once a day via the Cloud Run Admin API's
# `jobs.run` method, authenticated as compact_scheduler with an OAuth token.
resource "google_cloud_scheduler_job" "compact" {
  project   = var.project_id
  region    = var.region
  name      = "thinkingface-compact-${var.environment}"
  schedule  = var.compact_schedule
  time_zone = "UTC"

  http_target {
    http_method = "POST"
    uri         = "https://${var.region}-run.googleapis.com/apis/run.googleapis.com/v1/namespaces/${var.project_id}/jobs/${google_cloud_run_v2_job.compact.name}:run"

    oauth_token {
      service_account_email = google_service_account.compact_scheduler.email
    }
  }

  depends_on = [
    google_project_service.required,
    google_cloud_run_v2_job_iam_member.compact_scheduler_invoker,
  ]
}

# ---------------------------------------------------------------------------
# Cloud Run: stateless web (Next.js) frontend
# ---------------------------------------------------------------------------

resource "google_service_account" "web" {
  project      = var.project_id
  account_id   = "thinkingface-web-${var.environment}"
  display_name = "thinkingface web (${var.environment})"
}

resource "google_cloud_run_v2_service" "web" {
  project  = var.project_id
  name     = var.web_service_name
  location = var.region
  ingress  = "INGRESS_TRAFFIC_ALL"

  template {
    service_account = google_service_account.web.email

    containers {
      image = var.web_placeholder_image

      env {
        name  = "API_URL"
        value = local.api_public_url
      }
      env {
        name  = "NEXT_PUBLIC_API_URL"
        value = local.api_public_url
      }

      ports {
        container_port = 3000
      }
    }
  }

  lifecycle {
    ignore_changes = [
      template[0].containers[0].image,
    ]
  }

  depends_on = [google_project_service.required]
}

resource "google_cloud_run_v2_service_iam_member" "web_public" {
  project  = var.project_id
  location = google_cloud_run_v2_service.web.location
  name     = google_cloud_run_v2_service.web.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
