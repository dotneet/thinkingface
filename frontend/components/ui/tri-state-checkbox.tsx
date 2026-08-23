"use client";

import { Checkbox } from "@/components/ui/field";

/**
 * A {@link Checkbox} that can also show "some, but not all" — what a
 * select-all box needs when part of the list is selected. Without it, partial
 * and empty selections look identical.
 *
 * Its own client module rather than a prop on `Checkbox`: `indeterminate` is a
 * DOM property with no matching attribute, so it can only be set through a
 * ref, and `ui/field.tsx` has no `"use client"` — it renders inside Server
 * Components, where a ref is a hard error.
 */
export function TriStateCheckbox({
  indeterminate,
  ...props
}: React.ComponentProps<"input"> & { indeterminate: boolean }) {
  return (
    <Checkbox
      ref={(el: HTMLInputElement | null) => {
        if (el) el.indeterminate = indeterminate;
      }}
      {...props}
    />
  );
}
