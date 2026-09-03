import { cloneElement, isValidElement, useId } from "react";
import { cn } from "@/lib/cn";

// The shared look that used to live in globals.css as `.tf-input`. Keeping it
// here means the three form controls stay in sync and nothing outside ui/ has
// to know the border/padding values.
// `border-control`, not `border`: a decorative hairline is allowed to be
// faint, but the boundary of a control has to reach 3:1 (WCAG 1.4.11) — and
// bg-sunken separates from a card by only ~1.1:1 in the light theme, so the
// border is the only thing that says "this is a field".
const CONTROL =
  "w-full rounded-md border border-border-control bg-bg-sunken px-3 py-2 text-fg outline-none transition-colors placeholder:text-fg-subtle focus:border-accent disabled:opacity-60";

export function Input({ className, ...props }: React.ComponentProps<"input">) {
  return <input className={cn(CONTROL, className)} {...props} />;
}

export function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return <textarea className={cn(CONTROL, "resize-y", className)} {...props} />;
}

export function Select({ className, ...props }: React.ComponentProps<"select">) {
  return <select className={cn(CONTROL, className)} {...props} />;
}

/**
 * Square toggle used in tables and filter bars; sized to sit on a text line.
 *
 * For the "some, but not all" state a select-all box needs, use
 * `TriStateCheckbox` (components/ui/tri-state-checkbox.tsx): `indeterminate`
 * is a DOM property with no attribute, so setting it needs a ref, and a ref
 * cannot be attached here — this module has no `"use client"` and renders
 * inside Server Components.
 */
export function Checkbox({ className, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type="checkbox"
      className={cn("h-3.5 w-3.5 shrink-0 rounded border-border-control accent-accent", className)}
      {...props}
    />
  );
}

/**
 * Range input, styled with the same accent token as {@link Checkbox} so it
 * does not fall back to the browser's own colour. An icon-free control like
 * this carries no visible label of its own, so `aria-label` is required.
 */
export function Slider({
  className,
  ...props
}: React.ComponentProps<"input"> & { "aria-label": string }) {
  return <input type="range" className={cn("accent-accent", className)} {...props} />;
}

/**
 * Label + control pair. The control is passed as `children` and wrapped by the
 * <label>, so clicking the text focuses it without needing matching ids.
 *
 * `hint`, when given, is wired to the control with `aria-describedby` rather
 * than being rendered inside the `<label>` with everything else. A wrapping
 * `<label>`'s full text content becomes the accessible *name* of whatever it
 * wraps, so a hint left inside it used to get read as part of the control's
 * name — a role select whose hint spelled out all three roles announced as
 * "role read grants membership only, write can push ... combobox". Splitting
 * the hint out into its own `id`, referenced via `aria-describedby`, makes it
 * a *description* instead, which every call site gets for free.
 */
export function Field({
  label,
  hint,
  className,
  children,
}: {
  label: string;
  hint?: string;
  className?: string;
  children: React.ReactNode;
}) {
  const hintId = useId();

  if (!hint) {
    return (
      <label className={cn("flex flex-col gap-1 text-sm", className)}>
        <span className="font-medium text-fg-muted">{label}</span>
        {children}
      </label>
    );
  }

  const describedControl = isValidElement<{ "aria-describedby"?: string }>(children)
    ? cloneElement(children, {
        "aria-describedby": children.props["aria-describedby"]
          ? `${children.props["aria-describedby"]} ${hintId}`
          : hintId,
      })
    : children;

  return (
    <div className={cn("flex flex-col gap-1 text-sm", className)}>
      <label className="flex flex-col gap-1">
        <span className="font-medium text-fg-muted">{label}</span>
        {describedControl}
      </label>
      <span id={hintId} className="text-xs font-medium text-fg-subtle">
        {hint}
      </span>
    </div>
  );
}
