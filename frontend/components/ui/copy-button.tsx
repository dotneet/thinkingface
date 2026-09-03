"use client";

import { Check, Copy, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";

type CopyStatus = "idle" | "copied" | "failed";

/**
 * Copies `text` to the clipboard, falling back to the legacy
 * `document.execCommand("copy")` path when the async Clipboard API is
 * unavailable.
 *
 * `navigator.clipboard` is `undefined` outside a secure context, and that is
 * exactly what a self-hosted deployment reached over plain HTTP looks like
 * from the browser's perspective (`lib/paths.ts`'s `publicApiBase()` even
 * defaults to `http://localhost:8080`, so this is an expected shape, not an
 * edge case). Calling `.writeText` on `undefined` throws before the promise
 * chain even starts, which the old code swallowed silently. The
 * `execCommand` path needs no secure context and still works in that
 * situation.
 */
async function writeToClipboard(text: string): Promise<boolean> {
  if (typeof navigator !== "undefined" && navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Permission denied or similar — fall through to the legacy path
      // rather than giving up.
    }
  }
  if (typeof document === "undefined") return false;
  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  // Off-screen, not hidden — a `display:none`/`hidden` element cannot be
  // selected, and selection is what `execCommand("copy")` copies.
  textarea.style.position = "fixed";
  textarea.style.top = "-1000px";
  textarea.style.left = "-1000px";
  document.body.appendChild(textarea);
  textarea.select();
  textarea.setSelectionRange(0, textarea.value.length);
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  } finally {
    document.body.removeChild(textarea);
  }
  return ok;
}

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
  const [status, setStatus] = useState<CopyStatus>("idle");
  const resolvedLabel = label ?? t("ui.copy");
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // A timeout scheduled by a click has no owner once the component unmounts
  // (navigating away right after copying, say) — clear it on unmount so it
  // doesn't fire `setState` on a gone component.
  useEffect(() => {
    return () => clearTimeout(timeoutRef.current);
  }, []);

  async function handleCopy() {
    const text = typeof value === "function" ? value() : value;
    const ok = await writeToClipboard(text);
    clearTimeout(timeoutRef.current);
    setStatus(ok ? "copied" : "failed");
    // The failure stays up longer: "Copied" is a confirmation the user can
    // glance past, but a failure is the only chance they get to notice nothing
    // actually landed on the clipboard before they paste garbage elsewhere.
    timeoutRef.current = setTimeout(() => setStatus("idle"), ok ? 1500 : 4000);
  }

  // Not `unexpectedError.title`: nothing went wrong with the page, and
  // saying so sends the reader looking for a fault to fix. The usual cause is
  // that the browser exposes no clipboard API outside a secure context, so a
  // self-hosted instance on plain HTTP lands here every time -- what the
  // reader needs is the manual route, which is what this string gives them.
  const failedText = t("ui.copyFailed");
  const statusText = status === "copied" ? t("ui.copied") : status === "failed" ? failedText : "";

  return (
    <button
      onClick={handleCopy}
      disabled={disabled}
      title={iconOnly ? (status === "idle" ? resolvedLabel : statusText) : undefined}
      aria-label={iconOnly ? resolvedLabel : undefined}
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-md border font-medium transition-colors hover:bg-bg-hover disabled:pointer-events-none disabled:opacity-40",
        status === "failed"
          ? "border-negative/40 text-negative-strong"
          : "border-border text-fg-muted hover:text-fg",
        iconOnly ? "p-1.5" : "px-2 py-1 text-xs",
        className,
      )}
      type="button"
    >
      {status === "copied" ? (
        <Check size={12} className="text-positive" />
      ) : status === "failed" ? (
        <X size={12} className="text-negative-strong" />
      ) : (
        <Copy size={12} />
      )}
      {!iconOnly && (status === "idle" ? resolvedLabel : statusText)}
      {/* A button's accessible name changing (the label text above, in the
          non-iconOnly case) is not reliably announced on its own — and an
          iconOnly button never shows that text at all. This live region
          carries the outcome regardless of layout. "failed" is assertive
          like Alert's negative tone (ui/alert.tsx): the user asked for a
          copy and it silently did not happen. */}
      <span
        role={status === "failed" ? "alert" : "status"}
        aria-live={status === "failed" ? "assertive" : "polite"}
        className="sr-only"
      >
        {statusText}
      </span>
    </button>
  );
}
