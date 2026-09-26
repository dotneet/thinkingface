"use client";

import { ChevronDown, ChevronRight } from "lucide-react";
import { type ReactNode, useId, useMemo, useState } from "react";

import { UplotChart } from "@/components/experiments/uplot-chart";
import { Button } from "@/components/ui/button";
import { SegmentedControl } from "@/components/ui/segmented-control";
import {
  alignSeriesForKey,
  colorForRun,
  dashForRun,
  emaSmooth,
  groupByKey,
} from "@/lib/chart-utils";
import { orderChartKeys } from "@/lib/exp-workspace";
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

/**
 * The chart grid: as many columns as fit at ~26rem each, so the project
 * workspace (wide) gets two or three and a phone gets one. `min(100%, …)`
 * keeps a single column from overflowing a narrow screen.
 */
const GRID = "grid gap-3 grid-cols-[repeat(auto-fill,minmax(min(100%,26rem),1fr))]";

export function MetricsCharts({
  series,
  runOrder,
  xIsTime,
  smoothing,
  logScale,
  baseline,
  syncZoom = true,
  keyOrder,
  systemLayout = "tabs",
  chartHeight = 220,
  titleAdornment,
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
  /**
   * Metric keys to draw first, in this order (the project's goal metrics);
   * everything else follows alphabetically.
   */
  keyOrder?: string[];
  /**
   * Where `system/*` charts go: behind a Metrics / System metrics switch
   * ("tabs", the run page), or in a folded section after the rest
   * ("section", the project workspace).
   */
  systemLayout?: "tabs" | "section";
  chartHeight?: number;
  /** Rendered next to each chart's title, e.g. the metric's goal marker. */
  titleAdornment?: (metricKey: string) => ReactNode;
}) {
  const t = useT();
  // Stable across re-renders so charts stay grouped; unique per dashboard
  // instance so two dashboards on the same page never sync with each other.
  const syncId = useId();
  const [tab, setTab] = useState<MetricsTab>("metrics");
  const [systemOpen, setSystemOpen] = useState(false);

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
  const activeTab = hasSystemMetrics && systemLayout === "tabs" ? tab : "metrics";
  const visibleSeries = activeTab === "system" ? systemSeries : normalSeries;

  const grouped = useMemo(() => groupByKey(visibleSeries), [visibleSeries]);
  const systemGrouped = useMemo(
    () =>
      systemLayout === "section" && systemOpen
        ? groupByKey(systemSeries)
        : new Map<string, { run: string; points: [number, number][] }[]>(),
    [systemLayout, systemOpen, systemSeries],
  );
  const systemKeyCount = useMemo(
    () => new Set(systemSeries.map((s) => s.key)).size,
    [systemSeries],
  );
  const runColor = useMemo(() => {
    const map = new Map<string, string>();
    runOrder.forEach((run, i) => {
      map.set(run, colorForRun(i));
    });
    return map;
  }, [runOrder]);
  // A comparison that outgrows the palette wraps colours (run 21 repeats run
  // 1's hue); the dash pattern changes on the same wrap, so a repeated
  // colour still reads as a different run.
  const runDash = useMemo(() => {
    const map = new Map<string, number[] | undefined>();
    runOrder.forEach((run, i) => {
      map.set(run, dashForRun(i));
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
    () => buildCharts(grouped, keyOrder ?? [], runOrder, smoothing),
    [grouped, keyOrder, runOrder, smoothing],
  );
  const systemCharts = useMemo(
    () => buildCharts(systemGrouped, [], runOrder, smoothing),
    [systemGrouped, runOrder, smoothing],
  );

  function renderChart({ key, ordered, data }: PlotInput) {
    return (
      <div key={key} className="min-w-0 rounded-lg border border-border bg-bg-raised p-3">
        <UplotChart
          title={key}
          titleAdornment={titleAdornment?.(key)}
          data={data}
          series={ordered.map((s) => ({
            label:
              s.run === baseline ? t("experiments.chart.baselineSuffix", { run: s.run }) : s.run,
            color: runColor.get(s.run) ?? "#5b8def",
            dash: s.run === baseline ? BASELINE_DASH : runDash.get(s.run),
            width: s.run === baseline ? 2.5 : undefined,
          }))}
          xIsTime={xIsTime}
          logScale={logScale}
          height={chartHeight}
          syncKey={syncZoom ? syncId : undefined}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {hasSystemMetrics && systemLayout === "tabs" && (
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
        <div className={GRID}>{charts.map(renderChart)}</div>
      )}

      {hasSystemMetrics && systemLayout === "section" && (
        <div className="flex flex-col gap-3">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setSystemOpen((open) => !open)}
            aria-expanded={systemOpen}
            aria-label={
              systemOpen
                ? t("experiments.workspace.hideSystemAria")
                : t("experiments.workspace.showSystemAria")
            }
            className="self-start text-sm"
          >
            {systemOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            {t("experiments.workspace.systemMetrics", { count: systemKeyCount })}
          </Button>
          {systemOpen && <div className={GRID}>{systemCharts.map(renderChart)}</div>}
        </div>
      )}
    </div>
  );
}

type PlotInput = {
  key: string;
  ordered: { run: string; points: [number, number][] }[];
  data: (number | null)[][];
};

/**
 * One chart's worth of plot input per metric, in display order (`keyOrder`
 * first, then alphabetical). Built inside a `useMemo` by the caller: the
 * identity of `data` matters, because UplotChart hands a new array to
 * uPlot.setData, which re-ranges x and drops whatever the user had zoomed
 * into. Rebuilding these arrays on every keystroke in a filter, on every
 * selection change elsewhere and on every 15-second live poll is what made a
 * running project impossible to zoom (DESIGN.md §8 — the chart is a target
 * too).
 */
function buildCharts(
  grouped: Map<string, { run: string; points: [number, number][] }[]>,
  keyOrder: string[],
  runOrder: string[],
  smoothing: number,
): PlotInput[] {
  const { main, system } = orderChartKeys(Array.from(grouped.keys()), keyOrder);
  return [...main, ...system].map((key) => {
    const seriesForKey = grouped.get(key) ?? [];
    const ordered = [...seriesForKey].sort(
      (a, b) => runOrder.indexOf(a.run) - runOrder.indexOf(b.run),
    );
    const aligned = alignSeriesForKey(ordered);
    const xs = aligned[0] ?? [];
    const smoothed = aligned.slice(1).map((s) => emaSmooth(s, smoothing));
    const data: (number | null)[][] = [xs, ...smoothed];
    return { key, ordered, data };
  });
}
