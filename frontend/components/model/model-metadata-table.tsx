"use client";

import { useT } from "@/lib/i18n/client";

const METADATA_VALUE_MAX_LEN = 120;

export function MetadataTable({ metadata }: { metadata: Record<string, string> }) {
  const t = useT();
  const entries = Object.entries(metadata);
  return (
    <div className="flex flex-col gap-2">
      <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
        {t("model.metadata.title")}
      </span>
      <div className="scroll-x overflow-x-auto rounded-lg border border-border">
        <table className="w-full min-w-max border-collapse text-xs">
          <tbody>
            {entries.map(([key, value]) => (
              <tr key={key} className="border-b border-border last:border-0">
                <td className="whitespace-nowrap px-3 py-2 align-top font-mono font-medium text-fg-muted">
                  {key}
                </td>
                <td className="max-w-[520px] px-3 py-2 align-top">
                  <MetadataValue value={value} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function MetadataValue({ value }: { value: string }) {
  if (value.length <= METADATA_VALUE_MAX_LEN) {
    return <span className="break-words font-mono">{value}</span>;
  }
  return (
    <details>
      <summary className="cursor-pointer break-words font-mono text-fg-subtle hover:text-fg">
        {value.slice(0, METADATA_VALUE_MAX_LEN)}…
      </summary>
      <pre className="scroll-x mt-1 max-h-64 overflow-y-auto whitespace-pre-wrap break-words rounded-md bg-bg-sunken p-2 font-mono">
        {value}
      </pre>
    </details>
  );
}
