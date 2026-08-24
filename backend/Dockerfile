# syntax=docker/dockerfile:1

# ---- builder -----------------------------------------------------------
FROM golang:1.25-alpine AS builder

# No apk packages needed: golang:alpine already ships ca-certificates (for
# `go mod download` over HTTPS), and the build no longer needs a C
# toolchain (see CGO_ENABLED=0 below) or git (.git is excluded from the
# build context by .dockerignore, so there is nothing for it to read).

WORKDIR /src

# Copy go.mod/go.sum first so `go mod download` is cached as its own layer
# and only re-runs when dependencies actually change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is disabled: the viewer reads Parquet with the pure-Go parquet-go
# library (CLAUDE.md invariant 4), not CGo bindings, so nothing in this
# binary needs a C toolchain. A static binary also makes the runtime image
# smaller and drops any glibc/musl dependency at runtime.
# Migration SQL is embedded via go:embed, so nothing else needs copying
# into the runtime image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/thinkingface ./cmd/thinkingface

# ---- litestream ----------------------------------------------------------
# Litestream replicates a SQLite file to GCS so the Cloud Run SQLite
# backend (infra/variables.tf's database_backend = "sqlite") survives
# restarts despite Cloud Run's ephemeral filesystem. Fetched in its own
# small stage (not the runtime image) so curl/tar never ship in the final
# image. Version pinned + sha256-verified against the upstream release
# (https://github.com/benbjohnson/litestream/releases/tag/v0.5.16); bump
# LITESTREAM_VERSION and both LITESTREAM_SHA256_* args together after
# checking the new release's checksums.txt.
FROM alpine:3.21 AS litestream

ARG LITESTREAM_VERSION=0.5.16
ARG LITESTREAM_SHA256_AMD64=9e29112380a942e4a62ee07773684396cb8b308dc4d67e130bef41f75e937f0a
ARG LITESTREAM_SHA256_ARM64=678022e4103145302598e35d37f8718392d42e153feeb1e2d4a64dd0cd3aaf10
ARG TARGETARCH

RUN apk add --no-cache curl && \
    case "$TARGETARCH" in \
      amd64) litestream_arch=x86_64; litestream_sha256=$LITESTREAM_SHA256_AMD64 ;; \
      arm64) litestream_arch=arm64;  litestream_sha256=$LITESTREAM_SHA256_ARM64 ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac && \
    curl -fsSLo /tmp/litestream.tar.gz \
      "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/litestream-${LITESTREAM_VERSION}-linux-${litestream_arch}.tar.gz" && \
    echo "${litestream_sha256}  /tmp/litestream.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/litestream.tar.gz -C /usr/local/bin litestream && \
    rm /tmp/litestream.tar.gz

# ---- runtime -------------------------------------------------------------
FROM alpine:3.21 AS runtime

# git + git-lfs are required at runtime: the server execs `git` for the
# smart HTTP endpoints (upload-pack / receive-pack) and `git-lfs` filters
# are installed system-wide for any local git operations.
RUN apk add --no-cache git git-lfs ca-certificates tzdata && \
    git lfs install --system && \
    update-ca-certificates

RUN addgroup -S thinkingface && \
    adduser -S -G thinkingface -h /home/thinkingface thinkingface && \
    mkdir -p /data/git /data/cache /data/db /data/ssh /opt/thinkingface/hooks && \
    chown -R thinkingface:thinkingface /data /home/thinkingface

COPY --from=builder /out/thinkingface /usr/local/bin/thinkingface
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream

# core.hooksPath fixed image location for the pre-receive hook (Continuity
# migration, docs/dev/continuity-design.md §6.2): the hook script lives in the
# image, never per-repository, so pushes always go through
# `thinkingface hook pre-receive`.
COPY hooks/pre-receive /opt/thinkingface/hooks/pre-receive
RUN chmod 0755 /opt/thinkingface/hooks/pre-receive /usr/local/bin/litestream && \
    chown thinkingface:thinkingface /opt/thinkingface/hooks/pre-receive /usr/local/bin/litestream

# entrypoint.sh restores/replicates SQLite via Litestream when the operator
# opts into database_backend = "sqlite" (infra/variables.tf); it's a plain
# exec pass-through to `thinkingface` in every other case (postgres mode,
# `hook`, etc.), see the script itself for the full decision tree.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh && \
    chown thinkingface:thinkingface /usr/local/bin/entrypoint.sh

USER thinkingface
WORKDIR /home/thinkingface

# TF_VIEWER_CACHE_DIR is WAL compaction's scratch space; the parquet viewer
# reads via storage range requests now and no longer caches to disk here.
ENV TF_ADDR=:8080 \
    GIT_ROOT=/data/git \
    TF_VIEWER_CACHE_DIR=/data/cache

EXPOSE 8080
# git over SSH, when TF_SSH_ENABLED=true (docs/dev/thinkingface-design.md §5).
EXPOSE 2222

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
    CMD wget -q -O - http://127.0.0.1:8080/healthz | grep -q '"ok"' || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
