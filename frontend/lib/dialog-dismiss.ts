/**
 * The close-guard decision behind `components/ui/dialog.tsx`.
 *
 * A `Dialog` can be dismissed three ways — Escape, a click on the backdrop,
 * and the header's × button — and all three used to fire `onClose`
 * unconditionally. Dismissing a dialog whose confirmed write is still in
 * flight is how a failure disappears: most call sites report the error into
 * dialog-local state, so a rejection that lands after the dialog closed is
 * rendered nowhere at all and the operation reads as "nothing happened".
 *
 * The decision lives here, framework-free, because `Dialog` itself needs a DOM
 * to exercise and the vitest setup is `environment: "node"` over `lib/` only.
 */

/** How the user asked for the dialog to close. */
export type DialogDismissSource =
  /** The Escape key, surfaced by <dialog> as its `cancel` event. */
  | "escape"
  /** A click that landed on the <dialog> element itself, outside the panel. */
  | "backdrop"
  /** The × in the dialog header. */
  | "closeButton";

export type DialogDismissDecision = {
  /** Whether to forward the intent to the caller's `onClose`. */
  close: boolean;
  /**
   * Whether the browser's own handling of the event must be suppressed.
   * Always true for Escape, **including while busy**: left alone, the native
   * <dialog> closes itself on `cancel`, so the element would be shut while
   * the caller's `open` prop still says it is open — the exact drift the
   * component's effect cannot repair, since it only acts on `open` changes.
   */
  preventDefault: boolean;
};

/**
 * Resolves one dismissal attempt. While `busy`, every route is refused: a
 * half-disabled dialog (Cancel greyed out, Escape still live) is worse than
 * none, because the one route left open is the one nobody tests.
 */
export function dialogDismiss(source: DialogDismissSource, busy: boolean): DialogDismissDecision {
  return { close: !busy, preventDefault: source === "escape" };
}

/**
 * True when a click on the dialog subtree was really a backdrop click. The
 * native <dialog> element's box covers the whole viewport, so a click outside
 * the panel still has the <dialog> as its target; a click on the panel targets
 * something inside it and must never close anything.
 */
export function isDialogBackdropClick(target: unknown, currentTarget: unknown): boolean {
  return target === currentTarget;
}
