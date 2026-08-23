"use client";

import { ExternalLink, Lock } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button, buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input, Textarea } from "@/components/ui/field";
import { SkeletonLines } from "@/components/ui/skeleton";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getMe } from "@/lib/auth";
import { useT } from "@/lib/i18n/client";
import { getNamespace, namespaceHref, updateMyProfile } from "@/lib/namespace";
import type { NamespaceProfile } from "@/types/api";

/**
 * The signed-in user's own profile (docs/namespace-design.md §5.3, §8.1).
 * The username field is read-only — it *is* the namespace and can never
 * change (§5.4) — everything else is `PATCH /api/v1/me/profile`.
 */
export function ProfileSettings() {
  const t = useT();
  const [profile, setProfile] = useState<NamespaceProfile | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);

  const [displayName, setDisplayName] = useState("");
  const [description, setDescription] = useState("");
  const [website, setWebsite] = useState("");
  const [avatarUrl, setAvatarUrl] = useState("");
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  // Any edit after a save invalidates the "saved" notice (and a stale error).
  function edit(setter: (v: string) => void, value: string) {
    setter(value);
    setSaved(false);
    setSaveError(null);
  }

  useEffect(() => {
    (async () => {
      const me = await getMe();
      if (!me.ok) {
        setNeedsLogin(isUnauthorized(me));
        setError(errorMessage(t, me));
        return;
      }
      // /me carries display_name / avatar_url but not description / website
      // (docs/namespace-design.md §7.1), so the namespace's own profile is
      // the source of truth here.
      const ns = await getNamespace(me.data.user.username);
      if (!ns.ok) {
        setNeedsLogin(isUnauthorized(ns));
        setError(errorMessage(t, ns));
        return;
      }
      setProfile(ns.data.namespace);
      setDisplayName(ns.data.namespace.display_name);
      setDescription(ns.data.namespace.description);
      setWebsite(ns.data.namespace.website);
      setAvatarUrl(ns.data.namespace.avatar_url);
    })();
  }, []);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!profile) return;
    setSaving(true);
    setSaveError(null);
    setSaved(false);
    const result = await updateMyProfile({
      display_name: displayName.trim(),
      description: description.trim(),
      website: website.trim(),
      avatar_url: avatarUrl.trim(),
    });
    setSaving(false);
    if (!result.ok) {
      setSaveError(errorMessage(t, result));
      return;
    }
    setProfile(result.data.namespace);
    setSaved(true);
  }

  if (profile === null && !error) return <SkeletonLines lines={6} />;

  if (needsLogin) {
    return (
      <ErrorState
        title={t("settings.profile.loginRequiredTitle")}
        message={t("settings.profile.loginRequiredMessage")}
        action={
          <Link
            href="/login?next=/settings/profile"
            className={buttonClass({ variant: "primary" })}
          >
            {t("settings.profile.login")}
          </Link>
        }
      />
    );
  }

  if (profile === null) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={error ?? t("settings.profile.loadFailed")}
        hint={t("settings.profile.loadFailedHint")}
      />
    );
  }

  return (
    <form onSubmit={handleSubmit} className="flex max-w-lg flex-col gap-4">
      <Field label={t("settings.profile.usernameLabel")}>
        <div className="flex items-center gap-2">
          <Input value={profile.name} disabled readOnly className="font-mono" />
          <Lock size={14} className="shrink-0 text-fg-subtle" aria-hidden="true" />
        </div>
      </Field>
      <p className="-mt-2 text-xs font-medium text-fg-subtle">
        {t("settings.profile.usernameLockedHint")}{" "}
        <Link href="/settings/transfers" className="text-accent hover:underline">
          {t("settings.profile.usernameLockedTransferLink")}
        </Link>
      </p>

      <Field label={t("settings.profile.displayNameLabel")}>
        <Input value={displayName} onChange={(e) => edit(setDisplayName, e.target.value)} />
      </Field>

      <Field label={t("settings.profile.bioLabel")}>
        <Textarea
          value={description}
          onChange={(e) => edit(setDescription, e.target.value)}
          className="min-h-20"
          placeholder={t("settings.profile.bioPlaceholder")}
        />
      </Field>

      <Field label={t("settings.profile.websiteLabel")}>
        <Input
          value={website}
          onChange={(e) => edit(setWebsite, e.target.value)}
          type="url"
          placeholder="https://example.com"
        />
      </Field>

      <Field
        label={t("settings.profile.avatarUrlLabel")}
        hint={t("settings.profile.avatarUrlHint")}
      >
        <Input
          value={avatarUrl}
          onChange={(e) => edit(setAvatarUrl, e.target.value)}
          type="url"
          placeholder="https://example.com/avatar.png"
        />
      </Field>

      <div className="flex items-center gap-3">
        <Button type="submit" variant="primary" disabled={saving} className="px-4 py-2">
          {saving ? t("settings.profile.saving") : t("settings.profile.save")}
        </Button>
        <Link
          href={namespaceHref(profile.name)}
          className="flex items-center gap-1 text-sm text-fg-muted hover:text-fg hover:underline"
        >
          {t("settings.profile.viewProfile")}
          <ExternalLink size={13} />
        </Link>
      </div>

      {/* Below the action row so a save result never pushes it down right
          before the next click (DESIGN.md §8). */}
      {saveError && <Alert tone="negative">{saveError}</Alert>}
      {saved && <Alert tone="positive">{t("settings.profile.saved")}</Alert>}
    </form>
  );
}
