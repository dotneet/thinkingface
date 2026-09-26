"use client";

import { CircleX, Trophy, TriangleAlert } from "lucide-react";
import Link from "next/link";

import { MetricGoalMarker } from "@/components/experiments/metric-goal-marker";
import { RunStatusIcon } from "@/components/experiments/run-status-indicator";
import { formatColumnValue } from "@/lib/exp-chart-ticks";
import { type MetricGoals, metricGoal } from "@/lib/exp-goals";
import { expRunHref } from "@/lib/experiments";
import { useT } from "@/lib/i18n/client";

/**
 * The top of the workspace: where this project sits, what it is called, and
 * one line that answers the two questions a researcher opens it with — "is
 * anything running (or dead)?" and "what is the best result so far, and
 * which run got it?".
 */
export function WorkspaceHeader({
  ns,
  repo,
  project,
  totalRuns,
  running,
  stale,
  failed,
  metric,
  goals,
  best,
  canWrite,
  onEditGoal,
}: {
  ns: string;
  repo: string;
  project: string;
  totalRuns: number;
  running: number;
  stale: number;
  failed: number;
  /** The metric the "best" fact is about, or undefined without any goal. */
  metric: string | undefined;
  goals: MetricGoals;
  best: { run: string; value: number } | null;
  canWrite: boolean;
  onEditGoal: (metric: string) => void;
}) {
  const t = useT();
  const repoHref = `/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}`;

  return (
    <div className="flex min-w-0 flex-col gap-1">
      <nav className="flex min-w-0 items-center gap-1.5 text-sm text-fg-subtle">
        <Link href="/experiments" className="shrink-0 hover:text-fg hover:underline">
          {t("experiments.repo.breadcrumbRoot")}
        </Link>
        <span aria-hidden>/</span>
        <Link href={repoHref} className="min-w-0 truncate hover:text-fg hover:underline">
          {ns}/{repo}
        </Link>
      </nav>
      <h1 className="truncate text-2xl font-semibold tracking-tight" title={project}>
        {project}
      </h1>
      <ul
        aria-label={t("experiments.workspace.factsAria")}
        className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-fg-muted"
      >
        <li className="whitespace-nowrap tabular-nums">
          {t(
            totalRuns === 1
              ? "experiments.workspace.factRunsOne"
              : "experiments.workspace.factRunsOther",
            { count: totalRuns },
          )}
        </li>
        {running > 0 && (
          <li className="inline-flex items-center gap-1.5 whitespace-nowrap tabular-nums text-fg">
            <RunStatusIcon status="running" />
            {t("experiments.workspace.factRunning", { count: running })}
          </li>
        )}
        {stale > 0 && (
          <li className="inline-flex items-center gap-1.5 whitespace-nowrap tabular-nums">
            <TriangleAlert size={13} className="text-warning" aria-hidden />
            {t("experiments.workspace.factStale", { count: stale })}
          </li>
        )}
        {failed > 0 && (
          <li className="inline-flex items-center gap-1.5 whitespace-nowrap tabular-nums">
            <CircleX size={13} className="text-negative" aria-hidden />
            {t("experiments.workspace.factFailed", { count: failed })}
          </li>
        )}
        {metric ? (
          <li className="inline-flex min-w-0 items-center gap-1.5 whitespace-nowrap">
            <Trophy size={13} className="shrink-0 text-warning" aria-hidden />
            <span className="font-mono text-xs">
              {t("experiments.workspace.factBest", { metric })}
            </span>
            <MetricGoalMarker
              metric={metric}
              goal={metricGoal(goals, metric)}
              canWrite={canWrite}
              onEdit={() => onEditGoal(metric)}
            />
            {best ? (
              <>
                <span className="font-semibold tabular-nums text-fg" title={String(best.value)}>
                  {formatColumnValue(best.value)}
                </span>
                <span className="text-fg-subtle">{t("experiments.workspace.factBestBy")}</span>
                <Link
                  href={expRunHref(ns, repo, project, best.run)}
                  className="min-w-0 truncate font-medium text-accent hover:underline"
                  title={best.run}
                >
                  {best.run}
                </Link>
              </>
            ) : (
              <span className="text-fg-subtle">—</span>
            )}
          </li>
        ) : (
          <li
            className="whitespace-nowrap text-fg-subtle"
            title={t("experiments.workspace.factNoGoalHint")}
          >
            {t("experiments.workspace.factNoGoal")}
          </li>
        )}
      </ul>
    </div>
  );
}
