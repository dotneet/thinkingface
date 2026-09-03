"use client";

import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Checkbox, Field, Input } from "@/components/ui/field";
import { createAdminUser } from "@/lib/admin";
import type { FailedApiResult } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";

/** Mirrors the backend's validatePassword (backend/internal/api/auth.go). */
const MIN_PASSWORD_LENGTH = 8;

type Draft = { username: string; email: string; password: string; isAdmin: boolean };
const EMPTY_DRAFT: Draft = { username: "", email: "", password: "", isAdmin: false };

/**
 * Add an account. Unlike the public signup form this is unaffected by
 * `TF_ALLOW_SIGNUP`: on an instance that closed signup it is the only way to
 * add anyone.
 *
 * The fields live here rather than in the directory that opens the dialog, so
 * a value typed into this form can never leak into the password-reset one, and
 * the directory does not grow a state variable per input.
 */
export function AdminUserCreateDialog({
  open,
  onClose,
  onCreated,
  describe,
}: {
  open: boolean;
  onClose: () => void;
  /** Called with the username the server actually assigned. */
  onCreated: (username: string) => void;
  /** Localizes a failed write; owned by the directory, which knows the keys. */
  describe: (result: FailedApiResult) => string;
}) {
  const t = useT();
  const [draft, setDraft] = useState<Draft>(EMPTY_DRAFT);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  // Emptied as the dialog opens rather than as it closes, so a form abandoned
  // half-filled is not still sitting there next time. Adjusted during render
  // (React's "reset state when a prop changes" pattern) rather than in an
  // effect, which would paint the stale values for one frame first.
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setDraft(EMPTY_DRAFT);
      setError(null);
    }
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (draft.password.length < MIN_PASSWORD_LENGTH) {
      setError(t("settings.account.tooShort"));
      return;
    }
    setSubmitting(true);
    setError(null);
    const result = await createAdminUser({
      username: draft.username.trim(),
      email: draft.email.trim(),
      password: draft.password,
      is_admin: draft.isAdmin,
    });
    setSubmitting(false);
    if (!result.ok) {
      setError(describe(result));
      return;
    }
    onCreated(result.data.user.username);
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      busy={submitting}
      title={t("settings.adminUsers.addTitle")}
      footer={
        <>
          <Button type="button" onClick={onClose} disabled={submitting}>
            {t("ui.confirmDialog.defaultCancel")}
          </Button>
          <Button
            type="submit"
            form="admin-create-user"
            variant="primary"
            disabled={
              submitting || !draft.username.trim() || !draft.email.trim() || !draft.password
            }
          >
            {submitting
              ? t("settings.adminUsers.addSubmitting")
              : t("settings.adminUsers.addSubmit")}
          </Button>
        </>
      }
      footerNote={error ? <Alert tone="negative">{error}</Alert> : undefined}
    >
      <form
        id="admin-create-user"
        onSubmit={handleSubmit}
        className="flex flex-col gap-4 px-4 py-4"
      >
        <p className="text-sm text-fg-muted">{t("settings.adminUsers.addDescription")}</p>
        <Field
          label={t("settings.adminUsers.addUsernameLabel")}
          hint={t("settings.adminUsers.addUsernameHint")}
        >
          <Input
            value={draft.username}
            onChange={(e) => {
              setDraft({ ...draft, username: e.target.value });
              setError(null);
            }}
            placeholder={t("settings.adminUsers.addUsernamePlaceholder")}
            autoComplete="off"
            required
          />
        </Field>
        <Field label={t("settings.adminUsers.addEmailLabel")}>
          <Input
            type="email"
            value={draft.email}
            onChange={(e) => {
              setDraft({ ...draft, email: e.target.value });
              setError(null);
            }}
            autoComplete="off"
            required
          />
        </Field>
        <Field
          label={t("settings.adminUsers.addPasswordLabel")}
          hint={t("settings.account.newPasswordHint")}
        >
          <Input
            type="password"
            value={draft.password}
            onChange={(e) => {
              setDraft({ ...draft, password: e.target.value });
              setError(null);
            }}
            autoComplete="new-password"
            required
          />
        </Field>
        <label className="flex items-start gap-2 text-sm">
          <Checkbox
            checked={draft.isAdmin}
            onChange={(e) => setDraft({ ...draft, isAdmin: e.target.checked })}
            className="mt-1"
          />
          <span className="flex flex-col gap-0.5">
            <span className="font-medium text-fg-muted">
              {t("settings.adminUsers.addIsAdminLabel")}
            </span>
            <span className="text-xs font-medium text-fg-subtle">
              {t("settings.adminUsers.addIsAdminHint")}
            </span>
          </span>
        </label>
      </form>
    </Dialog>
  );
}
