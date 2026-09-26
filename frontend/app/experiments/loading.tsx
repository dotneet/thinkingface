import { Skeleton } from "@/components/ui/skeleton";

/** First paint for the experiment repository listing: heading, search, count row, rows. */
export default function ExperimentsLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-4 w-80" />
      </div>
      <Skeleton className="h-9 w-full max-w-xl" />
      {/* The count / active-filter row, whose height the real page reserves */}
      <Skeleton className="h-7 w-40" />
      <div className="flex flex-col divide-y divide-border rounded-lg border border-border">
        {Array.from({ length: 8 }, (_, i) => `row-${i}`).map((key) => (
          <div key={key} className="flex items-center gap-4 px-4 py-3">
            <Skeleton className="h-4 w-56" />
            <Skeleton className="h-4 flex-1" />
            <Skeleton className="h-4 w-28" />
          </div>
        ))}
      </div>
    </div>
  );
}
