import { useCallback, useState } from "react";

/**
 * What the metrics toolbar controls: the x axis the series are read on, how
 * hard the curves are smoothed, whether the y axis is logarithmic, and whether
 * the charts share a cursor and zoom.
 *
 * `xMode` is part of this even though it is a *request* parameter rather than a
 * drawing option — it is the leftmost control in the same toolbar, and every
 * page that owns the toolbar also owns the metrics query keyed on it.
 */
export type ChartOptions = {
  xMode: "step" | "time";
  smoothing: number;
  logScale: boolean;
  /** Ignored by pages that plot a single run — see `MetricsToolbar`'s `showSyncZoom`. */
  syncZoom: boolean;
};

const DEFAULTS: ChartOptions = {
  xMode: "step",
  smoothing: 0,
  logScale: false,
  syncZoom: true,
};

/**
 * The four chart options as one piece of state, so a page that renders
 * `MetricsToolbar` holds one value and one setter instead of four `useState`
 * pairs that have to be threaded through in the same order every time.
 *
 * `setOptions` takes a patch (`setOptions({ logScale: true })`) rather than a
 * whole object: every control changes exactly one field, and a patch keeps the
 * others from being dropped by a call site that forgot to spread.
 */
export function useChartOptions(initial?: Partial<ChartOptions>): {
  options: ChartOptions;
  setOptions: (patch: Partial<ChartOptions>) => void;
} {
  const [options, setState] = useState<ChartOptions>(() => ({ ...DEFAULTS, ...initial }));
  const setOptions = useCallback(
    (patch: Partial<ChartOptions>) => setState((prev) => ({ ...prev, ...patch })),
    [],
  );
  return { options, setOptions };
}
