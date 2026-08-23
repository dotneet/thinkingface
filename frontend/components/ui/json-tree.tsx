"use client";

import { ChevronDown, ChevronRight } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n/client";

/**
 * Collapsible view of a parsed JSON value (a nested Parquet column, a JSON
 * string column, an experiment config …).
 *
 * Deliberately not a generic "object inspector": it renders only what
 * `JSON.parse` can produce plus BigInt, because that is all a table cell ever
 * holds. Everything below the fold stays collapsed — a single row of a
 * `datasets` config can carry thousands of keys, and mounting them all is what
 * makes naive tree views unusable.
 */
export function JsonTree({
  value,
  defaultDepth = 2,
  className,
}: {
  value: unknown;
  /** Levels expanded on first render; deeper nodes start collapsed. */
  defaultDepth?: number;
  className?: string;
}) {
  return (
    <div className={className}>
      <JsonNode value={value} depth={0} defaultDepth={defaultDepth} />
    </div>
  );
}

/** How many children of one container are mounted before "… N more". */
const PAGE_SIZE = 100;
/** Strings longer than this are clipped, with a control to reveal the rest. */
const MAX_STRING = 200;

type Entry = { key: string; value: unknown };

type Container = { kind: "object" | "array"; entries: Entry[] };

/** The children of a value, or null when it is a leaf. */
function containerOf(value: unknown): Container | null {
  if (Array.isArray(value)) {
    return { kind: "array", entries: value.map((v, i) => ({ key: String(i), value: v })) };
  }
  if (typeof value === "object" && value !== null) {
    return {
      kind: "object",
      entries: Object.entries(value as Record<string, unknown>).map(([key, v]) => ({
        key,
        value: v,
      })),
    };
  }
  return null;
}

function JsonNode({
  label,
  value,
  depth,
  defaultDepth,
}: {
  /** Object key or array index of this node; absent at the root. */
  label?: string;
  value: unknown;
  depth: number;
  defaultDepth: number;
}) {
  const t = useT();
  const container = containerOf(value);
  const [expanded, setExpanded] = useState(depth < defaultDepth);
  const [shown, setShown] = useState(PAGE_SIZE);

  if (container === null) {
    return (
      <div className="flex gap-1.5 py-0.5 font-mono text-xs leading-relaxed">
        {label !== undefined && <JsonKey label={label} />}
        <JsonLeaf value={value} />
      </div>
    );
  }

  const count = container.entries.length;
  const summary =
    container.kind === "array"
      ? t(count === 1 ? "ui.cell.itemsOne" : "ui.cell.itemsOther", { count })
      : t(count === 1 ? "ui.cell.keysOne" : "ui.cell.keysOther", { count });
  const brackets = container.kind === "array" ? "[…]" : "{…}";
  const visible = container.entries.slice(0, shown);
  const remaining = count - visible.length;

  return (
    <div className="font-mono text-xs leading-relaxed">
      <Button
        variant="ghost"
        size="sm"
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
        title={expanded ? t("ui.cell.collapse") : t("ui.cell.expand")}
        className="w-full justify-start gap-1.5 rounded-none px-0 py-0.5 font-mono text-xs font-normal hover:bg-transparent"
      >
        {expanded ? (
          <ChevronDown size={12} className="shrink-0 text-fg-subtle" />
        ) : (
          <ChevronRight size={12} className="shrink-0 text-fg-subtle" />
        )}
        {label !== undefined && <JsonKey label={label} />}
        <span className="text-fg-subtle">
          {brackets} {summary}
        </span>
      </Button>

      {expanded && count > 0 && (
        <div className="ml-[7px] border-l border-border pl-3">
          {visible.map((entry) => (
            <JsonNode
              key={entry.key}
              label={entry.key}
              value={entry.value}
              depth={depth + 1}
              defaultDepth={defaultDepth}
            />
          ))}
          {remaining > 0 && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShown((n) => n + PAGE_SIZE)}
              className="rounded-none px-0 py-0.5 font-mono text-xs font-normal hover:bg-transparent hover:underline"
            >
              {t("ui.cell.moreItems", { count: remaining })}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

function JsonKey({ label }: { label: string }) {
  return <span className="shrink-0 text-fg-muted">{label}:</span>;
}

function JsonLeaf({ value }: { value: unknown }) {
  const t = useT();
  const [full, setFull] = useState(false);

  if (value === null || value === undefined) {
    return <span className="text-fg-subtle">null</span>;
  }
  if (typeof value === "number" || typeof value === "bigint") {
    return <span className="text-accent">{value.toString()}</span>;
  }
  if (typeof value === "boolean") {
    return <span className="text-warning">{String(value)}</span>;
  }

  const text = typeof value === "string" ? value : String(value);
  if (text.length <= MAX_STRING || full) {
    return <span className="break-all text-positive">{JSON.stringify(text)}</span>;
  }
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => setFull(true)}
      title={t("ui.cell.showFullString")}
      className="block h-auto whitespace-normal rounded-none px-0 py-0 text-left font-mono text-xs font-normal text-positive hover:bg-transparent hover:underline"
    >
      <span className="break-all">{`"${text.slice(0, MAX_STRING)}…"`}</span>
    </Button>
  );
}
