import { describe, expect, it } from "vitest";
import {
  type DialogDismissSource,
  dialogDismiss,
  isDialogBackdropClick,
} from "@/lib/dialog-dismiss";

const SOURCES: DialogDismissSource[] = ["escape", "backdrop", "closeButton"];

describe("dialogDismiss", () => {
  it("closes on every route when the dialog is idle", () => {
    for (const source of SOURCES) {
      expect(dialogDismiss(source, false).close, source).toBe(true);
    }
  });

  it("refuses every route while a confirmed action is in flight", () => {
    // The regression this guards: Escape / backdrop / × each closed the
    // dialog mid-request, and the failure that arrived afterwards was written
    // into dialog-local state nobody was rendering any more.
    for (const source of SOURCES) {
      expect(dialogDismiss(source, true).close, source).toBe(false);
    }
  });

  it("always swallows the browser's own Escape handling, busy or not", () => {
    // Without preventDefault the native <dialog> closes itself, so the DOM
    // would say closed while the caller's `open` prop still says open.
    expect(dialogDismiss("escape", false).preventDefault).toBe(true);
    expect(dialogDismiss("escape", true).preventDefault).toBe(true);
  });

  it("leaves clicks alone — there is no default to suppress", () => {
    for (const source of ["backdrop", "closeButton"] as const) {
      expect(dialogDismiss(source, false).preventDefault, source).toBe(false);
      expect(dialogDismiss(source, true).preventDefault, source).toBe(false);
    }
  });
});

describe("isDialogBackdropClick", () => {
  it("is a backdrop click only when the event target is the dialog itself", () => {
    const dialog = { id: "dialog" };
    const panel = { id: "panel" };
    expect(isDialogBackdropClick(dialog, dialog)).toBe(true);
    expect(isDialogBackdropClick(panel, dialog)).toBe(false);
  });

  it("does not treat a null target as the dialog", () => {
    const dialog = { id: "dialog" };
    expect(isDialogBackdropClick(null, dialog)).toBe(false);
  });
});
