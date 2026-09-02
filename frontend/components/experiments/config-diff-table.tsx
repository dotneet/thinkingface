"use client";

import { SlidersHorizontal } from "lucide-react";
import { useMemo, useState } from "react";
import { RunColorDot } from "@/components/experiments/run-color-dot";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Checkbox } from "@/components/ui/field";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
import { runColorIndex } from "@/lib/chart-utils";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";
import { buildConfigDiff } from "@/lib/run-compare";
import { isReservedConfigKey } from "@/lib/run-config";
import type { ExpRun } from "@/types/api";

/**
 * Hyperparameter comparison: one row per config key, one column per selected
 * run. Rows where the runs disagree are highlighted, and "Differences only"
 * hides the rest — with a sweep of 40 keys, the handful that actually moved is
 * the whole question.
 */
export function ConfigDiffTable({
  runs,
  runOrder,
  baseline,
}: {
  runs: ExpRun[];
  /** Full project run order, so a run keeps its chart colour here too. */
  runOrder: string[];
  baseline?: string;
}) {
  const t = useT();
  const [diffOnly, setDiffOnly] = useState(false);
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

      {visible.length === 0 ? (
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
        <Table>
          <THead>
            <Th className="sticky left-0 z-10 bg-bg-raised">
              {t("experiments.configDiff.colParameter")}
            </Th>
            {runs.map((run) => (
              <Th key={run.name} className="whitespace-nowrap">
                <span className="flex items-center gap-2">
                  <RunColorDot run={run.name} colorIndex={colorIndex} />
                  <span className="text-fg">{run.name}</span>
                  {run.name === baseline && (
                    <Badge tone="accent">{t("experiments.table.baselineBadge")}</Badge>
                  )}
                </span>
              </Th>
            ))}
          </THead>
          <TBody>
            {visible.map((row) => (
              <Tr key={row.key} className={row.differs ? "bg-warning/10" : "hover:bg-bg-hover"}>
                {/* Opaque background so the horizontally scrolled values
                    pass behind the pinned key column rather than through it. */}
                <Th scope="row" className="sticky left-0 z-10 bg-bg-raised text-left text-fg-muted">
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
                    {value}
                  </Td>
                ))}
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
    </div>
  );
}
