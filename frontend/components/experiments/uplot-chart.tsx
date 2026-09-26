"use client";

import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import uPlot from "uplot";

import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { chartDataEquals, planLogScale } from "@/lib/chart-scale";
import { spanGapsForMode } from "@/lib/chart-utils";
import { axisSizeFor, formatAxisTicks, paddedRange } from "@/lib/exp-chart-ticks";
import { formatMetricValue } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { useLocale, useT } from "@/lib/i18n/client";
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

/** Legend entries shown before the rest collapse into "+N more". */
const LEGEND_LIMIT = 10;
/** Rows the hover readout lists before it stops. */
const READOUT_LIMIT = 12;
/**
 * How far either side of the cursor the readout looks for a series' nearest
 * point. Runs are downsampled independently, so at a given x most series have
 * a gap (`alignSeriesForKey`); the line spans it, and so should the readout.
 */
const READOUT_REACH = 40;

/** What the hover readout needs: which x index, and where the cursor is. */
type Hover = { idx: number; left: number; top: number; width: number };

/** The value series `s` has at `idx`, or at its nearest non-null neighbour. */
function nearestValue(column: readonly (number | null)[] | undefined, idx: number): number | null {
  if (!column) return null;
  for (let d = 0; d <= READOUT_REACH; d++) {
    const before = column[idx - d];
    if (before !== undefined && before !== null) return before;
    const after = column[idx + d];
    if (after !== undefined && after !== null) return after;
  }
  return null;
}

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
  titleAdornment,
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
  /** Rendered next to the title, e.g. a metric's goal marker. */
  titleAdornment?: ReactNode;
}) {
  const t = useT();
  const locale = useLocale();
  // The hover readout. Only the chart under the pointer shows one: a synced
  // chart moves its cursor line too, but a readout on every chart at once is
  // a wall of numbers.
  const [hover, setHover] = useState<Hover | null>(null);
  const hoveredRef = useRef(false);
  const wrapperRef = useRef<HTMLDivElement>(null);
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
      // The built-in legend printed a "step: -- run: --" table under every
      // chart whenever the pointer was elsewhere; the component draws a
      // compact one of its own, with values only while hovering.
      legend: { show: false },
      hooks: {
        setCursor: [
          (u) => {
            const idx = u.cursor.idx;
            const left = u.cursor.left ?? -1;
            if (!hoveredRef.current || idx == null || left < 0) {
              setHover((prev) => (prev === null ? prev : null));
              return;
            }
            const wrapper = wrapperRef.current;
            const over = u.over.getBoundingClientRect();
            const origin = wrapper?.getBoundingClientRect();
            setHover({
              idx,
              left: over.left - (origin?.left ?? 0) + left,
              top: over.top - (origin?.top ?? 0) + (u.cursor.top ?? 0),
              width: origin?.width ?? over.width,
            });
          },
        ],
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
            // A scatter's unzoomed x range is the data padded on both sides
            // (see `scales.x` below), not the data's own extremes.
            const [homeMin, homeMax] =
              mode === "scatter" && !xIsTime ? paddedRange(fullMin, fullMax) : [fullMin, fullMax];
            if (homeMin == null || homeMax == null) {
              markZoomed(false);
              return;
            }
            const tolerance = ZOOM_EPSILON * Math.max(1, Math.abs(homeMax - homeMin));
            markZoomed(Math.abs(min - homeMin) > tolerance || Math.abs(max - homeMax) > tolerance);
          },
        ],
      },
      scales: {
        // A scatter's axes are hyperparameters and results, not a timeline:
        // pad around the data instead of pinning the edges to the extreme
        // points (x) or reaching down to zero (y), which squashed a CER of
        // 0.04…0.09 into a line along the top.
        x:
          mode === "scatter" && !xIsTime
            ? { time: false, range: (_u, min, max) => paddedRange(min, max) }
            : { time: xIsTime },
        // plan.logEnabled, not the prop: a request for log over data with no
        // positive value at all falls back to linear rather than drawing an
        // empty chart (the Alert below says so).
        y:
          mode === "scatter" && !plan.logEnabled
            ? { distr: 1, range: (_u, min, max) => paddedRange(min, max) }
            : { distr: plan.logEnabled ? 3 : 1 },
      },
      axes: [
        {
          stroke: axisStroke,
          grid: { stroke: gridStroke },
          ticks: { stroke: gridStroke },
          label: xLabel,
          labelSize: xLabel ? 24 : undefined,
          // A time axis keeps uPlot's date formatting; a numeric one gets the
          // same readable ticks as y (steps of 15000 read "15k").
          ...(xIsTime ? {} : { values: (_u: uPlot, splits: number[]) => formatAxisTicks(splits) }),
        },
        {
          stroke: axisStroke,
          grid: { stroke: gridStroke },
          ticks: { stroke: gridStroke },
          label: yLabel,
          labelSize: yLabel ? 24 : undefined,
          // uPlot's formatter caps the decimals, so a learning rate of 3e-4
          // ticked "0, 0, 0, 0". Precision comes from the ticks instead, and
          // the axis is as wide as its widest label.
          values: (_u: uPlot, splits: number[]) => formatAxisTicks(splits, plan.logEnabled),
          size: (_u: uPlot, values: string[]) => axisSizeFor(values),
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
    const onEnter = () => {
      hoveredRef.current = true;
    };
    const onLeave = () => {
      hoveredRef.current = false;
      setHover(null);
    };
    plot.over.addEventListener("mouseenter", onEnter);
    plot.over.addEventListener("mouseleave", onLeave);

    const resizeObserver = new ResizeObserver(() => {
      if (containerRef.current) {
        plot.setSize({ width: containerRef.current.clientWidth || 400, height });
      }
    });
    resizeObserver.observe(containerRef.current);

    return () => {
      resizeObserver.disconnect();
      plot.over.removeEventListener("mouseenter", onEnter);
      plot.over.removeEventListener("mouseleave", onLeave);
      hoveredRef.current = false;
      setHover(null);
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

  const xs = plotData[0] ?? [];
  const hoverX = hover ? xs[hover.idx] : undefined;
  const readout =
    hover && hoverX !== undefined && hoverX !== null
      ? series
          .map((meta, i) => {
            const column = plotData[i + 1];
            const value =
              mode === "scatter" ? (column?.[hover.idx] ?? null) : nearestValue(column, hover.idx);
            return { meta, value };
          })
          .filter((row) => mode !== "scatter" || row.value !== null)
          .slice(0, READOUT_LIMIT)
      : [];
  const xText =
    hoverX === undefined || hoverX === null
      ? ""
      : xIsTime
        ? new Date(hoverX * 1000).toLocaleString(locale)
        : mode === "scatter"
          ? `${xLabel ?? ""} ${formatMetricValue(hoverX)}`
          : `${xLabel ?? t("experiments.chart.step")} ${formatNumber(hoverX)}`;
  const legend = series.slice(0, LEGEND_LIMIT);
  const legendRest = series.slice(LEGEND_LIMIT);
  const flip = hover !== null && hover.left > hover.width / 2;

  return (
    <div ref={wrapperRef} className="relative w-full">
      {/* The title is drawn here rather than by uPlot so it can carry an
          adornment (the goal marker) and truncate a long metric key; the row
          has a fixed height, so the Reset zoom button appearing in it moves
          nothing (DESIGN.md §8). */}
      <div className="flex h-6 items-center gap-1">
        <span className="min-w-0 truncate font-mono text-xs font-medium text-fg" title={title}>
          {title}
        </span>
        {titleAdornment}
        {isZoomed && (
          <Button variant="secondary" size="sm" onClick={resetZoom} className="ml-auto px-2 py-0.5">
            {t("experiments.chart.resetZoom")}
          </Button>
        )}
      </div>
      {/* uPlot draws into this div with its own canvases; it never touches the
          div's own attributes, so role/aria-label placed here survive and give
          the chart the same accessible name a screen reader gets from the
          parallel-coordinates <svg> (which sets role="img" directly). */}
      {/* min-height = the plot's own height: the plot is destroyed and rebuilt
          when the series change (a run toggled in the sidebar), and without a
          floor every chart collapses to 0px for that moment — the page gets
          shorter than the scroll position and the browser jumps to the top. */}
      <div
        ref={containerRef}
        className="w-full"
        style={{ minHeight: height }}
        role="img"
        aria-label={title}
      />
      {hover && readout.length > 0 && (
        <div
          aria-hidden
          className="pointer-events-none absolute top-7 z-20 max-w-[18rem] rounded-md border border-border bg-bg-raised/95 px-2 py-1.5 text-xs shadow-lg"
          style={
            flip ? { right: Math.max(0, hover.width - hover.left + 12) } : { left: hover.left + 12 }
          }
        >
          <div className="mb-1 whitespace-nowrap font-medium text-fg-subtle">{xText}</div>
          {readout.map(({ meta, value }) => (
            <div key={meta.label} className="flex items-center gap-1.5 whitespace-nowrap">
              <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: meta.color }} />
              <span className="min-w-0 truncate text-fg-muted">{meta.label}</span>
              <span className="ml-auto pl-2 font-medium tabular-nums text-fg">
                {value === null ? "—" : formatMetricValue(value)}
              </span>
            </div>
          ))}
        </div>
      )}
      {/* Names only: the numbers are in the hover readout, where they belong
          to a step. The line sample repeats the series' dash, so the baseline
          reads as the baseline here too. */}
      {series.length > 0 && (
        <ul className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-fg-muted">
          {legend.map((meta) => (
            <li key={meta.label} className="flex min-w-0 max-w-[14rem] items-center gap-1.5">
              {mode === "scatter" ? (
                <span
                  aria-hidden
                  className="h-2 w-2 shrink-0 rounded-full"
                  style={{ background: meta.color }}
                />
              ) : (
                <span
                  aria-hidden
                  className="w-3 shrink-0"
                  style={{
                    borderTop: `${meta.width && meta.width > 2 ? 3 : 2}px ${meta.dash ? "dashed" : "solid"} ${meta.color}`,
                  }}
                />
              )}
              <span className="truncate" title={meta.label}>
                {meta.label}
              </span>
            </li>
          ))}
          {legendRest.length > 0 && (
            <li
              className="font-medium text-fg-subtle"
              title={legendRest.map((meta) => meta.label).join("\n")}
            >
              {t("experiments.workspace.legendMore", { count: legendRest.length })}
            </li>
          )}
        </ul>
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
