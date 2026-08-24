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
# 1b. Refuse `gc` outright in this mode.
#
#    Everything below restores a *snapshot* of the database from the replica.
#    That is fine for a reader, and fine for `serve`, which then owns the only
#    current copy and streams its own writes back. It is not fine for `gc`:
#    reference-counted collection decides an object is garbage by not finding
#    a row for it, so a snapshot taken before the latest uploads makes gc
#    conclude that live LFS payloads are unreferenced and delete them from the
#    bucket. DeleteOrphanedLFSObject re-checks under a row lock, but that lock
#    is in a different database file from the one the live server is using, so
#    it coordinates nothing. Its own row deletions would then be dropped on
#    exit as well, since this script never replicates a non-serve subcommand.
#
#    infra/main.tf does not create the gc Job in sqlite mode; this is the
#    backstop for a hand-run `gcloud run jobs execute`. Reclaiming storage
#    under sqlite has to happen inside the serving process instead.
# ---------------------------------------------------------------------------
if [ "${1:-serve}" = "gc" ]; then
  echo "entrypoint: refusing to run gc against a Litestream-restored SQLite snapshot." >&2
  echo "entrypoint: it would delete LFS objects uploaded after the snapshot was taken." >&2
  exit 64
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
#    the GCS replica by this script, and non-serve subcommands read a
#    restored snapshot rather than the live database. `compact` (the only
#    one the Cloud Run Job invokes in this mode, see infra/main.tf) only
#    reads, so it is safe on both counts. `gc` is unsafe on both and is
#    refused in step 1b. A future subcommand that needs to persist SQLite
#    writes must either run through `litestream replicate` too or push a
#    new snapshot before exiting -- and one whose *correctness* depends on
#    seeing the live database, as gc's does, cannot run here at all.
# ---------------------------------------------------------------------------
cmd=${1:-serve}
if [ "$cmd" = "serve" ]; then
  exec litestream replicate -exec "/usr/local/bin/thinkingface serve" "$DB_PATH" "$TF_LITESTREAM_REPLICA_URL"
fi

exec /usr/local/bin/thinkingface "$@"
