"use client";

import { Braces, FileText } from "lucide-react";
import { useState } from "react";
import { CopyButton } from "@/components/ui/copy-button";
import { Dialog } from "@/components/ui/dialog";
import { JsonTree } from "@/components/ui/json-tree";
import { SegmentedControl } from "@/components/ui/segmented-control";
import {
  type CellFeature,
  imageSourceFor,
  jsonTreeValueFor,
  parseJsonValue,
  prettyJson,
  stringifyValue,
} from "@/lib/cell-value";
import { formatBytes } from "@/lib/format";
import { useT } from "@/lib/i18n/client";

/**
 * Expanded view of a single table cell: a full-size image, a JSON tree, or the
 * raw text. Which one is decided here rather than by the caller so every table
 * (Parquet rows, SQL results, CSV preview) expands a cell the same way.
 */
export function CellModal({
  value,
  feature,
  onClose,
}: {
  value: unknown;
  feature?: CellFeature;
  onClose: () => void;
}) {
  const t = useT();
  const [mode, setMode] = useState<"tree" | "raw">("tree");

  const image = feature === "image" ? imageSourceFor(value) : null;
  // The modal is more permissive than the cell: a plain string column that
  // holds a JSON document still gets the tree here (Raw is one click away),
  // whereas in the table only JSON-typed columns earn a click for it -- see
  // jsonTreeValueFor for the distinction.
  const json =
    image === null ? (jsonTreeValueFor(value, feature) ?? parseJsonValue(value)) : undefined;

  if (image !== null) {
    const alt = image.path ?? t("ui.cell.image");
    return (
      <Dialog
        open
        onClose={onClose}
        title={t("ui.cell.image")}
        headerAction={image.path ? <CopyButton value={image.path} /> : undefined}
      >
        <div className="flex flex-col items-center gap-3 p-4">
          {/* eslint-disable-next-line @next/next/no-img-element -- next/image cannot optimise a data: URL, which is what an inlined `datasets` Image column is. */}
          <img
            src={image.src}
            alt={alt}
            decoding="async"
            className="max-h-[70vh] max-w-full rounded border border-border object-contain"
          />
          <div className="flex flex-wrap items-center justify-center gap-3 font-mono text-xs font-medium text-fg-subtle">
            {image.path && <span className="break-all">{image.path}</span>}
            {image.bytes !== null && <span>{formatBytes(image.bytes)}</span>}
          </div>
        </div>
      </Dialog>
    );
  }

  if (json !== undefined) {
    const raw = prettyJson(json);
    return (
      <Dialog
        open
        onClose={onClose}
        title={t("ui.cellValue")}
        headerAction={<CopyButton value={raw} />}
      >
        <div className="sticky top-0 shrink-0 border-b border-border bg-bg-raised px-4 py-2">
          <SegmentedControl
            label={t("ui.cell.viewMode")}
            value={mode}
            onChange={setMode}
            options={[
              { value: "tree" as const, label: t("ui.cell.tree"), icon: Braces },
              { value: "raw" as const, label: t("ui.cell.raw"), icon: FileText },
            ]}
          />
        </div>
        <div className="scroll-x p-4">
          {mode === "tree" ? (
            <JsonTree value={json} />
          ) : (
            <pre className="whitespace-pre-wrap break-words font-mono text-xs leading-relaxed">
              {raw}
            </pre>
          )}
        </div>
      </Dialog>
    );
  }

  const text = stringifyValue(value);
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("ui.cellValue")}
      headerAction={<CopyButton value={text} />}
    >
      <pre className="scroll-x whitespace-pre-wrap break-words p-4 font-mono text-xs leading-relaxed">
        {text}
      </pre>
    </Dialog>
  );
}
