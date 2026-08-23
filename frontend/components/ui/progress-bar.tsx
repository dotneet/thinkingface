import { cn } from "@/lib/cn";

/**
 * Determinate progress, for work whose end is known — an upload with a byte
 * count. Anything indeterminate is a `Spinner`; anything that will be
 * replaced by content is a `Skeleton`.
 *
 * The track is always rendered at full width so the bar cannot change the
 * layout as it fills (DESIGN.md §8).
 */
export function ProgressBar({
  value,
  label,
  className,
}: {
  /** 0…1. Values outside the range are clamped. */
  value: number;
  /** Accessible name — required, since the bar carries no text of its own. */
  label: string;
  className?: string;
}) {
  const percent = Math.round(Math.min(1, Math.max(0, value)) * 100);
  return (
    <div
      role="progressbar"
      aria-label={label}
      aria-valuenow={percent}
      aria-valuemin={0}
      aria-valuemax={100}
      className={cn("h-1.5 w-full overflow-hidden rounded-full bg-bg-sunken", className)}
    >
      <div
        className="h-full rounded-full bg-accent transition-[width] duration-200 motion-reduce:transition-none"
        style={{ width: `${percent}%` }}
      />
    </div>
  );
}
