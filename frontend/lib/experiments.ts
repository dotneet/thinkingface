import { type ApiResult, apiFetch } from "@/lib/api";
import { repoBase, repoBlobHref, repoTreeHref, repoViewerHref } from "@/lib/paths";
import type {
  ExpArtifact,
  ExpArtifactListResponse,
  ExpMetricsResponse,
  ExpProject,
  ExpProjectListResponse,
  ExpRun,
  ExpRunAnnotationRequest,
  ExpRunAnnotationResponse,
  ExpRunModelRef,
  RepoSummary,
} from "@/types/api";

// See lib/repos.ts's FetchOpts: Server Components must pass `{ headers:
// await authHeaders() }` explicitly so the tf_session cookie reaches the
// backend (this module is shared with Client Components, so apiFetch can't
// inject it itself). Browser callers can omit `opts`.
export type FetchOpts = { headers?: Record<string, string> };

/**
 * Path of one run's detail page.
 *
 * A run name is only forbidden control characters at ingest, so it may carry
 * slashes and anything else that needs escaping. Next's dynamic segments and
 * chi's URL params both stay percent-encoded, so each side decodes on the way
 * in: `decodeRouteParams` (lib/paths.ts) here, `decodeRunSegment` on the
 * backend.
 */
export function expRunHref(ns: string, repo: string, project: string, run: string): string {
  return `/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}/${encodeURIComponent(run)}`;
}

export type ListExperimentsParams = {
  /**
   * Restrict the listing to one namespace, case-insensitively
   * (docs/dev/namespace-design.md §5.6) — what `/{ns}?tab=experiments` uses.
   */
  author?: string;
  /**
   * Full text search (backend's `store.RepoFilter.Search`), matched against
   * the repository name and card. What the global `/experiments` page uses:
   * the endpoint caps at 100 results, so past that a search is the only way
   * to reach a repository that isn't on the first page.
   */
  search?: string;
  limit?: number;
  offset?: number;
};

/**
 * Experiment repositories, newest first. `total` counts every match
 * regardless of `limit` / `offset`, so a namespace page can page through
 * them.
 */
export function listExperiments(
  params: ListExperimentsParams = {},
  opts?: FetchOpts,
): Promise<ApiResult<ExpProjectListResponse>> {
  return apiFetch<ExpProjectListResponse>("/api/v1/experiments", {
    query: params,
    headers: opts?.headers,
  });
}

export function getExperimentRepo(
  ns: string,
  repo: string,
  opts?: FetchOpts,
): Promise<ApiResult<{ repo: RepoSummary; projects: ExpProject[] }>> {
  return apiFetch<{ repo: RepoSummary; projects: ExpProject[] }>(
    `/api/v1/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}`,
    { headers: opts?.headers },
  );
}

export function listRuns(
  ns: string,
  repo: string,
  project: string,
  opts?: FetchOpts,
): Promise<ApiResult<{ runs: ExpRun[] }>> {
  return apiFetch<{ runs: ExpRun[] }>(
    `/api/v1/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}/runs`,
    { headers: opts?.headers },
  );
}

/**
 * Delete one run's indexed row and its live metric points. Irreversible for
 * anything ingested live; a run that also exists in the project's parquet
 * export comes back on the next index, since the export is that path's source
 * of truth. Requires write access to the backing dataset repository.
 */
export function deleteRun(
  ns: string,
  repo: string,
  project: string,
  run: string,
): Promise<ApiResult<void>> {
  return apiFetch<void>(
    `/api/v1/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}/runs/${encodeURIComponent(run)}`,
    { method: "DELETE" },
  );
}

/**
 * Update one run's annotations (tags / archived / baseline / note). Only the fields
 * present in `body` change, so a caller toggling one flag never has to resend
 * the others. Requires write access to the backing dataset repository; a
 * viewer without it gets `{ ok: false, status: 401|403 }` rather than a throw.
 */
