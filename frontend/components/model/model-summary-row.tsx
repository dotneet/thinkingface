"use client";

import { formatBytes, formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";

// Parameter counts read as "7.24B (7,241,732,096)": the short form is what
// people compare models by, the exact one is what they cite. formatCompactNumber
// rounds harder (7.2B), which drops the digit that distinguishes two models.
// Small counts get the exact form only -- "48 (48)" helps nobody.
export function formatParameters(n: number): string {
  if (!Number.isFinite(n)) return "-";
  const exact = formatNumber(n);
  if (n < 100_000) return exact;
  const compact = new Intl.NumberFormat("en-US", {
    notation: "compact",
    maximumFractionDigits: 2,
  }).format(n);
  return `${compact} (${exact})`;
}

export function SummaryRow({
  format,
  numParameters,
  numTensors,
  size,
}: {
  format: string;
  numParameters: number;
  numTensors: number;
  size: number;
}) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border border-border bg-bg-raised px-4 py-3 text-sm">
      <SummaryItem
        label={t("model.summary.format")}
        value={format === "safetensors" ? "safetensors" : "PyTorch"}
      />
      <SummaryItem label={t("model.summary.parameters")} value={formatParameters(numParameters)} />
      <SummaryItem label={t("model.summary.tensors")} value={formatNumber(numTensors)} />
      <SummaryItem label={t("model.summary.fileSize")} value={formatBytes(size)} />
    </div>
  );
}

function SummaryItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">{label}</span>
      <span className="font-medium tabular-nums text-fg">{value}</span>
    </div>
  );
}
