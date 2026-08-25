"use client";

import { Checkbox, Input, Select } from "@/components/ui/field";
import { SpinnerSlot } from "@/components/ui/spinner";
import type { RunFilters } from "@/hooks/use-run-filters";
import { useT } from "@/lib/i18n/client";
import { METRIC_FILTER_OPS } from "@/lib/run-grouping";

/**
 * What narrows the run table, and therefore the charts: a tag, a metric
 * threshold, and whether archived runs are listed at all.
 *
 * The tag picker and the metric picker only appear when the project has
 * anything to offer for them — a filter over an empty set of values is a
 * control that can only ever say "no". Every group that *is* shown keeps its
 * shape as the results change (DESIGN.md §8.4).
 */
export function RunFilterBar({
  filters,
  onChange,
  tags,
  metricKeys,
  archivedCount,
  selectedCount,
  visibleCount,
  saving,
}: {
  filters: RunFilters;
  onChange: (patch: Partial<RunFilters>) => void;
  /** Every tag any run in the project carries. */
  tags: string[];
  /** Every metric the project logged — not just the table's capped columns. */
  metricKeys: string[];
  archivedCount: number;
  selectedCount: number;
  visibleCount: number;
  /** An annotation write is in flight somewhere in the table. */
  saving: boolean;
}) {
  const t = useT();

  return (
    <div className="flex flex-wrap items-center gap-5 rounded-lg border border-border bg-bg-sunken px-4 py-3 text-sm">
      <span className="text-fg-subtle">
        {t("experiments.dashboard.selectedCount", {
          selected: selectedCount,
          total: visibleCount,
        })}
      </span>

      {tags.length > 0 && (
        <label className="flex items-center gap-2">
          <span className="text-fg-subtle">{t("experiments.dashboard.tagLabel")}</span>
          <Select
            value={filters.tag}
            onChange={(e) => onChange({ tag: e.target.value })}
            className="w-auto bg-bg-raised px-2 py-1"
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
        <div className="flex items-center gap-2">
          <span className="text-fg-subtle">{t("experiments.dashboard.metricFilterLabel")}</span>
          <Select
            value={filters.metric}
            onChange={(e) => onChange({ metric: e.target.value })}
            aria-label={t("experiments.dashboard.metricFilterMetricAria")}
            className="w-auto bg-bg-raised px-2 py-1 font-mono text-xs"
          >
            <option value="">{t("experiments.dashboard.metricFilterNone")}</option>
            {metricKeys.map((key) => (
              <option key={key} value={key}>
                {key}
              </option>
            ))}
          </Select>
          <Select
            value={filters.op}
            onChange={(e) => onChange({ op: e.target.value })}
            aria-label={t("experiments.dashboard.metricFilterOpAria")}
            disabled={!filters.metric}
            className="w-auto bg-bg-raised px-2 py-1"
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
            className="w-24 bg-bg-raised px-2 py-1 tabular-nums"
          />
        </div>
      )}

      <label className="flex items-center gap-2">
        <Checkbox
          checked={filters.showArchived}
          onChange={(e) => onChange({ showArchived: e.target.checked })}
        />
        <span className="text-fg-subtle">
          {t("experiments.dashboard.showArchived", { count: archivedCount })}
        </span>
      </label>

      <SpinnerSlot active={saving} size={14} label={t("experiments.dashboard.savingAnnotation")} />
    </div>
  );
}
