"use client";

import { SlidersHorizontal, Trophy } from "lucide-react";
import { useMemo, useState } from "react";

import { RunColorDot } from "@/components/experiments/run-color-dot";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Checkbox } from "@/components/ui/field";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
import { runColorIndex } from "@/lib/chart-utils";
import { cn } from "@/lib/cn";
import { effectiveDirection, type MetricGoals, type SummaryMode } from "@/lib/exp-goals";
import { metricCellText } from "@/lib/experiments";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { buildConfigDiff } from "@/lib/run-compare";
import { isReservedConfigKey } from "@/lib/run-config";
import type { ExpRun } from "@/types/api";

const MODE_LABEL: Record<SummaryMode, MessageKey> = {
  last: "experiments.workspace.modeLast",
  min: "experiments.workspace.modeMin",
  max: "experiments.workspace.modeMax",
  best: "experiments.workspace.modeBest",
};

/**
 * Hyperparameter comparison: one row per config key, one column per selected
 * run. Rows where the runs disagree are highlighted, and "Differences only"
 * (on by default) hides the rest — with a sweep of 40 keys, the handful that
 * actually moved is the whole question.
 *
 * With `resultMetrics`, the goal metrics come first as their own rows, so the
 * table reads config → result in one place: which of these differences went
 * with the better number.
 */
