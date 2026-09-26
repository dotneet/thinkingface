/**
 * Same-origin API proxy: every method under `/api/*` is forwarded to
 * `API_URL` (read at runtime, default `http://localhost:8080`). The logic —
 * header filtering, the CSRF check, `Location` rewriting, streaming — lives in
 * `lib/api-proxy.ts`; the design is docs/dev/agent-features.md §1.2.
 *
 * ## When the browser comes here
 *
 * Only when the image was built with `NEXT_PUBLIC_API_URL` empty (the default
 * for the compose build and the Dockerfile). Then `apiBaseUrl()` in
 * `lib/api.ts` is `window.location.origin` in the browser and
 * `publicApiBase()` in `lib/paths.ts` is `""`, so every browser-built API URL
 * is a path on this origin. A non-empty `NEXT_PUBLIC_API_URL` keeps the old
 * cross-origin behaviour and the browser never calls this route (it still
 * answers, under the same CSRF check, for anyone who does).
 *
 * ## Which paths are proxied, and why these
 *
 * The browser fetches from the API on exactly three URL shapes:
 *
 * 1. `/api/...` — the Web UI API (`/api/v1/*`, uploads included) and the
 *    HF-compatible metadata endpoints. This route. Nothing in `app/` lives
 *    under `/api`, so it cannot shadow a page.
 * 2. `/{kind}s/{ns}/{name}/resolve/{rev}/{path}` — file downloads, README
 *    images, the Parquet SQL console and the tabular preview. There is no
 *    `/api/v1` equivalent (`/api/v1/raw/...` is a JSON preview capped at
 *    512 KiB, not the file), so the path is proxied as-is by
 *    `app/models/[ns]/[name]/resolve/[...path]/route.ts` and its `datasets`
 *    twin — GET and HEAD only, which is all the API serves there. Models are
 *    addressed as `/models/{ns}/{name}/resolve/...` (the API mounts the same
 *    routes there as at the root) because the root-level HF shape
 *    `/{ns}/{name}/...` is the namespace page's territory in `app/[ns]`.
 *    `resolveFileUrl()` in `lib/repos.ts` picks that prefix only in
 *    same-origin mode, so a configured `NEXT_PUBLIC_API_URL` still gets
 *    byte-for-byte the URLs it always did.
 * 3. The git clone URL and the `HF_ENDPOINT` in the "use this model" snippets
 *    are **not** proxied: they are for git and huggingface_hub, not for the
 *    browser, and they come from the API's `TF_PUBLIC_URL` (`clone_url` in
 *    the repository detail) so they point wherever the API is really
 *    reachable. Proxying git smart HTTP or LFS transfers through a Next.js
 *    process would buy nothing and cost a hop.
 *
 * ## Deploying behind it
 *
 * - `proxy.ts` (Next.js middleware), if one exists, must not match these
 *   paths: Next.js buffers a request body for middleware and cuts it off at
 *   `proxyClientMaxBodySize` (10 MB), which would truncate every upload.
 * - The API sees every browser request as coming from the web server's
 *   address unless `TF_TRUST_PROXY_IPS=true`; see `forwardRequestHeaders`
 *   for what `X-Forwarded-For` carries.
 */
import { proxyRequest } from "@/lib/api-proxy";

// Node.js, not Edge: request bodies are streamed with `duplex: "half"` and can
// be gigabytes long. Never cached or prerendered — every call is a live API call.
export const runtime = "nodejs";
export const dynamic = "force-dynamic";

function handle(request: Request): Promise<Response> {
  return proxyRequest(request);
}

export {
  handle as DELETE,
  handle as GET,
  handle as HEAD,
  handle as OPTIONS,
  handle as PATCH,
  handle as POST,
  handle as PUT,
};
