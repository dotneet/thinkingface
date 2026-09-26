import { CLIENT_ADDR_HEADER, PROXY_SECRET_HEADER, WEB_EDGE_ENV } from "@/lib/client-addr";
import type { ApiErrorBody } from "@/types/api";

/**
 * The same-origin API proxy (docs/dev/agent-features.md §1.2), minus the
 * Next.js wiring: `app/api/[...path]/route.ts` and the two `resolve` route
 * files only re-export {@link proxyRequest}. Everything here is Web-standard
 * (`Request` / `Response` / `Headers` / `fetch`), so it runs under Node, Bun
 * (what the production image starts Next.js with) and vitest alike.
 *
 * Why it exists: the browser used to call the API at `NEXT_PUBLIC_API_URL`,
 * which Next.js inlines at build time, so reaching the API on another port
 * meant rebuilding the web image — and a web UI on a non-default port also
 * needed `TF_ALLOWED_ORIGINS`. Built with that variable empty, the browser
 * calls its own origin instead and this forwards to `API_URL`, read at
 * *runtime*.
 */

/** Where the proxy forwards to. Read per request, never inlined at build time. */
export function upstreamBase(env: Record<string, string | undefined> = process.env): string {
  const configured = env.API_URL?.trim();
  return (configured || "http://localhost:8080").replace(/\/+$/, "");
}

/**
 * The upstream URL for an incoming request: the configured base plus the
 * request's path and query string *exactly as the browser sent them*. Taken
 * from the raw URL rather than rebuilt from the route's `params`, which Next.js
 * hands over decoded — rebuilding from those would turn a revision spelled
 * `feature%2Fx` into two path segments.
 */
export function upstreamUrl(requestUrl: string, base: string): string {
  const url = new URL(requestUrl);
  return `${base}${url.pathname}${url.search}`;
}

/**
 * Whether a normalised request pathname is one this proxy is allowed to
 * forward.
 *
 * `new URL()` (used by both {@link upstreamUrl} and the Next.js route
 * handlers' own routing) resolves `.` / `..` segments per RFC 3986 — including
 * a percent-encoded `%2e%2e` — before this ever runs, so
 * `/api/%2e%2e/datasets/a/b/git-receive-pack` arrives here as the pathname
 * `/datasets/a/b/git-receive-pack`. Without this check that path would be
 * forwarded unchanged, reaching an upstream endpoint outside the surface the
 * calling route handler is meant to expose. Only `/` delimits a segment for
 * this normalisation — a `%2F`-encoded slash inside one (a revision spelled
 * `feature%2Fx`) is left alone, so it still reaches the API as one segment.
 *
 * Two shapes are allowed, matching the only callers of {@link proxyRequest}:
 *
 * - `/api/...` — `app/api/[...path]/route.ts`, the whole Web UI +
 *   HF-compatible API surface;
 * - `/models/{ns}/{name}/resolve/...` / `/datasets/{ns}/{name}/resolve/...` —
 *   the two `resolve` route handlers (file downloads).
 */
export function isConfinedUpstreamPath(pathname: string): boolean {
  if (pathname === "/api" || pathname.startsWith("/api/")) return true;
  return /^\/(?:models|datasets)\/[^/]+\/[^/]+\/resolve\//.test(pathname);
}

// RFC 9110 §7.6.1 connection-specific headers, plus the legacy ones every
// proxy strips. `proxy-*` is matched by prefix below.
const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "transfer-encoding",
  "te",
  "trailer",
  "upgrade",
]);

function isHopByHop(name: string, connectionTokens: Set<string>): boolean {
  return HOP_BY_HOP.has(name) || name.startsWith("proxy-") || connectionTokens.has(name);
}

