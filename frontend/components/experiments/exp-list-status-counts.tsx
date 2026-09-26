"use client";

import { RunStatusIcon } from "@/components/experiments/run-status-badge";
import { statusCountList } from "@/lib/exp-list-status";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { RunStatus } from "@/types/api";

const LABELS: Record<RunStatus, MessageKey> = {
  running: "experiments.listPage.statusRunning",
  stale: "experiments.listPage.statusStale",
  failed: "experiments.listPage.statusFailed",
  finished: "experiments.listPage.statusFinished",
};

/**
 * "● 1 running · ⚠ 1 stale · ✕ 1 failed · ✓ 22 finished" — a project's runs
 * by status, attention first. Each count carries its status glyph *and* its
 * word, so it never relies on colour alone.
 */
export function ExpListStatusCounts({
  counts,
}: {
  counts: Partial<Record<RunStatus, number>> | null | undefined;
}) {
  const t = useT();
  const items = statusCountList(counts, true);
  if (items.length === 0) {
    return (
      <span className="text-xs font-medium text-fg-subtle">{t("experiments.listPage.noRuns")}</span>
    );
  }
  return (
    <ul className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs font-medium">
      {items.map(({ status, count }) => (
        <li
          key={status}
          className={
            status === "finished"
              ? "flex items-center gap-1 text-fg-subtle"
              : "flex items-center gap-1 text-fg-muted"
          }
        >
          <RunStatusIcon status={status} size={12} label={false} />
          <span className="tabular-nums">{t(LABELS[status], { count })}</span>
        </li>
      ))}
    </ul>
  );
}
