"use client";

import { colorForRun } from "@/lib/chart-utils";
import { cn } from "@/lib/cn";

const SIZES = {
  sm: "h-2.5 w-2.5",
  md: "h-3 w-3",
} as const;

/**
 * The colour swatch that identifies a run wherever its name appears — the run
 * table, the run detail header, the config diff's column headers and the
 * parallel-coordinates legend.
 *
 * It is the only place in the experiment UI that paints a colour from outside
 * the token set, which is why it is a component rather than four copies of the
 * same inline `style`: the palette lives in `lib/chart-utils.ts`, the chart
 * lines read it from there, and these dots have to agree with the lines or the
 * legend lies.
 *
 * `colorIndex` is `runColorIndex(runOrder)` (`lib/chart-utils.ts`), memoized by
 * the caller — a table draws one of these per row, and resolving the position
 * with `runOrder.indexOf()` inside each one is a scan of the project per row.
 */
export function RunColorDot({
  run,
  colorIndex,
  size = "sm",
  className,
}: {
  run: string;
  /** `runColorIndex(runOrder)`; a run it does not know falls back to the first colour. */
  colorIndex: ReadonlyMap<string, number>;
  size?: keyof typeof SIZES;
  className?: string;
}) {
  return (
    <span
      className={cn("shrink-0 rounded-full", SIZES[size], className)}
      style={{ background: colorForRun(colorIndex.get(run) ?? -1) }}
    />
  );
}
