"use client";

import { KeyRound, Trash2 } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button, buttonClass } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { CopyButton } from "@/components/ui/copy-button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input, Select } from "@/components/ui/field";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { createToken, deleteToken, listTokens } from "@/lib/tokens";
import type { TokenItem } from "@/types/api";

export function TokensManager() {
  const t = useT();
  const [tokens, setTokens] = useState<TokenItem[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [newName, setNewName] = useState("");
  const [newScope, setNewScope] = useState<"read" | "write">("read");
  const [creating, setCreating] = useState(false);
  const [justCreated, setJustCreated] = useState<{ name: string; token: string } | null>(null);
  // Id of the row whose delete is in flight, so a second click on the same
  // button can't fire a second DELETE (the first succeeds, the second 404s and
  // surfaces a confusing "not found" for a token the user did delete).
  const [deletingId, setDeletingId] = useState<number | null>(null);
  // Id of the row pending confirmation in the ConfirmDialog; null closes it.
  const [confirmDeleteId, setConfirmDeleteId] = useState<number | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  async function refresh() {
    const result = await listTokens();
    if (!result.ok) {
      // 401 here means "you're signed out", not "something broke" — say so
      // and point at the fix instead of surfacing the raw API error.
      setNeedsLogin(isUnauthorized(result));
      setError(errorMessage(t, result));
      setTokens(null);
      return;
    }
    setNeedsLogin(false);
    setError(null);
    setTokens(result.data.items);
  }

  useEffect(() => {
    refresh();
  }, []);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!newName.trim()) return;
    setCreating(true);
    setError(null);
    const result = await createToken(newName.trim(), newScope);
    setCreating(false);
    if (!result.ok) {
      setError(
        isUnauthorized(result)
          ? t("settings.tokens.sessionExpiredCreate")
          : errorMessage(t, result),
      );
      return;
    }
    setJustCreated({ name: result.data.name, token: result.data.token });
    setNewName("");
    await refresh();
  }

  async function handleDelete() {
    if (confirmDeleteId === null || deletingId !== null) return;
    const id = confirmDeleteId;
    setDeleteError(null);
    setDeletingId(id);
    const result = await deleteToken(id);
    setDeletingId(null);
    if (!result.ok) {
      setDeleteError(
        isUnauthorized(result)
          ? t("settings.tokens.sessionExpiredDelete")
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
          onSubmit={handleCreate}
          className="flex flex-wrap items-end gap-3 rounded-lg border border-border bg-bg-sunken p-4"
        >
          <Field label={t("settings.tokens.nameLabel")} className="min-w-[180px] flex-1">
            <Input
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              placeholder={t("settings.tokens.namePlaceholder")}
            />
          </Field>
          <Field label={t("settings.tokens.scopeLabel")}>
            <Select
              value={newScope}
              onChange={(e) => setNewScope(e.target.value as "read" | "write")}
            >
              <option value="read">{t("settings.tokens.scopeRead")}</option>
              <option value="write">{t("settings.tokens.scopeWrite")}</option>
            </Select>
          </Field>
          <Button
            type="submit"
            variant="primary"
            disabled={creating || !newName.trim()}
            className="px-4 py-2"
          >
            {creating ? t("settings.tokens.creating") : t("settings.tokens.create")}
          </Button>
        </form>
      )}

      {justCreated && (
        <Alert
          tone="positive"
          title={t("settings.tokens.createdTitle", { name: justCreated.name })}
        >
          <p className="text-xs font-medium text-fg-subtle">{t("settings.tokens.copyNow")}</p>
          <div className="mt-1.5 flex items-center justify-between gap-2 rounded-md border border-border bg-bg-raised p-2.5">
            <code className="scroll-x whitespace-pre font-mono text-xs">{justCreated.token}</code>
            <CopyButton value={justCreated.token} />
          </div>
        </Alert>
      )}

      {error && !(tokens === null && needsLogin) && <Alert tone="negative">{error}</Alert>}

      {tokens === null && !error ? (
        <SkeletonLines lines={3} />
      ) : tokens === null && needsLogin ? (
        <ErrorState
          title={t("settings.tokens.loginRequiredTitle")}
          message={t("settings.tokens.loginRequiredMessage")}
          action={
            <Link
              href="/login?next=/settings/tokens"
              className={buttonClass({ variant: "primary" })}
            >
              {t("settings.tokens.login")}
            </Link>
          }
        />
      ) : tokens === null ? (
        <ErrorState
          title={t("settings.errorTitle")}
          message={error ?? t("settings.tokens.loadFailed")}
          hint={t("settings.tokens.loadFailedHint")}
        />
      ) : tokens.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title={t("settings.tokens.emptyTitle")}
          description={t("settings.tokens.emptyDescription")}
        />
      ) : (
        <div className="scroll-x rounded-lg border border-border">
          <table className="w-full min-w-[560px] border-collapse text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
                <th className="px-3 py-2 font-medium">{t("settings.tokens.colName")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.tokens.colScope")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.tokens.colCreated")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.tokens.colLastUsed")}</th>
                <th className="px-3 py-2"></th>
              </tr>
            </thead>
            <tbody>
              {tokens.map((token) => (
                <tr key={token.id} className="border-b border-border last:border-0">
                  <td className="px-3 py-2 font-medium">{token.name}</td>
                  <td className="px-3 py-2 capitalize text-fg-muted">{token.scope}</td>
                  <td className="px-3 py-2 text-fg-subtle">
                    <TimeText iso={token.created_at} style="dateTime" />
                  </td>
                  <td className="px-3 py-2 text-fg-subtle">
                    {token.last_used_at ? (
                      <TimeText iso={token.last_used_at} style="dateTime" />
                    ) : (
                      t("settings.tokens.neverUsed")
                    )}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Button
                      variant="danger"
                      size="sm"
                      disabled={deletingId !== null}
                      onClick={() => {
                        setDeleteError(null);
                        setConfirmDeleteId(token.id);
                      }}
                    >
                      <Trash2 size={12} />
                      {deletingId === token.id
                        ? t("settings.tokens.deleting")
                        : t("settings.tokens.delete")}
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
        title={t("settings.tokens.confirmDeleteTitle")}
        description={<p className="text-sm text-fg-muted">{t("settings.tokens.confirmDelete")}</p>}
        confirmLabel={t("settings.tokens.delete")}
        confirmingLabel={t("settings.tokens.deleting")}
        confirming={deletingId !== null}
        error={deleteError}
      />
    </div>
  );
}
