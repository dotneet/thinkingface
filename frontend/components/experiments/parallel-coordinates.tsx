"use client";

import { GitCompareArrows } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { colorForRun } from "@/lib/chart-utils";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";
import {
  axisTicks,
  axisX,
  axisY,
  linePath,
  type ParallelAxis,
  parallelAxes,
  parallelLines,
} from "@/lib/run-parallel";
import type { ExpRun } from "@/types/api";

/**
 * Parallel coordinates: one vertical axis per hyperparameter and metric, one
 * polyline per run. It is the sweep view — "the runs that reached a low loss
 * all came from the small-batch, high-warmup corner" is a shape you can see
 * here and cannot see in a stack of training curves.
 *
 * Drawn as plain SVG rather than through uPlot on purpose. uPlot draws one
 * cartesian plane with shared scales; parallel coordinates is N independent
 * axes, each with its own scale and its own tick labels (a categorical axis
 * has no numeric scale at all). Faking it would mean normalising every value
 * to 0..1 and hand-drawing the labels anyway — at which point uPlot is
 * carrying no weight, while its cursor, legend and zoom would all be lying
 * about what is on screen. The data here is tens of runs by a handful of axes,
 * which is nothing for the DOM, and an <svg> keeps every line hoverable,
 * focusable and themeable with the tokens the rest of the UI uses.
 */

const WIDTH = 900;
const HEIGHT = 380;
/** Horizontal room for the first and last axis' tick labels. */
const PAD_X = 76;
/** Vertical room for the axis title on top and the low tick underneath. */
const PAD_Y = 46;

/** How many axes are pre-selected when the view opens. */
const DEFAULT_AXES = 6;

