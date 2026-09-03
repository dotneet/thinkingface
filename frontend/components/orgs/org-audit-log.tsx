"use client";

import { ScrollText } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
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
  // loadMore is triggered from a click handler rather than an effect, so it
  // needs its own unmount guard (the initial-load effect above gets one for
  // free from its cleanup's `cancelled` flag) — otherwise a slow request that
  // outlives navigating away still lands a setState on a gone component.
  const mountedRef = useRef(true);

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

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  async function loadMore() {
    if (!nextBefore) return;
    setLoadingMore(true);
    const result = await listAuditLog(org, { before: nextBefore, limit: PAGE_SIZE });
    if (!mountedRef.current) return;
    setLoadingMore(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    // Clear a previous "load more" failure — otherwise a retry that succeeds
    // still leaves the table sitting above a permanent ErrorState claiming
    // the log couldn't be read at all (DESIGN.md §9: a state that has since
    // resolved must not keep standing in for the current one).
    setError(null);
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
      <Table minWidth={560}>
        <THead>
          <Th>{t("org.settings.auditLog.colWhen")}</Th>
          <Th>{t("org.settings.auditLog.colActor")}</Th>
          <Th>{t("org.settings.auditLog.colAction")}</Th>
          <Th>{t("org.settings.auditLog.colTarget")}</Th>
        </THead>
        <TBody>
          {entries.map((entry) => (
            <Tr key={entry.id}>
              <Td className="whitespace-nowrap tabular-nums text-fg-subtle">
                <TimeText iso={entry.created_at} style="dateTime" />
              </Td>
              <Td className="text-fg-muted">
                {entry.actor || t("org.settings.auditLog.unknownActor")}
              </Td>
              <Td className="font-mono text-xs text-fg">{entry.action}</Td>
              <Td className="font-mono text-xs text-fg-muted">{entry.target}</Td>
            </Tr>
          ))}
        </TBody>
      </Table>

      {nextBefore > 0 && (
        <Button onClick={loadMore} disabled={loadingMore} className="self-center">
          {loadingMore
            ? t("org.settings.auditLog.loadingMore")
            : t("org.settings.auditLog.loadMore")}
        </Button>
      )}

      {/* Below the button rather than between the table and it (DESIGN.md
          §8.1): the table already loaded fine, so a "load more" failure is
          an inline Alert next to its own retry — not a full-screen
          ErrorState — and it must never appear above the button the user is
          about to click again. */}
      {error && <Alert tone="negative">{error}</Alert>}
    </div>
  );
}
