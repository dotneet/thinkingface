import { type ApiResult, apiFetch } from "@/lib/api";
import type { ModelMetaResponse, RepoKind } from "@/types/api";

// See lib/parquet.ts's FetchOpts: Server Components must pass `{ headers:
// await authHeaders() }` explicitly so the tf_session cookie reaches the
// backend (this module is shared with Client Components, so apiFetch can't
// inject it itself). Browser callers can omit `opts`.
export type FetchOpts = { headers?: Record<string, string> };

export function getModelMeta(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  opts?: FetchOpts,
): Promise<ApiResult<ModelMetaResponse>> {
  const suffix = path.map(encodeURIComponent).join("/");
  return apiFetch<ModelMetaResponse>(
    `/api/v1/model-meta/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/${encodeURIComponent(rev)}/${suffix}`,
    { headers: opts?.headers },
  );
}
