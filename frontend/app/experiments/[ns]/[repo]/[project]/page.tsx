import type { Metadata } from "next";
import Link from "next/link";

import { titleMetadata } from "@/app/page-metadata";
import { ExperimentDashboard } from "@/components/experiments/experiment-dashboard";
import { ErrorState } from "@/components/ui/error-state";
import { errorMessage } from "@/lib/api-error-message";
import { runListBest, runListGoals } from "@/lib/exp-goals";
import { getProjectNotes, listRuns } from "@/lib/experiments";
import { getT } from "@/lib/i18n/server";
import { getExperimentLineage, type RunModels, toRunModels } from "@/lib/lineage";
import { decodeRouteParams } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getRepo } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; repo: string; project: string }>;
}): Promise<Metadata> {
  const [{ ns, repo, project }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(t("meta.experiments"), `${ns}/${repo}`, project);
}

export default async function ExperimentProjectPage({
  params,
}: {
  params: Promise<{ ns: string; repo: string; project: string }>;
}) {
  const { ns, repo, project } = decodeRouteParams(await params);
  // Forward the tf_session cookie so an experiment repo the viewer
  // can see resolves instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [result, lineage, repoResult, notes, t] = await Promise.all([
    listRuns(ns, repo, project, { headers }),
    // One request covers every run of the project, so the run table can show
    // the checkpoints each run produced without a query per row.
    getExperimentLineage(ns, repo, project, undefined, { headers }),
    // Only `can_write` is wanted here (goals, notes), and the run listing does
    // not carry it. A failure just means the editing affordances stay hidden.
    getRepo("dataset", ns, repo, { headers }),
    // The notebook. A failure here is the notes section's own error state
    // (it retries client-side), never the page's.
    getProjectNotes(ns, repo, project, { headers }),
    getT(),
  ]);
  const canWrite = repoResult.ok && repoResult.data.repo.can_write;
  redirectIfRepoMoved(
    result,
    (toNs, toRepo) =>
      `/experiments/${encodeURIComponent(toNs)}/${encodeURIComponent(toRepo)}/${encodeURIComponent(project)}`,
  );

  // Lineage is supporting information: if it fails to load, the charts and the
  // run table still work, they just carry no checkpoint links.
  const runModels: RunModels = lineage.ok ? toRunModels(lineage.data) : {};

  if (!result.ok) {
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
          </div>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">{project}</h1>
        </div>
        <ErrorState
          title={t("experiments.errorTitle")}
          message={errorMessage(t, result)}
          hint={t("experiments.project.errorHint")}
        />
      </div>
    );
  }

  // The workspace draws its own header (breadcrumb, title, live facts), so
  // the facts can follow the run list as it refreshes.
  return (
    <ExperimentDashboard
      ns={ns}
      repo={repo}
      project={project}
      runs={result.data.runs}
      metricGoals={runListGoals(result.data)}
      best={runListBest(result.data)}
      runModels={runModels}
      canWrite={canWrite}
      initialNotes={notes.ok ? notes.data : null}
    />
  );
}
