import { FlaskConical } from "lucide-react";
import Link from "next/link";
import { notFound } from "next/navigation";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { TimeText } from "@/components/ui/time-text";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getExperimentRepo } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export default async function ExperimentRepoPage({
  params,
}: {
  params: Promise<{ ns: string; repo: string }>;
}) {
  const { ns, repo } = decodeRouteParams(await params);
  // Forward the tf_session cookie so an experiment repo the viewer
  // can see resolves instead of 404ing (see lib/server-auth.ts).
  const [result, t] = await Promise.all([
    getExperimentRepo(ns, repo, { headers: await authHeaders() }),
    getT(),
  ]);

  redirectIfRepoMoved(
    result,
    (toNs, toRepo) => `/experiments/${encodeURIComponent(toNs)}/${encodeURIComponent(toRepo)}`,
  );
  if (isNotFound(result)) notFound();
  if (!result.ok) {
    return <ErrorState title={t("experiments.errorTitle")} message={errorMessage(t, result)} />;
  }

  const { repo: repoInfo, projects } = result.data;

  return (
    <div className="flex flex-col gap-6">
      <div>
        <div className="flex items-center gap-1.5 text-sm text-fg-subtle">
          <Link href="/experiments" className="hover:text-fg hover:underline">
            {t("experiments.repo.breadcrumbRoot")}
          </Link>
          <span>/</span>
          <span className="text-fg">{repoInfo.full_name}</span>
        </div>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">{repoInfo.full_name}</h1>
        {repoInfo.description && (
          <p className="mt-1 text-sm text-fg-subtle">{repoInfo.description}</p>
        )}
      </div>

      {projects.length === 0 ? (
        <EmptyState icon={FlaskConical} title={t("experiments.repo.noProjects")} />
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {projects.map((project) => (
            <Link
              key={project.name}
              href={`/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project.name)}`}
              className="flex flex-col gap-2 rounded-lg border border-border bg-bg-raised p-4 transition-colors hover:border-border-strong hover:bg-bg-hover"
            >
              <div className="flex items-center gap-1.5 text-sm font-medium">
                <FlaskConical size={14} className="text-fg-subtle" />
                {project.name}
              </div>
              <div className="flex items-center gap-3 text-xs font-medium text-fg-subtle">
                <span className="tabular-nums">
                  {t(
                    project.num_runs === 1
                      ? "experiments.repo.runsOne"
                      : "experiments.repo.runsOther",
                    { count: formatNumber(project.num_runs) },
                  )}
                </span>
                <TimeText iso={project.updated_at} style="relative" />
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}
