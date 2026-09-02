import type { NextConfig } from "next";

/**
 * Content-Security-Policy for every document this app serves.
 *
 * Why it lives here and not only in the backend: `securityHeaders` in
 * `backend/internal/api/server.go` explains itself in terms of "the settings
 * screen must not be clickjackable" and "a repository path must not leak in a
 * Referer", but neither of those pages is served by the API — Next.js serves
 * them. Until this block existed, `/settings/tokens` came back with no
 * `X-Frame-Options`, no `Referrer-Policy`, no `nosniff` and no CSP at all,
 * while `/api/v1/*` (which nobody frames) had all of them.
 *
 * ## What this policy is actually worth
 *
 * `script-src` keeps `'unsafe-inline'`, so this is NOT a defence against
 * injected inline script. It cannot be: the App Router streams its RSC payload
 * through per-page inline `self.__next_f.push(...)` scripts whose content is
 * different on every render, so neither a hash list nor a static policy can
 * cover them, and the nonce alternative needs a `middleware.ts` that rewrites
 * the header per request (a bigger change than this fix, and one that puts a
 * middleware in front of every asset request). `components/theme-script.tsx`
 * is inline for the same reason — it has to run before first paint.
 *
 * What the policy *does* buy, and the reason it is still worth enforcing next
 * to the `rehype-raw` Markdown pipeline:
 *
 * - `script-src 'self'` blocks `<script src="https://evil.example/x.js">`.
 *   Raw HTML in a README is sanitised (`lib/markdown-sanitize.ts` strips
 *   `script`/`iframe`/`object`/`embed`/`form`), so this is the second lock on
 *   a door that is already shut — which is the point of defence in depth.
 * - `object-src 'none'` / `frame-src 'none'` match that same allowlist.
 * - `base-uri 'self'` stops an injected `<base>` from re-pointing every
 *   relative URL on the page.
 * - `form-action 'self'` stops an injected form from posting elsewhere.
 * - `frame-ancestors 'none'` is the CSP half of `X-Frame-Options: DENY`
 *   (modern browsers honour this one; the header stays for older ones).
 *
 * ## The deliberately loose directives
 *
 * - `connect-src`: the browser talks to `NEXT_PUBLIC_API_URL`, which is a
 *   *deploy-time* value — `headers()` is evaluated when the routes manifest is
 *   built, and the web image is built once and pointed at different API
 *   origins afterwards, so the real origin genuinely cannot be written here.
 *   Pinning a guess would break every fetch on a deployment whose API is on
 *   another host, which is a far worse outcome than a permissive
 *   `connect-src`. Left open on purpose (`ws:`/`wss:` additionally cover
 *   `next dev`'s HMR socket).
 * - `img-src` / `media-src`: README images, avatars and the Parquet viewer's
 *   image columns all resolve to that same unknown API origin, and cell values
 *   can be inlined `data:` URLs. Same reasoning, same conclusion.
 * - `script-src 'unsafe-eval'` alongside `'wasm-unsafe-eval'`: DuckDB-WASM
 *   (`lib/duckdb.ts`) compiles a WebAssembly module in the SQL console.
 *   `'wasm-unsafe-eval'` is the narrow keyword for that, but browsers that
 *   predate it (Safari before 16.4) ignore the unknown source expression and
 *   block the compile outright, which would silently kill the SQL console.
 *   Since `'unsafe-inline'` is already in this directive, the marginal cost of
 *   `'unsafe-eval'` is small and the cost of breaking a feature is not.
 *   `next dev`'s react-refresh needs it regardless.
 *
 * If this ever grows a `middleware.ts`, the upgrade path is: emit a per-request
 * nonce, drop `'unsafe-inline'`, then revisit `'unsafe-eval'`.
 */
const CSP_DIRECTIVES: Record<string, string> = {
  "default-src": "'self'",
  "base-uri": "'self'",
  "object-src": "'none'",
  "frame-src": "'none'",
  "frame-ancestors": "'none'",
  "form-action": "'self'",
  "manifest-src": "'self'",
  "script-src": "'self' 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' blob:",
  // Tailwind ships as a stylesheet, but next/font injects an inline <style>
  // and rendered Markdown (KaTeX, mermaid's generated SVG) carries inline
  // style rules of its own.
  "style-src": "'self' 'unsafe-inline'",
  "img-src": "'self' data: blob: https: http:",
  "media-src": "'self' data: blob: https: http:",
  "font-src": "'self' data:",
  "connect-src": "'self' data: blob: https: http: ws: wss:",
  // duckdb-browser-*.worker.js is same-origin (public/duckdb/), but the
  // library wraps it in a blob: URL on some code paths.
  "worker-src": "'self' blob:",
};

const contentSecurityPolicy = Object.entries(CSP_DIRECTIVES)
  .map(([directive, value]) => `${directive} ${value}`)
  .join("; ");

/**
 * Sent on every route. Mirrors `securityHeaders` in
 * `backend/internal/api/server.go` so the two halves of the site agree.
 */
export const SECURITY_HEADERS = [
  { key: "Content-Security-Policy", value: contentSecurityPolicy },
  // Redundant with `frame-ancestors 'none'` for current browsers, kept for the
  // ones that only understand this.
  { key: "X-Frame-Options", value: "DENY" },
  // Without this, a repository path (`/models/acme/private-thing`) rides along
  // in the Referer of every outbound request a README link or a Parquet image
  // column makes. Cross-origin gets the bare origin instead.
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  { key: "X-Content-Type-Options", value: "nosniff" },
];

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // Backend is not reachable at build time; no route in this app fetches
  // data during static generation (everything is force-dynamic or fetched
  // client-side), so the build never depends on API connectivity.
  eslint: {
    ignoreDuringBuilds: false,
  },
  // DuckDB-WASM is browser-only (lib/duckdb.ts imports it dynamically from a
  // client effect), but the server build still resolves the specifier — and
  // the "node" export condition points at duckdb-node.cjs, whose dynamic
  // requires webpack cannot statically analyse. Marking it external keeps that
  // module out of the server bundle and off the build log.
  serverExternalPackages: ["@duckdb/duckdb-wasm"],
  async headers() {
    return [{ source: "/:path*", headers: SECURITY_HEADERS }];
  },
};

export default nextConfig;
