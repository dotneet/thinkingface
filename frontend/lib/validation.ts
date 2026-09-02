/**
 * Client-side mirrors of the server's input rules. These exist so the user
 * finds out about a bad name while typing instead of after a round trip.
 *
 * The name rule is `backend/internal/api/repos.go`'s `validateName` — keep the
 * two in sync. Unlike the Go side this returns an error *code* instead of an
 * English message: the caller maps the code onto a localized message in its
 * own i18n namespace (e.g. `auth.errors.usernameInvalid`).
 */

/** `^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$` — same expression as the Go side. */
const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/;

/** Why a name would be rejected by the API. */
export type NameError = "required" | "invalid" | "gitSuffix";

/**
 * Returns an error code when `name` would be rejected by the API, or null
 * when it is acceptable. Mirrors `validateName` in backend/internal/api/repos.go,
 * which guards both repository names and sign-up usernames.
 */
export function validateName(name: string): NameError | null {
  if (name === "") return "required";
  if (!NAME_RE.test(name)) return "invalid";
  if (name.endsWith(".git")) return "gitSuffix";
  return null;
}

/**
 * Names a new namespace (a sign-up username or an organisation) may not take,
 * because they collide with a front-end route under `app/`, with the backend's
 * `/{ns}/{name}` routes, or with the HF-compatible `/datasets/{ns}/{name}`
 * prefix (docs/dev/organization-design.md §6.3).
 *
 * MIRROR of `reservedNames` in `backend/internal/api/names.go` — the server is
 * authoritative and rejects with `{"error":{"type":"reserved_name"}}`; this
 * copy only saves a round trip. Keep the two lists in sync.
 *
 * Repository names are deliberately NOT checked against this: `alice/models`
 * is legal.
 */
export const RESERVED_NAMESPACE_NAMES: readonly string[] = [
  "api",
  "apis",
  "datasets",
  "models",
  "spaces",
  "experiments",
  "orgs",
  "organizations",
  "settings",
  "new",
  "login",
  "logout",
  "signup",
  "styleguide",
  "healthz",
  "static",
  "_next",
  "assets",
  "raw",
  "resolve",
  "lfs",
  "info",
  "git",
  "webhooks",
  "transfers",
  "me",
  "whoami-v2",
  // Frontend-only assets and routes (docs/dev/namespace-design.md §9).
  "favicon.ico",
  "robots.txt",
  "sitemap.xml",
  "duckdb",
  "public",
  "users",
  "namespaces",
  "profile",
  "search",
];

const RESERVED_SET = new Set(RESERVED_NAMESPACE_NAMES);

/**
 * True when `name` (any case) is on the reserved list. Shared by the
 * sign-up / organisation validation below and by the `/[ns]` route's
 * defence-in-depth check (lib/namespace.ts).
 */
export function isReservedNamespaceName(name: string): boolean {
  return RESERVED_SET.has(name.toLowerCase());
}

/** `validateName`'s codes plus the namespace-only "this name is taken by a route" one. */
export type NamespaceNameError = NameError | "reserved";

/**
 * Like {@link validateName}, plus the reserved-name check the API applies when
 * a *namespace* is being created (sign-up username, organisation name).
 * Comparison is case-insensitive because namespace names are matched that way
 * by the routes they would collide with.
 */
export function validateNamespaceName(name: string): NamespaceNameError | null {
  const base = validateName(name);
  if (base) return base;
  if (isReservedNamespaceName(name)) return "reserved";
  return null;
}

/**
 * The `/login?next=…` URL that sends the user back to the page they are on.
 *
 * `usePathname()` alone is not that page: it drops the query string, so the
 * header's "Log in" link used to send someone reading
 * `/datasets?search=bert&tags=nlp` back to a bare `/datasets` with every
 * filter cleared. `safeRedirectPath` already preserves `url.search`, so the
 * only thing missing was passing it in — the caller hands over
 * `useSearchParams().toString()`.
 *
 * `/login` itself never becomes its own `next`, and neither does an empty
 * pathname (the router has not resolved one yet).
 */
export function loginHref(pathname: string | null | undefined, search?: string | null): string {
  if (!pathname?.startsWith("/") || pathname === "/login") return "/login";
  const query = search ? (search.startsWith("?") ? search : `?${search}`) : "";
  return `/login?next=${encodeURIComponent(`${pathname}${query}`)}`;
}

/**
 * Narrows a caller-supplied `?next=` value to a path on this origin.
 *
 * Prefix checks alone are not enough: browsers follow the WHATWG URL parser,
 * which treats a backslash exactly like a forward slash when deciding where the
 * authority starts. That makes `/\evil.com` resolve to `http://evil.com`, so a
 * naive `startsWith("/") && !startsWith("//")` test lets an open redirect
 * through. Parsing against a known base and comparing origins uses the same
 * rules the browser will, and returning only the path component discards any
 * host the input tried to smuggle in.
 */
export function safeRedirectPath(next: string | null | undefined, fallback = "/"): string {
  if (!next) return fallback;
  const base = "http://redirect.invalid";
  let url: URL;
  try {
    url = new URL(next, base);
  } catch {
    return fallback;
  }
  if (url.origin !== base) return fallback;
  const path = `${url.pathname}${url.search}${url.hash}`;
  return path.startsWith("/") ? path : fallback;
}
