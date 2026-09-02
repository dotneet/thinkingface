"use client";

import { useId, useMemo, useState } from "react";
import { UplotChart } from "@/components/experiments/uplot-chart";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { alignSeriesForKey, colorForRun, emaSmooth, groupByKey } from "@/lib/chart-utils";
import { useT } from "@/lib/i18n/client";
import type { ExpMetricSeries } from "@/types/api";

/** Dash pattern marking the baseline run's line in every metric chart. */
const BASELINE_DASH = [7, 4];

/** Namespace prefix marking a metric key as machine/system telemetry
 * (GPU/CPU/memory, logged by the trackio shim's background collector)
 * rather than a training metric. Kept in its own tab so it never crowds
 * out the metrics a run actually optimizes for. */
const SYSTEM_METRIC_PREFIX = "system/";

type MetricsTab = "metrics" | "system";

export function MetricsCharts({
  series,
  runOrder,
  xIsTime,
  smoothing,
  logScale,
  baseline,
  syncZoom = true,
}: {
  series: ExpMetricSeries[];
  runOrder: string[];
  xIsTime: boolean;
  smoothing: number;
  logScale: boolean;
  /** Name of the run marked as baseline, drawn thicker and dashed. */
  baseline?: string;
  /** Sync cursor position and x-axis zoom across every chart in this grid. */
  syncZoom?: boolean;
}) {
  const t = useT();
  // Stable across re-renders so charts stay grouped; unique per dashboard
  // instance so two dashboards on the same page never sync with each other.
  const syncId = useId();
  const [tab, setTab] = useState<MetricsTab>("metrics");

  const { normalSeries, systemSeries } = useMemo(() => {
    const normal: ExpMetricSeries[] = [];
    const system: ExpMetricSeries[] = [];
    for (const s of series) {
      (s.key.startsWith(SYSTEM_METRIC_PREFIX) ? system : normal).push(s);
    }
    return { normalSeries: normal, systemSeries: system };
  }, [series]);
  const hasSystemMetrics = systemSeries.length > 0;
  // Never let a run with no system metrics land on a tab it can't show —
  // effectively falls back to "metrics" without needing an effect.
  const activeTab = hasSystemMetrics ? tab : "metrics";
  const visibleSeries = activeTab === "system" ? systemSeries : normalSeries;

  const grouped = useMemo(() => groupByKey(visibleSeries), [visibleSeries]);
  const runColor = useMemo(() => {
    const map = new Map<string, string>();
    runOrder.forEach((run, i) => {
      map.set(run, colorForRun(i));
    });
    return map;
  }, [runOrder]);

  // One chart's worth of plot input, built once per data change rather than
  // once per render. Alignment and smoothing are the expensive part, but the
  // identity of `data` matters more: UplotChart hands a new array to
  // uPlot.setData, which re-ranges x and drops whatever the user had zoomed
  // into. Rebuilding these arrays on every keystroke in the metric filter, on
  // every tag select and on every 15-second live poll is what made a running
  // project impossible to zoom (DESIGN.md §8 — the chart is a target too).
  const charts = useMemo(
    () =>
      Array.from(grouped.entries()).map(([key, seriesForKey]) => {
        const ordered = [...seriesForKey].sort(
          (a, b) => runOrder.indexOf(a.run) - runOrder.indexOf(b.run),
        );
        const aligned = alignSeriesForKey(ordered);
        const xs = aligned[0] ?? [];
        const smoothed = aligned.slice(1).map((s) => emaSmooth(s, smoothing));
        const data: (number | null)[][] = [xs, ...smoothed];
        return { key, ordered, data };
      }),
    [grouped, runOrder, smoothing],
  );

  return (
    <div className="flex flex-col gap-4">
      {hasSystemMetrics && (
        <SegmentedControl
          value={activeTab}
          onChange={setTab}
          label={t("experiments.chart.tabsLabel")}
          options={[
            { value: "metrics", label: t("experiments.chart.tabMetrics") },
            {
              value: "system",
              label: t("experiments.chart.tabSystemMetrics"),
            },
          ]}
        />
      )}

      {grouped.size === 0 ? (
        // Runs are selected (the dashboard only mounts this when some are) --
        // they just logged nothing for this tab, which is a different thing
        // from "pick a run", the message this used to borrow.
        <p className="text-sm text-fg-subtle">{t("experiments.chart.noSeries")}</p>
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {charts.map(({ key, ordered, data }) => (
            <div key={key} className="rounded-lg border border-border bg-bg-raised p-3">
              <UplotChart
                title={key}
                data={data}
                series={ordered.map((s) => ({
                  label:
                    s.run === baseline
                      ? t("experiments.chart.baselineSuffix", { run: s.run })
                      : s.run,
                  color: runColor.get(s.run) ?? "#5b8def",
                  dash: s.run === baseline ? BASELINE_DASH : undefined,
                  width: s.run === baseline ? 2.5 : undefined,
                }))}
                xIsTime={xIsTime}
                logScale={logScale}
                syncKey={syncZoom ? syncId : undefined}
              />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
