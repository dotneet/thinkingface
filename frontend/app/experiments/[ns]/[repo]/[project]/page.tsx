import Link from "next/link";
import { ExperimentDashboard } from "@/components/experiments/experiment-dashboard";
import { ErrorState } from "@/components/ui/error-state";
import { errorMessage } from "@/lib/api-error-message";
import { listRuns } from "@/lib/experiments";
import { getT } from "@/lib/i18n/server";
import { getExperimentLineage, type RunModels, toRunModels } from "@/lib/lineage";
import { decodeRouteParams } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export default async function ExperimentProjectPage({
  params,
}: {
  params: Promise<{ ns: string; repo: string; project: string }>;
}) {
  const { ns, repo, project } = decodeRouteParams(await params);
  // Forward the tf_session cookie so an experiment repo the viewer
  // can see resolves instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [result, lineage, t] = await Promise.all([
    listRuns(ns, repo, project, { headers }),
    // One request covers every run of the project, so the run table can show
    // the checkpoints each run produced without a query per row.
    getExperimentLineage(ns, repo, project, undefined, { headers }),
    getT(),
  ]);
  redirectIfRepoMoved(
    result,
    (toNs, toRepo) =>
      `/experiments/${encodeURIComponent(toNs)}/${encodeURIComponent(toRepo)}/${encodeURIComponent(project)}`,
  );

  // Lineage is supporting information: if it fails to load, the charts and the
  // run table still work, they just carry no checkpoint links.
  const runModels: RunModels = lineage.ok ? toRunModels(lineage.data) : {};

  return (
    <div className="flex flex-col gap-6">
      <div>
        <div className="flex flex-wrap items-center gap-1.5 text-sm text-fg-subtle">
          <Link href="/experiments" className="hover:text-fg hover:underline">
            {t("experiments.repo.breadcrumbRoot")}
          </Link>
          <span>/</span>
          <Link
            href={`/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}`}
            className="hover:text-fg hover:underline"
          >
            {ns}/{repo}
          </Link>
          <span>/</span>
          <span className="text-fg">{project}</span>
        </div>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight">{project}</h1>
      </div>

      {!result.ok ? (
        <ErrorState
          title={t("experiments.errorTitle")}
          message={errorMessage(t, result)}
          hint={t("experiments.project.errorHint")}
        />
      ) : (
        <ExperimentDashboard
          ns={ns}
          repo={repo}
          project={project}
          runs={result.data.runs}
          runModels={runModels}
        />
      )}
    </div>
  );
}