export function ParallelCoordinates({
  runs,
  runOrder,
  baseline,
}: {
  runs: ExpRun[];
  /** Full project run order, so a run keeps its table colour here too. */
  runOrder: string[];
  baseline?: string;
}) {
  const t = useT();
  const axes = useMemo(() => parallelAxes(runs), [runs]);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [highlight, setHighlight] = useState<string | null>(null);
  const [pinned, setPinned] = useState<string | null>(null);

  // Seed and repair the axis choice as the selection changes: a run leaving
  // the selection can take the last value for an axis with it.
  useEffect(() => {
    setSelectedIds((current) => {
      const kept = current.filter((id) => axes.some((a) => a.id === id));
      if (kept.length >= 2) return kept.length === current.length ? current : kept;
      return axes.slice(0, DEFAULT_AXES).map((a) => a.id);
    });
  }, [axes]);

  // Selected axes keep the left-to-right order of the axis list (config first,
  // then metrics) rather than the order they happened to be ticked in.
  const shown = useMemo(() => axes.filter((a) => selectedIds.includes(a.id)), [axes, selectedIds]);
  const lines = useMemo(() => parallelLines(runs, shown), [runs, shown]);

  const active = highlight ?? pinned;

  if (runs.length === 0) {
    return (
      <EmptyState
        icon={GitCompareArrows}
        title={t("experiments.parallel.noRunsTitle")}
        description={t("experiments.parallel.noRunsDescription")}
      />
    );
  }

  if (axes.length < 2) {
    return (
      <EmptyState
        icon={GitCompareArrows}
        title={t("experiments.parallel.notEnoughAxesTitle")}
        description={t("experiments.parallel.notEnoughAxesDescription")}
      />
    );
  }

  function togglePinned(run: string) {
    setPinned((current) => (current === run ? null : run));
  }

  function toggleAxis(id: string) {
    setSelectedIds((current) =>
      current.includes(id) ? current.filter((a) => a !== id) : [...current, id],
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2 rounded-lg border border-border bg-bg-sunken px-4 py-3 text-sm">
        <span className="text-fg-subtle">{t("experiments.parallel.axesLabel")}</span>
        <div className="flex flex-wrap gap-1.5">
          {axes.map((axis) => {
            const on = selectedIds.includes(axis.id);
            return (
              <Button
                key={axis.id}
                size="sm"
                variant={on ? "primary" : "secondary"}
                aria-pressed={on}
                onClick={() => toggleAxis(axis.id)}
                title={
                  axis.kind === "categorical"
                    ? t("experiments.parallel.categoricalHint", { name: axis.label })
                    : undefined
                }
              >
                <span className="font-mono text-xs">{axis.label}</span>
                {axis.kind === "categorical" && (
                  <span className="text-xs opacity-70">{t("experiments.parallel.catTag")}</span>
                )}
              </Button>
            );
          })}
        </div>
      </div>

      {shown.length < 2 ? (
        <EmptyState
          icon={GitCompareArrows}
          title={t("experiments.parallel.pickTwoTitle")}
          description={t("experiments.parallel.pickTwoDescription")}
        />
      ) : lines.length === 0 ? (
        <EmptyState
          icon={GitCompareArrows}
          title={t("experiments.parallel.noComparableTitle")}
          description={t("experiments.parallel.noComparableDescription")}
        />
      ) : (
        <div className="scroll-x rounded-lg border border-border bg-bg-raised p-3">
          <svg
            viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
            className="h-auto w-full min-w-[600px]"
            role="img"
            aria-label={t("experiments.parallel.chartAria", { count: lines.length })}
          >
            <title>{t("experiments.parallel.chartAria", { count: lines.length })}</title>

            {shown.map((axis, index) => (
              <AxisColumn key={axis.id} axis={axis} index={index} count={shown.length} />
            ))}

            {lines.map((line) => {
              const dimmed = active !== null && active !== line.run;
              return (
                <path
                  key={line.run}
                  // Pointer emphasis is CSS-only and the real control is the
                  // legend below: an interactive <path> would be a button that
                  // no keyboard can reach, and the legend gives every run a
                  // focusable chip that highlights and pins the same way.
                  className="transition-[stroke-width] hover:[stroke-width:3.5]"
                  d={linePath(line, shown.length, WIDTH, HEIGHT, PAD_X)}
                  fill="none"
                  stroke={colorForRun(runOrder.indexOf(line.run))}
                  strokeWidth={active === line.run ? 3.5 : line.run === baseline ? 2.5 : 1.75}
                  strokeOpacity={dimmed ? 0.18 : 1}
                  strokeDasharray={line.complete ? undefined : "6 4"}
                  strokeLinejoin="round"
                >
                  <title>{line.run}</title>
                </path>
              );
            })}
          </svg>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-1.5">
        {lines.map((line) => {
          const on = active === line.run;
          return (
            <Button
              key={line.run}
              size="sm"
              variant={pinned === line.run ? "primary" : "ghost"}
              aria-pressed={pinned === line.run}
              onMouseEnter={() => setHighlight(line.run)}
              onMouseLeave={() => setHighlight(null)}
              onFocus={() => setHighlight(line.run)}
              onBlur={() => setHighlight(null)}
              onClick={() => togglePinned(line.run)}
              aria-label={t("experiments.parallel.highlightAria", { name: line.run })}
            >
              <span
                className={cn(
                  "h-2.5 w-2.5 shrink-0 rounded-full",
                  on ? "opacity-100" : "opacity-70",
                )}
                style={{ background: colorForRun(runOrder.indexOf(line.run)) }}
              />
              <span className="max-w-[14rem] truncate">{line.run}</span>
              {line.run === baseline && (
                <Badge tone="accent">{t("experiments.table.baselineBadge")}</Badge>
              )}
            </Button>
          );
        })}
      </div>

      {lines.some((l) => !l.complete) && (
        <p className="text-xs font-medium text-fg-subtle">
          {t("experiments.parallel.incompleteHint")}
        </p>
      )}
    </div>
  );
}

/** One vertical axis: its line, its title and its end labels. */
function AxisColumn({ axis, index, count }: { axis: ParallelAxis; index: number; count: number }) {
  const x = axisX(index, count, WIDTH, PAD_X);
  const ticks = axisTicks(axis);
  // Colours come in as attributes rather than classes: an <svg> paint is a JS
  // value, which is the one place DESIGN.md allows a raw token (uPlot does the
  // same for its stroke colours).
  return (
    <g>
      <line
        x1={x}
        y1={axisY(1, HEIGHT, PAD_Y)}
        x2={x}
        y2={axisY(0, HEIGHT, PAD_Y)}
        stroke="var(--tf-border-strong)"
        strokeWidth={1}
      />
      <text
        x={x}
        y={axisY(1, HEIGHT, PAD_Y) - 16}
        textAnchor="middle"
        fontSize={13}
        fill="var(--tf-fg-muted)"
      >
        {axis.label}
      </text>
      {ticks.map((label, i) => {
        const t = ticks.length <= 1 ? 0.5 : i / (ticks.length - 1);
        return (
          <text
            key={label}
            x={x}
            y={axisY(t, HEIGHT, PAD_Y) + 4}
            textAnchor={index === 0 ? "end" : "start"}
            dx={index === 0 ? -8 : 8}
            fontSize={11}
            fill="var(--tf-fg-subtle)"
          >
            {label}
          </text>
        );
      })}
    </g>
  );
}
