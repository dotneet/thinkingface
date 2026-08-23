import { cn } from "@/lib/cn";

/**
 * Placeholder block for the loading state. Give it explicit height/width
 * utilities so the placeholder matches the shape of the content it replaces.
 */
export function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        // Unlike Spinner, the shape itself (not the pulse) is what tells the
        // reader "this is a placeholder", so `prefers-reduced-motion` can drop
        // the animation entirely here without losing information.
        "animate-pulse rounded bg-bg-sunken motion-reduce:animate-none",
        className,
      )}
      {...props}
    />
  );
}

/** Convenience wrapper for the common "n stacked text lines" placeholder. */
export function SkeletonLines({ lines = 3, className }: { lines?: number; className?: string }) {
  return (
    <div className={cn("flex flex-col gap-2", className)}>
      {Array.from({ length: lines }, (_, i) => `line-${i}`).map((key, i) => (
        <Skeleton key={key} className={i === lines - 1 ? "h-4 w-2/3" : "h-4 w-full"} />
      ))}
    </div>
  );
}
