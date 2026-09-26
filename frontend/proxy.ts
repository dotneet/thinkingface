import { type NextRequest, NextResponse } from "next/server";

import { apiBaseUrl } from "@/lib/api";
import {
  createServerInfoGate,
  fetchServerInfo,
  isGateExempt,
  isNavigationMethod,
  loginRedirectPath,
  SESSION_COOKIE,
  serverInfoUrl,
} from "@/lib/auth-gate";

/**
 * TF_REQUIRE_AUTH_FOR_READ (docs/dev/agent-features.md §1.5): when the API
 * refuses anonymous reads, a signed-out visitor is sent to `/login?next=…`
 * instead of landing on a page made of error states. The rules live in
 * `lib/auth-gate.ts`; this only applies them.
 *
 * Only the *absence* of the session cookie is looked at. A stale cookie gets
 * through and the page shows the API's 401s, as it always has — validating the
 * cookie here would cost a round trip on every navigation.
 */
const requiresLogin = createServerInfoGate({
  // Resolved per lookup, not at module load: API_URL is a runtime setting of
  // the web container.
  fetchInfo: () => fetchServerInfo(serverInfoUrl(apiBaseUrl()))(),
});

export async function proxy(request: NextRequest) {
  const { pathname, search } = request.nextUrl;
  if (
    !isNavigationMethod(request.method) ||
    isGateExempt(pathname) ||
    request.cookies.has(SESSION_COOKIE)
  ) {
    return NextResponse.next();
  }
  if (!(await requiresLogin())) return NextResponse.next();
  return NextResponse.redirect(new URL(loginRedirectPath(pathname, search), request.url));
}

export const config = {
  // Keeps the proxy off asset requests and, above all, off the routes that
  // stream to the API — `/api/*` and the `/{models,datasets}/*/*/resolve/*`
  // downloads: a request the proxy matches has its body buffered (and cut at
  // proxyClientMaxBodySize). isGateExempt repeats the same rules and stays the
  // authority for whatever the matcher lets through (e.g. a bare `/api`).
  matcher: [
    "/((?!_next/static|_next/image|api/|duckdb/|favicon.ico|icon.svg|robots.txt|(?:models|datasets)/[^/]+/[^/]+/resolve/).*)",
  ],
};
