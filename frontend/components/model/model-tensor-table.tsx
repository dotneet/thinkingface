"use client";

import { useVirtualizer } from "@tanstack/react-virtual";
import { useMemo, useRef, useState } from "react";
import { FilterInput } from "@/components/ui/search-input";
import { formatBytes, formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { ModelTensor } from "@/types/api";

// Row height for the virtualized tensor table (matches parquet-viewer.tsx's
// convention of a fixed estimate for tabular rows).
const ROW_HEIGHT = 34;

function formatShape(shape: number[]): string {
  return `[${shape.join(", ")}]`;
}

export function TensorTable({ tensors }: { tensors: ModelTensor[] }) {
  const t = useT();
  const [filter, setFilter] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return tensors;
    return tensors.filter((t) => t.name.toLowerCase().includes(q));
  }, [tensors, filter]);

  const virtualizer = useVirtualizer({
    count: filtered.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 12,
  });

  const virtualRows = virtualizer.getVirtualItems();
  const paddingTop = virtualRows.length > 0 ? virtualRows[0]!.start : 0;
  const paddingBottom =
    virtualRows.length > 0
      ? virtualizer.getTotalSize() - virtualRows[virtualRows.length - 1]!.end
      : 0;

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
          {t("model.tensors.title")}
        </span>
        <FilterInput
          value={filter}
          onChange={setFilter}
          placeholder={t("model.tensors.filterPlaceholder")}
          className="w-56 text-xs"
        />
      </div>

      <span className="text-xs font-medium tabular-nums text-fg-subtle">
        {t("model.tensors.countSummary", {
          filtered: formatNumber(filtered.length),
          total: formatNumber(tensors.length),
        })}
      </span>

      {filtered.length === 0 ? (
        <div className="rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-fg-subtle">
          {t("model.tensors.noMatch", { filter })}
        </div>
      ) : (
        <div
          ref={scrollRef}
          className="scroll-x max-h-[60vh] overflow-y-auto rounded-lg border border-border"
        >
          <table className="w-full min-w-max border-collapse text-xs tabular-nums">
            <thead className="sticky top-0 z-10 bg-bg-sunken">
              <tr>
                <th
                  scope="col"
                  className="whitespace-nowrap border-b border-border px-3 py-2 text-left font-mono font-medium text-fg-muted"
                >
                  {t("model.tensors.colName")}
                </th>
                <th
                  scope="col"
                  className="whitespace-nowrap border-b border-border px-3 py-2 text-left font-medium text-fg-muted"
                >
                  {t("model.tensors.colDtype")}
                </th>
                <th
                  scope="col"
                  className="whitespace-nowrap border-b border-border px-3 py-2 text-left font-medium text-fg-muted"
                >
                  {t("model.tensors.colShape")}
                </th>
                <th
                  scope="col"
                  className="whitespace-nowrap border-b border-border px-3 py-2 text-right font-medium text-fg-muted"
                >
                  {t("model.tensors.colParameters")}
                </th>
                <th
                  scope="col"
                  className="whitespace-nowrap border-b border-border px-3 py-2 text-right font-medium text-fg-muted"
                >
                  {t("model.tensors.colSize")}
                </th>
              </tr>
            </thead>
            <tbody>
              {paddingTop > 0 && (
                <tr>
                  <td colSpan={5} style={{ height: paddingTop, padding: 0, border: 0 }} />
                </tr>
              )}
              {virtualRows.map((vRow) => {
                const tensor = filtered[vRow.index];
                if (!tensor) return null;
                return (
                  <tr
                    // Tensor names are normally unique, but a checkpoint that
                    // nests unnamed tensors can repeat one; the index keeps
                    // the rows distinct either way.
                    key={`${vRow.index}:${tensor.name}`}
                    className="border-b border-border hover:bg-bg-hover"
                  >
                    <td className="max-w-[360px] truncate px-3 py-2 font-mono" title={tensor.name}>
                      {tensor.name}
                    </td>
                    <td className="px-3 py-2 font-mono text-fg-muted">{tensor.dtype}</td>
                    <td className="px-3 py-2 font-mono text-fg-muted">
                      {formatShape(tensor.shape)}
                    </td>
                    <td className="px-3 py-2 text-right">{formatNumber(tensor.num_parameters)}</td>
                    <td className="px-3 py-2 text-right">{formatBytes(tensor.size_bytes)}</td>
                  </tr>
                );
              })}
              {paddingBottom > 0 && (
                <tr>
                  <td colSpan={5} style={{ height: paddingBottom, padding: 0, border: 0 }} />
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