/** Header names a `Connection` header nominates as hop-by-hop for this one hop. */
function connectionTokens(headers: Headers): Set<string> {
  const tokens = new Set<string>();
  for (const token of (headers.get("connection") ?? "").split(",")) {
    const name = token.trim().toLowerCase();
    if (name) tokens.add(name);
  }
  return tokens;
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

export function isSafeMethod(method: string): boolean {
  return SAFE_METHODS.has(method.toUpperCase());
}

/**
 * `host[:port]`, lowercased, with the scheme's default port dropped — the
 * `Host` header omits it and `new URL(...).host` does too, but a hand-written
 * `Host: example.com:443` must still match `https://example.com`.
 */
function normalizeHost(host: string, scheme: string): string {
  const lower = host.trim().toLowerCase();
  if (scheme === "http:" && lower.endsWith(":80")) return lower.slice(0, -3);
  if (scheme === "https:" && lower.endsWith(":443")) return lower.slice(0, -4);
  return lower;
}

export type SameOriginVerdict = { ok: true } | { ok: false; message: string };

/**
 * The CSRF decision for one request, made here because the API cannot make it
 * any more.
 *
 * The API's own check (`requireSameOrigin`, backend/internal/api/server.go)
 * compares the browser's `Origin` against `TF_ALLOWED_ORIGINS`. Behind this
 * proxy the browser's origin *is* the web UI's, which the operator no longer
 * has to list — so the proxy performs the equivalent check against the one
 * origin it knows is legitimate, its own, and {@link forwardRequestHeaders}
 * then drops `Origin` / `Referer`. The API sees what it already treats as a
 * non-browser caller (no Origin at all: curl, huggingface_hub, a Server
 * Component forwarding a cookie) and lets it through, which is sound because
 * the only party able to tell a hostile page from the UI has already done so.
 *
 * The rules, for unsafe methods only (GET / HEAD / OPTIONS change nothing):
 *
 * - `Sec-Fetch-Site: cross-site` is refused outright. Every current browser
 *   sends it, and a hostile site cannot suppress it.
 * - `Origin`, or failing that the `Referer`'s origin, must name this host. An
 *   `Origin` that is present but unusable (`null` from a sandboxed frame or a
 *   `file:` page, or garbage) is refused. An unparsable `Referer` is ignored,
 *   exactly as the API's `requestOrigin` ignores one.
 * - Neither header at all passes: that is not a browser (every browser attaches
 *   `Origin` to a cross-site POST, form submissions included), and the API
 *   makes the same call.
 *
 * "This host" is the `Host` header, or any entry of `X-Forwarded-Host`. The
 * latter is there for a reverse proxy in front of the web UI that rewrites
 * `Host` to the upstream's name (nginx's default `proxy_pass` does), which
 * would otherwise refuse every write. Accepting it does not open a hole,
 * because the attacker this check exists for is a hostile *page* driving the
 * victim's browser, and such a page cannot put an `X-Forwarded-Host` on a
 * request: it is not a CORS-safelisted header, so a form cannot send it and a
 * `fetch` that sets it is preflighted — and the preflight (OPTIONS, forwarded
 * with its `Origin` intact) is answered by the API's CORS middleware, which
 * grants nothing to an origin outside `TF_ALLOWED_ORIGINS`, so the real request
 * is never sent. A non-browser client can forge the header freely, but it
 * holds no one else's cookie to ride.
 */
export function checkSameOrigin(method: string, headers: Headers): SameOriginVerdict {
  if (isSafeMethod(method)) return { ok: true };

  if (headers.get("sec-fetch-site")?.trim().toLowerCase() === "cross-site") {
    return { ok: false, message: "cross-site request refused" };
  }

  let source: URL | undefined;
  const origin = headers.get("origin");
  if (origin !== null) {
    try {
      source = new URL(origin);
    } catch {
      return { ok: false, message: "cross-origin request refused" };
    }
    // `null`, `file://`, and anything else without a host of its own.
    if (!source.host) return { ok: false, message: "cross-origin request refused" };
  } else {
    const referer = headers.get("referer");
    if (referer) {
      try {
        const parsed = new URL(referer);
        if (parsed.host) source = parsed;
      } catch {
        // Ignored, as requestOrigin in server.go ignores it.
      }
    }
  }
  if (!source) return { ok: true };

  const expected = normalizeHost(source.host, source.protocol);
  const candidates = [
    headers.get("host") ?? "",
    ...(headers.get("x-forwarded-host") ?? "").split(","),
  ];
  for (const candidate of candidates) {
    if (candidate.trim() && normalizeHost(candidate, source.protocol) === expected) {
      return { ok: true };
    }
  }
  return { ok: false, message: "cross-origin request refused" };
}

/**
 * The request headers sent upstream: everything the browser sent, minus
 *
 * - hop-by-hop headers (and whatever `Connection` nominates), which describe
 *   the browser→web connection and not the web→API one;
 * - `Host`, which `fetch` sets from the upstream URL;
 * - `Expect`, which undici refuses outright (curl sends `Expect: 100-continue`
 *   on any large upload, and Node's HTTP server has already answered it);
 * - `Origin` / `Referer` on unsafe methods, once {@link checkSameOrigin} has
 *   passed — see there for why dropping them preserves the API's guarantee.
 *   On safe methods they are kept, so a preflight still reaches the API's CORS
 *   middleware with the origin it has to judge.
 *
 * `Accept-Encoding` is pinned to `identity`: `fetch` decodes a compressed body
 * on its own and there is no way to ask it not to, so a compressed hop would
 * only be decoded here and sent on uncompressed anyway — with a
 * `Content-Length` and a `Content-Range` that no longer describe it.
 *
 * The browser's address: a route handler never sees the client socket, and
 * Next.js fills `X-Forwarded-For` with the TCP peer *only when the client sent
 * none* (`??=` in its base server), so that header proves nothing. The
 * production server (`server.ts`) pins the socket peer into
 * `X-TF-Client-Addr` before Next.js runs (`lib/client-addr.ts`); that header
 * is forwarded only when this process is that server (`edge`), together with
 * `TF_WEB_PROXY_SECRET` when one is configured, and dropped otherwise — so a
 * client can never supply it. The API believes it only from the web tier
 * (`backend/internal/api/webproxy.go`). `X-Forwarded-For` itself is forwarded
 * as Next.js computed it, for `TF_TRUST_PROXY_IPS` deployments.
 * `X-Forwarded-Proto` / `-Host` are forwarded the same way, derived from the
 * request when Next.js has not set them.
 */
export function forwardRequestHeaders(request: Request, edge: WebEdge = webEdgeFromEnv()): Headers {
  const incoming = request.headers;
  const hop = connectionTokens(incoming);
  const out = new Headers();
  const unsafe = !isSafeMethod(request.method);
  incoming.forEach((value, name) => {
    if (isHopByHop(name, hop)) return;
    if (name === "host" || name === "expect" || name === "accept-encoding") return;
    if (name === CLIENT_ADDR_HEADER || name === PROXY_SECRET_HEADER) return;
    if (unsafe && (name === "origin" || name === "referer")) return;
    out.set(name, value);
  });
  out.set("accept-encoding", "identity");
  const clientAddr = edge.pinned ? incoming.get(CLIENT_ADDR_HEADER) : null;
  if (clientAddr) {
    out.set(CLIENT_ADDR_HEADER, clientAddr);
    if (edge.proxySecret) out.set(PROXY_SECRET_HEADER, edge.proxySecret);
  }

  const url = new URL(request.url);
  if (!out.has("x-forwarded-proto")) out.set("x-forwarded-proto", url.protocol.replace(/:$/, ""));
  if (!out.has("x-forwarded-host")) out.set("x-forwarded-host", incoming.get("host") ?? url.host);
  return out;
}

// What `fetch` (undici, and Bun's) decodes transparently. An unknown coding
// anywhere in the list stops it from decoding at all, and the body then
// arrives exactly as the API sent it.
const DECODED_CODINGS = new Set(["gzip", "x-gzip", "deflate", "br"]);

/** True when `fetch` has already decoded the body `Content-Encoding` describes. */
export function bodyWasDecoded(contentEncoding: string | null): boolean {
  if (!contentEncoding) return false;
  const codings = contentEncoding
    .split(",")
    .map((c) => c.trim().toLowerCase())
    .filter((c) => c && c !== "identity");
  return codings.length > 0 && codings.every((c) => DECODED_CODINGS.has(c));
}

/**
 * Point a redirect that names the API's own origin back at the web origin, so
 * the browser follows it through this proxy instead of trying to reach an
 * address (`http://api:8080`) that only exists inside the deployment. Anything
 * else — a relative `Location`, a GCS signed URL — is passed through untouched.
 */
export function rewriteLocation(location: string, base: string): string {
  if (!/^[a-z][a-z0-9+.-]*:/i.test(location) && !location.startsWith("//")) return location;
  let target: URL;
  let upstream: URL;
  try {
    target = new URL(location, base);
    upstream = new URL(base);
  } catch {
    return location;
  }
  if (target.origin !== upstream.origin) return location;
  const path = `${target.pathname}${target.search}${target.hash}`;
  // A rewritten path that itself starts with `//` (or `/\`, which a special
  // scheme's URL parser folds into `//` before we ever see it — confirmed
  // above by `target.pathname`) is a network-path reference: browsers treat
  // a `Location` shaped like that as protocol-relative and navigate to
  // whatever host follows, exactly the redirect this rewrite exists to
  // prevent (e.g. an upstream `Location: http://api:8080//evil.com/x` would
  // otherwise rewrite to `//evil.com/x`). Collapse any leading run of slashes
  // to one so the result can only ever be a same-origin path.
  return path.replace(/^\/+/, "/");
}

/**
 * The response headers sent back to the browser: the API's, minus hop-by-hop
 * ones, with every `Set-Cookie` kept as its own header line (joining them the
 * way `Headers.get` does corrupts any cookie whose `Expires` has a comma in
 * it), `Location` rewritten by {@link rewriteLocation}, `WWW-Authenticate`
 * dropped, and — when `fetch` already decoded the body — without the
 * `Content-Encoding` and `Content-Length` that described the encoded bytes.
 *
 * `WWW-Authenticate` is the API's Basic-auth challenge (§1.5 of
 * docs/dev/agent-features.md, `TF_REQUIRE_AUTH_FOR_READ`): the API is a
 * legitimate target for it — `git`, `git-lfs` and `huggingface_hub` all
 * understand Basic — but on the Web UI's own origin it would instead pop the
 * browser's native credential prompt over the app's own login page, and
 * whatever the visitor types into that prompt is cached by the browser and
 * silently re-attached to later same-origin requests. The Web UI reads
 * `apiFetch`'s JSON error body (`ApiErrorBody.error.type ===
 * "authentication_required"`, invariant 3) to detect this, not the header, so
 * dropping it costs nothing.
 */
export function forwardResponseHeaders(upstream: Headers, base: string): Headers {
  const hop = connectionTokens(upstream);
  const decoded = bodyWasDecoded(upstream.get("content-encoding"));
  const out = new Headers();
  upstream.forEach((value, name) => {
    if (isHopByHop(name, hop) || name === "set-cookie" || name === "www-authenticate") return;
    if (decoded && (name === "content-encoding" || name === "content-length")) return;
    out.set(name, name === "location" ? rewriteLocation(value, base) : value);
  });
  for (const cookie of upstream.getSetCookie()) out.append("set-cookie", cookie);
  return out;
}

/** An error in the API's own `ApiErrorBody` shape, so `apiFetch` parses it like any other. */
export function proxyErrorResponse(status: number, type: string, message: string): Response {
  const body: ApiErrorBody = { error: { message, type } };
  return Response.json(body, { status, headers: { "cache-control": "no-store" } });
}

// Statuses whose response must not carry a body; `new Response(body, ...)`
// throws on them if one is passed.
const NULL_BODY_STATUSES = new Set([101, 103, 204, 205, 304]);

export type ProxyOptions = {
  /** Defaults to {@link upstreamBase} of `process.env`, read on every call. */
  base?: string;
  fetchImpl?: typeof fetch;
};

/**
 * Forward one request to the API and stream the answer back.
 *
 * Neither body is buffered: the request body is handed to `fetch` as the
 * stream it arrived as (`duplex: "half"`, which is what lets a multi-gigabyte
 * Web UI upload pass through in constant memory), and the response body is
 * returned as the stream `fetch` produced. Redirects are not followed
 * (`redirect: "manual"`) — a resolve of an LFS file answers 302 to a signed
 * URL, and that is for the browser to follow, not for this server to download.
 *
 * Never throws: an API that cannot be reached becomes a 502 in the API's error
 * shape, so `apiFetch` degrades the way it would for any other failure.
 *
 * Refuses with a 404 in the same shape when the request's own (normalised)
 * pathname falls outside {@link isConfinedUpstreamPath} — see there for why.
 */
export async function proxyRequest(
  request: Request,
  options: ProxyOptions = {},
): Promise<Response> {
  const verdict = checkSameOrigin(request.method, request.headers);
  if (!verdict.ok) return proxyErrorResponse(403, "forbidden", verdict.message);

  const requestPathname = new URL(request.url).pathname;
  if (!isConfinedUpstreamPath(requestPathname)) {
    return proxyErrorResponse(404, "not_found", "no such route");
  }

  const base = options.base ?? upstreamBase();
  const doFetch = options.fetchImpl ?? fetch;
  const method = request.method.toUpperCase();
  const hasBody = method !== "GET" && method !== "HEAD" && request.body !== null;

  let upstream: Response;
  try {
    upstream = await doFetch(upstreamUrl(request.url, base), {
      method,
      headers: forwardRequestHeaders(request),
      body: hasBody ? request.body : undefined,
      // Required by the fetch spec for a stream body; not in lib.dom's RequestInit yet.
      ...(hasBody ? { duplex: "half" } : {}),
      redirect: "manual",
      signal: request.signal,
      cache: "no-store",
    } as RequestInit);
  } catch (err) {
    // The detail stays in the server log: it names the internal API address,
    // which is nobody's business in the browser.
    console.error(`api proxy: ${method} ${new URL(request.url).pathname} failed:`, err);
    return proxyErrorResponse(502, "bad_gateway", "the web UI could not reach the API server");
  }

  const headers = forwardResponseHeaders(upstream.headers, base);
  const body = NULL_BODY_STATUSES.has(upstream.status) || method === "HEAD" ? null : upstream.body;
  return new Response(body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers,
  });
}

/**
 * Whether the client address header on an incoming request was written by
 * this process's own server (`server.ts` sets {@link WEB_EDGE_ENV}), and the
 * secret that vouches for it to the API.
 */
export type WebEdge = { pinned: boolean; proxySecret?: string };

export function webEdgeFromEnv(env: Record<string, string | undefined> = process.env): WebEdge {
  return {
    pinned: env[WEB_EDGE_ENV] === "1",
    proxySecret: env.TF_WEB_PROXY_SECRET || undefined,
  };
}
