"use client";

import { BestRunMarker, MetricGoalMarker } from "@/components/experiments/metric-goal-marker";
import { Card } from "@/components/ui/card";
import { type BestRuns, isBestRun, type MetricGoals, metricGoal } from "@/lib/exp-goals";
import { groupSummaryKeys } from "@/lib/exp-runpage-config";
import { formatMetricValue, metricCellText } from "@/lib/experiments";
import { useT } from "@/lib/i18n/client";
import type { ExpRun } from "@/types/api";

/**
 * Every metric a run reported, one card each — goal metrics first, then the
 * rest, then `system/*` telemetry folded away. Each card shows: the last value large, the
 * smallest and largest value the run ever logged below it.
 *
 * The last value alone hides the question people most often ask of a run —
 * "how good did it get?" — whenever the curve overshot or diverged at the
 * end. A metric with a goal carries its ↓ / ↑ marker, and the trophy when
 * this run is the project's best for it.
 *
 * `formatMetricValue` is the same function the run table's cells use, so a
 * value like `2.3e-10` cannot read one way here and another way there.
 */
export function RunSummaryCards({
  run,
  goals,
  best,
}: {
  run: ExpRun;
  goals: MetricGoals;
  best: BestRuns;
}) {
  const t = useT();
  const groups = groupSummaryKeys(Object.keys(run.summary ?? {}), goals);
  const main = [...groups.goals, ...groups.other];

  if (main.length === 0 && groups.system.length === 0) {
    return <p className="text-sm text-fg-subtle">{t("experiments.run.summaryEmpty")}</p>;
  }

  return (
    <div className="flex flex-col gap-2">
      {main.length > 0 && <CardGrid run={run} keys={main} goals={goals} best={best} />}
      {/* Telemetry (GPU memory, utilisation…) is the same for every run of a
          sweep and rarely the question; it stays one click away. */}
      {groups.system.length > 0 && (
        <details className="group">
          <summary className="w-fit cursor-pointer rounded-md py-1 text-xs font-medium text-fg-subtle hover:text-fg">
            {t("experiments.runPage.systemMetrics", { count: groups.system.length })}
          </summary>
          <div className="pt-2">
            <CardGrid run={run} keys={groups.system} goals={goals} best={best} />
          </div>
        </details>
      )}
    </div>
  );
}

function CardGrid({
  run,
  keys,
  goals,
  best,
}: {
  run: ExpRun;
  keys: string[];
  goals: MetricGoals;
  best: BestRuns;
}) {
  const t = useT();
  return (
    <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
      {keys.map((key) => {
        const value = run.summary[key];
        const goal = metricGoal(goals, key);
        return (
          <Card key={key} className="flex min-w-0 flex-col gap-0.5 px-3 py-2.5">
            <span className="flex min-w-0 items-center gap-1">
              <span className="truncate text-xs font-medium text-fg-subtle" title={key}>
                {key}
              </span>
              <MetricGoalMarker metric={key} goal={goal} canWrite={false} />
              {isBestRun(best, key, run.name) && <BestRunMarker metric={key} goal={goal} />}
            </span>
            <span className="truncate tabular-nums text-lg font-semibold">
              {value === undefined ? metricCellText(undefined) : formatMetricValue(value)}
            </span>
            <dl className="flex flex-wrap gap-x-3 text-xs font-medium text-fg-subtle">
              <div className="flex gap-1">
                <dt>{t("experiments.summaryCard.min")}</dt>
                <dd
                  className={goal === "min" ? "tabular-nums text-fg" : "tabular-nums text-fg-muted"}
                >
                  {metricCellText(run.summary_min?.[key])}
                </dd>
              </div>
              <div className="flex gap-1">
                <dt>{t("experiments.summaryCard.max")}</dt>
                <dd
                  className={goal === "max" ? "tabular-nums text-fg" : "tabular-nums text-fg-muted"}
                >
                  {metricCellText(run.summary_max?.[key])}
                </dd>
              </div>
            </dl>
          </Card>
        );
      })}
    </div>
  );
}
