import { cn } from "@/lib/cn";

export type BadgeTone = "neutral" | "muted" | "accent" | "positive" | "negative" | "warning";

const BASE = "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium";

const TONES: Record<BadgeTone, string> = {
  neutral: "border-border bg-bg-sunken text-fg-muted",
  // One step quieter than neutral: no fill, subtle text. For a label that is
  // present for completeness rather than to be noticed — the "read" role, for
  // instance, next to the "write" and "admin" ones that matter more.
  muted: "border-border bg-transparent text-fg-subtle",
  // The four tinted tones draw their text with the `-strong` token, not the
  // base one: the fill is the same hue as the text, so `text-warning` on
  // `bg-warning/20` measured 2.12 in the light theme. See globals.css.
  accent: "border-transparent bg-accent-muted text-accent-strong",
  positive: "border-transparent bg-positive/15 text-positive-strong",
  negative: "border-transparent bg-negative/15 text-negative-strong",
  warning: "border-transparent bg-warning/20 text-warning-strong",
};

/**
 * The badge look as a bare class string, for the handful of places that need
 * a <Link> or other non-<span> element to read as a badge (a tag/branch pill
 * that navigates, for instance). Mirrors buttonClass() in ui/button.tsx.
 * Prefer <Badge> whenever a plain <span> works.
 */
export function badgeClass({
  tone = "neutral",
  className,
}: {
  tone?: BadgeTone;
  className?: string;
} = {}): string {
  // cn() rather than string concatenation: with a plain template literal the
  // tone's own utilities and the caller's both land in class, and which one
  // wins is decided by their order in the stylesheet instead of by the
  // caller. tailwind-merge drops the overridden one, which is the contract
  // every other primitive here follows.
  return cn(BASE, TONES[tone], className);
}

export function Badge({
  children,
  tone = "neutral",
  className,
}: {
  children: React.ReactNode;
  tone?: BadgeTone;
  className?: string;
}) {
  return <span className={badgeClass({ tone, className })}>{children}</span>;
}
