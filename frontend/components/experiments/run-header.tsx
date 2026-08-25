"use client";

import { Archive, ArchiveRestore, Star, Tag } from "lucide-react";
import { RunColorDot } from "@/components/experiments/run-color-dot";
import { RunStatusBadge } from "@/components/experiments/run-status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SpinnerSlot } from "@/components/ui/spinner";
import { TimeText } from "@/components/ui/time-text";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { ExpRun } from "@/types/api";

/**
 * The top of a run's page: which run this is and what colour it draws in, how
 * it is doing, the handful of facts that identify it, its tags, and the
 * annotation controls for a viewer who can write.
 *
 * The controls sit *below* the metadata rather than beside the title so the
 * `SpinnerSlot` that reports a save in flight cannot move them (DESIGN.md §8),
 * and the slot reserves its width whether or not it is spinning.
 */
export function RunHeader({
  run,
  colorIndex,
  canWrite,
  saving,
  onToggleBaseline,
  onEditTags,
  onToggleArchived,
}: {
  run: ExpRun;
  /** `runColorIndex(runOrder)`: the same colour the dashboard gave this run. */
  colorIndex: ReadonlyMap<string, number>;
  /** Viewer has write access to the backing dataset repository. */
  canWrite: boolean;
  /** An annotation write is in flight; every control is disabled meanwhile. */
  saving: boolean;
  onToggleBaseline: () => void;
  onEditTags: () => void;
  onToggleArchived: () => void;
}) {
  const t = useT();

  return (
    <header className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <RunColorDot run={run.name} colorIndex={colorIndex} size="md" />
        <h1 className="text-2xl font-semibold tracking-tight break-all">{run.name}</h1>
        <RunStatusBadge status={run.status} updatedAt={run.updated_at} />
        {run.is_baseline && <Badge tone="accent">{t("experiments.table.baselineBadge")}</Badge>}
        {run.archived && <Badge>{t("experiments.table.archivedBadge")}</Badge>}
      </div>

      <dl className="flex flex-wrap items-center gap-x-6 gap-y-1 text-sm text-fg-subtle">
        <div className="flex items-center gap-1.5">
          <dt>{t("experiments.table.colStarted")}</dt>
          <dd className="text-fg-muted">
            <TimeText iso={run.started_at} style="dateTime" />
          </dd>
        </div>
        <div className="flex items-center gap-1.5">
          <dt>{t("experiments.run.updated")}</dt>
          <dd className="text-fg-muted">
            <TimeText iso={run.updated_at} style="dateTime" />
          </dd>
        </div>
        <div className="flex items-center gap-1.5">
          <dt>{t("experiments.table.colLastStep")}</dt>
          <dd className="tabular-nums text-fg-muted">{formatNumber(run.last_step)}</dd>
        </div>
        <div className="flex items-center gap-1.5">
          <dt>{t("experiments.run.points")}</dt>
          <dd className="tabular-nums text-fg-muted">{formatNumber(run.num_points)}</dd>
        </div>
        {run.group && (
          <div className="flex items-center gap-1.5">
            <dt>{t("experiments.run.group")}</dt>
            <dd className="text-fg-muted">{run.group}</dd>
          </div>
        )}
        {run.job_type && (
          <div className="flex items-center gap-1.5">
            <dt>{t("experiments.run.jobType")}</dt>
            <dd className="text-fg-muted">{run.job_type}</dd>
          </div>
        )}
      </dl>

      <div className="flex flex-wrap items-center gap-2">
        {run.tags.length === 0 ? (
          <span className="text-sm text-fg-subtle">{t("experiments.run.noTags")}</span>
        ) : (
          run.tags.map((tag) => <Badge key={tag}>{tag}</Badge>)
        )}
        {canWrite && (
          <div className="flex flex-wrap items-center gap-1.5">
            <Button
              size="sm"
              variant="secondary"
              disabled={saving}
              aria-pressed={run.is_baseline}
              onClick={onToggleBaseline}
            >
              <Star size={14} className={run.is_baseline ? "text-accent" : undefined} />
              {run.is_baseline
                ? t("experiments.table.clearBaseline")
                : t("experiments.table.setBaseline")}
            </Button>
            <Button size="sm" variant="secondary" disabled={saving} onClick={onEditTags}>
              <Tag size={14} />
              {t("experiments.table.editTags")}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              disabled={saving}
              aria-pressed={run.archived}
              onClick={onToggleArchived}
            >
              {run.archived ? <ArchiveRestore size={14} /> : <Archive size={14} />}
              {run.archived ? t("experiments.table.unarchive") : t("experiments.table.archive")}
            </Button>
            <SpinnerSlot
              active={saving}
              size={14}
              label={t("experiments.dashboard.savingAnnotation")}
            />
          </div>
        )}
      </div>
    </header>
  );
}
