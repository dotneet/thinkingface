import { Skeleton } from "@/components/ui/skeleton";

/**
 * Placeholder for the run detail route while the run listing is fetched.
 * Mirrors the real layout: breadcrumb, header, summary cards, a chart grid and
 * the config table below it.
 */
export default function ExperimentRunLoading() {
  return (
    <div className="flex flex-col gap-6">
      <Skeleton className="h-4 w-72" />

      <div className="flex flex-col gap-3">
        <Skeleton className="h-7 w-64" />
        <Skeleton className="h-4 w-96" />
        <Skeleton className="h-6 w-48" />
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => `stat-${i}`).map((key) => (
          <Skeleton key={key} className="h-20 w-full" />
        ))}
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {Array.from({ length: 2 }, (_, i) => `chart-${i}`).map((key) => (
          <Skeleton key={key} className="h-56 w-full" />
        ))}
      </div>

      <Skeleton className="h-48 w-full" />
    </div>
  );
}
