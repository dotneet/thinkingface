/**
 * Namespaces — a user or an organisation — as the UI sees them
 * (docs/dev/namespace-design.md).
 *
 * `/[ns]` is the one profile page for both kinds. Next.js matches static
 * segments (`/models`, `/datasets`, …) before the dynamic `[ns]` one, so the
 * route cannot shadow an existing page; `isReservedNamespace` is a second
 * line of defence for a future top-level route that is added without a
 * matching entry in the reserved list (frontend/scripts/check-ui.mjs checks
 * that the list and `app/` agree).
 *
 * Every fetch follows the `apiFetch` contract — never throws, returns an
 * `ApiResult<T>` (CLAUDE.md invariant 3) — and takes `FetchOpts` so a Server
 * Component can forward the `tf_session` cookie with `authHeaders()`
 * (invariant 2).
 */
import { type ApiResult, apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import { isReservedNamespaceName } from "@/lib/validation";
import type {
  NamespaceProfile,
  NamespaceProfileUpdate,
  NamespaceResponse,
  User,
} from "@/types/api";

export function isReservedNamespace(ns: string): boolean {
  return isReservedNamespaceName(ns);
}

/** URL of a namespace's profile page, for users and organisations alike. */
export function namespaceHref(ns: string): string {
  return `/${encodeURIComponent(ns)}`;
}

export type NamespaceTab = "models" | "datasets" | "experiments" | "members";

/**
 * Falls back to "models" (HF's own default) for anything unrecognised, and
 * for "members" unless the namespace is an organisation — a user namespace
 * has no member list.
 */
export function parseNamespaceTab(
  value: string | undefined,
  kind?: NamespaceProfile["kind"],
): NamespaceTab {
  if (value === "datasets" || value === "experiments") return value;
  if (value === "members" && kind === "org") return value;
  return "models";
}

/**
 * One namespace's public profile and counts. 200 for every existing
 * namespace (an account with zero repositories included), 404 for a name
 * nobody holds or a reserved one. `namespace.name` carries the canonical
 * spelling — compare it with the URL segment and redirect when they differ.
 */
export function getNamespace(ns: string, opts?: FetchOpts): Promise<ApiResult<NamespaceResponse>> {
  return apiFetch<NamespaceResponse>(`/api/v1/namespaces/${encodeURIComponent(ns)}`, {
    headers: opts?.headers,
  });
}

/** Update the signed-in user's own profile (display name, bio, website, avatar). */
export function updateMyProfile(
  req: NamespaceProfileUpdate,
  opts?: FetchOpts,
): Promise<ApiResult<NamespaceResponse>> {
  return apiFetch<NamespaceResponse>("/api/v1/me/profile", {
    method: "PATCH",
    body: req,
    headers: opts?.headers,
  });
}

/** URL of one tab on a namespace page; "models" is the bare page. */
export function namespaceTabHref(ns: string, tab: NamespaceTab): string {
  const base = namespaceHref(ns);
  return tab === "models" ? base : `${base}?tab=${tab}`;
}

/**
 * A profile `website` the UI may put in an `<a href>`: only http(s). The API
 * rejects anything else on write, but rows written before that check existed
 * may still hold e.g. a `javascript:` URL, so the renderer guards too.
 */
export function safeExternalHref(url: string): string | null {
  const lower = url.trim().toLowerCase();
  return lower.startsWith("http://") || lower.startsWith("https://") ? url.trim() : null;
}

/** True when the viewer may edit this namespace's profile / settings. */
export function canEditNamespace(profile: NamespaceProfile): boolean {
  return profile.can_edit;
}

/**
 * True for the two roles that grant write access to a namespace ("admin" and
 * "write"; "read" and "" do not). This is the single predicate behind
 * `canCreateInNamespace` and `writableNamespaces` below -- both check the
 * same bar the backend applies wherever the destination only needs write,
 * e.g. `startTransfer`'s `destRole >= RoleWrite`
 * (backend/internal/api/transfers.go).
 */
function hasWriteRole(role: string): boolean {
  return role === "admin" || role === "write";
}

/**
 * True when the viewer may create repositories here *through the UI*: the
 * namespace appears in their own `/api/v1/me` list with write or admin
 * (their user namespace, or an organisation they belong to at that level).
 * Drives the "create the first repository" call to action on an empty
 * namespace page, whose link opens `/new?ns=` -- and CreateRepoForm can only
 * preselect namespaces from that same list. Deciding from `viewer_role` would
 * mis-fire for a site admin, who is "admin" everywhere (roleIn) but whose
 * `/me` does not list other people's namespaces; the form would then fall
 * back to their own namespace and create the repository in the wrong place.
 */
export function canCreateInNamespace(profile: NamespaceProfile, me: User | null): boolean {
  if (!me) return false;
  const target = profile.name.toLowerCase();
  const mine = me.namespaces.find((n) => n.name.toLowerCase() === target);
  return mine !== undefined && hasWriteRole(mine.role);
}

/**
 * The subset of the viewer's own `/api/v1/me` namespaces they can actually
 * create in / write to (role "admin" or "write") -- `canCreateInNamespace`'s
 * predicate applied across the whole list instead of one profile.
 *
 * `/api/v1/me`'s `user.namespaces` is *not* pre-filtered by role: the
 * backend's `NamespacesForUser` (backend/internal/store/namespaces.go) lists
 * every namespace the user owns or is a member of, "read" memberships
 * included. A picker that offers repository creation or a transfer
 * destination must filter through this (or `hasWriteRole` directly) rather
 * than using `user.namespaces` as-is, or a read-only member gets an option
 * that 400s/403s only once they submit.
 */
export function writableNamespaces(user: User): User["namespaces"] {
  return user.namespaces.filter((n) => hasWriteRole(n.role));
}