export function ConfigDiffTable({
  runs,
  runOrder,
  baseline,
  resultMetrics = [],
  goals = {},
  summaryMode = "last",
}: {
  /** The runs to compare, with `summary` projected to `summaryMode`. */
  runs: ExpRun[];
  /** Full project run order, so a run keeps its chart colour here too. */
  runOrder: string[];
  baseline?: string;
  /** Metrics shown as result rows above the config, in this order. */
  resultMetrics?: string[];
  goals?: MetricGoals;
  /** Which summary `runs[].summary` holds, for the section's label. */
  summaryMode?: SummaryMode;
}) {
  const t = useT();
  const [diffOnly, setDiffOnly] = useState(true);
  // The reserved sections are folded away by default: the environment snapshot
  // and the TrainingArguments differ on something in almost every pair of runs
  // (a git commit, an output_dir), which buries the two hyperparameters the
  // sweep actually moved. The run detail page shows them in full.
  const [showReserved, setShowReserved] = useState(false);

  const colorIndex = useMemo(() => runColorIndex(runOrder), [runOrder]);
  const allRows = useMemo(() => buildConfigDiff(runs), [runs]);
  const reservedCount = useMemo(
    () => allRows.filter((r) => isReservedConfigKey(r.key)).length,
    [allRows],
  );
  const rows = showReserved ? allRows : allRows.filter((r) => !isReservedConfigKey(r.key));
  const visible = diffOnly ? rows.filter((r) => r.differs) : rows;
  const diffCount = rows.filter((r) => r.differs).length;
  const resultRows = useMemo(
    () =>
      resultMetrics
        .map((metric) => {
          const values = runs.map((run) => {
            const value = run.summary?.[metric];
            return typeof value === "number" && Number.isFinite(value) ? value : undefined;
          });
          const present = values.filter((v): v is number => v !== undefined);
          const best =
            present.length < 2
              ? undefined
              : effectiveDirection(goals, metric) === "min"
                ? Math.min(...present)
                : Math.max(...present);
          return { metric, values, best };
        })
        .filter((row) => row.values.some((v) => v !== undefined)),
    [resultMetrics, runs, goals],
  );

  if (runs.length === 0) {
    return (
      <EmptyState
        icon={SlidersHorizontal}
        title={t("experiments.configDiff.noRunsTitle")}
        description={t("experiments.configDiff.noRunsDescription")}
      />
    );
  }

  if (allRows.length === 0) {
    return (
      <EmptyState
        icon={SlidersHorizontal}
        title={t("experiments.configDiff.noConfigTitle")}
        description={t("experiments.configDiff.noConfigDescription")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
        <span className="text-fg-subtle">
          {t(
            runs.length === 1
              ? "experiments.configDiff.summaryOne"
              : "experiments.configDiff.summaryOther",
            { diff: diffCount, total: rows.length, count: runs.length },
          )}
        </span>
        <div className="flex flex-wrap items-center gap-4">
          {reservedCount > 0 && (
            <label className="flex items-center gap-2">
              <Checkbox
                checked={showReserved}
                onChange={(e) => setShowReserved(e.target.checked)}
              />
              <span className="text-fg-subtle">
                {t("experiments.configDiff.showReserved", { count: reservedCount })}
              </span>
            </label>
          )}
          <label className="flex items-center gap-2">
            <Checkbox checked={diffOnly} onChange={(e) => setDiffOnly(e.target.checked)} />
            <span className="text-fg-subtle">{t("experiments.configDiff.differencesOnly")}</span>
          </label>
        </div>
      </div>

      {visible.length === 0 && resultRows.length === 0 ? (
        // rows.length === 0 means everything this run logged lives in the
        // folded sections, which is a different answer from "the runs agree".
        <EmptyState
          icon={SlidersHorizontal}
          title={
            rows.length === 0
              ? t("experiments.configDiff.onlyReservedTitle")
              : t("experiments.configDiff.noDiffTitle")
          }
          description={
            rows.length === 0
              ? t("experiments.configDiff.onlyReservedDescription")
              : t("experiments.configDiff.noDiffDescription")
          }
        />
      ) : (
        <Table
          tableClassName="whitespace-nowrap"
          className="max-h-[calc(100dvh-14rem)] overflow-y-auto"
        >
          <THead sticky>
            <Th className="sticky left-0 z-20 bg-bg-sunken">
              {t("experiments.configDiff.colParameter")}
            </Th>
            {runs.map((run) => (
              <Th key={run.name}>
                <span className="flex max-w-[16rem] items-center gap-2">
                  <RunColorDot run={run.name} colorIndex={colorIndex} />
                  <span className="truncate text-fg" title={run.name}>
                    {run.name}
                  </span>
                  {run.name === baseline && (
                    <Badge tone="accent">{t("experiments.table.baselineBadge")}</Badge>
                  )}
                </span>
              </Th>
            ))}
          </THead>
          <TBody>
            {resultRows.length > 0 && (
              <>
                <SectionRow
                  label={t("experiments.workspace.configResults", {
                    mode: t(MODE_LABEL[summaryMode]),
                  })}
                  span={runs.length + 1}
                />
                {resultRows.map((row) => (
                  <Tr key={`metric:${row.metric}`} className="hover:bg-bg-hover">
                    <Th
                      scope="row"
                      className="sticky left-0 z-10 bg-bg-raised text-left font-mono text-xs text-fg"
                    >
                      {row.metric}
                    </Th>
                    {row.values.map((value, i) => {
                      const isBest = value !== undefined && value === row.best;
                      return (
                        <Td
                          key={runs[i]?.name ?? i}
                          className={cn(
                            "tabular-nums",
                            isBest ? "font-semibold text-fg" : "text-fg-muted",
                          )}
                        >
                          <span className="inline-flex items-center gap-1">
                            {metricCellText(value)}
                            {isBest && (
                              <Trophy
                                size={12}
                                className="text-warning"
                                role="img"
                                aria-label={t("experiments.workspace.configBestAria", {
                                  metric: row.metric,
                                })}
                              />
                            )}
                          </span>
                        </Td>
                      );
                    })}
                  </Tr>
                ))}
                <SectionRow
                  label={t("experiments.workspace.configParams")}
                  span={runs.length + 1}
                />
              </>
            )}
            {visible.length === 0 ? (
              <Tr>
                <Td colSpan={runs.length + 1} className="text-fg-subtle">
                  {rows.length === 0
                    ? t("experiments.configDiff.onlyReservedTitle")
                    : t("experiments.configDiff.noDiffTitle")}
                </Td>
              </Tr>
            ) : (
              visible.map((row) => (
                <Tr key={row.key} className={row.differs ? "bg-warning/10" : "hover:bg-bg-hover"}>
                  {/* Opaque background so the horizontally scrolled values
                      pass behind the pinned key column rather than through it. */}
                  <Th
                    scope="row"
                    className="sticky left-0 z-10 bg-bg-raised text-left text-fg-muted"
                  >
                    {row.key}
                  </Th>
                  {row.values.map((value, i) => (
                    <Td
                      // The run name is the column identity; values repeat.
                      key={runs[i]?.name ?? i}
                      className={cn(
                        "tabular-nums",
                        row.differs ? "font-medium text-fg" : "text-fg-muted",
                      )}
                    >
                      <span className="block max-w-[24rem] truncate" title={value}>
                        {value}
                      </span>
                    </Td>
                  ))}
                </Tr>
              ))
            )}
          </TBody>
        </Table>
      )}
    </div>
  );
}

/** A full-width label row separating the result rows from the config rows. */
function SectionRow({ label, span }: { label: string; span: number }) {
  return (
    <tr className="border-b border-border bg-bg-sunken">
      <th
        colSpan={span}
        scope="colgroup"
        className="sticky left-0 px-3 py-1.5 text-left text-xs font-medium text-fg-subtle"
      >
        {label}
      </th>
    </tr>
  );
}
