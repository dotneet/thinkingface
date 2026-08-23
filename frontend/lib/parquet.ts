import { type ApiResult, apiFetch } from "@/lib/api";
import type { ParquetRowsResponse, ParquetSchemaResponse, RepoKind } from "@/types/api";

// See lib/repos.ts's FetchOpts: Server Components must pass `{ headers:
// await authHeaders() }` explicitly so the tf_session cookie reaches the
// backend (this module is shared with Client Components, so apiFetch can't
// inject it itself). Browser callers can omit `opts`.
export type FetchOpts = { headers?: Record<string, string> };

export function getParquetSchema(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  opts?: FetchOpts,
): Promise<ApiResult<ParquetSchemaResponse>> {
  const suffix = path.map(encodeURIComponent).join("/");
  return apiFetch<ParquetSchemaResponse>(
    `/api/v1/parquet/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/schema/${encodeURIComponent(rev)}/${suffix}`,
    { headers: opts?.headers },
  );
}

export function getParquetRows(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  params: { offset?: number; limit?: number; columns?: string[] },
  opts?: FetchOpts,
): Promise<ApiResult<ParquetRowsResponse>> {
  const suffix = path.map(encodeURIComponent).join("/");
  return apiFetch<ParquetRowsResponse>(
    `/api/v1/parquet/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/rows/${encodeURIComponent(rev)}/${suffix}`,
    {
      query: {
        offset: params.offset,
        limit: params.limit,
        columns: params.columns?.length ? params.columns.join(",") : undefined,
      },
      headers: opts?.headers,
    },
  );
}
