"use client";

import { X } from "lucide-react";
import { useEffect, useRef } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";

/**
 * Thin wrapper over the native <dialog> element. The browser gives us the
 * focus trap, the inert background, the top layer, and Escape-to-close for
 * free, so this only has to keep the element in sync with the `open` prop and
 * forward the close intents back to the caller.
 */
export function Dialog({
  open,
  onClose,
  title,
  headerAction,
  footer,
  footerNote,
  children,
  className,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  /** Extra controls rendered next to the close button, e.g. a copy button. */
  headerAction?: React.ReactNode;
  /**
   * Pinned action row rendered below the scrolling body, separated by a top
   * border — put Cancel/Confirm (or any submit) buttons here, never in the
   * body. An error `Alert` that appears in the body pushes down anything
   * below it, so a button placed there moves out from under the pointer
   * right when the user is about to click it again (see DESIGN.md §8).
   * Omit for a dialog with no actions; the body then fills the full height,
   * same as before this prop existed.
   */
  footer?: React.ReactNode;
  /**
   * Message rendered *below* the action row — an error the action produced,
   * typically. Below, so the buttons it explains never move: everything above
   * it keeps its position and only the panel's bottom edge grows.
   */
  footerNote?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  const t = useT();
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);

  // Callers commonly mount this conditionally ({open && <Dialog open …/>}), so
  // the element can be removed from the DOM while still open. Removal drops the
  // top-layer entry but does not restore focus — only close() does — which
  // leaves focus on <body> and sends the next Tab back to the top of the page.
  // Closing on unmount hands focus back to whatever opened the dialog.
  useEffect(() => {
    const el = ref.current;
    return () => {
      if (el?.open) el.close();
    };
  }, []);

  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: Escape is handled by onCancel and the header has a focusable Close button
    <dialog
      ref={ref}
      aria-label={title}
      // `cancel` fires for Escape; without preventDefault the element closes
      // itself and the `open` prop would drift out of sync with the DOM.
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        // A click that lands on the <dialog> itself (rather than on the panel
        // inside it) is a backdrop click.
        if (e.target === e.currentTarget) onClose();
      }}
      className={cn(
        // Anchored near the top rather than `m-auto`: a vertically centred
        // panel re-centres itself whenever its content grows, so an error
        // appearing inside slid the footer buttons down by half the new
        // height — the very thing the pinned footer exists to prevent
        // (DESIGN.md §8). Anchored, growth only extends the bottom edge, and
        // `footerNote` keeps that growth below the action row.
        "mx-auto my-[8vh] w-full max-w-2xl rounded-lg border border-border bg-bg-raised p-0 text-fg shadow-xl backdrop:bg-black/40",
        className,
      )}
    >
      <div className="flex max-h-[70vh] flex-col">
        <div className="flex shrink-0 items-center justify-between gap-2 border-b border-border px-4 py-2.5">
          <span className="text-sm font-medium">{title}</span>
          <div className="flex items-center gap-2">
            {headerAction}
            <Button variant="ghost" size="sm" onClick={onClose} aria-label={t("ui.close")}>
              <X size={16} />
            </Button>
          </div>
        </div>
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">{children}</div>
        {footer && (
          <div className="flex shrink-0 items-center justify-end gap-2 border-t border-border px-4 py-2.5">
            {footer}
          </div>
        )}
      </div>
      {/* Outside the max-height box on purpose: inside it, a note appearing on
          an already-tall dialog would take its height out of the scrolling
          body and pull the footer *up*. Out here it only extends the panel
          downward, and caps itself so a long message cannot run off-screen. */}
      {footerNote && (
        <div className="max-h-[14vh] shrink-0 overflow-y-auto px-4 pb-4">{footerNote}</div>
      )}
    </dialog>
  );
}
