"use client";

import { ChevronDown, ChevronRight } from "lucide-react";
import { RunRow } from "@/components/experiments/run-row";
import { statusLabel, statusTone } from "@/components/experiments/run-status-badge";
import { runColumnKey, useRunTable } from "@/components/experiments/run-table-context";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Td } from "@/components/ui/table";
import { TimeText } from "@/components/ui/time-text";
import { TriStateCheckbox } from "@/components/ui/tri-state-checkbox";
import { metricCellText } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { bestMetric, bestRunFor, groupJobTypes, type RunGroup } from "@/lib/run-grouping";
import type { RunStatus } from "@/types/api";

/**
 * A group header row followed by its members when it is open.
 *
 * The header summarises the sweep in the same columns its members use — a
 * status count per status, the furthest step any member reached, the best
 * value each metric column reached inside the group — which is only coherent
 * because both rows are drawn from the one column list in the context.
 */
export function GroupRows({
  group,
  open,
  onToggleExpanded,
  selected,
  onToggle,
  onToggleMany,
}: {
  group: RunGroup;
  open: boolean;
  onToggleExpanded: () => void;
  selected: Set<string>;
  onToggle: (name: string) => void;
  onToggleMany: (names: string[], select: boolean) => void;
}) {
  const t = useT();
  const { columns } = useRunTable();
  const names = group.runs.map((r) => r.name);
  const allSelected = names.every((name) => selected.has(name));
  const someSelected = !allSelected && names.some((name) => selected.has(name));
  const jobTypes = groupJobTypes(group.runs);
  const lastStep = Math.max(...group.runs.map((r) => r.last_step));
  const started = group.runs
    .map((r) => r.started_at)
    .filter((value): value is string => Boolean(value))
    .sort()[0];

  const statusCounts = new Map<RunStatus, number>();
  for (const run of group.runs) {
    statusCounts.set(run.status, (statusCounts.get(run.status) ?? 0) + 1);
  }

  return (
    <>
      {/* A raw <tr> rather than ui/table's <Tr>: a group header is followed by
          its members and by more groups, so it always wants its bottom rule —
          `Tr`'s `last:border-0` would drop it from a folded group that happens
          to be the final row. */}
      <tr className="border-b border-border bg-bg-sunken">
        {columns.map((column) => {
          const key = runColumnKey(column);
          switch (column.id) {
            case "select":
              return (
                <Td key={key}>
                  <TriStateCheckbox
                    checked={allSelected}
                    indeterminate={someSelected}
                    onChange={() => onToggleMany(names, !allSelected)}
                    aria-label={t("experiments.table.selectGroup", { name: group.key })}
                  />
                </Td>
              );
            case "name":
              return (
                <Td key={key} className="sticky left-0 z-[1] bg-bg-sunken">
                  <span className="flex items-center gap-2 font-medium">
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={onToggleExpanded}
                      aria-expanded={open}
                      aria-label={
                        open
                          ? t("experiments.table.collapseGroupAria", { name: group.key })
                          : t("experiments.table.expandGroupAria", { name: group.key })
                      }
                    >
                      {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                    </Button>
                    <span>{group.key}</span>
                    <Badge>
                      {t(
                        group.runs.length === 1
                          ? "experiments.table.groupMembersOne"
                          : "experiments.table.groupMembersOther",
                        { count: group.runs.length },
                      )}
                    </Badge>
                    {jobTypes.map((type) => (
                      <Badge key={type} tone="accent">
                        {type}
                      </Badge>
                    ))}
                  </span>
                </Td>
              );
            case "status":
              return (
                <Td key={key}>
                  <div className="flex flex-wrap gap-1.5">
                    {Array.from(statusCounts.entries()).map(([status, count]) => (
                      <Badge key={status} tone={statusTone(status)}>
                        {t("experiments.table.statusCount", {
                          status: statusLabel(t, status),
                          count,
                        })}
                      </Badge>
                    ))}
                  </div>
                </Td>
              );
            case "tags":
              return (
                <Td key={key} className="text-fg-subtle">
                  —
                </Td>
              );
            case "lastStep":
              return (
                <Td key={key} className="tabular-nums">
                  {formatNumber(lastStep)}
                </Td>
              );
            case "metric": {
              const best = bestMetric(group.runs, column.metric);
              const holder = bestRunFor(group.runs, column.metric);
              return (
                <Td key={key} className="tabular-nums">
                  {best === null ? (
                    <span className="text-fg-subtle">—</span>
                  ) : (
                    <span
                      className="text-fg"
                      title={t("experiments.table.bestInGroup", {
                        metric: column.metric,
                        run: holder?.name ?? "",
                      })}
                    >
                      {metricCellText(best)}
                    </span>
                  )}
                </Td>
              );
            }
            case "started":
              return (
                <Td key={key} className="text-fg-muted">
                  <TimeText iso={started ?? null} style="dateTime" />
                </Td>
              );
            case "models":
              return (
                <Td key={key} className="text-fg-subtle">
                  —
                </Td>
              );
            case "actions":
              return <Td key={key} />;
            default: {
              // Unreachable, and assigning `column` to `never` is what makes a
              // new `RunColumn` id a compile error here too rather than a
              // header cell with no body cell under it — see the same guard in
              // run-row.tsx.
              const exhaustive: never = column;
              return exhaustive;
            }
          }
        })}
      </tr>
      {open &&
        group.runs.map((run) => (
          <RunRow key={run.name} run={run} selected={selected} onToggle={onToggle} nested />
        ))}
    </>
  );
}
