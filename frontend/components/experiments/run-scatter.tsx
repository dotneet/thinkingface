"use client";

import { ScatterChart } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { UplotChart } from "@/components/experiments/uplot-chart";
import { EmptyState } from "@/components/ui/empty-state";
import { Checkbox, Select } from "@/components/ui/field";
import { colorForRun } from "@/lib/chart-utils";
import { useT } from "@/lib/i18n/client";
import { axisLabel, scatterAxes, scatterPoints } from "@/lib/run-compare";
import type { ExpRun } from "@/types/api";

/**
 * Scatter of the selected runs: any numeric hyperparameter or final metric on
 * either axis. This is the "did the learning rate actually matter?" view that
 * a stack of training curves cannot answer.
 *
 * Each run becomes its own single-point uPlot series so it keeps its table
 * colour and shows up in the legend; the shared x row carries every run's x
 * value with nulls elsewhere, which is how uPlot's aligned data expresses
 * "this series has no value here".
 */
export function RunScatter({
  runs,
  runOrder,
  baseline,
}: {
  runs: ExpRun[];
  runOrder: string[];
  baseline?: string;
}) {
  const t = useT();
  const axes = useMemo(() => scatterAxes(runs), [runs]);
  // Translated once per render and handed to axisLabel(), which stays
  // framework-free — see its doc comment in lib/run-compare.ts.
  const axisPrefixes = useMemo(
    () => ({
      config: t("experiments.scatter.axisConfigPrefix"),
      metric: t("experiments.scatter.axisMetricPrefix"),
    }),
    [t],
  );
  // Lazily initialized from `axes` (already computed above) rather than
  // starting at "" and fixing it up in the effect below: starting empty made
  // the very first paint of every mount — including switching back to this
  // view — render with no axis chosen and flash the "no comparable runs"
  // EmptyState before the effect ever ran (DESIGN.md §9 — a state that is
  // right one tick later still reads as wrong on first paint).
  const [xId, setXId] = useState(() => {
    const config = axes.find((a) => a.source === "config");
    return (config ?? axes[0])?.id ?? "";
  });
  const [yId, setYId] = useState(() => {
    const metric = axes.find((a) => a.source === "metric");
    return (metric ?? axes[axes.length - 1])?.id ?? "";
  });
  const [logScale, setLogScale] = useState(false);

  // Repairs the axis choice as the selection changes after mount: a run
  // leaving the selection can take the last numeric value for an axis with
  // it. A no-op on the very first run (the lazy initializers above already
  // match), so it only ever fires for a real change.
  useEffect(() => {
    if (axes.length === 0) return;
    setXId((current) => {
      if (axes.some((a) => a.id === current)) return current;
      const config = axes.find((a) => a.source === "config");
      return (config ?? axes[0])?.id ?? "";
    });
    setYId((current) => {
      if (axes.some((a) => a.id === current)) return current;
      const metric = axes.find((a) => a.source === "metric");
      return (metric ?? axes[axes.length - 1])?.id ?? "";
    });
  }, [axes]);

  const xAxis = axes.find((a) => a.id === xId);
  const yAxis = axes.find((a) => a.id === yId);
  // uPlot's aligned data must be sorted on x: its cursor and scale logic binary
  // searches the x row, so an unsorted one reads the wrong point on hover.
  const points = useMemo(
    () => scatterPoints(runs, xAxis, yAxis).sort((a, b) => a.x - b.x),
    [runs, xAxis, yAxis],
  );

  // One column per run: xs holds every run's x, and run i's y sits at index i.
  const data = useMemo<(number | null)[][]>(() => {
    const xs = points.map((p) => p.x);
    return [xs, ...points.map((p, i) => xs.map((_, j) => (i === j ? p.y : null)))];
  }, [points]);

  if (runs.length === 0) {
    return (
      <EmptyState
        icon={ScatterChart}
        title={t("experiments.scatter.noRunsTitle")}
        description={t("experiments.scatter.noRunsDescription")}
      />
    );
  }

  if (axes.length < 1) {
    return (
      <EmptyState
        icon={ScatterChart}
        title={t("experiments.scatter.nothingNumericTitle")}
        description={t("experiments.scatter.nothingNumericDescription")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-5 rounded-lg border border-border bg-bg-sunken px-4 py-3 text-sm">
        <label className="flex items-center gap-2">
          <span className="text-fg-subtle">{t("experiments.scatter.xAxis")}</span>
          <Select
            value={xId}
            onChange={(e) => setXId(e.target.value)}
            className="w-auto bg-bg-raised px-2 py-1"
          >
            {axes.map((axis) => (
              <option key={axis.id} value={axis.id}>
                {axisLabel(axis, axisPrefixes)}
              </option>
            ))}
          </Select>
        </label>

        <label className="flex items-center gap-2">
          <span className="text-fg-subtle">{t("experiments.scatter.yAxis")}</span>
          <Select
            value={yId}
            onChange={(e) => setYId(e.target.value)}
            className="w-auto bg-bg-raised px-2 py-1"
          >
            {axes.map((axis) => (
              <option key={axis.id} value={axis.id}>
                {axisLabel(axis, axisPrefixes)}
              </option>
            ))}
          </Select>
        </label>

        <label className="flex items-center gap-2">
          <Checkbox checked={logScale} onChange={(e) => setLogScale(e.target.checked)} />
          <span className="text-fg-subtle">{t("experiments.scatter.logY")}</span>
        </label>
      </div>

      {points.length === 0 ? (
        <EmptyState
          icon={ScatterChart}
          title={t("experiments.scatter.noComparableTitle")}
          description={t("experiments.scatter.noComparableDescription")}
        />
      ) : (
        <div className="rounded-lg border border-border bg-bg-raised p-3">
          <UplotChart
            title={t("experiments.scatter.vsTitle", {
              y: yAxis ? axisLabel(yAxis, axisPrefixes) : "",
              x: xAxis ? axisLabel(xAxis, axisPrefixes) : "",
            })}
            data={data}
            series={points.map((p) => ({
              label:
                p.run === baseline ? t("experiments.chart.baselineSuffix", { run: p.run }) : p.run,
              color: colorForRun(runOrder.indexOf(p.run)),
              pointSize: p.isBaseline ? 15 : undefined,
            }))}
            mode="scatter"
            xIsTime={false}
            logScale={logScale}
            xLabel={xAxis ? axisLabel(xAxis, axisPrefixes) : undefined}
            yLabel={yAxis ? axisLabel(yAxis, axisPrefixes) : undefined}
            height={320}
          />
        </div>
      )}
    </div>
  );
}
