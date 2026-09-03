"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import uPlot from "uplot";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { chartDataEquals, planLogScale } from "@/lib/chart-scale";
import { spanGapsForMode } from "@/lib/chart-utils";
import { useT } from "@/lib/i18n/client";
import {
  CHART_THEME_FALLBACKS,
  type ChartThemeColors,
  readChartThemeColors,
  subscribeThemeChange,
} from "@/lib/theme-colors";
import "uplot/dist/uPlot.min.css";

/**
 * Relative tolerance for deciding whether the x scale still matches the full
 * data range. uPlot's auto-range sets it to the exact first/last x value (no
 * padding in mode 1), so this only needs to absorb float noise.
 */
const ZOOM_EPSILON = 1e-6;

export type UplotSeriesMeta = {
  label: string;
  color: string;
  /**
   * Dash pattern for the stroke, e.g. [6, 4]. Used to set the baseline run
   * apart from the rest without spending a second colour on it.
   */
  dash?: number[];
  /** Stroke width; defaults to 1.5, thicker for the baseline. */
  width?: number;
  /** Marker diameter in scatter mode; defaults to 9, larger for the baseline. */
  pointSize?: number;
};

/**
 * "line" joins the points of each series; "scatter" draws the points alone,
 * which is what the run comparison plot needs (one marker per run, no path
 * through them).
 */
export type UplotMode = "line" | "scatter";

