# thinkingface

thinkingface is a self-hosted clone of the Hugging Face Hub. Host your own datasets and
model checkpoints, version them with git + Git LFS, and keep the actual bytes in your own
Google Cloud Storage bucket.

Your existing Python code keeps working: point `HF_ENDPOINT` at your instance and
`huggingface_hub` / `datasets` talk to thinkingface instead of hf.co — no code changes.

## What you get

- **Dataset and model repositories** — create them from the web UI, the API, or the `tf`
  CLI, then `git clone` / `git push` with Git LFS support.
- **A Hugging Face-compatible API** — `whoami`, `create_repo`, `upload_file`,
  `hf_hub_download`, `load_dataset`, and the rest work unchanged.
- **Browse without downloading** — a file tree, a Parquet table viewer, and a checkpoint
  metadata viewer that reads only the header of safetensors / PyTorch files.
- **Experiment tracking** — a trackio-compatible interface records runs, configs, and
  metrics, and charts them in the web UI.
- **Organizations** — shared team namespaces with `admin` / `write` / `read` roles.
- **Your storage, your layout** — objects are stored content-addressed in GCS, and the web
  UI generates a `gcloud storage cp` script that restores any revision to its original
  file structure.

## Next steps

- [Getting Started](getting-started.md) — run thinkingface locally and upload your first
  dataset in a few minutes.
