import { Skeleton } from "@/components/ui/skeleton";

/** How many chart placeholders stand in while the first series load. */
const PLACEHOLDER_CHARTS = 2;

/**
 * First paint of a metrics grid: the same two-column layout `MetricsCharts`
 * draws, filled with placeholders the height of a chart. Mirroring the real
 * shape is the point — a "Loading…" line would collapse the region and then
 * push everything below it back down once the charts arrive (DESIGN.md §4, §8).
 */
export function MetricsChartsSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {Array.from({ length: PLACEHOLDER_CHARTS }, (_, i) => `chart-${i}`).map((key) => (
        <Skeleton key={key} className="h-56 w-full" />
      ))}
    </div>
  );
}
