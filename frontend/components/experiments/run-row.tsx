"use client";

import { Archive, ArchiveRestore, Star, Tag, Trash2 } from "lucide-react";
import Link from "next/link";

import { BestRunMarker } from "@/components/experiments/metric-goal-marker";
import { RunColorDot } from "@/components/experiments/run-color-dot";
import { RunStatusInline } from "@/components/experiments/run-status-indicator";
import { runColumnKey, useRunTable } from "@/components/experiments/run-table-context";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/field";
import { Td, Tr } from "@/components/ui/table";
import { TimeText } from "@/components/ui/time-text";
import { cn } from "@/lib/cn";
import { isBestRun, metricGoal } from "@/lib/exp-goals";
import { expRunHref, metricCellText } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { repoBase } from "@/lib/paths";
import type { ExpRun } from "@/types/api";

/** Placeholder for a cell this run has nothing to put in. */
function Dash() {
  return <span className="text-fg-subtle">—</span>;
}

/**
 * One run's row.
 *
 * Everything that is the same for every row — the project it belongs to, the
 * colour order, the column list, the checkpoint index and the action handlers
 * — comes from `useRunTable()`; the props are only what actually varies per
 * row. The cells are rendered by walking the shared column list rather than
 * written out one after another, so this row and the group header above it
 * cannot end up with different numbers of `<td>`s.
 */
