"use client";

import { SlidersHorizontal } from "lucide-react";
import { EmptyState } from "@/components/ui/empty-state";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
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
    <Table>
      <THead>
        <Th className="w-1/3">{t("experiments.configDiff.colParameter")}</Th>
        <Th>{t("experiments.run.colValue")}</Th>
      </THead>
      <TBody>
        {entries.map((entry) => (
          <Tr key={entry.key} className="hover:bg-bg-hover">
            <Th scope="row" className="text-left align-top break-all text-fg-muted">
              {entry.key}
            </Th>
            <Td className="font-mono text-xs break-all whitespace-pre-wrap text-fg">
              {formatConfigValue(entry.value)}
            </Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}
