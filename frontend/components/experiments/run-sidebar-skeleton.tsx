import { Skeleton } from "@/components/ui/skeleton";

/** Row placeholders: enough to fill a sidebar on a tall screen. */
const ROWS = 14;

/**
 * The run sidebar before the run list has arrived: the search box, the status
 * chips, and fixed-height rows the pitch of the real ones (DESIGN.md §4 —
 * mirror the shape, so nothing moves when the list lands).
 */
export function RunSidebarSkeleton() {
  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-col gap-2 border-b border-border p-3">
        <Skeleton className="h-8 w-full" />
        <div className="flex gap-1">
          {Array.from({ length: 4 }, (_, i) => `chip-${i}`).map((key) => (
            <Skeleton key={key} className="h-6 w-14" />
          ))}
        </div>
        <Skeleton className="h-7 w-full" />
      </div>
      <div className="flex flex-col gap-0 px-3 py-2">
        {Array.from({ length: ROWS }, (_, i) => `row-${i}`).map((key, i) => (
          <div key={key} className="flex h-8 items-center gap-2">
            <Skeleton className="h-3.5 w-3.5" />
            <Skeleton className={i % 3 === 0 ? "h-4 w-32" : "h-4 w-40"} />
            <Skeleton className="ml-auto h-3 w-10" />
          </div>
        ))}
      </div>
    </div>
  );
}
