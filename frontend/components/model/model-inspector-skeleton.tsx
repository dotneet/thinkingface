import { Skeleton } from "@/components/ui/skeleton";

// Placeholder shown while the (potentially slow) checkpoint metadata query is
// in flight. Keys are static because the list never changes.
const SUMMARY_KEYS = ["format", "parameters", "tensors", "size"];

export function ModelInspectorSkeleton() {
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-4">
        {SUMMARY_KEYS.map((key) => (
          <Skeleton key={key} className="h-5 w-24" />
        ))}
      </div>
      <Skeleton className="h-24 rounded-lg border border-border" />
      <Skeleton className="h-72 rounded-lg border border-border" />
    </div>
  );
}
