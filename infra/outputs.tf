output "bucket_name" {
  description = "GCS bucket holding lfs/, blobs/, wal/ and tmp/ -- all content-addressed and immutable except tmp/ (design doc §4)."
  value       = google_storage_bucket.main.name
}

output "bucket_url" {
  value = "gs://${google_storage_bucket.main.name}"
}

output "artifact_registry_repository" {
  description = "Push images to {region}-docker.pkg.dev/{project}/{repo}/{image}."
  value       = google_artifact_registry_repository.images.id
}

output "network_name" {
  value = google_compute_network.main.name
}

output "sql_instance_connection_name" {
  description = "Not used by the api workload itself (it reaches Cloud SQL over Direct VPC egress + private IP, see infra/README.md). Useful for `gcloud sql connect` / a local Cloud SQL Auth Proxy for manual debugging. null when database_backend = \"sqlite\" (no Cloud SQL instance)."
  value       = try(google_sql_database_instance.main[0].connection_name, null)
}

output "sql_instance_private_ip" {
  description = "null when database_backend = \"sqlite\" (no Cloud SQL instance)."
  value       = try(google_sql_database_instance.main[0].private_ip_address, null)
}

output "database_url_secret_id" {
  description = "Secret Manager secret holding the full DATABASE_URL. null when database_backend = \"sqlite\": DATABASE_URL is a plain (non-secret) Cloud Run env var in that mode, see infra/main.tf's local.api_env."
  value       = try(google_secret_manager_secret.database_url[0].secret_id, null)
}

output "admin_password_secret_id" {
  value = google_secret_manager_secret.admin_password.secret_id
}

output "session_secret_secret_id" {
  value = google_secret_manager_secret.session_secret.secret_id
}

output "api_service_account_email" {
  description = "Service account the Cloud Run api service and compact Job run as."
  value       = google_service_account.api.email
}

output "web_service_account_email" {
  value = google_service_account.web.email
}

output "api_service_name" {
  value = google_cloud_run_v2_service.api.name
}

output "api_url" {
  description = "Public *.run.app URL of the Cloud Run api service (point TF_PUBLIC_URL / DNS at this, or at a custom domain mapping)."
  value       = google_cloud_run_v2_service.api.uri
}

output "compact_job_name" {
  value = google_cloud_run_v2_job.compact.name
}

output "web_url" {
  description = "Public URL of the Cloud Run web frontend."
  value       = google_cloud_run_v2_service.web.uri
}
