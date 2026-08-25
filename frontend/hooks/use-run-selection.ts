import { useCallback, useState } from "react";

export type RunSelection = {
  selected: Set<string>;
  /** Adds the run when it is not selected, removes it when it is. */
  toggle: (name: string) => void;
  /** Selects or deselects a whole group at once. */
  toggleMany: (names: string[], select: boolean) => void;
  /**
   * "Select all" over `names`: deselects them when every one is already
   * selected, selects the lot otherwise. `names` is the *visible* set, so the
   * header checkbox acts on what is on screen and leaves the rest alone.
   */
  toggleAll: (names: string[]) => void;
  /** Drops one run, for a deletion the selection has no other way to hear about. */
  remove: (name: string) => void;
};

/**
 * Which runs are plotted.
 *
 * A `Set` in React state has to be copied to be updated, and doing that by
 * hand at each call site is where the four variants of "copy, mutate, return"
 * came from. All four updates live here instead, and every one of them
 * returns a new `Set` so a caller cannot accidentally mutate the state in
 * place.
 */
export function useRunSelection(initialNames: string[]): RunSelection {
  const [selected, setSelected] = useState<Set<string>>(() => new Set(initialNames));

  const update = useCallback((mutate: (next: Set<string>) => void) => {
    setSelected((prev) => {
      const next = new Set(prev);
      mutate(next);
      return next;
    });
  }, []);

  const toggle = useCallback(
    (name: string) =>
      update((next) => {
        if (next.has(name)) next.delete(name);
        else next.add(name);
      }),
    [update],
  );

  const toggleMany = useCallback(
    (names: string[], select: boolean) =>
      update((next) => {
        for (const name of names) {
          if (select) next.add(name);
          else next.delete(name);
        }
      }),
    [update],
  );

  const toggleAll = useCallback((names: string[]) => {
    setSelected((prev) => {
      // Read against the previous state rather than the render's snapshot, so
      // two clicks in the same tick cannot disagree about what "all" meant.
      const everySelected = names.every((name) => prev.has(name));
      const next = new Set(prev);
      for (const name of names) {
        if (everySelected) next.delete(name);
        else next.add(name);
      }
      return next;
    });
  }, []);

  const remove = useCallback((name: string) => update((next) => next.delete(name)), [update]);

  return { selected, toggle, toggleMany, toggleAll, remove };
}
