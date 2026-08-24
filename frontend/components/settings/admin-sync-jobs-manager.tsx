"use client";

import { CheckCircle2, RefreshCw, RotateCw } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
import type { SyncJob } from "@/lib/admin";
import { listFailedSyncJobs, retrySyncJob, syncJobErrorKey } from "@/lib/admin";
import type { FailedApiResult } from "@/lib/api-error-message";
import { errorMessage } from "@/lib/api-error-message";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";

/** The store's own default page size, restated so the two agree. */
const PAGE_SIZE = 50;

/**
 * The failed post-push sync jobs, and the one action that gets them moving
 * again (docs/dev/api-contract.md §1.3, "Failed sync jobs").
 *
 * A parked job is quiet: the repository still serves its previous push, so
 * only its file listing, search entry and blobs/ export are frozen. This
 * screen is the whole trace of that, and "Retry" is the whole recovery — it
 * used to be an UPDATE against the database by hand.
 *
 * Retrying answers 204 and the job leaves the listing, so the list is
 * re-read after every action rather than patched in place: the row's fate
 * afterwards belongs to the worker, not to this component's copy of it.
 */
export function AdminSyncJobsManager() {
  const t = useT();
  const [jobs, setJobs] = useState<SyncJob[] | null>(null);
  const [total, setTotal] = useState<number | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [offset, setOffset] = useState(0);

  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [reloading, setReloading] = useState(false);

  const describe = useCallback(
    (result: FailedApiResult) => {
      const key = syncJobErrorKey(result);
      return key ? t(key) : errorMessage(t, result);
    },
    [t],
  );

  const refresh = useCallback(
    async (isStale: () => boolean = () => false) => {
      const result = await listFailedSyncJobs({ limit: PAGE_SIZE, offset });
      if (isStale()) return;
      if (!result.ok) {
        setLoadError(describe(result));
        setJobs(null);
        // Never carry a count over from a failed read: an empty table next to
        // a stale total states something the page does not know (DESIGN.md §9).
        setTotal(null);
        return;
      }
      setLoadError(null);
      setJobs(result.data.items);
      setTotal(result.data.total);
    },
    [offset, describe],
  );

  // Guards against a fast page change letting an older, slower response land
  // after the newer one and overwrite it.
  useEffect(() => {
    let cancelled = false;
    refresh(() => cancelled);
    return () => {
      cancelled = true;
    };
  }, [refresh]);

  async function handleReload() {
    setReloading(true);
    setActionError(null);
    setNotice(null);
    await refresh();
    setReloading(false);
  }

  async function handleRetry(job: SyncJob) {
    setBusy(job.id);
    setActionError(null);
    setNotice(null);
    const result = await retrySyncJob(job.id);
    setBusy(null);
    if (!result.ok) {
      setActionError(describe(result));
      // A 404 means somebody else already dealt with it, so the listing on
      // screen is the stale part — re-read it rather than leaving a row that
      // no longer exists next to the error.
      if (result.status === 404) await refresh();
      return;
    }
    setNotice(t("settings.adminSyncJobs.retryDone", { repo: job.repo }));
    await refresh();
  }

  const hasPrev = offset > 0;
  const hasNext = total !== null && offset + PAGE_SIZE < total;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        {/* Only ever rendered from a successful read (DESIGN.md §9). */}
        {total !== null && (
          <span className="text-xs font-medium tabular-nums text-fg-subtle">
            {t(
              total === 1 ? "settings.adminSyncJobs.countOne" : "settings.adminSyncJobs.countOther",
              { count: formatNumber(total) },
            )}
          </span>
        )}
        <Button
          size="sm"
          onClick={handleReload}
          disabled={reloading || busy !== null}
          className="ml-auto"
        >
          <RefreshCw size={14} />
          {t("settings.adminSyncJobs.refresh")}
        </Button>
      </div>

      {actionError && <Alert tone="negative">{actionError}</Alert>}
      {notice && (
        <Alert tone="positive">
          {notice} {t("settings.adminSyncJobs.retryNote")}
        </Alert>
      )}

      {jobs === null && !loadError ? (
        <SkeletonLines lines={5} />
      ) : jobs === null ? (
        <ErrorState
          title={t("settings.adminSyncJobs.loadFailed")}
          message={loadError ?? t("settings.adminSyncJobs.loadFailed")}
          hint={t("settings.adminSyncJobs.loadFailedHint")}
        />
      ) : jobs.length === 0 ? (
        <EmptyState
          icon={CheckCircle2}
          title={t("settings.adminSyncJobs.emptyTitle")}
          description={t("settings.adminSyncJobs.emptyDescription")}
        />
      ) : (
        <div className="scroll-x rounded-lg border border-border">
          <table className="w-full min-w-[720px] border-collapse text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
                <th className="px-3 py-2 font-medium">{t("settings.adminSyncJobs.colRepo")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.adminSyncJobs.colRef")}</th>
                <th className="px-3 py-2 text-right font-medium">
                  {t("settings.adminSyncJobs.colAttempts")}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t("settings.adminSyncJobs.colLastError")}
                </th>
                <th className="px-3 py-2 font-medium">{t("settings.adminSyncJobs.colUpdated")}</th>
                <th className="px-3 py-2 text-right font-medium">
                  {t("settings.adminSyncJobs.colActions")}
                </th>
              </tr>
            </thead>
            <tbody>
              {jobs.map((job) => (
                <tr key={job.id} className="border-b border-border align-top last:border-0">
                  <td className="px-3 py-2">
                    <RepoLink repo={job.repo} />
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">{job.ref}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-fg-muted">
                    {job.attempts}
                  </td>
                  <td className="px-3 py-2">
                    {/* An error can be arbitrarily long, so it scrolls inside
                        its own cell instead of widening the table until the
                        page itself scrolls sideways (DESIGN.md §9). */}
                    <pre className="scroll-x max-w-[28rem] whitespace-pre font-mono text-xs leading-relaxed text-fg-muted">
                      {job.last_error}
                    </pre>
                  </td>
                  <td className="px-3 py-2 text-xs font-medium text-fg-subtle">
                    <TimeText iso={job.updated_at} style="dateTime" />
                  </td>
                  <td className="px-3 py-2">
                    <div className="flex justify-end">
                      <Button
                        size="sm"
                        variant="primary"
                        disabled={busy !== null}
                        onClick={() => void handleRetry(job)}
                      >
                        <RotateCw size={13} />
                        {busy === job.id
                          ? t("settings.adminSyncJobs.retrying")
                          : t("settings.adminSyncJobs.retry")}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(hasPrev || hasNext) && (
        <div className="flex items-center justify-between text-sm text-fg-subtle">
          <span className="tabular-nums">
            {t("ui.pagination.range", {
              from: offset + 1,
              to: Math.min(offset + PAGE_SIZE, total ?? 0),
              total: formatNumber(total ?? 0),
            })}
          </span>
          <div className="flex gap-2">
            <Button
              size="sm"
              disabled={!hasPrev}
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            >
              {t("ui.pagination.prev")}
            </Button>
            <Button size="sm" disabled={!hasNext} onClick={() => setOffset(offset + PAGE_SIZE)}>
              {t("ui.pagination.next")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * `repo` arrives as "datasets/acme/imdb-ja" — the API's kind segment plus the
 * namespace and name, which is exactly the web UI's own path for that
 * repository. Anything that is not those three segments (a name carrying a
 * slash could not exist, but the string is data from the server either way) is
 * rendered as plain text rather than turned into a link to nowhere.
 */
function RepoLink({ repo }: { repo: string }) {
  const t = useT();
  const parts = repo.split("/");
  if (parts.length !== 3 || parts.some((p) => p === "")) {
    return <span className="font-medium text-fg">{repo}</span>;
  }
  const href = `/${parts.map(encodeURIComponent).join("/")}`;
  return (
    <Link
      href={href}
      title={t("settings.adminSyncJobs.openRepo")}
      className="font-medium text-accent hover:underline"
    >
      {repo}
    </Link>
  );
}
