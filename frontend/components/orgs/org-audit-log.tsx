"use client";

import { ScrollText } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { listAuditLog } from "@/lib/orgs";
import type { OrgAuditEntry } from "@/types/api";

const PAGE_SIZE = 50;

/**
 * Cursor-paged audit log (§5). Explicitly a "Load more" button rather than an
 * infinite scroll: this is a page people read backwards looking for one event,
 * and a scroll listener that keeps appending makes the browser's own find
 * useless.
 *
 * `action` and `target` are identifiers the server writes (`member.added`,
 * `alice`, `team/foo`), so they are shown verbatim — DESIGN.md §7 keeps
 * identifiers out of the dictionary.
 */
export function OrgAuditLog({ org }: { org: string }) {
  const t = useT();
  const [entries, setEntries] = useState<OrgAuditEntry[] | null>(null);
  const [nextBefore, setNextBefore] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const result = await listAuditLog(org, { limit: PAGE_SIZE });
      if (cancelled) return;
      if (!result.ok) {
        setError(errorMessage(t, result));
        setEntries(null);
        return;
      }
      setError(null);
      setEntries(result.data.items);
      setNextBefore(result.data.next_before);
    })();
    return () => {
      cancelled = true;
    };
  }, [org]);

  async function loadMore() {
    if (!nextBefore) return;
    setLoadingMore(true);
    const result = await listAuditLog(org, { before: nextBefore, limit: PAGE_SIZE });
    setLoadingMore(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    setEntries((prev) => [...(prev ?? []), ...result.data.items]);
    setNextBefore(result.data.next_before);
  }

  if (entries === null && !error) return <SkeletonLines lines={6} />;
  if (entries === null) {
    return (
      <ErrorState
        title={t("org.settings.auditLog.loadFailed")}
        message={error ?? t("org.settings.auditLog.loadFailed")}
        hint={t("org.settings.auditLog.loadFailedHint")}
      />
    );
  }
  if (entries.length === 0) {
    return (
      <EmptyState
        icon={ScrollText}
        title={t("org.settings.auditLog.emptyTitle")}
        description={t("org.settings.auditLog.emptyDescription")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="scroll-x rounded-lg border border-border">
        <table className="w-full min-w-[560px] border-collapse text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
              <th className="px-3 py-2 font-medium">{t("org.settings.auditLog.colWhen")}</th>
              <th className="px-3 py-2 font-medium">{t("org.settings.auditLog.colActor")}</th>
              <th className="px-3 py-2 font-medium">{t("org.settings.auditLog.colAction")}</th>
              <th className="px-3 py-2 font-medium">{t("org.settings.auditLog.colTarget")}</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => (
              <tr key={entry.id} className="border-b border-border last:border-0">
                <td className="whitespace-nowrap px-3 py-2 tabular-nums text-fg-subtle">
                  <TimeText iso={entry.created_at} style="dateTime" />
                </td>
                <td className="px-3 py-2 text-fg-muted">
                  {entry.actor || t("org.settings.auditLog.unknownActor")}
                </td>
                <td className="px-3 py-2 font-mono text-xs text-fg">{entry.action}</td>
                <td className="px-3 py-2 font-mono text-xs text-fg-muted">{entry.target}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {error && <ErrorState title={t("org.settings.auditLog.loadFailed")} message={error} />}

      {nextBefore > 0 && (
        <Button onClick={loadMore} disabled={loadingMore} className="self-center">
          {loadingMore
            ? t("org.settings.auditLog.loadingMore")
            : t("org.settings.auditLog.loadMore")}
        </Button>
      )}
    </div>
  );
}
