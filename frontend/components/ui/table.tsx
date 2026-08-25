import { cn } from "@/lib/cn";

/**
 * The static table shell (DESIGN.md §5). Twelve screens had copied the same
 * four class strings verbatim — the `scroll-x rounded-lg border` box, the
 * `w-full … border-collapse text-sm` table, the `text-xs font-medium
 * text-fg-subtle` header row, and `px-3 py-2` on every cell — so a change to
 * any of them meant twelve edits and, in practice, drift.
 *
 * `ui/data-table.tsx` is *not* this: it is a virtualized grid over
 * `Record<string, unknown>` rows for the Parquet viewer. Anything whose rows
 * are JSX belongs here.
 *
 * No `"use client"`: these are presentational and both Server and Client
 * Components render them.
 */

/**
 * The scroll box plus the `<table>`.
 *
 * `minWidth` is applied as an inline style rather than a `min-w-[…]` class on
 * purpose. Tailwind only emits an arbitrary-value class it can see spelled out
 * in the source, so a class built from a prop would generate nothing at all;
 * the inline style produces exactly the same `min-width` the call sites had.
 *
 * `className` lands on the scroll box (that is what a caller overrides — e.g.
 * `max-h-[60vh] overflow-y-auto` for a sticky-header listing); `tableClassName`
 * lands on the `<table>` itself.
 */
export function Table({
  minWidth,
  className,
  tableClassName,
  style,
  children,
  ...props
}: {
  /** Below this width the box scrolls sideways instead of squeezing columns. */
  minWidth?: number | string;
  /** Extra classes for the outer scroll box. */
  className?: string;
  /** Extra classes for the `<table>` element. */
  tableClassName?: string;
} & Omit<React.TableHTMLAttributes<HTMLTableElement>, "className">) {
  return (
    <div className={cn("scroll-x rounded-lg border border-border", className)}>
      <table
        {...props}
        className={cn("w-full border-collapse text-sm", tableClassName)}
        style={minWidth === undefined ? style : { minWidth, ...style }}
      >
        {children}
      </table>
    </div>
  );
}

/**
 * `<thead>` **and** the single header `<tr>` — every call site has exactly one
 * header row, and keeping the row here is what stops its class string from
 * being retyped. Children are `<Th>`s.
 *
 * `sticky` pins the header while the body scrolls (DESIGN.md §8.6). It needs
 * an opaque background of its own, since a transparent `thead` lets the rows
 * pass through underneath it.
 */
export function THead({
  sticky = false,
  className,
  rowClassName,
  children,
  ...props
}: {
  sticky?: boolean;
  /** Extra classes for the `<thead>`. */
  className?: string;
  /** Extra classes for the header `<tr>`. */
  rowClassName?: string;
} & Omit<React.HTMLAttributes<HTMLTableSectionElement>, "className">) {
  return (
    <thead className={cn(sticky && "sticky top-0 z-10 bg-bg-sunken", className)} {...props}>
      <tr
        className={cn(
          "border-b border-border text-left text-xs font-medium text-fg-subtle",
          rowClassName,
        )}
      >
        {children}
      </tr>
    </thead>
  );
}

/** `<tbody>`, for symmetry with `THead`. Carries no styling of its own. */
export function TBody(props: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody {...props} />;
}

/**
 * A body row. The divider is `border-b … last:border-0` so the box's own
 * border is not doubled on the final row; pass `className="border-b-0"` for a
 * row that continues into the next one (an expanded detail panel).
 */
export function Tr({ className, ...props }: React.HTMLAttributes<HTMLTableRowElement>) {
  return <tr className={cn("border-b border-border last:border-0", className)} {...props} />;
}

/** A header cell. `align="right"` for a column of numbers or trailing actions. */
export function Th({
  align,
  className,
  ...props
}: {
  align?: "left" | "right";
} & Omit<React.ThHTMLAttributes<HTMLTableCellElement>, "align">) {
  return (
    <th
      className={cn("px-3 py-2 font-medium", align === "right" && "text-right", className)}
      {...props}
    />
  );
}

/** A body cell. */
export function Td({
  align,
  className,
  ...props
}: {
  align?: "left" | "right";
} & Omit<React.TdHTMLAttributes<HTMLTableCellElement>, "align">) {
  return (
    <td className={cn("px-3 py-2", align === "right" && "text-right", className)} {...props} />
  );
}
