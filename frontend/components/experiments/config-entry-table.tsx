"use client";

import { SlidersHorizontal } from "lucide-react";
import { EmptyState } from "@/components/ui/empty-state";
import { useT } from "@/lib/i18n/client";
import { formatConfigValue } from "@/lib/run-compare";
import type { ConfigEntry } from "@/lib/run-config";

/**
 * Key/value table for one section of a run's config (hyperparameters,
 * TrainingArguments, environment extras). The run detail page shows a single
 * run, so unlike the dashboard's ConfigDiffTable there is nothing to compare
 * and every row is just a name and a value.
 */
export function ConfigEntryTable({
  entries,
  emptyTitle,
  emptyDescription,
}: {
  entries: ConfigEntry[];
  emptyTitle: string;
  emptyDescription?: string;
}) {
  const t = useT();

  if (entries.length === 0) {
    return (
      <EmptyState icon={SlidersHorizontal} title={emptyTitle} description={emptyDescription} />
    );
  }

  return (
    <div className="scroll-x rounded-lg border border-border">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
            <th className="w-1/3 px-3 py-2 font-medium">
              {t("experiments.configDiff.colParameter")}
            </th>
            <th className="px-3 py-2 font-medium">{t("experiments.run.colValue")}</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((entry) => (
            <tr key={entry.key} className="border-b border-border last:border-0 hover:bg-bg-hover">
              <th
                scope="row"
                className="px-3 py-2 text-left align-top font-medium break-all text-fg-muted"
              >
                {entry.key}
              </th>
              <td className="px-3 py-2 font-mono text-xs break-all whitespace-pre-wrap text-fg">
                {formatConfigValue(entry.value)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
