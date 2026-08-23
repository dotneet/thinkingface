import Link from "next/link";
import { notFound } from "next/navigation";
import { ParquetViewer } from "@/components/parquet/parquet-viewer";
import { IndexingBanner } from "@/components/repo/indexing-banner";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { badgeClass } from "@/components/ui/badge";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { getParquetSchema } from "@/lib/parquet";
import { repoViewerHref } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getRepo } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind } from "@/types/api";

export async function RepoViewer({
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
  const [repoResult, schemaResult, t] = await Promise.all([
    getRepo(kind, ns, name, { headers }),
    getParquetSchema(kind, ns, name, rev, path, { headers }),
    getT(),
  ]);

  redirectIfRepoMoved(repoResult, (toNs, toName) =>
    repoViewerHref(kind, toNs, toName, rev, path.join("/")),
  );
  // [S15]: a repo 404 can still offer a log-in route back; a schema 404
  // (repo found, this parquet path within it isn't) never does.
  if (isNotFound(repoResult)) {
    return (
      <RepoNotFoundOrLogin currentPath={repoViewerHref(kind, ns, name, rev, path.join("/"))} />
    );
  }
  if (isNotFound(schemaResult)) notFound();
  if (!repoResult.ok) {
    return <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, repoResult)} />;
  }
  const repo = repoResult.data.repo;
  const currentPath = path.join("/");

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb
        kind={kind}
        ns={ns}
        name={name}
        trail={[{ label: `viewer/${rev}` }, { label: currentPath }]}
      />
      <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>
      <RepoTabs
        kind={kind}
        ns={ns}
        name={name}
        repo={repo}
        active="viewer"
        viewerPath={currentPath}
      />

      {repo.indexing && <IndexingBanner />}

      {repo.parquet_files.length > 1 && (
        <div className="flex flex-wrap gap-1.5">
          {repo.parquet_files.map((file) => {
            const selected = file.path === currentPath;
            return (
              <Link
                key={file.path}
                href={repoViewerHref(kind, ns, name, rev, file.path)}
                className={badgeClass({
                  tone: selected ? "accent" : "neutral",
                  className: selected
                    ? "border-accent font-mono"
                    : "bg-transparent font-mono hover:border-border-strong hover:text-fg",
                })}
              >
                {file.path}
              </Link>
            );
          })}
        </div>
      )}

      {!schemaResult.ok ? (
        <ErrorState
          title={t("ui.errorStateTitle")}
          message={errorMessage(t, schemaResult)}
          hint={t("repo.viewer.errorHint")}
        />
      ) : (
        <ParquetViewer
          kind={kind}
          ns={ns}
          name={name}
          rev={rev}
          path={path}
          schema={schemaResult.data}
        />
      )}
    </div>
  );
}
