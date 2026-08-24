import { apiFetch } from "@/lib/api";
import type { FetchOpts } from "@/lib/repos";
import type { RepoKind } from "@/types/api";

/**
 * Where a path's bytes would go if they were written now. "unknown" is a
 * failed check, deliberately distinct from "regular": a check that could not
 * run has learned nothing, and callers must not read it as "this is fine"
 * (DESIGN.md §9 — empty, zero and failed are three different states).
 */
export type UploadRouting = "lfs" | "regular" | "unknown";

/**
 * Response shape of the HF-compatible preupload endpoint. Hand-written on
 * purpose: `types/api.gen.ts` covers the Web-UI API only, because for the
 * HF-compatible endpoints the external protocol is the source of truth
 * (CLAUDE.md, invariant 1).
 */
type PreuploadResponse = { files?: { path?: string; uploadMode?: string }[] };

/**
 * Asks the server whether a path is Git LFS-managed in this repository.
 *
 * The rule lives in `gitrepo.LFSRules.ShouldUseLFS` and depends on the
 * repository's own `.gitattributes` at this revision, so it is **not**
 * reimplementable here — a copy of the pattern list in the frontend would go
 * stale the moment someone edits `.gitattributes`, and would disagree with
 * the server that actually refuses the commit. This asks the same endpoint
 * `huggingface_hub` asks before every upload.
 *
 * `size: 0` makes the answer depend on `.gitattributes` patterns alone, which
 * is the only question worth asking about a file that does not exist yet.
 * Size cannot change the verdict for anything the browser editor can produce
 * anyway: it caps edits at 2MiB, far below the 10MiB threshold that routes an
 * unmatched path to LFS.
 */
export async function routeForPath(
  kind: RepoKind,
  ns: string,
  name: string,
  rev: string,
  path: string,
  opts?: FetchOpts,
): Promise<UploadRouting> {
  const result = await apiFetch<PreuploadResponse>(
    `/api/${kind}s/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/preupload/${encodeURIComponent(rev)}`,
    {
      method: "POST",
      body: { files: [{ path, sample: "", size: 0 }] },
      headers: opts?.headers,
    },
  );
  if (!result.ok) return "unknown";
  const mode = result.data?.files?.[0]?.uploadMode;
  if (mode === "lfs") return "lfs";
  if (mode === "regular") return "regular";
  return "unknown";
}
