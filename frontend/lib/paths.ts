import type { RepoKind } from "@/types/api";

/** Public API origin as seen from the browser (resolve URLs, images, downloads). */
export function publicApiBase(): string {
  return process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";
}

/** Encode each path segment but keep slashes as directory separators. */
export function encodePathSegments(path: string): string {
  return path
    .split("/")
    .filter((s) => s.length > 0)
    .map(encodeURIComponent)
    .join("/");
}

export function repoBase(kind: RepoKind, ns: string, name: string): string {
  return `/${kind}s/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`;
}

export function repoTreeHref(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path = "",
): string {
  const base = `${repoBase(kind, ns, name)}/tree/${encodeURIComponent(rev)}`;
  const suffix = encodePathSegments(path);
  return suffix ? `${base}/${suffix}` : base;
}

export function repoCommitsHref(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path = "",
): string {
  const base = `${repoBase(kind, ns, name)}/commits/${encodeURIComponent(rev)}`;
  return path ? `${base}?path=${encodeURIComponent(path)}` : base;
}

export function repoBlobHref(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string,
): string {
  return `${repoBase(kind, ns, name)}/blob/${encodeURIComponent(rev)}/${encodePathSegments(path)}`;
}

export function repoEditHref(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string,
): string {
  return `${repoBase(kind, ns, name)}/edit/${encodeURIComponent(rev)}/${encodePathSegments(path)}`;
}

export function repoViewerHref(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string,
): string {
  return `${repoBase(kind, ns, name)}/viewer/${encodeURIComponent(rev)}/${encodePathSegments(path)}`;
}

/**
 * Next.js hands dynamic route params over as the **raw URL segments**: a
 * directory called `dir with spaces` arrives as `dir%20with%20spaces`. Every
 * `lib/repos.ts` helper percent-encodes what it is given, so passing a param
 * straight through double-encodes it (`%2520`) and the backend answers 404.
 * Decode at the page boundary instead, so the rest of the app only ever deals
 * in real names. A malformed escape is passed through unchanged.
 */
export function decodeSegment(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/** {@link decodeSegment} for a catch-all route's path segments. */
export function decodeSegments(path: string[] | undefined): string[] {
  return (path ?? []).map(decodeSegment);
}

/**
 * {@link decodeSegment} applied to a whole `params` object, so a page can undo
 * the encoding in one line and hand real names to everything downstream.
 */
export function decodeRouteParams<T extends Record<string, string | string[] | undefined>>(
  params: T,
): T {
  const decoded: Record<string, string | string[] | undefined> = {};
  for (const [key, value] of Object.entries(params)) {
    decoded[key] =
      value === undefined
        ? undefined
        : Array.isArray(value)
          ? value.map(decodeSegment)
          : decodeSegment(value);
  }
  return decoded as T;
}

/**
 * Resolves what "Create a new file" will actually create: the typed path is
 * relative to the directory being browsed, the way GitHub's own "Add file" is.
 * Creating `README.md` from inside `docs/` therefore makes `docs/README.md` —
 * being sent to the repository root from a directory you are looking at would
 * be the more surprising of the two behaviours.
 *
 * A leading slash is stripped rather than treated as "from the root": it is a
 * typo, not an escape hatch, and silently rooting the path is exactly the
 * ambiguity this function exists to remove. The dialog shows the resolved
 * path back to the user as they type.
 */
export function resolveNewFilePath(dir: string[], typed: string): string {
  const relative = typed.trim().replace(/^\/+/, "").replace(/\/+/g, "/").replace(/\/+$/, "");
  if (!relative) return "";
  return [...dir, relative].join("/");
}
