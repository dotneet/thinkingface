import { Archive, FolderOpen } from "lucide-react";
import Link from "next/link";
import { Suspense } from "react";
import { IndexingBanner } from "@/components/repo/indexing-banner";
import { LineageSection } from "@/components/repo/lineage-section";
import { NewVersionBanner } from "@/components/repo/new-version-banner";
import { ReadmeCard } from "@/components/repo/readme-card";
import { ReadmeToc } from "@/components/repo/readme-toc";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoSidebar } from "@/components/repo/repo-sidebar";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { Badge } from "@/components/ui/badge";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { extractToc } from "@/lib/markdown-toc";
import { publicApiBase, repoBase, repoBlobHref, repoTreeHref } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getRepo, resolveFileUrl } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind } from "@/types/api";

export async function RepoOverview({
  kind,
  ns,
  name,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
}) {
  // Forward the tf_session cookie so the request is authenticated and
  // resolve instead of 404ing (see lib/server-auth.ts).
  const [result, t] = await Promise.all([
    getRepo(kind, ns, name, { headers: await authHeaders() }),
    getT(),
  ]);

  redirectIfRepoMoved(result, kind);
  if (isNotFound(result)) {
    return <RepoNotFoundOrLogin currentPath={repoBase(kind, ns, name)} />;
  }

  if (!result.ok) {
    return (
      <ErrorState
        title={t("ui.errorStateTitle")}
        message={errorMessage(t, result)}
        hint={t("repo.overview.backendHint")}
      />
    );
  }

  const repo = result.data.repo;
  // No branch needed even for an empty README: extractToc just returns an empty
  // array (and ReadmeToc itself renders nothing when there are fewer than 3 entries).
  const tocEntries = extractToc(repo.readme);

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb kind={kind} ns={ns} name={name} />

      <div className="flex flex-wrap items-center gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>
        {repo.archived && (
          <Badge tone="warning">
            <Archive size={11} />
            {t("repo.archived.badge")}
          </Badge>
        )}
      </div>
      {repo.description && <p className="text-sm text-fg-subtle">{repo.description}</p>}

      <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="card" />

      {repo.indexing && <IndexingBanner />}

      {/* "Use the newer version instead", when the card declares one. Streams
          in on its own so the README never waits for the lineage lookup. */}
      <Suspense fallback={null}>
        <NewVersionBanner kind={kind} ns={ns} name={name} />
      </Suspense>

      <div className="flex flex-col gap-6 lg:flex-row">
        <div className="min-w-0 flex-1">
          <ReadmeCard
            readme={repo.readme}
            tooLarge={repo.readme_too_large}
            fileHref={repoBlobHref(kind, ns, name, repo.default_branch, "README.md")}
            assetBaseUrl={resolveFileUrl(kind, ns, name, repo.default_branch, [], publicApiBase())}
            repoRootUrl={resolveFileUrl(kind, ns, name, repo.default_branch, [], publicApiBase())}
            linkContext={{ kind, ns, name, rev: repo.default_branch, dir: [] }}
          />

          {/* Lineage is a second round trip, so it streams in on its own rather
              than holding the README back. */}
          <Suspense
            fallback={
              <div className="mt-6 rounded-lg border border-border bg-bg-raised p-4">
                <SkeletonLines lines={3} />
              </div>
            }
          >
            <div className="mt-6">
              <LineageSection kind={kind} ns={ns} name={name} />
            </div>
          </Suspense>
        </div>
        <div className="flex flex-col gap-4 lg:w-72 lg:shrink-0">
          <ReadmeToc entries={tocEntries} />
          <RepoSidebar repo={repo} />
        </div>
      </div>

      <div>
        <Link
          href={repoTreeHref(kind, ns, name, repo.default_branch)}
          className="inline-flex items-center gap-1.5 text-sm text-accent hover:underline"
        >
          <FolderOpen size={14} />
          {t(
            repo.num_files === 1
              ? "repo.overview.browseFilesOne"
              : "repo.overview.browseFilesOther",
            { count: repo.num_files },
          )}
        </Link>
      </div>
    </div>
  );
}
