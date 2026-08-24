# syntax=docker/dockerfile:1

# Base images are pinned by digest, not just by tag: a tag is mutable, so
# `oven/bun:1` alone lets whatever was pushed most recently into the build.
# The tag is kept alongside the digest for readability, and Renovate
# (renovate.json) bumps the pair together. See docs/dev/supply-chain.md.
FROM oven/bun:1@sha256:5ff609364c049b54eb0ff560ec96319729a972078ef2c755d758f0c6ef89c2d6 AS deps
WORKDIR /app
# bun.lock without a glob: if the lockfile is ever missing from the build
# context, this must fail loudly rather than fall through to an unpinned
# install. bunfig.toml carries the install policy (minimumReleaseAge).
COPY package.json bun.lock bunfig.toml ./
# No `|| bun install` fallback. The fallback used to turn a lockfile that had
# drifted out of sync into a silent, fully unpinned resolution -- the newest
# version of every dependency, baked into a production image. A drifted
# lockfile is a bug to fix, not something to route around.
RUN bun install --frozen-lockfile

FROM oven/bun:1@sha256:5ff609364c049b54eb0ff560ec96319729a972078ef2c755d758f0c6ef89c2d6 AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# The backend is not reachable during the image build; every route is
# force-dynamic and no page fetches data at build time, so this never
# depends on API connectivity.
RUN bun run build

FROM oven/bun:1-slim@sha256:e0ee68d16ccb9927bf02aa7dd8fd4bf3369ee6d46da04faa72b05ce8bfd135f6 AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=3000

# The bun images already ship an unprivileged "bun" user; the slim variant has
# no adduser/groupadd, so reuse it rather than creating another.
COPY --from=builder /app/public ./public
COPY --from=builder --chown=bun:bun /app/.next ./.next
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/package.json ./package.json
COPY --from=builder /app/next.config.ts ./next.config.ts

USER bun

EXPOSE 3000

CMD ["bun", "run", "start"]
