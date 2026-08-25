"use client";

import { Badge, type BadgeTone } from "@/components/ui/badge";
import { TimeText } from "@/components/ui/time-text";
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
}: {
  status: RunStatus;
  /** The run's `updated_at`, i.e. when it was last heard from. */
  updatedAt: string;
}) {
  const t = useT();
  return (
    <span className="flex flex-col items-start gap-1">
      <Badge tone={statusTone(status)}>{statusLabel(t, status)}</Badge>
      {status === RunStatusStale && (
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
