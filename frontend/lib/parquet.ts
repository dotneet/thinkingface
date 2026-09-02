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
        // Repeated `column=` rather than a single comma-joined `columns=`:
        // the server splits the joined form on "," and trims each piece, so a
        // column literally named `height,cm` split into two names that match
        // nothing and ` age` lost its space — the Rows tab then failed forever
        // while the schema panel (which never round-trips the name) looked
        // fine. `apiFetch` sends a string[] as one key per entry, so the name
        // travels percent-encoded and arrives byte-for-byte.
        column: params.columns?.length ? params.columns : undefined,
      },
      headers: opts?.headers,
    },
  );
}
