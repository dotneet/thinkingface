"use client";

import { FileText, Inbox, RotateCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
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
  const [deliveries, setDeliveries] = useState<WebhookDelivery[] | null>(null);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [redeliveringId, setRedeliveringId] = useState<number | null>(null);
  // The delivery whose response is open in the detail dialog. The row itself
  // is held (not just its id) so a background refresh that drops the row from
  // the current page cannot blank out a dialog the user is reading.
  const [detail, setDetail] = useState<WebhookDelivery | null>(null);

  async function refresh(atOffset: number, isStale: () => boolean = () => false) {
    const result = await listWebhookDeliveries(webhookId, { limit: PAGE_SIZE, offset: atOffset });
    if (isStale()) return;
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    setError(null);
    setDeliveries(result.data.items);
    setTotal(result.data.total);
    setOffset(atOffset);
  }

  useEffect(() => {
    let cancelled = false;
    refresh(0, () => cancelled);
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [webhookId]);

  async function handleRedeliver(deliveryId: number) {
    if (redeliveringId !== null) return;
    setRedeliveringId(deliveryId);
    const result = await redeliverWebhook(webhookId, deliveryId);
    setRedeliveringId(null);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    await refresh(0);
  }

  if (deliveries === null && !error) {
    return <SkeletonLines lines={3} />;
  }
  if (deliveries === null) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={error ?? t("settings.deliveries.loadFailed")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {error && <p className="text-xs text-negative">{error}</p>}
      {deliveries.length === 0 ? (
        <EmptyState
          icon={Inbox}
          title={t("settings.deliveries.emptyTitle")}
          description={t("settings.deliveries.emptyDescription")}
        />
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
      {total > PAGE_SIZE && (
        <div className="flex items-center justify-between text-xs font-medium text-fg-subtle">
          <span>
            {t("settings.deliveries.pageInfo", {
              from: offset + 1,
              to: Math.min(offset + PAGE_SIZE, total),
              total,
            })}
          </span>
          <div className="flex gap-2">
            <Button
              size="sm"
              disabled={offset === 0}
              onClick={() => refresh(Math.max(0, offset - PAGE_SIZE))}
            >
              {t("settings.deliveries.prev")}
            </Button>
            <Button
              size="sm"
              disabled={offset + PAGE_SIZE >= total}
              onClick={() => refresh(offset + PAGE_SIZE)}
            >
              {t("settings.deliveries.next")}
            </Button>
          </div>
        </div>
      )}

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
