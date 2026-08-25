import { type ApiResult, apiFetch } from "@/lib/api";
import type {
  CommitDiffResponse,
  CommitListResponse,
  RawFileResponse,
  RefsResponseUI,
  RepoDetail,
  RepoGCSResponse,
  RepoKind,
  RepoListResponse,
  TreeResponseUI,
} from "@/types/api";

export type RepoListParams = {
  kind?: RepoKind;
  /** Legacy substring match (name/namespace/description ILIKE). */
  q?: string;
  /** Full text search: tsquery-based, prefix matching. */
  search?: string;
  author?: string;
  /** Single tag, kept for old links (e.g. the tag badges on a repo page). */
  tag?: string;
  /** Multiple tags, AND-combined; what the facet sidebar sends. */
  tags?: string[];
  license?: string;
  task?: string;
  /**
   * "ns/name" of a base model: only its derivatives. Any "@rev" is ignored,
   * so a revision-pinned edge still matches (docs/dev/api-contract.md §2).
   */
  base_model?: string;
  /**
   * Narrows `base_model` to one kind of derivative, or spans all base models
   * when used alone. Usually a `LineageRelation`, but a card may declare
   * anything and the server matches it verbatim.
   */
  relation?: string;
  /** "ns/name" of a dataset: only the repositories trained on it. */
  dataset?: string;
  /** Only repositories that declare no base model ("Base only"). */
  base_only?: boolean;
  /**
   * Tri-state: omit for both, `false` to leave experiment repositories out
   * (a namespace page's Datasets tab, whose count excludes them), `true` for
   * experiments only.
   */
  experiment?: boolean;
  /** Tri-state: omit for both, `false` for active only, `true` for the archive. */
  archived?: boolean;
  sort?: "updated" | "created" | "downloads" | "name";
  limit?: number;
  offset?: number;
};

/**
 * The query string of the /models and /datasets listings, which is the single
 * source of truth for their filters: the sidebar pushes URLs rather than
 * holding state, so every control round-trips through this shape.
 *
 * Values are raw strings because that is what a URL carries; `listFlagOn` and
 * `listTriState` read the boolean ones.
 */
export type RepoListSearch = {
  q?: string;
  search?: string;
  tag?: string;
  tags?: string | string[];
  license?: string;
  task?: string;
  /** Lineage filters, mirroring the API (docs/dev/api-contract.md §2). */
  base_model?: string;
  relation?: string;
  dataset?: string;
  base_only?: string;
  /** Tri-state: absent shows both, "true"/"false" narrow to one side. */
  archived?: string;
  sort?: string;
  offset?: string;
  /**
   * Not read by the listing itself: it is the host page's own state (which
   * tab an organisation page is on) and only travels through so paging
   * links keep it.
   */
  tab?: string;
};

/** How a flag is spelled in a URL, matching the backend's `queryFlag`. */
export function listFlagOn(value: string | null | undefined): boolean {
  return value === "true" || value === "1";
}

/** A tri-state parameter: absent means "no filter", not "false". */
export function listTriState(value: string | null | undefined): boolean | undefined {
  if (value === undefined || value === null || value === "") return undefined;
  return listFlagOn(value);
}

/**
 * Merges the repeatable `tags=` param with the legacy singular `tag=` one
 * (still used by the tag badges on a repository page), deduplicated.
 */
export function listSearchTags(sp: RepoListSearch): string[] {
  const raw = sp.tags;
  const list = raw === undefined ? [] : Array.isArray(raw) ? raw : [raw];
  if (sp.tag) list.push(sp.tag);
  return Array.from(new Set(list.filter((t) => t !== "")));
}

/**
 * One filter currently narrowing a listing, as the chip row above the results
 * shows it. `value` is what the user reads; `tags` is the only repeatable one,
 * so it is the only kind where two refs can share a `key`.
 */
export type RepoFilterRef =
  | { key: "search"; value: string }
  | { key: "tags"; value: string }
  | { key: "license"; value: string }
  | { key: "task"; value: string }
  | { key: "relation"; value: string }
  | { key: "base_model"; value: string }
  | { key: "dataset"; value: string }
  | { key: "base_only"; value: "true" }
  | { key: "archived"; value: "true" | "false" };

