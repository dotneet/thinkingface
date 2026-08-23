import { Pencil } from "lucide-react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { CommitBar } from "@/components/repo/commit-bar";
import { DeleteFileButton } from "@/components/repo/delete-file-button";
import { FileNav } from "@/components/repo/file-nav";
import { FilePreview } from "@/components/repo/file-preview";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { Badge } from "@/components/ui/badge";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { formatBytes } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import {
  publicApiBase,
  repoBlobHref,
  repoCommitsHref,
  repoEditHref,
  repoViewerHref,
} from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getCommits, getRawFile, getRefs, getRepo, getTree, resolveFileUrl } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind, TreeEntryUI } from "@/types/api";

export async function RepoBlob({
  kind,
  ns,
  name,
  rev,
  path,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
}) {
  const dirPath = path.slice(0, -1);

  // Forward the tf_session cookie so the request is authenticated and
  // resolve instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const pathStr = path.join("/");
  const [repoResult, dirResult, refsResult, commitsResult, t] = await Promise.all([
    getRepo(kind, ns, name, { headers }),
    getTree(kind, ns, name, rev, dirPath, { headers }),
    getRefs(kind, ns, name, { headers }),
    getCommits(kind, ns, name, rev, { path: pathStr, limit: 1 }, { headers }),
    getT(),
  ]);

  redirectIfRepoMoved(repoResult, (toNs, toName) => repoBlobHref(kind, toNs, toName, rev, pathStr));
  if (isNotFound(repoResult)) {
    return <RepoNotFoundOrLogin currentPath={repoBlobHref(kind, ns, name, rev, pathStr)} />;
  }
  if (!repoResult.ok) {
    return <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, repoResult)} />;
  }
  const repo = repoResult.data.repo;

  const entry: TreeEntryUI | undefined = dirResult.ok
    ? dirResult.data.entries.find((e) => e.path === pathStr)
    : undefined;

  if (dirResult.ok && !entry) notFound();

  const downloadUrl = resolveFileUrl(kind, ns, name, rev, path, publicApiBase());
  const repoRootUrl = resolveFileUrl(kind, ns, name, rev, [], publicApiBase());
  const assetBaseUrl = resolveFileUrl(kind, ns, name, rev, dirPath, publicApiBase());

  const shouldFetchRaw = entry ? entry.preview === "text" || entry.preview === "markdown" : false;
  const rawResult = shouldFetchRaw
    ? await getRawFile(kind, ns, name, rev, path, { headers })
    : null;

  const lastCommit = commitsResult.ok ? (commitsResult.data.commits[0] ?? null) : null;

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb kind={kind} ns={ns} name={name} />
      <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>
      <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="files" />

      <FileNav
        kind={kind}
        ns={ns}
        name={name}
        rev={rev}
        path={path}
        refs={refsResult.ok ? refsResult.data : undefined}
        target="blob"
      />

      {!dirResult.ok ? (
        <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, dirResult)} />
      ) : !entry ? (
        <ErrorState title={t("ui.errorStateTitle")} message={t("repo.blob.fileNotFound")} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2 text-sm text-fg-subtle">
            <span className="tabular-nums">{formatBytes(entry.size)}</span>
            {entry.lfs && <Badge tone="accent">LFS</Badge>}
            {repo.can_write && repo.branches.includes(rev) && (
              <div className="ml-auto flex items-center gap-3">
                {/* Editing is limited to text this browser can round-trip;
                    deleting only drops a tree entry, so it is offered for
                    every file, LFS pointers included. */}
                {(entry.preview === "text" || entry.preview === "markdown") && !entry.lfs && (
                  <Link
                    href={repoEditHref(kind, ns, name, rev, entry.path)}
                    className="flex items-center gap-1.5 text-accent hover:underline"
                  >
                    <Pencil size={14} />
                    {t("repo.blob.edit")}
                  </Link>
                )}
                <DeleteFileButton
                  kind={kind}
                  ns={ns}
                  name={name}
                  rev={rev}
                  path={path}
                  baseOid={entry.oid}
                  lfs={entry.lfs}
                />
              </div>
            )}
          </div>
          {lastCommit && (
            <CommitBar
              commit={lastCommit}
              historyHref={repoCommitsHref(kind, ns, name, rev, pathStr)}
              className="rounded-lg border"
            />
          )}
          <FilePreview
            entry={entry}
            raw={rawResult?.ok ? rawResult.data : null}
            // Distinguish "we never asked for the contents" from "we asked and
            // it failed"; both leave `raw` null, but only one is an error.
            rawError={rawResult && !rawResult.ok ? rawResult : null}
            downloadUrl={downloadUrl}
            viewerHref={
              entry.is_parquet ? repoViewerHref(kind, ns, name, rev, entry.path) : undefined
            }
            assetBaseUrl={assetBaseUrl}
            repoRootUrl={repoRootUrl}
            linkContext={{ kind, ns, name, rev, dir: dirPath }}
            modelSource={entry.is_model ? { kind, ns, name, rev, path } : undefined}
          />
        </>
      )}
    </div>
  );
}
