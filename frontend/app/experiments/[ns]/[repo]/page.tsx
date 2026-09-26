import { ArrowDown, ArrowUp, Database, FlaskConical, FolderOpen } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";

import { titleMetadata } from "@/app/page-metadata";
import { ExpListStatusCounts } from "@/components/experiments/exp-list-status-counts";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { TimeText } from "@/components/ui/time-text";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { expRunHref, formatMetricValue, getExperimentRepo } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { decodeRouteParams, repoBase, repoTreeHref } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { authHeaders } from "@/lib/server-auth";

export const dynamic = "force-dynamic";

export async function generateMetadata({
  params,
}: {
  params: Promise<{ ns: string; repo: string }>;
}): Promise<Metadata> {
  const [{ ns, repo }, t] = await Promise.all([params.then(decodeRouteParams), getT()]);
  return titleMetadata(t("meta.experiments"), `${ns}/${repo}`);
}

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
  const repoHref = `/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}`;

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-1.5 text-sm text-fg-subtle">
          <Link href="/experiments" className="hover:text-fg hover:underline">
            {t("experiments.repo.breadcrumbRoot")}
          </Link>
          <span>/</span>
          <span className="truncate text-fg">{repoInfo.full_name}</span>
        </div>
        <h1 className="truncate text-2xl font-semibold tracking-tight">{repoInfo.full_name}</h1>
        {repoInfo.description && <p className="text-sm text-fg-subtle">{repoInfo.description}</p>}
        {/* The runs are files in a dataset repository; say so, and link to it
            — that is where `git clone`, the file browser and the HF API
            reach the same data. */}
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
          <span className="flex items-center gap-1.5 text-fg-subtle">
            <Database size={14} className="shrink-0" />
            {t("experiments.listPage.datasetRepo")}
          </span>
          <Link
            href={repoBase("dataset", ns, repo)}
            className="font-medium text-accent hover:underline"
          >
            {repoInfo.full_name}
          </Link>
          <Link
            href={repoTreeHref("dataset", ns, repo, repoInfo.default_branch || "main")}
            className="flex items-center gap-1 text-fg-muted hover:text-fg hover:underline"
          >
            <FolderOpen size={14} />
            {t("experiments.listPage.files")}
          </Link>
          <span className="text-xs font-medium text-fg-subtle">
            {t("experiments.listPage.dataHint")}
          </span>
        </div>
      </div>

      {projects.length === 0 ? (
        <EmptyState icon={FlaskConical} title={t("experiments.repo.noProjects")} />
      ) : (
        <section className="flex flex-col gap-2">
          <h2 className="text-sm font-semibold">
            {t("experiments.listPage.projectsTitle")}{" "}
            <span className="font-medium tabular-nums text-fg-subtle">
              {formatNumber(projects.length)}
            </span>
          </h2>
          <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-lg border border-border bg-bg-raised">
            {projects.map((project) => {
              const projectHref = `${repoHref}/${encodeURIComponent(project.name)}`;
              const best = project.best ?? [];
              return (
                <li
                  key={project.name}
                  className="relative grid grid-cols-1 gap-x-6 gap-y-2 px-4 py-3 transition-colors hover:bg-bg-hover md:grid-cols-[minmax(0,22rem)_minmax(0,1fr)]"
                >
                  <div className="flex min-w-0 flex-col gap-1">
                    {/* Stretched: the whole row opens the project. The best
                        runs' links sit above it (z-10) and open that run. */}
                    <Link
                      href={projectHref}
                      className="flex min-w-0 items-center gap-1.5 text-base font-semibold text-fg after:absolute after:inset-0 after:content-[''] hover:underline"
                    >
                      <FlaskConical size={15} className="shrink-0 text-fg-subtle" />
                      <span className="truncate" title={project.name}>
                        {project.name}
                      </span>
                    </Link>
                    <div className="flex items-center gap-2 text-xs font-medium text-fg-subtle">
                      <span className="tabular-nums">
                        {t(
                          project.num_runs === 1
                            ? "experiments.repo.runsOne"
                            : "experiments.repo.runsOther",
                          { count: formatNumber(project.num_runs) },
                        )}
                      </span>
                      <span aria-hidden>·</span>
                      <span>
                        {t("experiments.listPage.updated")}{" "}
                        <TimeText iso={project.updated_at} style="relative" />
                      </span>
                    </div>
                    <ExpListStatusCounts counts={project.status_counts} />
                  </div>

                  {best.length === 0 ? (
                    <p
                      className="self-center text-xs font-medium text-fg-subtle"
                      title={t("experiments.listPage.noGoalsHint")}
                    >
                      {t("experiments.listPage.noGoals")}
                    </p>
                  ) : (
                    <dl className="grid min-w-0 grid-cols-[minmax(0,max-content)_max-content_minmax(0,1fr)] items-center gap-x-3 gap-y-0.5 self-center text-sm">
                      {best.map((entry) => (
                        <div key={entry.metric} className="contents">
                          <dt className="flex min-w-0 items-center gap-1 text-fg-muted">
                            <span className="truncate" title={entry.metric}>
                              {entry.metric}
                            </span>
                            <span
                              role="img"
                              className="inline-flex shrink-0 text-fg-subtle"
                              aria-label={t(
                                entry.goal === "min"
                                  ? "experiments.goals.markerMin"
                                  : "experiments.goals.markerMax",
                                { metric: entry.metric },
                              )}
                              title={t(
                                entry.goal === "min"
                                  ? "experiments.goals.markerMin"
                                  : "experiments.goals.markerMax",
                                { metric: entry.metric },
                              )}
                            >
                              {entry.goal === "min" ? (
                                <ArrowDown size={12} />
                              ) : (
                                <ArrowUp size={12} />
                              )}
                            </span>
                          </dt>
                          <dd className="text-right font-semibold tabular-nums text-fg">
                            {formatMetricValue(entry.value)}
                          </dd>
                          <dd className="min-w-0 truncate text-xs font-medium text-fg-subtle">
                            <span className="sr-only">{t("experiments.listPage.bestBy")} </span>
                            <Link
                              href={expRunHref(ns, repo, project.name, entry.run)}
                              title={entry.run}
                              className="relative z-10 text-accent hover:underline"
                            >
                              {entry.run}
                            </Link>
                          </dd>
                        </div>
                      ))}
                    </dl>
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      )}
    </div>
  );
}
