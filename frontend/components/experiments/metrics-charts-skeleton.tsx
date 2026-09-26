import { Skeleton } from "@/components/ui/skeleton";

/**
 * First paint of a metrics grid: the same auto-fill grid `MetricsCharts`
 * draws, filled with placeholders the height of a chart card (title row,
 * plot, legend). Mirroring the real shape is the point — a "Loading…" line
 * would collapse the region and then push everything below it back down once
 * the charts arrive (DESIGN.md §4, §8).
 */
export function MetricsChartsSkeleton({ count = 4 }: { count?: number }) {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,26rem),1fr))] gap-3">
      {Array.from({ length: count }, (_, i) => `chart-${i}`).map((key) => (
        <Skeleton key={key} className="h-[17.5rem] w-full rounded-lg" />
      ))}
    </div>
  );
}
