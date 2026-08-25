import { useCallback, useState } from "react";

/**
 * What the run filter bar narrows the table (and therefore the charts) with.
 *
 * The metric threshold is three fields rather than a query language, so the
 * metric names on offer are the ones this project actually logged.
 * `buildMetricFilter` (`lib/run-grouping.ts`) turns them into a filter, and
 * answers `null` for a combination that is not usable yet — which means "no
 * filtering", never "match nothing".
 */
export type RunFilters = {
  showArchived: boolean;
  tag: string;
  metric: string;
  op: string;
  value: string;
};

const EMPTY: RunFilters = {
  showArchived: false,
  tag: "",
  metric: "",
  op: "<",
  value: "",
};

/**
 * The run filters as one value, plus the "clear everything" the table's empty
 * state offers — the fastest way back to "something is showing" when a filter
 * combination matches nothing.
 *
 * Keeping them together is what makes `reset` a single assignment instead of
 * five setter calls that have to stay in step with the five `useState` pairs
 * above them.
 */
export function useRunFilters(): {
  filters: RunFilters;
  setFilters: (patch: Partial<RunFilters>) => void;
  reset: () => void;
} {
  const [filters, setState] = useState<RunFilters>(EMPTY);
  const setFilters = useCallback(
    (patch: Partial<RunFilters>) => setState((prev) => ({ ...prev, ...patch })),
    [],
  );
  const reset = useCallback(() => setState(EMPTY), []);
  return { filters, setFilters, reset };
}
