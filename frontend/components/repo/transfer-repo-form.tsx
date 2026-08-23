"use client";

import { useRouter } from "next/navigation";
import { useEffect, useId, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input, Select } from "@/components/ui/field";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { SkeletonLines } from "@/components/ui/skeleton";
import { useFormattedTime } from "@/components/ui/time-text";
import { isUnauthorized } from "@/lib/api";
import { errorMessage, type FailedApiResult } from "@/lib/api-error-message";
import { getMe } from "@/lib/auth";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { repoBase } from "@/lib/paths";
import { cancelTransfer, getPendingTransfer, transferRepo } from "@/lib/transfers";
import { type NameError, validateName } from "@/lib/validation";
import type { RepoKind, RepoTransfer } from "@/types/api";

type DestMode = "mine" | "other";

/** Maps validateName error codes to the transfer form's new-name-field message keys. */
const NEW_NAME_ERROR_KEYS: Partial<Record<NameError, MessageKey>> = {
  invalid: "repo.settings.transfer.errors.nameInvalid",
  gitSuffix: "repo.settings.transfer.errors.nameGitSuffix",
};

export function TransferRepoForm({ kind, ns, name }: { kind: RepoKind; ns: string; name: string }) {
  const t = useT();
  const router = useRouter();
  const confirmFormId = useId();

  const [namespaces, setNamespaces] = useState<string[] | null>(null); // null = loading
  const [needsLogin, setNeedsLogin] = useState(false);
  // Raw failed results (not translated strings) so the message is computed
  // at render time with the current `t` — keeping `t` out of the effects'
  // dependencies avoids refetching on every locale change.
  const [namespacesError, setNamespacesError] = useState<FailedApiResult | null>(null);

  const [transfer, setTransfer] = useState<RepoTransfer | null | undefined>(undefined); // undefined = loading
  const [loadError, setLoadError] = useState<FailedApiResult | null>(null);
  const transferExpiresAt = useFormattedTime(transfer?.expires_at);

  const [mode, setMode] = useState<DestMode>("mine");
  const [selectedNamespace, setSelectedNamespace] = useState("");
  const [otherNamespace, setOtherNamespace] = useState("");
  const [newName, setNewName] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirmText, setConfirmText] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const [cancelling, setCancelling] = useState(false);
  const [cancelError, setCancelError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      const result = await getMe();
      if (!result.ok) {
        if (isUnauthorized(result)) {
          setNeedsLogin(true);
        } else {
          setNamespacesError(result);
        }
        setNamespaces([]);
        return;
      }
      const names = result.data.user.namespaces.map((n) => n.name);
      setNamespaces(names);
      setSelectedNamespace(names[0] ?? "");
      setMode(names.length > 0 ? "mine" : "other");
    })();
  }, []);

  useEffect(() => {
    (async () => {
      const result = await getPendingTransfer(kind, ns, name);
      if (!result.ok) {
        // No pending transfer is the common case, not an error to surface.
        if (result.status === 404) {
          setTransfer(null);
          return;
        }
        setLoadError(result);
        setTransfer(null);
        return;
      }
      setTransfer(result.data.transfer);
    })();
  }, [kind, ns, name]);

  async function handleCancel() {
    setCancelling(true);
    setCancelError(null);
    const result = await cancelTransfer(kind, ns, name);
    setCancelling(false);
    if (!result.ok) {
      setCancelError(errorMessage(t, result));
      return;
    }
    setTransfer(null);
  }

  const destNamespace = (mode === "mine" ? selectedNamespace : otherNamespace).trim();
  const destName = newName.trim() || name;
  const confirmValue = `${ns}/${name}`;
  const canConfirm = confirmText === confirmValue;

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitError(null);
    if (!destNamespace) {
      setSubmitError(t("repo.settings.transfer.errors.namespaceRequired"));
      return;
    }
    const trimmedName = newName.trim();
    if (trimmedName) {
      const nameError = validateName(trimmedName);
      const key = nameError ? NEW_NAME_ERROR_KEYS[nameError] : undefined;
      if (key) {
        setSubmitError(t(key));
        return;
      }
    }
    setConfirmText("");
    setConfirmOpen(true);
  }

  async function handleConfirmTransfer() {
    setSubmitting(true);
    setSubmitError(null);
    const result = await transferRepo(kind, ns, name, {
      namespace: destNamespace,
      name: newName.trim() || undefined,
    });
    setSubmitting(false);
    if (!result.ok) {
      setSubmitError(errorMessage(t, result));
      return;
    }
    setConfirmOpen(false);
    if (result.data.repo) {
      router.push(repoBase(kind, result.data.repo.namespace, result.data.repo.name));
      return;
    }
    setTransfer(result.data.transfer);
  }

  if (namespaces === null || transfer === undefined) {
    return <SkeletonLines lines={4} />;
  }

  if (needsLogin) {
    return (
      <ErrorState
        title={t("repo.settings.transfer.loginRequiredTitle")}
        message={t("repo.settings.transfer.loginRequiredMessage")}
      />
    );
  }

  if (namespacesError) {
    return (
      <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, namespacesError)} />
    );
  }

  if (loadError) {
    return <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, loadError)} />;
  }

  if (transfer) {
    return (
      <Alert tone="warning" title={t("repo.settings.transfer.pendingTitle")}>
        <p>
          {t("repo.settings.transfer.pendingDestination", {
            destination: `${transfer.to_namespace}/${transfer.to_name}`,
          })}
        </p>
        <p className="text-xs font-medium text-fg-subtle">
          {t("repo.settings.transfer.pendingExpires", {
            date: transferExpiresAt,
          })}
        </p>
        {cancelError && <p className="text-xs text-negative">{cancelError}</p>}
        <Button
          variant="danger"
          size="sm"
          disabled={cancelling}
          onClick={handleCancel}
          className="mt-1 self-start"
        >
          {cancelling ? t("repo.settings.transfer.cancelling") : t("repo.settings.transfer.cancel")}
        </Button>
      </Alert>
    );
  }

  return (
    <>
      <form onSubmit={handleSubmit} className="flex max-w-lg flex-col gap-4">
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium text-fg-muted">
            {t("repo.settings.transfer.destinationLabel")}
          </span>
          <SegmentedControl<DestMode>
            label={t("repo.settings.transfer.destinationModeLabel")}
            value={mode}
            onChange={setMode}
            options={[
              {
                value: "mine",
                label: t("repo.settings.transfer.destinationModeMine"),
                disabled: namespaces.length === 0,
              },
              { value: "other", label: t("repo.settings.transfer.destinationModeOther") },
            ]}
          />
          {mode === "mine" ? (
            namespaces.length > 0 ? (
              <Select
                value={selectedNamespace}
                onChange={(e) => setSelectedNamespace(e.target.value)}
              >
                {namespaces.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </Select>
            ) : (
              <p className="text-xs font-medium text-fg-subtle">
                {t("repo.settings.transfer.noOwnNamespaces")}
              </p>
            )
          ) : (
            <Input
              value={otherNamespace}
              onChange={(e) => setOtherNamespace(e.target.value)}
              placeholder={t("repo.settings.transfer.otherNamespacePlaceholder")}
            />
          )}
        </div>

        <Field
          label={t("repo.settings.transfer.newNameLabel")}
          hint={t("repo.settings.transfer.newNameHint")}
        >
          <Input value={newName} onChange={(e) => setNewName(e.target.value)} placeholder={name} />
        </Field>

        <Button
          type="submit"
          variant="primary"
          disabled={!destNamespace}
          className="self-start px-4 py-2"
        >
          {t("repo.settings.transfer.submit")}
        </Button>

        {/* Below the submit button so a validation failure never pushes it
            down right before the retry click (DESIGN.md §8). */}
        {submitError && !confirmOpen && <Alert tone="negative">{submitError}</Alert>}
      </form>

      {confirmOpen && (
        <Dialog
          open={confirmOpen}
          onClose={() => setConfirmOpen(false)}
          title={t("repo.settings.transfer.confirmTitle")}
          footer={
            <>
              <Button onClick={() => setConfirmOpen(false)} disabled={submitting}>
                {t("repo.settings.transfer.confirmCancel")}
              </Button>
              <Button
                type="submit"
                form={confirmFormId}
                variant="danger"
                disabled={!canConfirm || submitting}
              >
                {submitting
                  ? t("repo.settings.transfer.confirming")
                  : t("repo.settings.transfer.confirmSubmit")}
              </Button>
            </>
          }
          // Below the action row, so a failed transfer grows the panel
          // downward instead of moving Cancel/Confirm (DESIGN.md §8).
          footerNote={submitError ? <Alert tone="negative">{submitError}</Alert> : undefined}
        >
          <form
            id={confirmFormId}
            className="flex flex-col gap-4 px-4 py-4"
            onSubmit={(e) => {
              e.preventDefault();
              if (canConfirm && !submitting) handleConfirmTransfer();
            }}
          >
            <p className="text-sm text-fg-muted">
              {t("repo.settings.transfer.confirmBody", {
                from: `${ns}/${name}`,
                to: `${destNamespace}/${destName}`,
              })}
            </p>
            <Field label={t("repo.settings.transfer.confirmInputLabel", { value: confirmValue })}>
              {/* Typing the source ns/name confirms intent, GitHub-style. */}
              <Input
                autoFocus
                value={confirmText}
                onChange={(e) => setConfirmText(e.target.value)}
                placeholder={confirmValue}
              />
            </Field>
          </form>
        </Dialog>
      )}
    </>
  );
}
