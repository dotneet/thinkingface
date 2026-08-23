#!/bin/sh
# entrypoint.sh -- SQLite persistence for Cloud Run via Litestream.
#
# Cloud Run's container filesystem is ephemeral: a new revision, a restart,
# or scale-to-zero all wipe anything written outside the image layers. In
# postgres mode this doesn't matter (all state lives in Cloud SQL), so this
# script must be a no-op pass-through in that mode -- it only changes
# behavior when the operator has opted into SQLite + Litestream via
# infra/main.tf's `database_backend = "sqlite"` (TF_LITESTREAM_REPLICA_URL
# set and DATABASE_URL a sqlite: URL). See docs/dev/thinkingface-design.md and
# CLAUDE.md invariant 5: this must never change HF-compatible behavior.
set -eu

# ---------------------------------------------------------------------------
# 1. Pass-through cases: postgres mode, sqlite without a configured replica
#    (e.g. plain local sqlite, no Litestream), and any invocation that isn't
#    even a DB-backed subcommand (e.g. `thinkingface hook pre-receive`,
#    invoked by git itself and never touching the DB at all).
# ---------------------------------------------------------------------------
case "${DATABASE_URL:-}" in
  sqlite:*) : ;;
  *)
    exec /usr/local/bin/thinkingface "$@"
    ;;
esac

if [ -z "${TF_LITESTREAM_REPLICA_URL:-}" ]; then
  exec /usr/local/bin/thinkingface "$@"
fi

# ---------------------------------------------------------------------------
# 2. Derive the local SQLite file path from DATABASE_URL.
#    sqlite:///data/db/thinkingface.db -> /data/db/thinkingface.db
#    (strip the "sqlite://" scheme prefix, then drop a trailing "?query"
#    if present -- the Go side parses DATABASE_URL the same way).
# ---------------------------------------------------------------------------
DB_PATH=${DATABASE_URL#sqlite://}
DB_PATH=${DB_PATH%%\?*}

mkdir -p "$(dirname "$DB_PATH")"

# ---------------------------------------------------------------------------
# 3. Restore from the GCS replica before anything touches the DB file.
#    -if-db-not-exists: skip restore if a local file is somehow already
#      there (shouldn't happen given the ephemeral filesystem, but harmless
#      and avoids clobbering it if it ever does).
#    -if-replica-exists: don't fail on the very first boot, when nothing
#      has been replicated yet -- `thinkingface serve` will create the DB
#      itself (migrations + admin seed) and Litestream will pick it up
#      once `replicate` starts below.
#    Flag names verified against litestream v${LITESTREAM_VERSION}'s
#    cmd/litestream/restore.go (github.com/benbjohnson/litestream).
#
#    NOTE: the replica URL scheme is "gs://", not "gcs://" -- litestream's
#    gs/replica_client.go registers itself under litestream.RegisterReplica-
#    ClientFactory("gs", ...); "gcs://" is not a recognized scheme even
#    though some litestream docs pages use it as a shorthand. Auth is via
#    Application Default Credentials (the Cloud Run service account), no
#    GOOGLE_APPLICATION_CREDENTIALS needed.
# ---------------------------------------------------------------------------
litestream restore -if-db-not-exists -if-replica-exists -o "$DB_PATH" "$TF_LITESTREAM_REPLICA_URL"

# ---------------------------------------------------------------------------
# 4. `serve` needs continuous replication for as long as it runs, so it's
#    wrapped by `litestream replicate -exec`, which starts the real server
#    as a subprocess and streams WAL changes to GCS in the background.
#    Any other subcommand (compact, migrate, gc, wal-seed, wal-verify, ...)
#    runs once against the restored file and exits -- there's no long-lived
#    process for `replicate` to attach to, so it's run directly instead.
#
#    NOTE: writes made by non-serve subcommands are NOT replicated back to
#    the GCS replica by this script. Today `compact` (the only one the
#    Cloud Run Job invokes, see infra/main.tf) only reads the DB, so this
#    is safe. If a future subcommand needs to persist SQLite writes, it
#    must either run through `litestream replicate` too or explicitly
#    push a new snapshot before exiting.
# ---------------------------------------------------------------------------
cmd=${1:-serve}
if [ "$cmd" = "serve" ]; then
  exec litestream replicate -exec "/usr/local/bin/thinkingface serve" "$DB_PATH" "$TF_LITESTREAM_REPLICA_URL"
fi

exec /usr/local/bin/thinkingface "$@"
