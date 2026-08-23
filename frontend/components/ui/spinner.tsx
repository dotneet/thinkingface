import { cn } from "@/lib/cn";

/** Indeterminate progress for in-place refreshes that are too short for a skeleton. */
export function Spinner({
  size = 16,
  className,
  label = "Loading",
}: {
  size?: number;
  className?: string;
  label?: string;
}) {
  return (
    <span
      role="status"
      aria-label={label}
      style={{ width: size, height: size }}
      className={cn(
        // `motion-reduce:` slows the spin rather than freezing it outright: a
        // fully static ring would drop the only visual "still loading" signal
        // for a sighted user with prefers-reduced-motion set, and role="status"
        // alone does not help them. Screen readers get the aria-label either way.
        "inline-block shrink-0 animate-spin motion-reduce:animate-[spin_2.5s_linear_infinite] rounded-full border-2 border-border border-t-accent",
        className,
      )}
    />
  );
}

/**
 * A Spinner that occupies its space whether or not it is spinning.
 *
 * Dropping a bare `{isFetching && <Spinner/>}` into a `flex-wrap` toolbar
 * changes where that row wraps, so the controls next to it jump to another
 * line at the exact moment the user is reaching for them (see DESIGN.md §8).
 * This keeps the box reserved and only toggles visibility, so a refresh never
 * moves anything.
 *
 * `aria-hidden` while idle: the reserved box is presentational, and a
 * permanently mounted `role="status"` would otherwise sit in the
 * accessibility tree announcing nothing.
 */
export function SpinnerSlot({
  active,
  size = 16,
  className,
  label,
}: {
  active: boolean;
  size?: number;
  className?: string;
  label: string;
}) {
  return (
    <span
      style={{ width: size, height: size }}
      className={cn("inline-flex shrink-0 items-center justify-center", className)}
      aria-hidden={!active}
    >
      {active ? <Spinner size={size} label={label} /> : null}
    </span>
  );
}
