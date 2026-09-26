import { Skeleton } from "@/components/ui/skeleton";

/** First paint for an experiment repository's project list. */
export default function ExperimentRepoLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-5 w-56" />
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-5 w-full max-w-xl" />
      </div>
      <div className="flex flex-col gap-2">
        <Skeleton className="h-5 w-24" />
        <div className="flex flex-col divide-y divide-border rounded-lg border border-border">
          {Array.from({ length: 3 }, (_, i) => `row-${i}`).map((key) => (
            <div
              key={key}
              className="grid grid-cols-1 gap-x-6 gap-y-2 px-4 py-3 md:grid-cols-[22rem_1fr]"
            >
              <div className="flex flex-col gap-1.5">
                <Skeleton className="h-5 w-40" />
                <Skeleton className="h-4 w-48" />
                <Skeleton className="h-4 w-56" />
              </div>
              <Skeleton className="h-14 w-full max-w-md self-center" />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
