"use client";

import { ScatterChart } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { UplotChart } from "@/components/experiments/uplot-chart";
import { EmptyState } from "@/components/ui/empty-state";
import { Checkbox, Select } from "@/components/ui/field";
import { colorForRun } from "@/lib/chart-utils";
import { useT } from "@/lib/i18n/client";
import { scatterAxes, scatterPoints } from "@/lib/run-compare";
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
  const [xId, setXId] = useState("");
  const [yId, setYId] = useState("");
  const [logScale, setLogScale] = useState(false);

  // Seed and repair the axis choice as the selection changes: a run leaving the
  // selection can take the last numeric value for an axis with it.
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
                {axis.label}
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
                {axis.label}
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
              y: yAxis?.label ?? "",
              x: xAxis?.label ?? "",
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
            xLabel={xAxis?.label}
            yLabel={yAxis?.label}
            height={320}
          />
        </div>
      )}
    </div>
  );
}
