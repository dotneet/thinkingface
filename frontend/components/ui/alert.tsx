import type { LucideIcon } from "lucide-react";
import { AlertTriangle, CheckCircle2, Info, XCircle } from "lucide-react";
import { cn } from "@/lib/cn";

export type AlertTone = "info" | "positive" | "negative" | "warning";

const TONES: Record<AlertTone, string> = {
  info: "border-border bg-bg-sunken text-fg-muted",
  positive: "border-positive/40 bg-positive/10 text-fg-muted",
  negative: "border-negative/40 bg-negative/10 text-fg-muted",
  warning: "border-warning/40 bg-warning/10 text-fg-muted",
};

// Same reasoning as badge.tsx: the icon sits on a fill of its own hue, so it
// uses the `-strong` token. The icon is the only thing carrying the tone here
// (the body text is fg-muted), which makes its contrast load-bearing.
const ICON_TONES: Record<AlertTone, string> = {
  info: "text-fg-subtle",
  positive: "text-positive-strong",
  negative: "text-negative-strong",
  warning: "text-warning-strong",
};

const ICONS: Record<AlertTone, LucideIcon> = {
  info: Info,
  positive: CheckCircle2,
  negative: XCircle,
  warning: AlertTriangle,
};

// An Alert usually appears *after* first paint — a form submission failed, a
// token was created, a push is still indexing — so it needs a live region or a
// screen reader never learns it arrived. "negative" is assertive (role="alert")
// because it means the thing the user just asked for did not happen; the other
// tones are polite (role="status") so they wait for a pause in speech.
const ROLES: Record<AlertTone, "alert" | "status"> = {
  info: "status",
  positive: "status",
  negative: "alert",
  warning: "status",
};

/** Inline, non-blocking message attached to a page or form. */
export function Alert({
  tone = "info",
  title,
  icon,
  role,
  children,
  className,
}: {
  tone?: AlertTone;
  title?: string;
  /** Overrides the tone's default icon (e.g. a spinner for in-progress work). */
  icon?: LucideIcon;
  /**
   * Overrides the tone's live-region role. Pass "presentation" for a purely
   * decorative banner that is part of the page on first paint.
   */
  role?: React.AriaRole;
  children?: React.ReactNode;
  className?: string;
}) {
  const Icon = icon ?? ICONS[tone];
  return (
    <div
      role={role ?? ROLES[tone]}
      className={cn(
        "flex items-start gap-2.5 rounded-lg border px-3 py-2.5 text-sm",
        TONES[tone],
        className,
      )}
    >
      <Icon size={16} strokeWidth={1.5} className={cn("mt-0.5 shrink-0", ICON_TONES[tone])} />
      <div className="flex min-w-0 flex-col gap-0.5">
        {title && <span className="font-medium text-fg">{title}</span>}
        {children}
      </div>
    </div>
  );
}
