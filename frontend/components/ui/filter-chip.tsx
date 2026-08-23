import { X } from "lucide-react";
import Link from "next/link";
import { cn } from "@/lib/cn";

const BASE =
  "inline-flex max-w-full items-center gap-1.5 rounded-full border border-border bg-bg-sunken py-1 pl-2.5 pr-1.5 text-xs text-fg-muted";

/**
 * One active filter, shown above the results with a control that removes it.
 *
 * A `<Link>` rather than a button so the row works in a Server Component and
 * without JavaScript: `href` is the current listing URL minus this one filter.
 * Rendering active filters here — not only inside the `EmptyState` — is what
 * makes a filter removable while it still matches something (DESIGN.md §8).
 */
export function FilterChip({
  label,
  value,
  href,
  removeLabel,
  className,
}: {
  /** What is being filtered on ("Tag", "License"), shown before the value. */
  label?: string;
  value: string;
  /** Where "remove this filter" goes: the listing URL without it. */
  href: string;
  /** Accessible name for the remove control, e.g. `Remove filter: nlp`. */
  removeLabel: string;
  className?: string;
}) {
  return (
    <span className={cn(BASE, className)}>
      {label && <span className="shrink-0 text-fg-subtle">{label}</span>}
      <span className="min-w-0 truncate">{value}</span>
      <Link
        href={href}
        aria-label={removeLabel}
        title={removeLabel}
        className="shrink-0 rounded-full p-0.5 text-fg-subtle transition-colors hover:bg-bg-hover hover:text-fg"
      >
        <X size={12} />
      </Link>
    </span>
  );
}
