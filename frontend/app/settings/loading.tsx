import { Skeleton } from "@/components/ui/skeleton";

/**
 * First paint for every personal settings screen. The side nav comes from the
 * layout and stays put, so this only stands in for the pane beside it: the
 * page's own heading and description, then its first panel.
 */
export default function SettingsLoading() {
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-8 w-56" />
        <Skeleton className="h-4 w-80" />
      </div>
      <Skeleton className="h-64 w-full" />
    </div>
  );
}
