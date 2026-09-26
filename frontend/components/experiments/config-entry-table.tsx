"use client";

import { Diff, SlidersHorizontal } from "lucide-react";

import { EmptyState } from "@/components/ui/empty-state";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";
import { formatConfigValue } from "@/lib/run-compare";
import type { ConfigEntry } from "@/lib/run-config";

/** The baseline column of a {@link ConfigEntryTable}. */
export type ConfigBaselineColumn = {
  /** Header text, e.g. "Baseline (e1_baseline)". */
  label: string;
  /** The baseline run's value per key. A key it lacks reads as "—". */
  values: ReadonlyMap<string, unknown>;
  /** Keys whose value differs from this run's; highlighted. */
  differing: ReadonlySet<string>;
};

/**
 * Key/value table for one section of a run's config (hyperparameters,
 * TrainingArguments, environment extras). Compact rows — one line per key in
 * the common case — so a dozen hyperparameters read at a glance.
 *
 * With `baseline`, a third column shows the baseline run's value and the rows
 * that differ are tinted and marked with an icon (never colour alone).
 */
export function ConfigEntryTable({
  entries,
  emptyTitle,
  emptyDescription,
  baseline,
}: {
  entries: ConfigEntry[];
  emptyTitle: string;
  emptyDescription?: string;
  baseline?: ConfigBaselineColumn;
}) {
  const t = useT();

  if (entries.length === 0) {
    return (
      <EmptyState icon={SlidersHorizontal} title={emptyTitle} description={emptyDescription} />
    );
  }

  return (
    <Table>
      <THead>
        <Th className="w-2/5">{t("experiments.configDiff.colParameter")}</Th>
        <Th>{t("experiments.run.colValue")}</Th>
        {baseline && (
          <Th className="max-w-[12rem] truncate" title={baseline.label}>
            {baseline.label}
          </Th>
        )}
      </THead>
      <TBody>
        {entries.map((entry) => {
          const differs = baseline?.differing.has(entry.key) ?? false;
          return (
            <Tr
              key={entry.key}
              className={cn("hover:bg-bg-hover", differs && "bg-warning/10 hover:bg-warning/15")}
            >
              <Th
                scope="row"
                className="py-1 text-left align-top font-medium break-all text-fg-muted"
              >
                <span className="inline-flex items-start gap-1">
                  {differs && (
                    <span
                      role="img"
                      aria-label={t("experiments.runPage.differs")}
                      title={t("experiments.runPage.differs")}
                      className="mt-0.5 inline-flex shrink-0 text-warning-strong"
                    >
                      <Diff size={12} />
                    </span>
                  )}
                  {entry.key}
                </span>
              </Th>
              <Td className="py-1 font-mono text-xs break-all whitespace-pre-wrap text-fg">
                {formatConfigValue(entry.value)}
              </Td>
              {baseline && (
                <Td className="py-1 font-mono text-xs break-all whitespace-pre-wrap text-fg-muted">
                  {formatConfigValue(baseline.values.get(entry.key))}
                </Td>
              )}
            </Tr>
          );
        })}
      </TBody>
    </Table>
  );
}
