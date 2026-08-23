import { type ApiResult, apiFetch } from "@/lib/api";
import type { EditFileRequest, EditFileResponse, RepoKind } from "@/types/api";

/**
 * Commit an in-browser edit to a text/markdown file. Path encoding mirrors
 * `getRawFile` in lib/repos.ts so the URL matches what the backend expects.
 */
export function editFile(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  body: EditFileRequest,
): Promise<ApiResult<EditFileResponse>> {
  const suffix = path.map(encodeURIComponent).join("/");
  return apiFetch<EditFileResponse>(
    `/api/v1/edit/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/${encodeURIComponent(rev)}/${suffix}`,
    { method: "PUT", body },
  );
}
