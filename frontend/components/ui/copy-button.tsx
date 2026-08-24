"use client";

import { Check, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";

export function CopyButton({
  value,
  label,
  disabled,
  className,
  iconOnly = false,
}: {
  /**
   * The text to copy, or a thunk producing it. Pass a function when building
   * the string is expensive (serialising thousands of result rows to CSV, for
   * instance) so the work happens on click rather than on every render.
   */
  value: string | (() => string);
  label?: string;
  disabled?: boolean;
  className?: string;
  /**
   * Renders just the icon (no label text) in a tighter square button, for
   * inline use next to other controls (e.g. a breadcrumb-style nav row).
   * `label` still supplies the accessible name and the tooltip.
   */
  iconOnly?: boolean;
}) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const resolvedLabel = label ?? t("ui.copy");
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // A timeout scheduled by a click has no owner once the component unmounts
  // (navigating away right after copying, say) — clear it on unmount so it
  // doesn't fire `setState` on a gone component.
  useEffect(() => {
    return () => clearTimeout(timeoutRef.current);
  }, []);

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(typeof value === "function" ? value() : value);
      setCopied(true);
      clearTimeout(timeoutRef.current);
      timeoutRef.current = setTimeout(() => setCopied(false), 1500);
    } catch {
      // clipboard API unavailable; silently ignore
    }
  }

  return (
    <button
      onClick={handleCopy}
      disabled={disabled}
      title={iconOnly ? (copied ? t("ui.copied") : resolvedLabel) : undefined}
      aria-label={iconOnly ? resolvedLabel : undefined}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-md border border-border font-medium text-fg-muted transition-colors hover:bg-bg-hover hover:text-fg disabled:pointer-events-none disabled:opacity-40",
        iconOnly ? "p-1.5" : "px-2 py-1 text-xs",
        className,
      )}
      type="button"
    >
      {copied ? <Check size={12} className="text-positive" /> : <Copy size={12} />}
      {!iconOnly && (copied ? t("ui.copied") : resolvedLabel)}
    </button>
  );
}
