"use client";

import { Input, Select } from "@/components/ui/field";
import type { RunFilters } from "@/hooks/use-run-filters";
import { useT } from "@/lib/i18n/client";
import { METRIC_FILTER_OPS } from "@/lib/run-grouping";

/**
 * The less common ways to narrow the run list: a tag, and a metric threshold.
 * (The search box, the status chips and the archived toggle sit in the run
 * sidebar itself; these live behind its "More filters" disclosure.)
 *
 * Stacked rather than one long row, because they sit in a ~300px sidebar. The
 * tag picker and the metric picker only appear when the project has anything
 * to offer for them — a filter over an empty set of values is a control that
 * can only ever say "no". Every group that *is* shown keeps its shape as the
 * results change (DESIGN.md §8.4).
 */
export function RunFilterBar({
  filters,
  onChange,
  tags,
  metricKeys,
}: {
  filters: RunFilters;
  onChange: (patch: Partial<RunFilters>) => void;
  /** Every tag any run in the project carries. */
  tags: string[];
  /** Every metric the project logged — not just the table's capped columns. */
  metricKeys: string[];
}) {
  const t = useT();

  return (
    <div className="flex flex-col gap-2 text-sm">
      {tags.length > 0 && (
        <label className="flex items-center gap-2">
          <span className="w-16 shrink-0 text-xs font-medium text-fg-subtle">
            {t("experiments.dashboard.tagLabel")}
          </span>
          <Select
            value={filters.tag}
            onChange={(e) => onChange({ tag: e.target.value })}
            className="min-w-0 flex-1 bg-bg-raised px-2 py-1 text-sm"
          >
            <option value="">{t("experiments.dashboard.allTags")}</option>
            {tags.map((tag) => (
              <option key={tag} value={tag}>
                {tag}
              </option>
            ))}
          </Select>
        </label>
      )}

      {metricKeys.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <span className="text-xs font-medium text-fg-subtle">
            {t("experiments.dashboard.metricFilterLabel")}
          </span>
          <Select
            value={filters.metric}
            onChange={(e) => onChange({ metric: e.target.value })}
            aria-label={t("experiments.dashboard.metricFilterMetricAria")}
            className="bg-bg-raised px-2 py-1 font-mono text-xs"
          >
            <option value="">{t("experiments.dashboard.metricFilterNone")}</option>
            {metricKeys.map((key) => (
              <option key={key} value={key}>
                {key}
              </option>
            ))}
          </Select>
          <div className="flex items-center gap-2">
            <Select
              value={filters.op}
              onChange={(e) => onChange({ op: e.target.value })}
              aria-label={t("experiments.dashboard.metricFilterOpAria")}
              disabled={!filters.metric}
              className="w-16 bg-bg-raised px-2 py-1"
            >
              {METRIC_FILTER_OPS.map((op) => (
                <option key={op} value={op}>
                  {op}
                </option>
              ))}
            </Select>
            <Input
              value={filters.value}
              onChange={(e) => onChange({ value: e.target.value })}
              inputMode="decimal"
              disabled={!filters.metric}
              placeholder={t("experiments.dashboard.metricFilterValuePlaceholder")}
              aria-label={t("experiments.dashboard.metricFilterValueAria")}
              className="min-w-0 flex-1 bg-bg-raised px-2 py-1 tabular-nums"
            />
          </div>
        </div>
      )}
    </div>
  );
}
