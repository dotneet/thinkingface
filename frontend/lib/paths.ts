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
 * Why a typed path cannot be used. Each maps to a segment git itself refuses
 * (`gitrepo.validatePath`, backend/internal/gitrepo/commit.go).
 */
export type NewFilePathIssue = "relativeSegment" | "gitDirectory";

/**
 * The outcome of resolving what "Create a new file" would create.
 *
 * Three states, not two: nothing typed yet is not the same as typed something
 * unusable (DESIGN.md §9). The dialog keeps Create disabled for both but says
 * something different about each — a disabled button with no stated reason is
 * the failure mode this type exists to prevent.
 */
export type NewFilePathResult =
  | { status: "empty" }
  | { status: "invalid"; issue: NewFilePathIssue }
  | { status: "ok"; path: string };

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
 *
 * **`.`, `..` and `.git` segments are refused here, and the backend is still
 * the authority on them.** This is not a second line of validation — the
 * commit path re-checks every path with `gitrepo.ValidatePath` and nothing
 * that gets past this function can write outside the tree. It is about the
 * dialog not making a promise it cannot keep:
 *
 * - The resolved path is shown to the user *and* pushed as a URL, and the two
 *   disagree the moment it contains `..`. `docs/../x.md` renders in the hint
 *   as written, while the router resolves the pushed URL to `…/edit/main/x.md`
 *   and the editor opens on a file at the repository root. Shown one path,
 *   given another, with the file eventually created somewhere the dialog never
 *   named. A bare `.` or `..` collapses the URL down to a path with no file
 *   segment at all, which matches no route.
 * - `.git` survives URL resolution intact, so it fails differently: the editor
 *   opens happily and the commit is refused afterwards, which is a dead end
 *   reached only after the user has typed a whole file.
 *
 * Both are answered here, before navigation, with a reason the dialog renders.
 */
export function resolveNewFilePath(dir: string[], typed: string): NewFilePathResult {
  const relative = typed.trim().replace(/^\/+/, "").replace(/\/+/g, "/").replace(/\/+$/, "");
  if (!relative) return { status: "empty" };

  for (const segment of relative.split("/")) {
    if (segment === "." || segment === "..") {
      return { status: "invalid", issue: "relativeSegment" };
    }
    // toLowerCase, never toLocaleLowerCase: the latter maps "I" to a dotless
    // "ı" in a Turkish locale, so ".GIT" would stop matching for exactly the
    // users whose filesystem is just as case-insensitive as everyone else's.
    // git compares this fold-insensitively too (strings.EqualFold).
    if (segment.toLowerCase() === ".git") {
      return { status: "invalid", issue: "gitDirectory" };
    }
  }
  return { status: "ok", path: [...dir, relative].join("/") };
}
