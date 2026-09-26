import { Skeleton } from "@/components/ui/skeleton";

/**
 * Placeholder for the run detail route while the run listing is fetched.
 * Mirrors the real layout: breadcrumb and run switcher, the header with its
 * facts line, summary cards, config beside the note, then the charts.
 */
export default function ExperimentRunLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-3">
          <Skeleton className="h-5 w-72 max-w-[50%]" />
          <Skeleton className="h-7 w-52" />
        </div>
        <div className="flex items-center justify-between gap-3">
          <Skeleton className="h-8 w-64" />
          <Skeleton className="h-7 w-48" />
        </div>
        <Skeleton className="h-5 w-full max-w-2xl" />
      </div>

      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
        {Array.from({ length: 5 }, (_, i) => `stat-${i}`).map((key) => (
          <Skeleton key={key} className="h-[4.5rem] w-full" />
        ))}
      </div>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <Skeleton className="h-64 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {Array.from({ length: 2 }, (_, i) => `chart-${i}`).map((key) => (
          <Skeleton key={key} className="h-56 w-full" />
        ))}
      </div>
    </div>
  );
}
