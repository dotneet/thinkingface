import { cn } from "@/lib/cn";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "sm" | "md";

// Same shape as badge.tsx's tone map: every variant is spelled out so the set
// of allowed looks is visible in one place and nothing can be invented at a
// call site.
const VARIANTS: Record<ButtonVariant, string> = {
  primary: "border-transparent bg-accent text-accent-fg hover:opacity-90",
  secondary: "border-border bg-transparent text-fg-muted hover:bg-bg-hover hover:text-fg",
  ghost: "border-transparent bg-transparent text-fg-muted hover:bg-bg-hover hover:text-fg",
  // The hover tint is the same hue as the label, so the pair loses contrast
  // exactly when the pointer is on it (4.41 on the page canvas in the light
  // theme). The label switches to `-strong` for that state — same reasoning
  // as badge.tsx, applied to a state rather than a fill.
  danger:
    "border-transparent bg-transparent text-negative hover:bg-negative/10 hover:text-negative-strong",
};

const SIZES: Record<ButtonSize, string> = {
  sm: "gap-1 rounded-md px-2 py-1 text-xs",
  md: "gap-1.5 rounded-md px-3 py-1.5 text-sm",
};

const BASE =
  "inline-flex shrink-0 items-center justify-center border font-medium transition-colors disabled:pointer-events-none disabled:opacity-40";

/**
 * The button look as a bare class string, for the handful of places that need
 * a <Link> (a real navigation) to read as a button. Prefer <Button> whenever
 * the control actually performs an action.
 */
export function buttonClass({
  variant = "secondary",
  size = "md",
  className,
}: {
  variant?: ButtonVariant;
  size?: ButtonSize;
  className?: string;
} = {}): string {
  return cn(BASE, VARIANTS[variant], SIZES[size], className);
}

export type ButtonProps = Omit<React.ComponentProps<"button">, "type"> & {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /**
   * Defaults to "button": a bare <button> inside a <form> submits it, which is
   * almost never what a toolbar or menu button wants. Pass "submit" explicitly.
   */
  type?: "button" | "submit" | "reset";
};

export function Button({
  variant = "secondary",
  size = "md",
  type = "button",
  className,
  ...props
}: ButtonProps) {
  return <button type={type} className={buttonClass({ variant, size, className })} {...props} />;
}
