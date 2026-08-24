import { GitCommitVertical, History } from "lucide-react";
import Link from "next/link";
import { FileNav } from "@/components/repo/file-nav";
import { IndexingBanner } from "@/components/repo/indexing-banner";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { TimeText } from "@/components/ui/time-text";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { repoCommitsHref, repoTreeHref } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getCommits, getRefs, getRepo } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind } from "@/types/api";

const PAGE_SIZE = 50;

export async function RepoCommits({
  kind,
  ns,
  name,
  rev,
  after,
  path,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  after?: string;
  /** Restrict the history to commits that touched this file/directory. */
  path?: string;
}) {
  // Forward the tf_session cookie so the request is authenticated and
  // resolve instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [repoResult, commitsResult, refsResult, t] = await Promise.all([
    getRepo(kind, ns, name, { headers }),
    getCommits(kind, ns, name, rev, { after, limit: PAGE_SIZE, path }, { headers }),
    getRefs(kind, ns, name, { headers }),
    getT(),
  ]);

  redirectIfRepoMoved(repoResult, (toNs, toName) => {
    const base = repoCommitsHref(kind, toNs, toName, rev, path ?? "");
    if (!after) return base;
    const sep = base.includes("?") ? "&" : "?";
    return `${base}${sep}after=${encodeURIComponent(after)}`;
  });
  if (isNotFound(repoResult)) {
    return <RepoNotFoundOrLogin currentPath={repoCommitsHref(kind, ns, name, rev, path ?? "")} />;
  }

  if (!repoResult.ok) {
    return <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, repoResult)} />;
  }
  const repo = repoResult.data.repo;
  const pathSegments = path ? path.split("/").filter(Boolean) : [];

  // getCommits scans a bounded number of commits per page when filtering by
  // path, so a page can come back with fewer results than PAGE_SIZE — even
  // zero — while next_cursor still points further back in history. That is
  // not the same as "no commits touched this path": only the absence of
  // next_cursor means the walk reached the root.
  const nextCursor = commitsResult.ok ? commitsResult.data.next_cursor : null;
  let olderHref: string | undefined;
  if (nextCursor) {
    const base = repoCommitsHref(kind, ns, name, rev, path ?? "");
    const sep = base.includes("?") ? "&" : "?";
    olderHref = `${base}${sep}after=${encodeURIComponent(nextCursor)}`;
  }

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb kind={kind} ns={ns} name={name} />

      <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>

      <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="files" />

      {repo.indexing && <IndexingBanner />}

      <FileNav
        kind={kind}
        ns={ns}
        name={name}
        rev={rev}
        path={pathSegments}
        refs={refsResult.ok ? refsResult.data : undefined}
        target="commits"
        canCreateBranch={repo.can_write}
      />

      {path && (
        <div className="flex flex-wrap items-center gap-2 text-sm text-fg-subtle">
          <span>
            {/* Split the translation template so only {path} renders in monospace. */}
            {t("repo.commits.historyFor").split("{path}")[0]}
            <span className="font-mono text-fg">{path}</span>
            {t("repo.commits.historyFor").split("{path}")[1]}
          </span>
          <Link
            href={repoCommitsHref(kind, ns, name, rev)}
            className="text-xs font-medium text-accent hover:underline"
          >
            {t("repo.commits.clearFilter")}
          </Link>
        </div>
      )}

      {!commitsResult.ok ? (
        <ErrorState
          title={t("ui.errorStateTitle")}
          message={errorMessage(t, commitsResult)}
          hint={t("repo.commits.errorHint")}
        />
      ) : commitsResult.data.commits.length === 0 ? (
        nextCursor ? (
          <div className="flex flex-col items-center gap-3 rounded-lg border border-border p-6 text-center text-sm text-fg-subtle">
            <p>{t("repo.commits.emptyPage")}</p>
            {olderHref && (
              <Link href={olderHref} className={buttonClass({ variant: "secondary", size: "sm" })}>
                {t("repo.commits.older")}
              </Link>
            )}
          </div>
        ) : (
          <EmptyState
            icon={History}
            title={path ? t("repo.commits.emptyForPath") : t("repo.commits.empty")}
          />
        )
      ) : (
        <>
          <div className="divide-y divide-border rounded-lg border border-border">
            {commitsResult.data.commits.map((commit) => (
              <div key={commit.oid} className="flex items-center gap-3 px-4 py-3 text-sm">
                <GitCommitVertical size={16} className="shrink-0 text-fg-subtle" />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-fg">{commit.message}</p>
                  <p className="truncate text-xs font-medium text-fg-subtle">{commit.author}</p>
                </div>
                <TimeText
                  iso={commit.date}
                  style="relative"
                  className="shrink-0 text-xs font-medium text-fg-subtle"
                />
                <span className="shrink-0 font-mono text-xs font-medium text-fg-subtle">
                  {commit.oid.slice(0, 7)}
                </span>
                <Link
                  href={repoTreeHref(kind, ns, name, commit.oid)}
                  className="shrink-0 text-xs font-medium text-accent hover:underline"
                >
                  {t("repo.commits.browseFiles")}
                </Link>
              </div>
            ))}
          </div>

          {olderHref && (
            <div className="flex justify-center">
              <Link href={olderHref} className={buttonClass({ variant: "secondary", size: "sm" })}>
                {t("repo.commits.older")}
              </Link>
            </div>
          )}
        </>
      )}
    </div>
  );
}
