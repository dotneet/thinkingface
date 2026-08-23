#!/usr/bin/env bash
# Run the branch's Go API on the host, without rebuilding the compose image.
#
# Why this exists: `docker compose up -d api` rebuilds a container image on
# every backend change, which is far too slow for a read-fix-retry loop, and
# the compose api is also the one the E2E suite and the docker web(:3000) are
# pointed at — restarting it disrupts both. This starts a *second* API on
# ${API_DEV_PORT} backed by its own SQLite database under .dev/, sharing only
# the compose GCS emulator (through scripts/gcs-host-proxy.py, which is
# started automatically; see that file for why the Host rewrite is needed).
#
# The database is separate from the compose Postgres, so this instance has its
# own users and repositories. TF_ADMIN_* seeds admin/admin on first start.
#
#   make dev-api                       # :8081, SQLite under .dev/
#   API_DEV_PORT=8082 make dev-api     # a second one alongside it
#
# Point a host-side web at it with:
#   NEXT_PUBLIC_API_URL=http://localhost:8081 API_URL=http://localhost:8081 make dev-web
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"

: "${API_DEV_PORT:=8081}"
: "${GCS_PROXY_PORT:=14443}"
: "${WEB_DEV_PORT:=3100}"
: "${GCS_BUCKET:=thinkingface}"
: "${DEV_DIR:=$ROOT/.dev}"

mkdir -p "$DEV_DIR"/{db,git,cache,bin}

# The emulator is only reachable with the right Host header, so bring the proxy
# up unless something is already listening on its port (a previous run, or
# `make gcs-proxy` in another terminal).
proxy_pid=""
if lsof -ti "tcp:${GCS_PROXY_PORT}" >/dev/null 2>&1; then
	echo "==> gcs-host-proxy already listening on ${GCS_PROXY_PORT}"
else
	echo "==> starting gcs-host-proxy on ${GCS_PROXY_PORT}"
	"$ROOT/scripts/gcs-host-proxy.py" --port "$GCS_PROXY_PORT" &
	proxy_pid=$!
	# Only tear down the proxy if this script is the one that started it.
	trap 'kill "$proxy_pid" 2>/dev/null || true' EXIT
fi

echo "==> building backend/cmd/thinkingface"
(cd backend && go build -o "$DEV_DIR/bin/thinkingface" ./cmd/thinkingface)

echo "==> api on http://localhost:${API_DEV_PORT} (sqlite: ${DEV_DIR}/db/tf.db, admin/admin)"
exec env \
	TF_ADDR=":${API_DEV_PORT}" \
	TF_PUBLIC_URL="http://localhost:${API_DEV_PORT}" \
	DATABASE_URL="sqlite://${DEV_DIR}/db/tf.db" \
	GIT_ROOT="${DEV_DIR}/git" \
	TF_VIEWER_CACHE_DIR="${DEV_DIR}/cache" \
	STORAGE_DRIVER=gcs-emulator \
	GCS_BUCKET="${GCS_BUCKET}" \
	GCS_PREFIX="${GCS_PREFIX:-}" \
	STORAGE_EMULATOR_HOST="http://localhost:${GCS_PROXY_PORT}" \
	TF_ADMIN_USERNAME="${TF_ADMIN_USERNAME:-admin}" \
	TF_ADMIN_PASSWORD="${TF_ADMIN_PASSWORD:-admin}" \
	TF_ADMIN_EMAIL="${TF_ADMIN_EMAIL:-admin@example.com}" \
	TF_SESSION_SECRET="${TF_SESSION_SECRET:-dev-insecure-session-secret}" \
	TF_ALLOW_SIGNUP="${TF_ALLOW_SIGNUP:-true}" \
	TF_SSH_ENABLED="${TF_SSH_ENABLED:-false}" \
	`# credentialed CORS from a host-side web needs the exact origin; the env` \
	`# var is TF_ALLOWED_ORIGINS, not TF_CORS_ORIGINS.` \
	TF_ALLOWED_ORIGINS="${TF_ALLOWED_ORIGINS:-http://localhost:${WEB_DEV_PORT}}" \
	"$DEV_DIR/bin/thinkingface"