/**
 * Every filter currently applied, in the order the sidebar lists them.
 *
 * Read from the URL rather than from the facet response on purpose: a facet
 * value can drop out of the response while still being selected (the tag
 * facets are computed with the license/task filters still applied), and a
 * filter that has disappeared from the sidebar has to stay removable
 * somewhere -- see DESIGN.md §8.
 */
export function activeRepoFilters(sp: RepoListSearch): RepoFilterRef[] {
  const refs: RepoFilterRef[] = [];
  const search = sp.search ?? sp.q;
  if (search) refs.push({ key: "search", value: search });
  if (sp.base_model) refs.push({ key: "base_model", value: sp.base_model });
  if (sp.dataset) refs.push({ key: "dataset", value: sp.dataset });
  if (sp.relation) refs.push({ key: "relation", value: sp.relation });
  if (listFlagOn(sp.base_only)) refs.push({ key: "base_only", value: "true" });
  const archived = listTriState(sp.archived);
  if (archived !== undefined) refs.push({ key: "archived", value: archived ? "true" : "false" });
  for (const tag of listSearchTags(sp)) refs.push({ key: "tags", value: tag });
  if (sp.license) refs.push({ key: "license", value: sp.license });
  if (sp.task) refs.push({ key: "task", value: sp.task });
  return refs;
}

/** True when `ref` is the filter this URL parameter/value pair stands for. */
function isOmitted(omit: RepoFilterRef | undefined, key: RepoFilterRef["key"], value: string) {
  if (omit === undefined || omit.key !== key) return false;
  // Only `tags` is repeatable, so only there does the value decide which of
  // several refs with the same key is being dropped.
  return key === "tags" ? omit.value === value : true;
}

/**
 * Rebuilds the listing URL from its current filters, with `overrides` applied
 * -- what pagination links and the filter chips' remove links are made of.
 * Defaults are left out so a plain listing keeps a clean URL, and the legacy
 * `q=`/`tag=` spellings are normalised to `search=`/`tags=` on the way through.
 *
 * `omit` drops exactly one filter and resets paging: page 3 of the old filter
 * set is meaningless once the set changes.
 */
export function repoListHref(
  basePath: string,
  sp: RepoListSearch,
  overrides: { offset?: number; omit?: RepoFilterRef } = {},
): string {
  const { omit } = overrides;
  const params = new URLSearchParams();
  const search = sp.search ?? sp.q;
  if (search && !isOmitted(omit, "search", search)) params.set("search", search);
  for (const tag of listSearchTags(sp)) {
    if (!isOmitted(omit, "tags", tag)) params.append("tags", tag);
  }
  if (sp.license && !isOmitted(omit, "license", sp.license)) params.set("license", sp.license);
  if (sp.task && !isOmitted(omit, "task", sp.task)) params.set("task", sp.task);
  const droppedBaseModel =
    sp.base_model !== undefined && isOmitted(omit, "base_model", sp.base_model);
  if (sp.base_model && !droppedBaseModel) params.set("base_model", sp.base_model);
  // A relation narrows one base model's derivatives, so removing the base
  // model takes its relation with it -- the same pairing the sidebar's
  // "derived from" chip has always had.
  if (sp.relation && !droppedBaseModel && !isOmitted(omit, "relation", sp.relation)) {
    params.set("relation", sp.relation);
  }
  if (sp.dataset && !isOmitted(omit, "dataset", sp.dataset)) params.set("dataset", sp.dataset);
  if (listFlagOn(sp.base_only) && !isOmitted(omit, "base_only", "true")) {
    params.set("base_only", "true");
  }
  const archived = listTriState(sp.archived);
  if (archived !== undefined && !isOmitted(omit, "archived", String(archived))) {
    params.set("archived", String(archived));
  }
  if (sp.sort && sp.sort !== "updated") params.set("sort", sp.sort);
  if (sp.tab) params.set("tab", sp.tab);
  const offset = omit !== undefined ? 0 : (overrides.offset ?? (Number(sp.offset ?? 0) || 0));
  if (offset > 0) params.set("offset", String(offset));
  const qs = params.toString();
  return qs ? `${basePath}?${qs}` : basePath;
}

