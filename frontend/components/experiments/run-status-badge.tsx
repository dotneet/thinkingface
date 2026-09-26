"use client";

import { CircleCheck, CircleDot, CircleX, type LucideIcon, TriangleAlert } from "lucide-react";

import { Badge, type BadgeTone } from "@/components/ui/badge";
import { TimeText } from "@/components/ui/time-text";
import { cn } from "@/lib/cn";
import type { Translator } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { RunStatus } from "@/types/api";
import { RunStatusFailed, RunStatusFinished, RunStatusStale } from "@/types/api";

/**
 * How a run's lifecycle state reads, in one place.
 *
 * The run table and the run detail page each used to spell the tone and the
 * label out inline, which is how a fourth state ("stale") could have been
 * added to the API and shown up as "running" on one of the two.
 */
export function statusTone(status: RunStatus): BadgeTone {
  switch (status) {
    case RunStatusFinished:
      return "positive";
    case RunStatusFailed:
      return "negative";
    // Stale is a warning, not an error: nothing is known to have failed, the
    // run has simply stopped saying anything.
    case RunStatusStale:
      return "warning";
    default:
      return "accent";
  }
}

export function statusLabel(t: Translator, status: RunStatus): string {
  switch (status) {
    case RunStatusFinished:
      return t("experiments.table.statusFinished");
    case RunStatusFailed:
      return t("experiments.table.statusFailed");
    case RunStatusStale:
      return t("experiments.table.statusStale");
    default:
      return t("experiments.table.statusRunning");
  }
}

const STATUS_ICONS: Record<RunStatus, LucideIcon> = {
  running: CircleDot,
  stale: TriangleAlert,
  failed: CircleX,
  finished: CircleCheck,
};

// The base tone tokens, not `-strong`: the icon sits on a neutral surface,
// never on a tinted fill of its own hue (DESIGN.md §1).
const STATUS_ICON_CLASS: Record<RunStatus, string> = {
  running: "text-accent",
  stale: "text-warning",
  failed: "text-negative",
  finished: "text-positive",
};

/**
 * A run status as a glyph — a distinct *shape* per state (dot / triangle / ×
 * / check), so it never relies on colour alone. For the compact places a
 * badge does not fit: the run picker, the listings' status counts.
 *
 * `label` false marks it decorative, for when the status word is printed next
 * to it anyway.
 */
export function RunStatusIcon({
  status,
  size = 14,
  label = true,
  className,
}: {
  status: RunStatus;
  size?: number;
  label?: boolean;
  className?: string;
}) {
  const t = useT();
  const Icon = STATUS_ICONS[status] ?? CircleDot;
  const text = statusLabel(t, status);
  return (
    <span
      className={cn("inline-flex shrink-0", STATUS_ICON_CLASS[status], className)}
      {...(label ? { role: "img", "aria-label": text, title: text } : { "aria-hidden": true })}
    >
      <Icon size={size} strokeWidth={2} />
    </span>
  );
}

/**
 * A run's status badge, plus — for a stale run — how long ago it was last
 * heard from.
 *
 * The elapsed time is the whole point of the stale state: "stale" on its own
 * says a job stopped reporting, but only "last seen 3 days ago" tells the
 * reader whether to wait for it or go and look at the cluster.
 */
export function RunStatusBadge({
  status,
  updatedAt,
  showLastSeen = true,
}: {
  status: RunStatus;
  /** The run's `updated_at`, i.e. when it was last heard from. */
  updatedAt: string;
  /**
   * False where the caller shows "last seen" itself — the run header does,
   * for running runs too, next to the staleness rule.
   */
  showLastSeen?: boolean;
}) {
  const t = useT();
  return (
    <span className="flex flex-col items-start gap-1">
      <Badge tone={statusTone(status)}>{statusLabel(t, status)}</Badge>
      {showLastSeen && status === RunStatusStale && (
        <span
          className="whitespace-nowrap text-xs font-medium text-fg-subtle"
          title={t("experiments.table.staleHint")}
        >
          {t("experiments.table.lastSeen")} <TimeText iso={updatedAt} style="relative" />
        </span>
      )}
    </span>
  );
}
