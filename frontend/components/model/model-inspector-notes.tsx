"use client";

import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";

/**
 * Trailing footnotes of the inspector: the "list was truncated" hint and any
 * parser warnings the backend attached to the metadata response.
 */
export function ModelInspectorNotes({
  truncated,
  shownTensorCount,
  warnings,
}: {
  truncated: boolean;
  shownTensorCount: number;
  warnings: string[];
}) {
  const t = useT();
  return (
    <>
      {truncated && (
        <p className="text-xs font-medium text-fg-subtle">
          {t("model.notes.truncated", { count: formatNumber(shownTensorCount) })}
        </p>
      )}
      {warnings.length > 0 && (
        <ul className="flex flex-col gap-0.5 text-xs font-medium text-fg-subtle">
          {warnings.map((w, i) => (
            <li
              // biome-ignore lint/suspicious/noArrayIndexKey: warning strings are not unique and the list is never reordered
              key={i}
            >
              {w}
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
