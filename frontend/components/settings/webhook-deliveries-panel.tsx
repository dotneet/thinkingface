"use client";

import { Inbox, RotateCw } from "lucide-react";
import { useEffect, useState } from "react";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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

  async function refresh(atOffset: number) {
    const result = await listWebhookDeliveries(webhookId, { limit: PAGE_SIZE, offset: atOffset });
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
    refresh(0);
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
          <table className="w-full min-w-[520px] border-collapse text-xs">
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
                  <td className="px-2.5 py-1.5 text-fg-muted">{d.response_status ?? "-"}</td>
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
    </div>
  );
}
