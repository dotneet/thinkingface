import { useEffect } from "react";

/**
 * Closes an open panel (menu, popover, mobile nav) on an outside click, or
 * on Escape from anywhere — a keyboard user with no pointer has no other way
 * to dismiss it, since these panels close on click-outside rather than on a
 * blur/focus-trap.
 *
 * Escape additionally returns focus to the trigger when focus was inside the
 * panel (the first focusable `button`/link inside `ref`, which every caller
 * renders before the panel itself) — otherwise closing leaves focus on a
 * menu item that's about to unmount, dropping it back to `<body>` and
 * sending the next Tab to the top of the page. Guarded on
 * `ref.contains(document.activeElement)` so an Escape press with focus
 * elsewhere on the page (a different panel, an unrelated input) never
 * steals it — every instance of this hook stays mounted and listening even
 * while its own panel is closed, so an ungated `.focus()` here would hijack
 * focus on any Escape press anywhere in the app.
 */
export function useOnClickOutside<T extends HTMLElement>(
  ref: React.RefObject<T | null>,
  handler: () => void,
) {
  useEffect(() => {
    function onPointerDown(event: MouseEvent) {
      const el = ref.current;
      if (!el || el.contains(event.target as Node)) return;
      handler();
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      const el = ref.current;
      const focusWasInside = !!el?.contains(document.activeElement);
      handler();
      if (focusWasInside) {
        el?.querySelector<HTMLElement>("button, a[href]")?.focus();
      }
    }
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [ref, handler]);
}
