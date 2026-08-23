import Link from "next/link";
import { RunDetail } from "@/components/experiments/run-detail";
import { ErrorState } from "@/components/ui/error-state";
import { listRuns } from "@/lib/experiments";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getRepo } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export default async function ExperimentRunPage({
  params,
}: {
  params: Promise<{ ns: string; repo: string; project: string; run: string }>;
}) {
  // A run name may contain a slash and anything else that needs escaping;
  // `decodeRouteParams` undoes every escape Next leaves in the segment.
  const { ns, repo, project, run } = decodeRouteParams(await params);

  // Forward the tf_session cookie so an experiment repo the viewer can
  // see resolves instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [result, repoResult, t] = await Promise.all([
    listRuns(ns, repo, project, { headers }),
    // Only `can_write` is wanted here, and the run listing does not carry it.
    // A failure just means the editing affordances stay hidden.
    getRepo("dataset", ns, repo, { headers }),
    getT(),
  ]);
  redirectIfRepoMoved(
    result,
    (toNs, toRepo) =>
      `/experiments/${encodeURIComponent(toNs)}/${encodeURIComponent(toRepo)}/${encodeURIComponent(project)}/${encodeURIComponent(run)}`,
  );

  const projectHref = `/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}`;

  return (
    <div className="flex flex-col gap-6">
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
        <Link href={projectHref} className="hover:text-fg hover:underline">
          {project}
        </Link>
        <span>/</span>
        <span className="break-all text-fg">{run}</span>
      </div>

      {!result.ok ? (
        <ErrorState
          title={t("experiments.errorTitle")}
          message={result.message}
          hint={t("experiments.project.errorHint")}
        />
      ) : (
        <RunDetail
          ns={ns}
          repo={repo}
          project={project}
          runName={run}
          runs={result.data.runs}
          canWrite={repoResult.ok && repoResult.data.repo.can_write}
        />
      )}
    </div>
  );
}
