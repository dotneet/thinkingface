"use client";

import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { updateAdminUser } from "@/lib/admin";
import type { FailedApiResult } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import type { AdminUser } from "@/types/api";

/** Mirrors the backend's validatePassword (backend/internal/api/auth.go). */
const MIN_PASSWORD_LENGTH = 8;

/**
 * Replace one account's password — `PATCH /api/v1/admin/users/{username}` with
 * the one field set.
 *
 * `target` doubles as the open flag: there is no state in which the dialog is
 * open without an account to act on, and a separate boolean could contradict
 * it. The two password fields live here so they cannot survive into the next
 * account's dialog.
 */
export function AdminUserResetDialog({
  target,
  onClose,
  onDone,
  describe,
}: {
  /** The account whose password is being reset; `null` closes the dialog. */
  target: AdminUser | null;
  onClose: () => void;
  onDone: (username: string) => void;
  describe: (result: FailedApiResult) => string;
}) {
  const t = useT();
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  // Cleared as the dialog opens on an account, so nothing typed for a previous
  // one is still in the boxes. Adjusted during render rather than in an effect
  // for the same reason as the create dialog: an effect paints first.
  const [shownTarget, setShownTarget] = useState(target);
  if (target !== shownTarget) {
    setShownTarget(target);
    if (target) {
      setPassword("");
      setConfirm("");
      setError(null);
    }
  }

  const username = target?.username ?? "";

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!target) return;
    if (password !== confirm) {
      setError(t("settings.account.mismatch"));
      return;
    }
    if (password.length < MIN_PASSWORD_LENGTH) {
      setError(t("settings.account.tooShort"));
      return;
    }
    setSubmitting(true);
    setError(null);
    const result = await updateAdminUser(target.username, { password });
    setSubmitting(false);
    if (!result.ok) {
      setError(describe(result));
      return;
    }
    onDone(target.username);
  }

  return (
    <Dialog
      open={target !== null}
      onClose={onClose}
      title={t("settings.adminUsers.resetTitle", { username })}
      footer={
        <>
          <Button type="button" onClick={onClose} disabled={submitting}>
            {t("ui.confirmDialog.defaultCancel")}
          </Button>
          <Button
            type="submit"
            form="admin-reset-password"
            variant="primary"
            disabled={submitting || !password || !confirm}
          >
            {submitting
              ? t("settings.adminUsers.resetSubmitting")
              : t("settings.adminUsers.resetSubmit")}
          </Button>
        </>
      }
      footerNote={error ? <Alert tone="negative">{error}</Alert> : undefined}
    >
      <form
        id="admin-reset-password"
        onSubmit={handleSubmit}
        className="flex flex-col gap-4 px-4 py-4"
      >
        <p className="text-sm text-fg-muted">
          {t("settings.adminUsers.resetDescription", { username })}
        </p>
        <Field
          label={t("settings.adminUsers.resetNewPasswordLabel")}
          hint={t("settings.account.newPasswordHint")}
        >
          <Input
            type="password"
            value={password}
            onChange={(e) => {
              setPassword(e.target.value);
              setError(null);
            }}
            autoComplete="new-password"
            required
          />
        </Field>
        <Field label={t("settings.adminUsers.resetConfirmLabel")}>
          <Input
            type="password"
            value={confirm}
            onChange={(e) => {
              setConfirm(e.target.value);
              setError(null);
            }}
            autoComplete="new-password"
            required
          />
        </Field>
      </form>
    </Dialog>
  );
}