export type FetchOpts = { headers?: Record<string, string> };

export function listRepos(
  params: RepoListParams,
  opts?: FetchOpts,
): Promise<ApiResult<RepoListResponse>> {
  return apiFetch<RepoListResponse>("/api/v1/repos", { query: params, headers: opts?.headers });
}

export function getRepo(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<{ repo: RepoDetail }>> {
  return apiFetch<{ repo: RepoDetail }>(
    `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`,
    { headers: opts?.headers },
  );
}

export function getTree(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  opts?: FetchOpts,
): Promise<ApiResult<TreeResponseUI>> {
  const suffix = path.length ? `/${path.map(encodeURIComponent).join("/")}` : "";
  return apiFetch<TreeResponseUI>(
    `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/tree/${encodeURIComponent(rev)}${suffix}`,
    { headers: opts?.headers },
  );
}

export function getRefs(
  kind: RepoKind,
  ns: string,
  name: string,
  opts?: FetchOpts,
): Promise<ApiResult<RefsResponseUI>> {
  return apiFetch<RefsResponseUI>(
    `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/refs`,
    { headers: opts?.headers },
  );
}

export function getCommits(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  params?: { after?: string; limit?: number; path?: string },
  opts?: FetchOpts,
): Promise<ApiResult<CommitListResponse>> {
  return apiFetch<CommitListResponse>(
    `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/commits/${encodeURIComponent(rev)}`,
    { query: params, headers: opts?.headers },
  );
}

/**
 * What one commit changed, against its **first parent**
 * (docs/dev/api-contract.md §2). `rev` may be a branch, a tag or a SHA.
 *
 * Unlike `getCommits`, a repository with no commits answers 404 here rather
 * than an empty body: the response describes one commit and there is none to
 * describe. Callers should render that as an error, not as "nothing changed".
 */
export function getCommitDiff(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  opts?: FetchOpts,
): Promise<ApiResult<CommitDiffResponse>> {
  return apiFetch<CommitDiffResponse>(
    `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/diff/${encodeURIComponent(rev)}`,
    { headers: opts?.headers },
  );
}

/**
 * GCS access for one revision: every indexed file's bucket location, plus a
 * ready-made `gcloud storage cp` script and (when the revision has any
 * parquet files) a DuckDB `read_parquet()` snippet. Backs the "GCS access"
 * dialog on the repository sidebar (RepoDetail no longer carries
 * `gcloud_command` -- it is fetched on demand instead, since walking every
 * file of the revision isn't free).
 */
export function getRepoGCS(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  opts?: FetchOpts,
): Promise<ApiResult<RepoGCSResponse>> {
  return apiFetch<RepoGCSResponse>(
    `/api/v1/repos/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/gcs/${encodeURIComponent(rev)}`,
    { headers: opts?.headers },
  );
}

export function getRawFile(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  opts?: FetchOpts,
): Promise<ApiResult<RawFileResponse>> {
  const suffix = path.map(encodeURIComponent).join("/");
  return apiFetch<RawFileResponse>(
    `/api/v1/raw/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/${encodeURIComponent(rev)}/${suffix}`,
    { headers: opts?.headers },
  );
}

export type CreateRepoInput = {
  kind: RepoKind;
  namespace: string;
  name: string;
  description: string;
};

export function createRepo(input: CreateRepoInput): Promise<ApiResult<{ repo: RepoDetail }>> {
  return apiFetch<{ repo: RepoDetail }>("/api/v1/repos", {
    method: "POST",
    body: input,
  });
}

export function resolveFileUrl(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  baseUrl: string,
): string {
  const prefix = kind === "dataset" ? "/datasets" : "";
  const suffix = path.map(encodeURIComponent).join("/");
  const root = `${baseUrl.replace(/\/$/, "")}${prefix}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/resolve/${encodeURIComponent(rev)}`;
  return suffix ? `${root}/${suffix}` : `${root}/`;
}
