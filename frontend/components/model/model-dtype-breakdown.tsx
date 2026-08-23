"use client";

import { formatBytes, formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { ModelDTypeStat } from "@/types/api";

export function DTypeBreakdown({ dtypes }: { dtypes: ModelDTypeStat[] }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-2">
      <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
        {t("model.dtypes.title")}
      </span>
      <div className="scroll-x overflow-x-auto rounded-lg border border-border">
        <table className="w-full min-w-max border-collapse text-xs">
          <thead className="bg-bg-sunken">
            <tr>
              <th
                scope="col"
                className="whitespace-nowrap border-b border-border px-3 py-2 text-left font-mono font-medium text-fg-muted"
              >
                {t("model.dtypes.colDtype")}
              </th>
              <th
                scope="col"
                className="whitespace-nowrap border-b border-border px-3 py-2 text-right font-medium text-fg-muted"
              >
                {t("model.dtypes.colTensors")}
              </th>
              <th
                scope="col"
                className="whitespace-nowrap border-b border-border px-3 py-2 text-right font-medium text-fg-muted"
              >
                {t("model.dtypes.colParameters")}
              </th>
              <th
                scope="col"
                className="whitespace-nowrap border-b border-border px-3 py-2 text-right font-medium text-fg-muted"
              >
                {t("model.dtypes.colSize")}
              </th>
            </tr>
          </thead>
          <tbody>
            {dtypes.map((d) => (
              <tr key={d.dtype} className="border-b border-border last:border-0">
                <td className="px-3 py-2">
                  <span className="inline-flex items-center rounded-full border border-transparent bg-accent-muted px-2 py-0.5 font-mono font-medium text-accent-strong">
                    {d.dtype}
                  </span>
                </td>
                <td className="px-3 py-2 text-right tabular-nums">{formatNumber(d.num_tensors)}</td>
                <td className="px-3 py-2 text-right tabular-nums">
                  {formatNumber(d.num_parameters)}
                </td>
                <td className="px-3 py-2 text-right tabular-nums">{formatBytes(d.size_bytes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
