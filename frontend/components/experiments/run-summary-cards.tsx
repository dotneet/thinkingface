"use client";

import { Card } from "@/components/ui/card";
import { formatMetricValue } from "@/lib/experiments";
import { useT } from "@/lib/i18n/client";

/**
 * The final value of every metric a run reported, one card each.
 *
 * `formatMetricValue` is the same function the run table's cells use, so a
 * value like `2.3e-10` cannot read one way here and another way there.
 */
export function RunSummaryCards({ entries }: { entries: [string, number][] }) {
  const t = useT();

  if (entries.length === 0) {
    return <p className="text-sm text-fg-subtle">{t("experiments.run.summaryEmpty")}</p>;
  }

  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
      {entries.map(([key, value]) => (
        <Card key={key} className="flex flex-col gap-1">
          <span className="truncate text-xs font-medium text-fg-subtle" title={key}>
            {key}
          </span>
          <span className="tabular-nums text-lg font-semibold">{formatMetricValue(value)}</span>
        </Card>
      ))}
    </div>
  );
}
