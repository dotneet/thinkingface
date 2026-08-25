"use client";

import { ArrowLeftRight } from "lucide-react";
import { useEffect, useState } from "react";
import { LoginRequiredState } from "@/components/settings/login-required-state";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { useFormattedTime } from "@/components/ui/time-text";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { acceptTransfer, cancelTransfer, listMyTransfers, rejectTransfer } from "@/lib/transfers";
import type { RepoTransfer } from "@/types/api";

/** The "expires <date>" fragment of a transfer row's detail line. */
function TransferExpiry({ transfer }: { transfer: RepoTransfer }) {
  const t = useT();
  const date = useFormattedTime(transfer.expires_at);
  return <>{t("settings.transfers.expires", { date })}</>;
}

function TransferRow({
  transfer,
  detail,
  busy,
  children,
}: {
  transfer: RepoTransfer;
  /** Extra line under the from/to summary (requested-by for incoming, nothing for outgoing). */
  detail?: React.ReactNode;
  busy: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-3 text-sm">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5 font-mono text-xs">
          <span className="text-fg-subtle">
            {transfer.from_namespace}/{transfer.from_name}
          </span>
          <span aria-hidden="true">→</span>
          <span className="text-fg">
            {transfer.to_namespace}/{transfer.to_name}
          </span>
          <Badge>{transfer.kind}</Badge>
        </div>
        {detail && <div className="mt-1 text-xs font-medium text-fg-subtle">{detail}</div>}
      </div>
      <div aria-busy={busy} className="flex shrink-0 items-center gap-2">
        {children}
      </div>
    </div>
  );
}

export function TransfersManager() {
  const t = useT();
  const [incoming, setIncoming] = useState<RepoTransfer[] | null>(null);
  const [outgoing, setOutgoing] = useState<RepoTransfer[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  // Transfer whose acceptance is pending confirmation — accepting hands over
  // ownership immediately and can't be undone from here, unlike reject/cancel
  // which merely decline or withdraw a request the other side can re-send.
  const [confirmAccept, setConfirmAccept] = useState<RepoTransfer | null>(null);
  const [acceptError, setAcceptError] = useState<string | null>(null);

  async function refresh() {
    const result = await listMyTransfers();
    if (!result.ok) {
      setNeedsLogin(isUnauthorized(result));
      setError(errorMessage(t, result));
      setIncoming(null);
      setOutgoing(null);
      return;
    }
    setError(null);
    setIncoming(result.data.incoming);
    setOutgoing(result.data.outgoing);
  }

  useEffect(() => {
    refresh();
  }, []);

  async function handleAccept(transfer: RepoTransfer) {
    setBusyId(transfer.id);
    setAcceptError(null);
    const result = await acceptTransfer(transfer.id);
    setBusyId(null);
    if (!result.ok) {
      setAcceptError(errorMessage(t, result));
      return;
    }
    setConfirmAccept(null);
    await refresh();
  }

  async function handleReject(transfer: RepoTransfer) {
    setBusyId(transfer.id);
    setActionError(null);
    const result = await rejectTransfer(transfer.id);
    setBusyId(null);
    if (!result.ok) {
      setActionError(errorMessage(t, result));
      return;
    }
    await refresh();
  }

  async function handleCancel(transfer: RepoTransfer) {
    setBusyId(transfer.id);
    setActionError(null);
    const result = await cancelTransfer(transfer.kind, transfer.from_namespace, transfer.from_name);
    setBusyId(null);
    if (!result.ok) {
      setActionError(errorMessage(t, result));
      return;
    }
    await refresh();
  }

  if (incoming === null && !error) {
    return <SkeletonLines lines={4} />;
  }

  if (needsLogin) {
    return <LoginRequiredState next="/settings/transfers" />;
  }

  if (incoming === null || outgoing === null) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={error ?? t("settings.transfers.loadFailed")}
        hint={t("settings.transfers.loadFailedHint")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-8">
      {actionError && <Alert tone="negative">{actionError}</Alert>}

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-semibold text-fg">{t("settings.transfers.incomingTitle")}</h2>
        {incoming.length === 0 ? (
          <EmptyState
            icon={ArrowLeftRight}
            title={t("settings.transfers.incomingEmptyTitle")}
            description={t("settings.transfers.incomingEmptyDescription")}
          />
        ) : (
          <div className="flex flex-col gap-3">
            {incoming.map((tr) => (
              <TransferRow
                key={tr.id}
                transfer={tr}
                busy={busyId === tr.id}
                detail={
                  <>
                    {t("settings.transfers.requestedBy", { username: tr.requested_by })} ·{" "}
                    <TransferExpiry transfer={tr} />
                  </>
                }
              >
                <Button
                  variant="primary"
                  size="sm"
                  disabled={busyId === tr.id}
                  onClick={() => {
                    setAcceptError(null);
                    setConfirmAccept(tr);
                  }}
                >
                  {busyId === tr.id
                    ? t("settings.transfers.accepting")
                    : t("settings.transfers.accept")}
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  disabled={busyId === tr.id}
                  onClick={() => handleReject(tr)}
                >
                  {busyId === tr.id
                    ? t("settings.transfers.rejecting")
                    : t("settings.transfers.reject")}
                </Button>
              </TransferRow>
            ))}
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-semibold text-fg">{t("settings.transfers.outgoingTitle")}</h2>
        {outgoing.length === 0 ? (
          <EmptyState
            icon={ArrowLeftRight}
            title={t("settings.transfers.outgoingEmptyTitle")}
            description={t("settings.transfers.outgoingEmptyDescription")}
          />
        ) : (
          <div className="flex flex-col gap-3">
            {outgoing.map((tr) => (
              <TransferRow
                key={tr.id}
                transfer={tr}
                busy={busyId === tr.id}
                detail={<TransferExpiry transfer={tr} />}
              >
                <Button
                  variant="danger"
                  size="sm"
                  disabled={busyId === tr.id}
                  onClick={() => handleCancel(tr)}
                >
                  {busyId === tr.id
                    ? t("settings.transfers.cancelling")
                    : t("settings.transfers.cancel")}
                </Button>
              </TransferRow>
            ))}
          </div>
        )}
      </section>

      <ConfirmDialog
        open={confirmAccept !== null}
        onClose={() => setConfirmAccept(null)}
        onConfirm={() => {
          if (confirmAccept) void handleAccept(confirmAccept);
        }}
        title={t("settings.transfers.confirmAcceptTitle")}
        description={
          confirmAccept && (
            <p className="text-sm text-fg-muted">
              {t("settings.transfers.confirmAccept", {
                repo: `${confirmAccept.from_namespace}/${confirmAccept.from_name}`,
              })}
            </p>
          )
        }
        confirmLabel={t("settings.transfers.accept")}
        confirmingLabel={t("settings.transfers.accepting")}
        tone="primary"
        confirming={busyId !== null}
        error={acceptError}
      />
    </div>
  );
}
