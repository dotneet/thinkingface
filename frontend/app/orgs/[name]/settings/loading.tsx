import { Skeleton } from "@/components/ui/skeleton";

/**
 * First paint for every organization settings screen. The back link, heading
 * and side nav come from the layout, so this stands in for the pane beside
 * them only.
 */
export default function OrgSettingsLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-4 w-72" />
      </div>
      <Skeleton className="h-64 w-full" />
    </div>
  );
}
