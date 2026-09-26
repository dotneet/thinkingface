/**
 * The decision `proxy.ts` makes for `TF_REQUIRE_AUTH_FOR_READ`
 * (docs/dev/agent-features.md §1.5): send a signed-out visitor to
 * `/login?next=…` instead of letting every page render as an error state.
 *
 * Kept free of `next/server` so the rules are unit-testable on their own; the
 * proxy only wires them to the request.
 *
 * It is an optimistic check, not an access control: the API refuses the
 * anonymous reads itself. So it errs toward *not* redirecting — a missing or
 * failed server-info answer lets the page render (and degrade the way it
 * always has), rather than locking everyone out because the backend is down.
 */

import type { ServerInfo } from "@/types/api";

import { loginHref } from "./validation";

/** The session cookie the API issues (`auth.SessionCookieName`). */
export const SESSION_COOKIE = "tf_session";

/**
 * Static files served from `public/` or by the App Router's file conventions.
 * Matched exactly or as a directory prefix — never by "has a dot in it",
 * since repository file pages (`/alice/bert/blob/main/config.json`) do.
 */
const EXEMPT_FILES = new Set(["/favicon.ico", "/icon.svg", "/robots.txt", "/sitemap.xml"]);
const EXEMPT_PREFIXES = ["/_next/", "/duckdb/", "/.well-known/"];

/**
 * `app/{models,datasets}/[ns]/[name]/resolve/[...path]/route.ts`: browser
 * downloads proxied to the API's HF resolve paths. Like `/api/`, the API
 * answers these itself (a Bearer-token curl or an `<img>` must reach it), and
 * the `matcher` in `proxy.ts` keeps them out as well so their bodies are never
 * buffered by the proxy layer.
 */
const RESOLVE_ROUTE = /^\/(?:models|datasets)\/[^/]+\/[^/]+\/resolve(?:\/|$)/;

/**
 * Whether the gate leaves this path alone whatever the server says: the
 * sign-in page itself (it holds both the login and the sign-up form), the
 * same-origin API proxy under `/api/` and the resolve download routes (the
 * API answers for itself there, and a redirect would turn a JSON 401 into an
 * HTML page), Next.js internals and static assets.
 */
export function isGateExempt(pathname: string): boolean {
  if (pathname === "/login" || pathname.startsWith("/login/")) return true;
  if (pathname === "/api" || pathname.startsWith("/api/")) return true;
  if (RESOLVE_ROUTE.test(pathname)) return true;
  if (EXEMPT_FILES.has(pathname)) return true;
  return EXEMPT_PREFIXES.some((prefix) => pathname.startsWith(prefix));
}

/** Only page loads are redirected; anything else is not a navigation. */
export function isNavigationMethod(method: string): boolean {
  return method === "GET" || method === "HEAD";
}

/**
 * Where a signed-out visitor to `pathname` + `search` is sent: `/login` with
 * the page they asked for as `next`. The login form reads it back through
 * `safeRedirectPath`, which is what keeps it on this origin.
 */
export function loginRedirectPath(pathname: string, search: string): string {
  return loginHref(pathname, search);
}

/** The server-info URL under an API base URL (`apiBaseUrl()` on the server). */
export function serverInfoUrl(apiBase: string): string {
  return `${apiBase.replace(/\/$/, "")}/api/v1/server-info`;
}

export type ServerInfoGateOptions = {
  /** Resolves to the server's answer, or null when it could not be had. */
  fetchInfo: () => Promise<ServerInfo | null>;
  /** Milliseconds clock; injectable for tests. */
  now?: () => number;
  /** How long a successful answer is reused. */
  ttlMs?: number;
  /** How long a failed lookup is remembered, so a down backend is not asked on every request. */
  failureTtlMs?: number;
};

/**
 * Memoises whether the server requires sign-in for reads. The answer only
 * changes when the API restarts with a different environment, so it is held
 * for `ttlMs` (30 s) in module memory; concurrent callers share one in-flight
 * lookup. A failure is held for `failureTtlMs` (5 s) as "no".
 */
export function createServerInfoGate({
  fetchInfo,
  now = Date.now,
  ttlMs = 30_000,
  failureTtlMs = 5_000,
}: ServerInfoGateOptions): () => Promise<boolean> {
  let cached: { value: boolean; expires: number } | null = null;
  let inflight: Promise<boolean> | null = null;

  return async function requiresLogin(): Promise<boolean> {
    if (cached && now() < cached.expires) return cached.value;
    if (inflight) return inflight;
    inflight = (async () => {
      let info: ServerInfo | null = null;
      try {
        info = await fetchInfo();
      } catch {
        info = null;
      }
      const value = info?.require_auth_for_read === true;
      cached = { value, expires: now() + (info ? ttlMs : failureTtlMs) };
      return value;
    })();
    try {
      return await inflight;
    } finally {
      inflight = null;
    }
  };
}

/** `fetchInfo` for the real server: never throws, gives up after `timeoutMs`. */
export function fetchServerInfo(url: string, timeoutMs = 2_000): () => Promise<ServerInfo | null> {
  return async () => {
    try {
      const res = await fetch(url, {
        cache: "no-store",
        signal: AbortSignal.timeout(timeoutMs),
      });
      if (!res.ok) return null;
      const body: unknown = await res.json();
      if (typeof body !== "object" || body === null) return null;
      return body as ServerInfo;
    } catch {
      return null;
    }
  };
}
