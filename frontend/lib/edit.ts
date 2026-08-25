import { type ApiResult, apiFetch } from "@/lib/api";
import type {
  DeleteFileRequest,
  EditFileRequest,
  EditFileResponse,
  RenameFileRequest,
  RenameFileResponse,
  RepoKind,
} from "@/types/api";

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

/**
 * Delete one file in a commit of its own. Same URL as {@link editFile} with a
 * different method, and the same response shape — `oid` is empty and `size`
 * is 0, because the file it describes is gone.
 */
export function deleteFile(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  body: DeleteFileRequest = {},
): Promise<ApiResult<EditFileResponse>> {
  const suffix = path.map(encodeURIComponent).join("/");
  return apiFetch<EditFileResponse>(
    `/api/v1/edit/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/${encodeURIComponent(rev)}/${suffix}`,
    { method: "DELETE", body },
  );
}

/**
 * Move one file to a new path in a single commit — which is the whole point of
 * the endpoint: the browser used to have to write the new path and then delete
 * the old one, putting two commits in the history for one rename and leaving
 * the repository momentarily holding both copies.
 *
 * `new_path` is a full path from the repository root, so renaming and moving
 * are the same call: editing only the last segment renames, editing the
 * directory part moves. The destination must be free — an occupied path comes
 * back as a 409 rather than overwriting what is there.
 */
export function renameFile(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string[],
  body: RenameFileRequest,
): Promise<ApiResult<RenameFileResponse>> {
  const suffix = path.map(encodeURIComponent).join("/");
  return apiFetch<RenameFileResponse>(
    `/api/v1/rename/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/${encodeURIComponent(rev)}/${suffix}`,
    { method: "POST", body },
  );
}
