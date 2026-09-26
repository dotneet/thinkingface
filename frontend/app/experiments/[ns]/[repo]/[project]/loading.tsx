import { RunSidebarSkeleton } from "@/components/experiments/run-sidebar-skeleton";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * First paint of a project workspace, in its real shape: the run sidebar
 * (≥lg) with placeholder rows, and next to it the header, the view bar and a
 * grid of chart placeholders. `data-full-bleed` widens the page exactly as
 * the loaded workspace does, so nothing jumps sideways when it arrives.
 */
export default function ExperimentProjectLoading() {
  return (
    <div
      data-full-bleed=""
      className="mx-auto w-full max-w-[1920px] lg:grid lg:grid-cols-[19rem_minmax(0,1fr)] lg:gap-6 xl:grid-cols-[21rem_minmax(0,1fr)]"
    >
      <div className="hidden overflow-hidden rounded-lg border border-border bg-bg-raised lg:block lg:h-[calc(100dvh-3.5rem-1px-2rem)]">
        <RunSidebarSkeleton />
      </div>
      <div className="flex min-w-0 flex-col gap-4">
        <div className="flex flex-col gap-2">
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-8 w-64" />
          <Skeleton className="h-5 w-96 max-w-full" />
        </div>
        <Skeleton className="h-10 w-full max-w-xl" />
        <Skeleton className="h-12 w-full" />
        <div className="grid grid-cols-[repeat(auto-fill,minmax(min(100%,26rem),1fr))] gap-3">
          {Array.from({ length: 4 }, (_, i) => `chart-${i}`).map((key) => (
            <Skeleton key={key} className="h-[17.5rem] w-full rounded-lg" />
          ))}
        </div>
      </div>
    </div>
  );
}
