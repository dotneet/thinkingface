"use client";

import { useEffect, useState } from "react";
import { LoginRequiredState } from "@/components/settings/login-required-state";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input } from "@/components/ui/field";
import { SkeletonLines } from "@/components/ui/skeleton";
import { changeMyPassword } from "@/lib/admin";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getMe } from "@/lib/auth";
import { useT } from "@/lib/i18n/client";

/** Mirrors the backend's validatePassword (backend/internal/api/auth.go). */
const MIN_PASSWORD_LENGTH = 8;

/**
 * Change your own password (`PATCH /api/v1/me/password`).
 *
 * The current password is required and checked server-side; every other
 * session is revoked while this browser's cookie is re-issued, so submitting
 * the form does not sign the user out of the tab they are looking at.
 */
export function AccountSettings() {
  const t = useT();
  const [username, setUsername] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);

  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Any edit invalidates the previous outcome, so a stale "changed" notice
  // can never sit above a form the user is filling in again.
  function edit(setter: (v: string) => void, value: string) {
    setter(value);
    setSaved(false);
    setSaveError(null);
  }

  // Initial load only: the fetched username never changes with the language,
  // so re-running this on a locale change would just refetch it — the same
  // reason `usePagedList` keeps the translator out of its inputs (it
  // re-renders its error in the new language instead of re-reading).
  useEffect(() => {
    (async () => {
      const me = await getMe();
      if (!me.ok) {
        setNeedsLogin(isUnauthorized(me));
        setLoadError(errorMessage(t, me));
        return;
      }
      setUsername(me.data.user.username);
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaved(false);
    // Checked here as well as on the server: the confirmation field has no
    // server-side counterpart at all, and the length rule is worth saying
    // before a round trip.
    if (next !== confirm) {
      setSaveError(t("settings.account.mismatch"));
      return;
    }
    if (next.length < MIN_PASSWORD_LENGTH) {
      setSaveError(t("settings.account.tooShort"));
      return;
    }
    if (next === current) {
      setSaveError(t("settings.account.sameAsCurrent"));
      return;
    }
    setSaving(true);
    setSaveError(null);
    const result = await changeMyPassword({ current_password: current, new_password: next });
    setSaving(false);
    if (!result.ok) {
      if (result.status === 401) {
        // The backend answers both "current password is wrong" and "the
        // session is missing/expired" with the same 401 + type
        // "unauthorized" (requireWrite and refusePasswordChange in
        // backend/internal/api/auth.go both go through unauthorized() /
        // writeError(..., "unauthorized", ...)), so a 401 alone cannot tell
        // the two apart. Re-check the session itself before blaming the
        // password: if it is still valid, the password really was wrong; if
        // it is gone, re-entering the same password can never succeed.
        const me = await getMe();
        if (!me.ok && isUnauthorized(me)) {
          setNeedsLogin(true);
          return;
        }
        if (!me.ok) {
          setSaveError(errorMessage(t, me));
          return;
        }
        setSaveError(t("settings.account.wrongCurrentPassword"));
        return;
      }
      setSaveError(errorMessage(t, result));
      return;
    }
    setCurrent("");
    setNext("");
    setConfirm("");
    setSaved(true);
  }

  if (username === null && !loadError) return <SkeletonLines lines={5} />;

  if (needsLogin) {
    return <LoginRequiredState next="/settings/account" />;
  }

  if (username === null) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={loadError ?? t("settings.account.loadFailed")}
        hint={t("settings.account.loadFailedHint")}
      />
    );
  }

  return (
    <form onSubmit={handleSubmit} className="flex max-w-lg flex-col gap-4">
      <div>
        <h2 className="text-sm font-semibold text-fg">
          {t("settings.account.changePasswordTitle")}
        </h2>
        <p className="mt-1 text-sm text-fg-subtle">{t("settings.account.changePasswordHint")}</p>
      </div>

      <Field label={t("settings.account.currentPasswordLabel")}>
        <Input
          type="password"
          value={current}
          onChange={(e) => edit(setCurrent, e.target.value)}
          autoComplete="current-password"
          required
        />
      </Field>

      <Field
        label={t("settings.account.newPasswordLabel")}
        hint={t("settings.account.newPasswordHint")}
      >
        <Input
          type="password"
          value={next}
          onChange={(e) => edit(setNext, e.target.value)}
          autoComplete="new-password"
          required
        />
      </Field>

      <Field label={t("settings.account.confirmPasswordLabel")}>
        <Input
          type="password"
          value={confirm}
          onChange={(e) => edit(setConfirm, e.target.value)}
          autoComplete="new-password"
          required
        />
      </Field>

      <Button
        type="submit"
        variant="primary"
        disabled={saving || !current || !next || !confirm}
        className="self-start px-4 py-2"
      >
        {saving ? t("settings.account.submitting") : t("settings.account.submit")}
      </Button>

      {/* Below the action row so a result never pushes the button out from
          under the pointer before the next click (DESIGN.md §8). */}
      {saveError && <Alert tone="negative">{saveError}</Alert>}
      {saved && <Alert tone="positive">{t("settings.account.changed")}</Alert>}
    </form>
  );
}
