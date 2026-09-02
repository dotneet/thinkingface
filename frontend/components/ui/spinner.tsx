import { cn } from "@/lib/cn";

/** Indeterminate progress for in-place refreshes that are too short for a skeleton. */
export function Spinner({
  size = 16,
  className,
  label,
}: {
  size?: number;
  className?: string;
  /**
   * What is loading, announced by screen readers. It comes from the caller's
   * dictionary: this primitive renders inside Server Components too, so it
   * cannot resolve a translator itself, and the hardcoded English default it
   * used to carry ("Loading") was read out verbatim in the Japanese UI —
   * "Loading 利用可否を確認中…".
   *
   * Omit it only when the spinner already sits inside a live region that says
   * what is loading (`namespace-availability.tsx`). It is then decorative in
   * exactly the sense DESIGN.md §3 gives an icon beside a text label, and is
   * hidden from the accessibility tree rather than announced twice.
   */
  label?: string;
}) {
  const decorative = label === undefined;
  return (
    <span
      role={decorative ? undefined : "status"}
      aria-label={label}
      aria-hidden={decorative || undefined}
      style={{ width: size, height: size }}
      className={cn(
        // `motion-reduce:` slows the spin rather than freezing it outright: a
        // fully static ring would drop the only visual "still loading" signal
        // for a sighted user with prefers-reduced-motion set, and role="status"
        // alone does not help them. Screen readers get the label either way —
        // from this element when it has one, from the surrounding live region
        // when it does not.
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