export function RunRow({
  run,
  nested,
  selected,
  onToggle,
}: {
  run: ExpRun;
  /** True for a member of an expanded group, which is indented. */
  nested: boolean;
  selected: Set<string>;
  onToggle: (name: string) => void;
}) {
  const t = useT();
  const { ns, repo, project, colorIndex, columns, runModels, actions, goals, best } = useRunTable();
  const busy = actions.pendingRun === run.name;
  const models = runModels?.[run.name] ?? [];

  return (
    <Tr className={cn("group hover:bg-bg-hover", run.archived && "opacity-60")}>
      {columns.map((column) => {
        const key = runColumnKey(column);
        switch (column.id) {
          case "select":
            return (
              <Td key={key}>
                <Checkbox
                  checked={selected.has(run.name)}
                  onChange={() => onToggle(run.name)}
                  aria-label={t("experiments.table.selectRun", { name: run.name })}
                />
              </Td>
            );
          case "name":
            return (
              <Td key={key} className="sticky left-0 z-[1] bg-bg group-hover:bg-bg-hover">
                <span
                  className={cn(
                    "flex items-center gap-2 font-medium",
                    nested && "pl-6",
                    run.archived && "text-fg-subtle line-through",
                  )}
                >
                  <RunColorDot run={run.name} colorIndex={colorIndex} />
                  <Link
                    href={expRunHref(ns, repo, project, run.name)}
                    title={run.name}
                    className="max-w-[20rem] truncate hover:text-accent hover:underline"
                  >
                    {run.name}
                  </Link>
                  {run.is_baseline && (
                    <Badge tone="accent">{t("experiments.table.baselineBadge")}</Badge>
                  )}
                  {run.archived && <Badge>{t("experiments.table.archivedBadge")}</Badge>}
                </span>
              </Td>
            );
          case "status":
            return (
              <Td key={key}>
                <RunStatusInline status={run.status} updatedAt={run.updated_at} />
              </Td>
            );
          case "tags":
            return (
              <Td key={key}>
                {run.tags.length === 0 ? (
                  <Dash />
                ) : (
                  // One line, clipped: the full list is on hover. Wrapping here
                  // is what made a row with three tags three lines tall.
                  <div
                    className="flex max-w-[16rem] gap-1.5 overflow-hidden"
                    title={run.tags.join(", ")}
                  >
                    {run.tags.map((tag) => (
                      <Badge key={tag} className="shrink-0">
                        {tag}
                      </Badge>
                    ))}
                  </div>
                )}
              </Td>
            );
          case "lastStep":
            return (
              <Td key={key} align="right" className="tabular-nums">
                {formatNumber(run.last_step)}
              </Td>
            );
          case "metric": {
            const value = run.summary?.[column.metric];
            return (
              <Td key={key} align="right" className="tabular-nums">
                <span className="inline-flex items-center justify-end gap-1">
                  {typeof value === "number" ? metricCellText(value) : <Dash />}
                  {isBestRun(best, column.metric, run.name) && (
                    <BestRunMarker metric={column.metric} goal={metricGoal(goals, column.metric)} />
                  )}
                </span>
              </Td>
            );
          }
          case "started":
            return (
              <Td key={key} className="text-fg-muted">
                {/* Relative on the page, the exact time on hover (TimeText's
                    title) — "2026年9月27日 05:51" wrapped to three lines. */}
                <TimeText iso={run.started_at} style="relative" />
              </Td>
            );
          case "models":
            return (
              <Td key={key}>
                {models.length === 0 || !models[0] ? (
                  <Dash />
                ) : (
                  // The first checkpoint as a link, the rest counted: a stacked
                  // list made the row as tall as the number of checkpoints.
                  <span
                    className="inline-flex items-center gap-1.5"
                    title={models.map((m) => m.repo.full_name).join("\n")}
                  >
                    <Link
                      href={repoBase(
                        models[0].repo.kind,
                        models[0].repo.namespace,
                        models[0].repo.name,
                      )}
                      className="font-mono text-xs text-accent hover:underline"
                    >
                      {models[0].repo.full_name}
                    </Link>
                    {models.length > 1 && (
                      <span className="text-xs font-medium text-fg-subtle">
                        +{models.length - 1}
                      </span>
                    )}
                  </span>
                )}
              </Td>
            );
          case "actions":
            return (
              <Td key={key}>
                <div className="flex items-center justify-end gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => actions.onToggleBaseline(run)}
                    aria-pressed={run.is_baseline}
                    aria-label={
                      run.is_baseline
                        ? t("experiments.table.clearBaselineAria", { name: run.name })
                        : t("experiments.table.setBaselineAria", { name: run.name })
                    }
                    title={
                      run.is_baseline
                        ? t("experiments.table.clearBaseline")
                        : t("experiments.table.setBaseline")
                    }
                  >
                    <Star size={14} className={run.is_baseline ? "text-accent" : undefined} />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => actions.onEditTags(run)}
                    aria-label={t("experiments.table.editTagsAria", { name: run.name })}
                    title={t("experiments.table.editTags")}
                  >
                    <Tag size={14} />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => actions.onToggleArchived(run)}
                    aria-pressed={run.archived}
                    aria-label={
                      run.archived
                        ? t("experiments.table.unarchiveAria", { name: run.name })
                        : t("experiments.table.archiveAria", { name: run.name })
                    }
                    title={
                      run.archived
                        ? t("experiments.table.unarchive")
                        : t("experiments.table.archive")
                    }
                  >
                    {run.archived ? <ArchiveRestore size={14} /> : <Archive size={14} />}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => actions.onDelete(run)}
                    aria-label={t("experiments.deleteRun.rowAria", { name: run.name })}
                    title={t("experiments.deleteRun.button")}
                  >
                    <Trash2 size={14} className="text-negative" />
                  </Button>
                </div>
              </Td>
            );
          default: {
            // Unreachable, and the assignment is what keeps it that way:
            // `column` has narrowed to `never` here only while every member of
            // `RunColumn` has a case above, so adding an id without adding a
            // case fails to compile rather than silently dropping that cell.
            // A `default` arm returning `null` would have type-checked
            // happily. Returning the binding also satisfies Biome's
            // `useIterableCallbackReturn`, which wants every path of a map()
            // callback to return — the reason the arm exists at all.
            const exhaustive: never = column;
            return exhaustive;
          }
        }
      })}
    </Tr>
  );
}
