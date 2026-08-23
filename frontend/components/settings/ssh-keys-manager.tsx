"use client";

import { KeyRound, Trash2 } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button, buttonClass } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input, Textarea } from "@/components/ui/field";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { createSSHKey, deleteSSHKey, listSSHKeys } from "@/lib/ssh-keys";
import type { SSHKeyItem } from "@/types/api";

export function SSHKeysManager() {
  const t = useT();
  const [keys, setKeys] = useState<SSHKeyItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [newTitle, setNewTitle] = useState("");
  const [newKey, setNewKey] = useState("");
  const [adding, setAdding] = useState(false);
  const [justAdded, setJustAdded] = useState<SSHKeyItem | null>(null);
  // Id of the row whose delete is in flight, so a second click on the same
  // button can't fire a second DELETE (the first succeeds, the second 404s and
  // surfaces a confusing "not found" for a key the user did delete).
  const [deletingId, setDeletingId] = useState<number | null>(null);
  // Id of the row pending confirmation in the ConfirmDialog; null closes it.
  const [confirmDeleteId, setConfirmDeleteId] = useState<number | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  async function refresh() {
    const result = await listSSHKeys();
    if (!result.ok) {
      // 401 here means "you're signed out", not "something broke" — say so
      // and point at the fix instead of surfacing the raw API error.
      setNeedsLogin(isUnauthorized(result));
      setError(errorMessage(t, result));
      setKeys(null);
      return;
    }
    setNeedsLogin(false);
    setError(null);
    setKeys(result.data.items);
  }

  useEffect(() => {
    refresh();
  }, []);

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    if (!newKey.trim()) return;
    setAdding(true);
    const result = await createSSHKey(newTitle.trim(), newKey.trim());
    setAdding(false);
    if (!result.ok) {
      // Anything but 401 is the backend's own validation message ("RSA keys
      // must be at least 2048 bits", "this key is already registered"), which
      // is written for the person pasting the key — show it verbatim
      // (bad_request keeps its detail, see lib/api-error-message.ts).
      setError(
        isUnauthorized(result)
          ? t("settings.sshKeys.sessionExpiredCreate")
          : errorMessage(t, result),
      );
      return;
    }
    setError(null);
    setJustAdded(result.data);
    setNewTitle("");
    setNewKey("");
    await refresh();
  }

  async function handleDelete() {
    if (confirmDeleteId === null || deletingId !== null) return;
    const id = confirmDeleteId;
    setDeleteError(null);
    setDeletingId(id);
    const result = await deleteSSHKey(id);
    setDeletingId(null);
    if (!result.ok) {
      setDeleteError(
        isUnauthorized(result)
          ? t("settings.sshKeys.sessionExpiredDelete")
          : errorMessage(t, result),
      );
      return;
    }
    setConfirmDeleteId(null);
    await refresh();
  }

  return (
    <div className="flex flex-col gap-6">
      {!needsLogin && (
        <form
          onSubmit={handleAdd}
          className="flex flex-col gap-3 rounded-lg border border-border bg-bg-sunken p-4"
        >
          <Field label={t("settings.sshKeys.titleLabel")}>
            <Input
              value={newTitle}
              onChange={(e) => setNewTitle(e.target.value)}
              placeholder={t("settings.sshKeys.titlePlaceholder")}
            />
          </Field>
          <Field label={t("settings.sshKeys.keyLabel")} hint={t("settings.sshKeys.keyHint")}>
            <Textarea
              value={newKey}
              onChange={(e) => setNewKey(e.target.value)}
              placeholder={t("settings.sshKeys.keyPlaceholder")}
              rows={4}
              spellCheck={false}
              className="font-mono text-xs"
            />
          </Field>
          <div>
            <Button
              type="submit"
              variant="primary"
              disabled={adding || !newKey.trim()}
              className="px-4 py-2"
            >
              {adding ? t("settings.sshKeys.adding") : t("settings.sshKeys.add")}
            </Button>
          </div>
        </form>
      )}

      {justAdded && (
        <Alert tone="positive" title={t("settings.sshKeys.addedTitle", { title: justAdded.title })}>
          <p className="text-xs font-medium text-fg-subtle">{t("settings.sshKeys.addedBody")}</p>
          <code className="mt-1.5 block scroll-x whitespace-pre font-mono text-xs">
            {justAdded.fingerprint}
          </code>
        </Alert>
      )}

      {error && !(keys === null && needsLogin) && <Alert tone="negative">{error}</Alert>}

      {keys === null && !error ? (
        <SkeletonLines lines={3} />
      ) : keys === null && needsLogin ? (
        <ErrorState
          title={t("settings.sshKeys.loginRequiredTitle")}
          message={t("settings.sshKeys.loginRequiredMessage")}
          action={
            <Link
              href="/login?next=/settings/ssh-keys"
              className={buttonClass({ variant: "primary" })}
            >
              {t("settings.sshKeys.login")}
            </Link>
          }
        />
      ) : keys === null ? (
        <ErrorState
          title={t("settings.errorTitle")}
          message={error ?? t("settings.sshKeys.loadFailed")}
          hint={t("settings.sshKeys.loadFailedHint")}
        />
      ) : keys.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title={t("settings.sshKeys.emptyTitle")}
          description={t("settings.sshKeys.emptyDescription")}
        />
      ) : (
        <div className="scroll-x rounded-lg border border-border">
          <table className="w-full min-w-[720px] border-collapse text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
                <th className="px-3 py-2 font-medium">{t("settings.sshKeys.colTitle")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.sshKeys.colType")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.sshKeys.colFingerprint")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.sshKeys.colAdded")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.sshKeys.colLastUsed")}</th>
                <th className="px-3 py-2"></th>
              </tr>
            </thead>
            <tbody>
              {keys.map((key) => (
                <tr key={key.id} className="border-b border-border last:border-0">
                  <td className="px-3 py-2 font-medium">{key.title}</td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">{key.key_type}</td>
                  <td className="px-3 py-2 font-mono text-xs text-fg-muted">{key.fingerprint}</td>
                  <td className="px-3 py-2 text-fg-subtle">
                    <TimeText iso={key.created_at} style="dateTime" />
                  </td>
                  <td className="px-3 py-2 text-fg-subtle">
                    {key.last_used_at ? (
                      <TimeText iso={key.last_used_at} style="dateTime" />
                    ) : (
                      t("settings.sshKeys.neverUsed")
                    )}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Button
                      variant="danger"
                      size="sm"
                      disabled={deletingId !== null}
                      onClick={() => {
                        setDeleteError(null);
                        setConfirmDeleteId(key.id);
                      }}
                    >
                      <Trash2 size={12} />
                      {deletingId === key.id
                        ? t("settings.sshKeys.deleting")
                        : t("settings.sshKeys.delete")}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={confirmDeleteId !== null}
        onClose={() => setConfirmDeleteId(null)}
        onConfirm={handleDelete}
        title={t("settings.sshKeys.confirmDeleteTitle")}
        description={<p className="text-sm text-fg-muted">{t("settings.sshKeys.confirmDelete")}</p>}
        confirmLabel={t("settings.sshKeys.delete")}
        confirmingLabel={t("settings.sshKeys.deleting")}
        confirming={deletingId !== null}
        error={deleteError}
      />
    </div>
  );
}