export function UplotChart({
  title,
  data,
  series,
  xIsTime,
  logScale,
  mode = "line",
  xLabel,
  yLabel,
  height = 240,
  syncKey,
}: {
  title: string;
  /** [xValues, ...yValuesPerSeries], numbers or null for gaps */
  data: (number | null)[][];
  series: UplotSeriesMeta[];
  xIsTime: boolean;
  logScale: boolean;
  mode?: UplotMode;
  xLabel?: string;
  yLabel?: string;
  height?: number;
  /**
   * When set, this chart's cursor position and x-axis zoom are synced with
   * every other chart that shares the same key (uPlot's cursor.sync). Leave
   * unset for a standalone chart.
   */
  syncKey?: string;
}) {
  const t = useT();
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  // What a log y axis can actually be handed: uPlot draws a 0 or a negative
  // value at scaleMin / 10 (below the axis, at a position that is not the
  // value), and ranges an all-non-positive series from [Infinity, -Infinity],
  // which paints nothing at all. Masked here, admitted to the reader below.
  const plan = useMemo(() => planLogScale(data, logScale), [data, logScale]);
  const plotData = plan.data;
  // Latest x column, read from the setScale hook to decide whether the x
  // scale still spans the full data range. A ref (not the `data` prop
  // closed over at plot-creation time) because data updates are applied via
  // plot.setData() below without recreating the plot/hooks.
  const dataRef = useRef(plotData);
  // Resolved axis/grid colours for the current theme. uPlot draws on a canvas,
  // and Canvas2D does not resolve CSS custom properties — see lib/theme-colors.
  // Kept in a ref and read through the axis stroke *functions* below, which
  // uPlot re-invokes on every draw: a theme change then only needs a redraw,
  // never a rebuild of the plot (a dashboard renders one of these per metric).
  // The fallback holds only until the mount effect reads the real tokens;
  // `document` does not exist while this renders on the server.
  const themeColorsRef = useRef<ChartThemeColors>(CHART_THEME_FALLBACKS);
  const [isZoomed, setIsZoomed] = useState(false);
  // The same value the state holds, readable from the data effect below
  // without adding it to that effect's dependencies (which would re-run it,
  // and re-running it is exactly what drops the zoom).
  const isZoomedRef = useRef(false);

  // Identity of the plotted series as a single string: swapping run A for run B
  // with the same selection size, or restyling one as the baseline, changes it,
  // while a re-render that only produced new point data does not.
  const seriesSignature = series
    .map(
      (s) =>
        `${s.label}:${s.color}:${s.dash?.join("-") ?? ""}:${s.width ?? ""}:${s.pointSize ?? ""}`,
    )
    .join("|");

  useEffect(() => {
    if (!containerRef.current) return;

    themeColorsRef.current = readChartThemeColors();
    const axisStroke = () => themeColorsRef.current.axis;
    const gridStroke = () => themeColorsRef.current.grid;
    const markZoomed = (zoomed: boolean) => {
      isZoomedRef.current = zoomed;
      setIsZoomed(zoomed);
    };

    const width = containerRef.current.clientWidth || 400;

    const opts: uPlot.Options = {
      width,
      height,
      title,
      cursor: {
        points: { size: 5 },
        // Drag-zoom only along x (step/time): the default also drags a y
        // selection, which the built-in double-click reset does not clear
        // (it only re-ranges x), leaving the chart looking "stuck" zoomed.
        drag: { x: true, y: false },
        // Dim series other than the one closest to the cursor so a run is
        // easy to pick out of a busy chart; legend hover does the same.
        focus: { prox: 30 },
        ...(syncKey ? { sync: { key: syncKey, scales: ["x", null] as [string, null] } } : {}),
      },
      legend: { show: true },
      hooks: {
        setScale: [
          (u, key) => {
            if (key !== "x") return;
            const xs = dataRef.current[0] ?? [];
            const fullMin = xs[0];
            const fullMax = xs[xs.length - 1];
            const min = u.scales.x?.min;
            const max = u.scales.x?.max;
            if (fullMin == null || fullMax == null || min == null || max == null) {
              markZoomed(false);
              return;
            }
            // A single distinct x value (one point, or every point sharing an
            // x) has nothing to zoom out of: uPlot still pads the auto-range
            // around that one value, which would otherwise read as "zoomed"
            // from the very first render and never clear.
            if (fullMin === fullMax) {
              markZoomed(false);
              return;
            }
            const tolerance = ZOOM_EPSILON * Math.max(1, Math.abs(fullMax - fullMin));
            markZoomed(Math.abs(min - fullMin) > tolerance || Math.abs(max - fullMax) > tolerance);
          },
        ],
      },
      scales: {
        x: { time: xIsTime },
        // plan.logEnabled, not the prop: a request for log over data with no
        // positive value at all falls back to linear rather than drawing an
        // empty chart (the Alert below says so).
        y: { distr: plan.logEnabled ? 3 : 1 },
      },
      axes: [
        {
          stroke: axisStroke,
          grid: { stroke: gridStroke },
          ticks: { stroke: gridStroke },
          label: xLabel,
          labelSize: xLabel ? 24 : undefined,
        },
        {
          stroke: axisStroke,
          grid: { stroke: gridStroke },
          ticks: { stroke: gridStroke },
          label: yLabel,
          labelSize: yLabel ? 24 : undefined,
        },
      ],
      series: [
        {
          label: xIsTime ? t("experiments.chart.time") : (xLabel ?? t("experiments.chart.step")),
        },
        ...series.map((s) => ({
          label: s.label,
          stroke: s.color,
          width: s.width ?? 1.5,
          dash: s.dash,
          // A scatter series has no path: only the markers are drawn, so two
          // runs at the same x do not get joined into a meaningless line.
          ...(mode === "scatter"
            ? { paths: () => null, points: { show: true, size: s.pointSize ?? 9, fill: s.color } }
            : { points: { show: false }, spanGaps: spanGapsForMode(mode) }),
        })),
      ],
    };

    const plot = new uPlot(opts, plotData as uPlot.AlignedData, containerRef.current);
    plotRef.current = plot;

    const resizeObserver = new ResizeObserver(() => {
      if (containerRef.current) {
        plot.setSize({ width: containerRef.current.clientWidth || 400, height });
      }
    });
    resizeObserver.observe(containerRef.current);

    return () => {
      resizeObserver.disconnect();
      plot.destroy();
      plotRef.current = null;
    };
    // Recreate on the series signature rather than on `series` itself, whose
    // array identity changes on every render; `plotData` is deliberately
    // absent too — it is applied by the effect below instead of rebuilding
    // the plot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [title, xIsTime, plan.logEnabled, mode, xLabel, yLabel, seriesSignature, height, t, syncKey]);

  // Follow theme switches (the toggle's `data-theme` attribute, or the OS
  // changing `prefers-color-scheme` while the preference is "system"). Only a
  // real theme change gets here, and it costs a repaint of the existing plot —
  // the axis strokes are read back out of the ref on the next draw — so the
  // chart keeps its zoom and no series paths are rebuilt.
  useEffect(
    () =>
      subscribeThemeChange(() => {
        themeColorsRef.current = readChartThemeColors();
        plotRef.current?.redraw(false);
      }),
    [],
  );

  // Apply new points without touching the scales the user chose.
  //
  // Two separate hazards, both of which used to clear a drag-zoom on every
  // unrelated re-render (a keystroke in the metric filter, a tag select, the
  // 15-second live poll — each of which rebuilds the `data` array):
  //
  //  1. an equal-but-new array still counts as an update, so compare by value
  //     and do nothing when the numbers are the same;
  //  2. `setData(d)` defaults to `_resetScales: true`, which calls
  //     autoScaleX() and snaps x back to the full range. While the user is
  //     zoomed the update goes in with `false` and is committed by redraw(),
  //     which re-ranges x to the *current* window (and y to what it contains)
  //     rather than to everything.
  useEffect(() => {
    const previous = dataRef.current;
    dataRef.current = plotData;
    const plot = plotRef.current;
    if (!plot || chartDataEquals(previous, plotData)) return;
    if (isZoomedRef.current) {
      plot.setData(plotData as uPlot.AlignedData, false);
      plot.redraw();
      return;
    }
    plot.setData(plotData as uPlot.AlignedData);
  }, [plotData]);

  // Re-dispatch uPlot's own double-click-to-reset gesture on the plotting
  // area: it already runs the exact reset logic (re-range x, drop the
  // selection) and, when this chart is in a sync group, propagates to every
  // other chart sharing the key the same way a real double-click would.
  function resetZoom() {
    plotRef.current?.over.dispatchEvent(
      new MouseEvent("dblclick", { bubbles: true, cancelable: true, button: 0 }),
    );
  }

  return (
    <div className="relative w-full">
      {/* uPlot draws into this div with its own canvases; it never touches the
          div's own attributes, so role/aria-label placed here survive and give
          the chart the same accessible name a screen reader gets from the
          parallel-coordinates <svg> (which sets role="img" directly). */}
      <div ref={containerRef} className="w-full" role="img" aria-label={title} />
      {isZoomed && (
        <Button
          variant="secondary"
          size="sm"
          onClick={resetZoom}
          className="absolute right-1 top-1 z-10 bg-bg-raised"
        >
          {t("experiments.chart.resetZoom")}
        </Button>
      )}
      {/* Below the plot, never above it: a note that appears when the log
          toggle is flipped must not push the chart (and the Reset zoom button
          on it) out from under the pointer (DESIGN.md §8). */}
      {plan.unavailable && (
        <Alert tone="warning" className="mt-2">
          {t("experiments.chart.logUnavailable")}
        </Alert>
      )}
      {plan.hiddenPoints > 0 && (
        <Alert tone="warning" className="mt-2">
          {t(
            plan.hiddenPoints === 1
              ? "experiments.chart.logHiddenPointsOne"
              : "experiments.chart.logHiddenPointsOther",
            { count: plan.hiddenPoints },
          )}
        </Alert>
      )}
    </div>
  );
}
