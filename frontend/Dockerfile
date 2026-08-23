# syntax=docker/dockerfile:1

FROM oven/bun:1 AS deps
WORKDIR /app
COPY package.json bun.lock* bun.lockb* ./
RUN bun install --frozen-lockfile || bun install

FROM oven/bun:1 AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# The backend is not reachable during the image build; every route is
# force-dynamic and no page fetches data at build time, so this never
# depends on API connectivity.
RUN bun run build

FROM oven/bun:1-slim AS runner
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
