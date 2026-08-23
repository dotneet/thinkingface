"use client";

import { useEffect, useRef, useState } from "react";
import uPlot from "uplot";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n/client";
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
  // Latest x column, read from the setScale hook to decide whether the x
  // scale still spans the full data range. A ref (not the `data` prop
  // closed over at plot-creation time) because data updates are applied via
  // plot.setData() below without recreating the plot/hooks.
  const dataRef = useRef(data);
  const [isZoomed, setIsZoomed] = useState(false);

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
              setIsZoomed(false);
              return;
            }
            // A single distinct x value (one point, or every point sharing an
            // x) has nothing to zoom out of: uPlot still pads the auto-range
            // around that one value, which would otherwise read as "zoomed"
            // from the very first render and never clear.
            if (fullMin === fullMax) {
              setIsZoomed(false);
              return;
            }
            const tolerance = ZOOM_EPSILON * Math.max(1, Math.abs(fullMax - fullMin));
            setIsZoomed(Math.abs(min - fullMin) > tolerance || Math.abs(max - fullMax) > tolerance);
          },
        ],
      },
      scales: {
        x: { time: xIsTime },
        y: { distr: logScale ? 3 : 1 },
      },
      axes: [
        {
          stroke: "var(--tf-fg-subtle)",
          grid: { stroke: "var(--tf-border)" },
          label: xLabel,
          labelSize: xLabel ? 24 : undefined,
        },
        {
          stroke: "var(--tf-fg-subtle)",
          grid: { stroke: "var(--tf-border)" },
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
            : { points: { show: false } }),
        })),
      ],
    };

    const plot = new uPlot(opts, data as uPlot.AlignedData, containerRef.current);
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
    // array identity changes on every render; `data` is applied by the effect
    // below instead of rebuilding the plot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [title, xIsTime, logScale, mode, xLabel, yLabel, seriesSignature, height, t, syncKey]);

  useEffect(() => {
    dataRef.current = data;
    if (plotRef.current) {
      plotRef.current.setData(data as uPlot.AlignedData);
    }
  }, [data]);

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
      <div ref={containerRef} className="w-full" />
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
    </div>
  );
}
