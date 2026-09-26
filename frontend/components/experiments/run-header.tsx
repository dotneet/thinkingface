"use client";

import { Info, Star } from "lucide-react";

import { RunColorDot } from "@/components/experiments/run-color-dot";
import { RunStatusBadge } from "@/components/experiments/run-status-badge";
import { Badge } from "@/components/ui/badge";
import { TimeText } from "@/components/ui/time-text";
import { durationParts, showsLastSeen, staleWindowSecs } from "@/lib/exp-liveness";
import { formatNumber } from "@/lib/format";
import type { MessageKey, Translator } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { ExpRun } from "@/types/api";

const DURATION_KEYS: Record<ReturnType<typeof durationParts>["unit"], MessageKey> = {
  seconds: "experiments.liveness.seconds",
  minutes: "experiments.liveness.minutes",
  hours: "experiments.liveness.hours",
};

function durationText(t: Translator, secs: number): string {
  const { unit, count } = durationParts(secs);
  return t(DURATION_KEYS[unit], { count });
}

/**
 * How a silent run becomes stale, in words: the heartbeat it declared and the
 * window that follows from it, or the 30-minute fallback for a client that
 * declared none (lib/exp-liveness.ts mirrors the server's rule).
 */
function stalenessHint(t: Translator, heartbeatSecs: number): string {
  const window = durationText(t, staleWindowSecs(heartbeatSecs));
  return heartbeatSecs > 0
    ? t("experiments.liveness.heartbeatHint", {
        interval: durationText(t, heartbeatSecs),
        window,
      })
    : t("experiments.liveness.noHeartbeatHint", { window });
}

/**
 * The top of a run's page: which run this is and what colour it draws in, its
 * status, the toolbar, and one line of facts.
 *
 * The facts line never wraps — a header that grows a second or third line as
 * tags and groups appear pushes everything below it around. On a narrow screen
 * it scrolls sideways instead. Times are relative (the absolute time is the
 * tooltip), because "3 minutes ago" is what tells a live run from a dead one.
 */
export function RunHeader({
  run,
  colorIndex,
  actions,
}: {
  run: ExpRun;
  /** `runColorIndex(runOrder)`: the same colour the dashboard gave this run. */
  colorIndex: ReadonlyMap<string, number>;
  /** The toolbar (RunPageActions), laid out to the right of the title. */
  actions: React.ReactNode;
}) {
  const t = useT();
  const live = showsLastSeen(run.status);

  return (
    <header className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 items-center gap-2">
          <RunColorDot run={run.name} colorIndex={colorIndex} size="md" />
          <h1 className="truncate text-2xl font-semibold tracking-tight" title={run.name}>
            {run.name}
          </h1>
          <RunStatusBadge status={run.status} updatedAt={run.updated_at} showLastSeen={false} />
          {run.is_baseline && (
            <Badge tone="accent">
              <Star size={12} className="fill-current" />
              {t("experiments.table.baselineBadge")}
            </Badge>
          )}
          {run.archived && <Badge>{t("experiments.table.archivedBadge")}</Badge>}
        </div>
        {actions}
      </div>

      <dl className="scroll-x flex items-center gap-x-5 text-sm whitespace-nowrap text-fg-subtle">
        <Fact label={t("experiments.runPage.started")}>
          <TimeText iso={run.started_at} style="relative" />
        </Fact>
        {/* A run still recorded as running shows when it was last heard from,
            next to the rule that turns silence into "stale"; any other run
            just shows its last update. */}
        {live ? (
          <Fact label={t("experiments.liveness.lastSeen")}>
            <span className="inline-flex items-center gap-1">
              <TimeText iso={run.updated_at} style="relative" />
              <span
                role="img"
                aria-label={stalenessHint(t, run.heartbeat_secs ?? 0)}
                title={stalenessHint(t, run.heartbeat_secs ?? 0)}
                className="inline-flex"
              >
                <Info size={14} className="text-fg-subtle" />
              </span>
            </span>
          </Fact>
        ) : (
          <Fact label={t("experiments.run.updated")}>
            <TimeText iso={run.updated_at} style="relative" />
          </Fact>
        )}
        <Fact label={t("experiments.runPage.step")}>
          <span className="tabular-nums">{formatNumber(run.last_step)}</span>
        </Fact>
        <Fact label={t("experiments.run.points")}>
          <span className="tabular-nums">{formatNumber(run.num_points)}</span>
        </Fact>
        {run.group && <Fact label={t("experiments.run.group")}>{run.group}</Fact>}
        {run.job_type && <Fact label={t("experiments.run.jobType")}>{run.job_type}</Fact>}
        {run.tags.length > 0 && (
          <Fact label={t("experiments.runPage.tags")}>
            <span className="inline-flex items-center gap-1">
              {run.tags.map((tag) => (
                <Badge key={tag}>{tag}</Badge>
              ))}
            </span>
          </Fact>
        )}
      </dl>
    </header>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex shrink-0 items-center gap-1.5">
      <dt>{label}</dt>
      <dd className="text-fg-muted">{children}</dd>
    </div>
  );
}
