import { TableSkeleton } from "@/components/repo/repo-page-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * First paint for a project's dashboard: breadcrumb and title, then the chart
 * grid and the run table `ExperimentDashboard` renders.
 */
export default function ExperimentProjectLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-5 w-72" />
        <Skeleton className="h-8 w-64" />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {Array.from({ length: 4 }, (_, i) => `chart-${i}`).map((key) => (
          <Skeleton key={key} className="h-56 w-full" />
        ))}
      </div>

      <TableSkeleton rows={8} />
    </div>
  );
}
