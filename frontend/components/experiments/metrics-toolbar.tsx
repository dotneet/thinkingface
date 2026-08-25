"use client";

import { CsvDownloadButton } from "@/components/experiments/csv-download-button";
import { Checkbox, Select, Slider } from "@/components/ui/field";
import { SpinnerSlot } from "@/components/ui/spinner";
import type { ChartOptions } from "@/hooks/use-chart-options";
import { useT } from "@/lib/i18n/client";

/**
 * The controls above a grid of metric charts: which x axis the series are read
 * on, how hard the curves are smoothed, whether the y axis is logarithmic,
 * whether the charts share a cursor and zoom, and the CSV export of whatever
 * is plotted.
 *
 * One toolbar for the project dashboard and the single-run page. They differ
 * in exactly two things — the dashboard syncs zoom across a grid of charts
 * (`showSyncZoom`), and the two export different filenames — so everything
 * else being one component is what keeps the smoothing range, the value
 * readout's width and the spinner slot from drifting apart between the two
 * pages.
 *
 * `SpinnerSlot` rather than `{fetching && <Spinner/>}`: the slot reserves its
 * width whether or not it is spinning, so a background refetch never nudges
 * the export button out from under the pointer (DESIGN.md §8).
 */
export function MetricsToolbar({
  options,
  onChange,
  showSyncZoom = false,
  fetching,
  csvFilename,
  csvDisabled,
  buildCsv,
}: {
  options: ChartOptions;
  onChange: (patch: Partial<ChartOptions>) => void;
  /** Only a page that draws more than one chart has anything to sync. */
  showSyncZoom?: boolean;
  /** A refetch of series already on screen — the spinner, not a skeleton. */
  fetching: boolean;
  csvFilename: string;
  /** True when there is nothing plotted to export. */
  csvDisabled: boolean;
  /** Thunk, not a string: serialising the rows happens on the click. */
  buildCsv: () => string;
}) {
  const t = useT();

  return (
    <div className="flex flex-wrap items-center gap-5 rounded-lg border border-border bg-bg-sunken px-4 py-3 text-sm">
      <label className="flex items-center gap-2">
        <span className="text-fg-subtle">{t("experiments.dashboard.xAxis")}</span>
        <Select
          value={options.xMode}
          onChange={(e) => onChange({ xMode: e.target.value as ChartOptions["xMode"] })}
          className="w-auto bg-bg-raised px-2 py-1"
        >
          <option value="step">{t("experiments.dashboard.xStep")}</option>
          <option value="time">{t("experiments.dashboard.xTime")}</option>
        </Select>
      </label>

      <label className="flex min-w-[200px] flex-1 items-center gap-2">
        <span className="whitespace-nowrap text-fg-subtle">
          {t("experiments.dashboard.smoothing")}
        </span>
        <Slider
          min={0}
          max={0.95}
          step={0.05}
          value={options.smoothing}
          onChange={(e) => onChange({ smoothing: Number(e.target.value) })}
          aria-label={t("experiments.dashboard.smoothingAria")}
          className="flex-1"
        />
        <span className="w-10 tabular-nums text-fg-subtle">{options.smoothing.toFixed(2)}</span>
      </label>

      <label className="flex items-center gap-2">
        <Checkbox
          checked={options.logScale}
          onChange={(e) => onChange({ logScale: e.target.checked })}
        />
        <span className="text-fg-subtle">{t("experiments.dashboard.logScale")}</span>
      </label>

      {showSyncZoom && (
        <label className="flex items-center gap-2">
          <Checkbox
            checked={options.syncZoom}
            onChange={(e) => onChange({ syncZoom: e.target.checked })}
          />
          <span className="text-fg-subtle">{t("experiments.dashboard.syncZoom")}</span>
        </label>
      )}

      <SpinnerSlot active={fetching} size={14} label={t("experiments.dashboard.loadingMetrics")} />

      <CsvDownloadButton
        label={t("experiments.dashboard.exportMetricsCsv")}
        filename={csvFilename}
        disabled={csvDisabled}
        build={buildCsv}
      />
    </div>
  );
}
