"use client";

import { CircleX, TriangleAlert } from "lucide-react";

import { statusLabel, statusTone } from "@/components/experiments/run-status-badge";
import { Badge } from "@/components/ui/badge";
import { TimeText } from "@/components/ui/time-text";
import { cn } from "@/lib/cn";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { RunStatus } from "@/types/api";
import { RunStatusFailed, RunStatusRunning, RunStatusStale } from "@/types/api";

const ICON_LABEL: Record<RunStatus, MessageKey> = {
  running: "experiments.sidebar.statusIconRunning",
  stale: "experiments.sidebar.statusIconStale",
  failed: "experiments.sidebar.statusIconFailed",
  finished: "experiments.sidebar.statusIconFinished",
};

/**
 * A run's status as one 14px glyph, for a list row with no room for a badge:
 * a pulsing dot while it runs, a warning triangle once it has gone quiet, a
 * cross when it failed — shape and colour both, never colour alone. A finished
 * run, the common case, draws nothing but keeps the slot (and a label for a
 * screen reader), so the value column after it never moves.
 */
export function RunStatusIcon({ status, className }: { status: RunStatus; className?: string }) {
  const t = useT();
  const label = t(ICON_LABEL[status]);
  return (
    <span
      role="img"
      aria-label={label}
      title={status === "finished" ? undefined : label}
      className={cn("inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center", className)}
    >
      {status === RunStatusRunning && (
        <span className="relative flex h-2 w-2">
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-accent opacity-60 motion-reduce:animate-none" />
          <span className="relative inline-flex h-2 w-2 rounded-full bg-accent" />
        </span>
      )}
      {status === RunStatusStale && <TriangleAlert size={13} className="text-warning" />}
      {status === RunStatusFailed && <CircleX size={13} className="text-negative" />}
    </span>
  );
}

/**
 * A run's status badge on a single line, for a table row. The stale run's
 * "last seen" follows on the same line (the shared `RunStatusBadge` stacks it
 * underneath, which is what made the old table's rows two lines tall).
 */
export function RunStatusInline({ status, updatedAt }: { status: RunStatus; updatedAt: string }) {
  const t = useT();
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <Badge tone={statusTone(status)}>{statusLabel(t, status)}</Badge>
      {status === RunStatusStale && (
        <span
          className="text-xs font-medium text-fg-subtle"
          title={t("experiments.table.staleHint")}
        >
          <TimeText iso={updatedAt} style="relative" />
        </span>
      )}
    </span>
  );
}
