"use client";

import { FileText, Inbox, RotateCw } from "lucide-react";
import { useCallback, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { OutOfRangeEmptyState, PaginationControls } from "@/components/ui/pagination-controls";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
import { usePagedList } from "@/hooks/use-paged-list";
import type { FailedApiResult } from "@/lib/api-error-message";
import { errorMessage } from "@/lib/api-error-message";
import type { MessageKey, Translator } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { listWebhookDeliveries, redeliverWebhook } from "@/lib/webhooks";
import type { WebhookDelivery, WebhookDeliveryStatus } from "@/types/api";

const STATUS_TONE: Record<WebhookDeliveryStatus, BadgeTone> = {
  pending: "warning",
  success: "positive",
  failed: "negative",
};

// WebhookDeliveryStatus is an open string type; translate the known states and
// show anything else verbatim.
const STATUS_KEY: Record<string, MessageKey> = {
  pending: "settings.deliveries.statusPending",
  success: "settings.deliveries.statusSuccess",
  failed: "settings.deliveries.statusFailed",
};

function statusLabel(t: Translator, status: WebhookDeliveryStatus): string {
  const key = STATUS_KEY[status];
  return key ? t(key) : status;
}

const PAGE_SIZE = 20;

export function WebhookDeliveriesPanel({ webhookId }: { webhookId: number }) {
  const t = useT();
  const [actionError, setActionError] = useState<string | null>(null);
  const [redeliveringId, setRedeliveringId] = useState<number | null>(null);
  // The delivery whose response is open in the detail dialog. The row itself
  // is held (not just its id) so a background refresh that drops the row from
  // the current page cannot blank out a dialog the user is reading.
  const [detail, setDetail] = useState<WebhookDelivery | null>(null);

  const describe = useCallback((result: FailedApiResult) => errorMessage(t, result), [t]);

  const {
    items: deliveries,
    offset,
    setOffset,
    loadError,
    reload: refresh,
    outOfRange,
    pager,
  } = usePagedList({
    pageSize: PAGE_SIZE,
    deps: [webhookId],
    fetchPage: ({ limit, offset }) => listWebhookDeliveries(webhookId, { limit, offset }),
    describe,
  });

  // A different webhook starts back on its first page rather than wherever
  // this one happened to be paged to. Adjusted during render rather than in an
  // effect: an effect would run after this render's fetch had already been
  // scheduled for the old offset, so switching webhooks from page 2 would fire
  // a request for page 2 of the new one and immediately abandon it.
  const [shownWebhookId, setShownWebhookId] = useState(webhookId);
  if (webhookId !== shownWebhookId) {
    setShownWebhookId(webhookId);
    setOffset(0);
  }

  async function handleRedeliver(deliveryId: number) {
    if (redeliveringId !== null) return;
    setRedeliveringId(deliveryId);
    setActionError(null);
    const result = await redeliverWebhook(webhookId, deliveryId);
    setRedeliveringId(null);
    if (!result.ok) {
      setActionError(errorMessage(t, result));
      return;
    }
    // Jump back to the first page so the just-redelivered attempt is visible.
    // If we're already there, offset won't change (and so won't re-trigger
    // the effect above), so fetch directly.
    if (offset === 0) {
      await refresh();
    } else {
      setOffset(0);
    }
  }

  return (
    <div className="flex flex-col gap-3">
      {actionError && <Alert tone="negative">{actionError}</Alert>}
      {deliveries === null && !loadError ? (
        <SkeletonLines lines={3} />
      ) : deliveries === null ? (
        <ErrorState
          title={t("settings.errorTitle")}
          message={loadError ?? t("settings.deliveries.loadFailed")}
        />
      ) : deliveries.length === 0 ? (
        outOfRange ? (
          <OutOfRangeEmptyState onBackToFirstPage={() => setOffset(0)} />
        ) : (
          <EmptyState
            icon={Inbox}
            title={t("settings.deliveries.emptyTitle")}
            description={t("settings.deliveries.emptyDescription")}
          />
        )
      ) : (
        <div className="scroll-x rounded-md border border-border">
          <table className="w-full min-w-[620px] border-collapse text-xs">
            <thead>
              <tr className="border-b border-border text-left text-fg-subtle">
                <th className="px-2.5 py-1.5 font-medium">{t("settings.deliveries.colEvent")}</th>
                <th className="px-2.5 py-1.5 font-medium">{t("settings.deliveries.colStatus")}</th>
                <th className="px-2.5 py-1.5 font-medium">
                  {t("settings.deliveries.colResponse")}
                </th>
                <th className="px-2.5 py-1.5 font-medium">
                  {t("settings.deliveries.colAttempts")}
                </th>
                <th className="px-2.5 py-1.5 font-medium">
                  {t("settings.deliveries.colLastAttempt")}
                </th>
                <th className="px-2.5 py-1.5"></th>
              </tr>
            </thead>
            <tbody>
              {deliveries.map((d) => (
                <tr key={d.id} className="border-b border-border last:border-0">
                  <td className="px-2.5 py-1.5 font-mono">{d.event}</td>
                  <td className="px-2.5 py-1.5">
                    <Badge tone={STATUS_TONE[d.status] ?? "neutral"}>
                      {statusLabel(t, d.status)}
                    </Badge>
                  </td>
                  <td className="px-2.5 py-1.5">
                    <div className="flex items-center gap-2">
                      <span className="tabular-nums text-fg-muted">{d.response_status ?? "-"}</span>
                      {/* A dialog rather than an expanding detail row: the
                          response body is arbitrary text of arbitrary length,
                          and inserting it into the table would push every row
                          below it — including their Redeliver buttons — down by
                          an unpredictable amount (DESIGN.md §8). The dialog
                          leaves the table exactly where it was. */}
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={t("settingsDetail.deliveries.viewResponseAria")}
                        onClick={() => setDetail(d)}
                      >
                        <FileText size={11} />
                        {t("settingsDetail.deliveries.viewResponse")}
                      </Button>
                    </div>
                  </td>
                  <td className="px-2.5 py-1.5 text-fg-muted">{d.attempts}</td>
                  <td className="px-2.5 py-1.5 text-fg-subtle">
                    <TimeText iso={d.last_attempt_at} style="dateTime" />
                  </td>
                  <td className="px-2.5 py-1.5 text-right">
                    <Button
                      size="sm"
                      disabled={redeliveringId !== null}
                      onClick={() => handleRedeliver(d.id)}
                    >
                      <RotateCw size={11} />
                      {redeliveringId === d.id
                        ? t("settings.deliveries.redelivering")
                        : t("settings.deliveries.redeliver")}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <PaginationControls pager={pager} />

      <Dialog
        open={detail !== null}
        onClose={() => setDetail(null)}
        title={t("settingsDetail.deliveries.responseTitle")}
        headerAction={
          detail && detail.response_body !== "" ? (
            <CopyButton
              value={detail.response_body}
              label={t("settingsDetail.deliveries.copyResponse")}
              iconOnly
            />
          ) : undefined
        }
      >
        {detail && (
          <div className="flex flex-col gap-3 px-4 py-4">
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
              <dt className="font-medium text-fg-subtle">
                {t("settingsDetail.deliveries.metaEvent")}
              </dt>
              <dd className="font-mono text-fg-muted">{detail.event}</dd>
              <dt className="font-medium text-fg-subtle">
                {t("settingsDetail.deliveries.metaStatus")}
              </dt>
              <dd className="text-fg-muted">
                {statusLabel(t, detail.status)}
                {detail.response_status !== null &&
                  ` · ${t("settingsDetail.deliveries.httpStatus", { status: detail.response_status })}`}
              </dd>
            </dl>

            {/* Three outcomes that all collapse to a falsy `response_body`,
                kept apart on purpose (DESIGN.md §9): never attempted, no
                response at all, and a real response that carried no body. A
                failure to load the list itself is the ErrorState above — this
                dialog is only reachable from a row that did load. */}
            {detail.response_status === null ? (
              detail.attempts === 0 ? (
                <Alert tone="info" title={t("settingsDetail.deliveries.notAttemptedTitle")}>
                  <p className="text-xs font-medium text-fg-subtle">
                    {t("settingsDetail.deliveries.notAttemptedBody")}
                  </p>
                </Alert>
              ) : (
                <Alert tone="warning" title={t("settingsDetail.deliveries.noResponseTitle")}>
                  <p className="text-xs font-medium text-fg-subtle">
                    {t("settingsDetail.deliveries.noResponseBody")}
                  </p>
                </Alert>
              )
            ) : detail.response_body === "" ? (
              <Alert tone="info" title={t("settingsDetail.deliveries.emptyBodyTitle")}>
                <p className="text-xs font-medium text-fg-subtle">
                  {t("settingsDetail.deliveries.emptyBodyBody", { status: detail.response_status })}
                </p>
              </Alert>
            ) : (
              <div className="flex flex-col gap-1.5">
                <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
                  {t("settingsDetail.deliveries.bodyLabel")}
                </span>
                {/* The endpoint's own output: rendered as a text node, never
                    as HTML — an error page is usually exactly that. It wraps
                    (`whitespace-pre-wrap break-words`) and caps its own
                    height, so neither a 4 KiB stack trace nor one very long
                    JSON line can scroll the page sideways or bury the rest of
                    the dialog. */}
                <pre className="scroll-x max-h-72 overflow-y-auto whitespace-pre-wrap break-words rounded-md border border-border bg-bg-sunken p-2.5 font-mono text-xs leading-relaxed text-fg-muted">
                  {detail.response_body}
                </pre>
                <p className="text-xs font-medium text-fg-subtle">
                  {t("settingsDetail.deliveries.truncationHint")}
                </p>
              </div>
            )}
          </div>
        )}
      </Dialog>
    </div>
  );
}