export function updateRunAnnotations(
  ns: string,
  repo: string,
  project: string,
  run: string,
  body: ExpRunAnnotationRequest,
): Promise<ApiResult<ExpRunAnnotationResponse>> {
  return apiFetch<ExpRunAnnotationResponse>(
    `/api/v1/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}/runs/${encodeURIComponent(run)}`,
    { method: "PATCH", body },
  );
}

/**
 * Files `trackio.log_artifact` committed for one run, read from
 * `{project}/artifacts/{run}` on the repository's default branch.
 *
 * There is no artifact store: these are ordinary repository files, so each one
 * links straight into the existing file browser. A run that logged nothing
 * yields an empty list rather than an error.
 */
export function listRunArtifacts(
  ns: string,
  repo: string,
  project: string,
  run: string,
  opts?: FetchOpts,
): Promise<ApiResult<ExpArtifactListResponse>> {
  return apiFetch<ExpArtifactListResponse>(
    `/api/v1/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}/runs/${encodeURIComponent(run)}/artifacts`,
    { headers: opts?.headers },
  );
}

/**
 * Where one artifact opens.
 *
 * An artifact is an ordinary file in the experiment's dataset repository, so
 * it goes to the file browser that already exists: the dedicated viewer for
 * Parquet, the blob page for everything else (which renders images, Markdown,
 * text and checkpoint headers on its own).
 */
export function expArtifactHref(
  ns: string,
  repo: string,
  rev: string,
  artifact: ExpArtifact,
): string {
  return artifact.preview === "parquet"
    ? repoViewerHref("dataset", ns, repo, rev, artifact.path)
    : repoBlobHref("dataset", ns, repo, rev, artifact.path);
}

/**
 * Where a model a run declared it produced opens, or null when the reference
 * does not resolve on this server.
 *
 * A dangling reference is kept and rendered as text with a note, never as a
 * broken link — the same rule dangling `lineage:` references follow. With a
 * revision the file browser opens at exactly what the run produced; the
 * revision itself is never verified, so a rewritten one lands on the file
 * browser's own error rather than being hidden here.
 */
export function expRunModelHref(model: ExpRunModelRef): string | null {
  if (!model.exists) return null;
  const [ns = "", name = ""] = model.repo_id.split("/");
  if (!ns || !name) return null;
  return model.revision
    ? repoTreeHref("model", ns, name, model.revision)
    : repoBase("model", ns, name);
}

/**
 * How a metric value reads anywhere in the experiment UI (run table cells,
 * run detail summary cards). `toPrecision` keeps six significant digits
 * whatever the magnitude (a loss of 3.2e-7 and an epoch count of 12 both read
 * correctly), and a value whose magnitude is below 1e-4 or at/above 1e7 falls
 * back to exponential notation — the alternative is rounding a tiny value to
 * a `0` indistinguishable from a real zero, or printing a wall of digits for
 * a huge one. Values in between get thousands separators (`toLocaleString`),
 * same as `formatNumber` (`lib/format.ts`) does for step counts.
 */
export function formatMetricValue(value: number): string {
  if (!Number.isFinite(value)) return String(value);
  const magnitude = Math.abs(value);
  if (magnitude !== 0 && (magnitude < 1e-4 || magnitude >= 1e7)) return value.toExponential(4);
  if (Number.isInteger(value)) return new Intl.NumberFormat("en-US").format(value);
  return Number(value.toPrecision(6)).toLocaleString("en-US", { maximumFractionDigits: 6 });
}

export function getMetrics(
  ns: string,
  repo: string,
  project: string,
  params: { runs?: string[]; keys?: string[]; x?: "step" | "time"; max_points?: number },
  opts?: FetchOpts,
): Promise<ApiResult<ExpMetricsResponse>> {
  return apiFetch<ExpMetricsResponse>(
    `/api/v1/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}/metrics`,
    {
      query: {
        runs: params.runs?.length ? params.runs.join(",") : undefined,
        keys: params.keys?.length ? params.keys.join(",") : undefined,
        x: params.x,
        max_points: params.max_points,
      },
      headers: opts?.headers,
    },
  );
}
