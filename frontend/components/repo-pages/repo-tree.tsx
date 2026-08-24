import { FolderOpen } from "lucide-react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { AddFileMenu } from "@/components/repo/add-file-menu";
import { FileNav } from "@/components/repo/file-nav";
import { FileTreeTable } from "@/components/repo/file-tree-table";
import { IndexingBanner } from "@/components/repo/indexing-banner";
import { ReadmeCard } from "@/components/repo/readme-card";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { publicApiBase, repoBlobHref, repoCommitsHref, repoTreeHref } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getRefs, getRepo, getTree, resolveFileUrl } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind } from "@/types/api";

export async function RepoTree({
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
  // Forward the tf_session cookie so the request is authenticated and
  // resolve instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [repoResult, treeResult, refsResult, t] = await Promise.all([
    getRepo(kind, ns, name, { headers }),
    getTree(kind, ns, name, rev, path, { headers }),
    getRefs(kind, ns, name, { headers }),
    getT(),
  ]);

  redirectIfRepoMoved(repoResult, (toNs, toName) =>
    repoTreeHref(kind, toNs, toName, rev, path.join("/")),
  );
  // [S15]: a 404 on the repository itself can still offer a log-in route
  // it" for a signed-out visitor; a 404 on just the tree path (repo found,
  // path within it isn't) never does, since reaching that branch already
  // means the repo resolved and is accessible.
  if (isNotFound(repoResult)) {
    return <RepoNotFoundOrLogin currentPath={repoTreeHref(kind, ns, name, rev, path.join("/"))} />;
  }
  if (isNotFound(treeResult)) notFound();

  if (!repoResult.ok) {
    return <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, repoResult)} />;
  }
  const repo = repoResult.data.repo;

  // The backend answers an unresolvable revision with an empty tree rather
  // than a 404, so "branch does not exist" and "directory is empty" arrive
  // looking identical. Tell them apart from the ref listing: a revision that
  // is neither a known branch or tag nor a commit id never resolved. A
  // repository with no branches at all is simply empty, not misaddressed.
  const knownRefs = refsResult.ok
    ? [...refsResult.data.branches, ...refsResult.data.tags].map((r) => r.name)
    : [];
  const unknownRevision =
    knownRefs.length > 0 && !knownRefs.includes(rev) && !/^[0-9a-f]{7,40}$/i.test(rev);

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb kind={kind} ns={ns} name={name} />

      <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>

      <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="files" />

      {repo.indexing && <IndexingBanner />}

      <div className="flex flex-wrap items-center justify-between gap-2">
        {/* min-w-0 flex-1: the breadcrumb wraps inside its own box rather
            than pushing the menu out of the row on a deep path. */}
        <div className="min-w-0 flex-1">
          <FileNav
            kind={kind}
            ns={ns}
            name={name}
            rev={rev}
            path={path}
            refs={refsResult.ok ? refsResult.data : undefined}
            target="tree"
          />
        </div>
        {/* Above the listing rather than inside it, so an empty repository --
            the case that used to be a dead end, with no way to add a file
            without git -- still offers both routes in. Only a branch can be
            committed to, and can_write already folds in archived and
            signed-out. */}
        {repo.can_write && repo.branches.includes(rev) && (
          <AddFileMenu kind={kind} ns={ns} name={name} rev={rev} path={path} />
        )}
      </div>

      {!treeResult.ok ? (
        <ErrorState
          title={t("ui.errorStateTitle")}
          message={errorMessage(t, treeResult)}
          hint={t("repo.tree.errorHint")}
        />
      ) : treeResult.data.entries.length === 0 && unknownRevision ? (
        <ErrorState
          title={t("repo.tree.unknownRevTitle")}
          message={t("repo.tree.unknownRev", { rev })}
          hint={t("repo.tree.unknownRevHint", {
            branch: refsResult.ok ? refsResult.data.default_branch : repo.default_branch,
          })}
          action={
            <Link
              href={repoTreeHref(
                kind,
                ns,
                name,
                refsResult.ok ? refsResult.data.default_branch : repo.default_branch,
              )}
              className={buttonClass({ variant: "secondary" })}
            >
              {t("repo.tree.unknownRevAction")}
            </Link>
          }
        />
      ) : treeResult.data.entries.length === 0 ? (
        <EmptyState icon={FolderOpen} title={t("repo.tree.emptyDir")} />
      ) : (
        <FileTreeTable
          entries={treeResult.data.entries}
          kind={kind}
          ns={ns}
          name={name}
          rev={rev}
          path={path}
          latestCommit={treeResult.data.latest_commit}
          commitsHref={repoCommitsHref(kind, ns, name, rev)}
        />
      )}

      {treeResult.ok && (treeResult.data.readme || treeResult.data.readme_too_large) && (
        <ReadmeCard
          readme={treeResult.data.readme ?? ""}
          tooLarge={treeResult.data.readme_too_large}
          fileHref={repoBlobHref(kind, ns, name, rev, [...path, "README.md"].join("/"))}
          assetBaseUrl={resolveFileUrl(kind, ns, name, rev, path, publicApiBase())}
          repoRootUrl={resolveFileUrl(kind, ns, name, rev, [], publicApiBase())}
          linkContext={{ kind, ns, name, rev, dir: path }}
        />
      )}
    </div>
  );
}
