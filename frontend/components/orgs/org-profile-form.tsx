"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, Select, Textarea } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { orgErrorKey, updateOrg } from "@/lib/orgs";
import type { MembersVisibility, Org } from "@/types/api";

/**
 * Profile and policy for one organisation (§7.1 PATCH /orgs/{org}, §4.1).
 * Every field is sent on save: PATCH treats an absent field as "leave alone",
 * and sending the whole form keeps a value the user cleared from silently
 * surviving.
 */
export function OrgProfileForm({ org }: { org: Org }) {
  const t = useT();
  const router = useRouter();
  const [displayName, setDisplayName] = useState(org.display_name);
  const [description, setDescription] = useState(org.description);
  const [website, setWebsite] = useState(org.website);
  const [avatarUrl, setAvatarUrl] = useState(org.avatar_url);
  const [membersVisibility, setMembersVisibility] = useState<MembersVisibility>(
    org.members_visibility,
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Any edit after a save invalidates the "saved" notice (and a stale
  // error) — otherwise it lingers on screen through further, unrelated
  // changes and reads as "this too has been saved" (mirrors
  // ProfileSettings.edit() in components/settings/profile-settings.tsx).
  function edit<T>(setter: (v: T) => void, value: T) {
    setter(value);
    setSaved(false);
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSaving(true);
    setError(null);
    setSaved(false);
    const result = await updateOrg(org.name, {
      display_name: displayName.trim(),
      description: description.trim(),
      website: website.trim(),
      avatar_url: avatarUrl.trim(),
      members_visibility: membersVisibility,
    });
    setSaving(false);
    if (!result.ok) {
      const key = orgErrorKey(result);
      setError(key ? t(key) : errorMessage(t, result));
      return;
    }
    setSaved(true);
    // Server Components elsewhere (the org header, the settings nav) render
    // this same data — refresh them so a renamed/redescribed org doesn't
    // look unsaved until the next full navigation.
    router.refresh();
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-col gap-6">
      <Card className="flex flex-col gap-4">
        <CardHeader>
          <CardTitle>{t("org.settings.profile.title")}</CardTitle>
        </CardHeader>
        <p className="-mt-2 text-sm text-fg-subtle">{t("org.settings.profile.description")}</p>

        <Field
          label={t("org.settings.profile.nameLabel")}
          hint={t("org.settings.profile.nameHint")}
        >
          <Input value={org.name} readOnly disabled className="font-mono" />
        </Field>
        <Field label={t("org.settings.profile.displayNameLabel")}>
          <Input value={displayName} onChange={(e) => edit(setDisplayName, e.target.value)} />
        </Field>
        <Field label={t("org.settings.profile.descriptionLabel")}>
          <Textarea
            value={description}
            onChange={(e) => edit(setDescription, e.target.value)}
            className="min-h-20"
          />
        </Field>
        <Field label={t("org.settings.profile.websiteLabel")}>
          <Input
            value={website}
            onChange={(e) => edit(setWebsite, e.target.value)}
            type="url"
            placeholder="https://example.com"
          />
        </Field>
        <Field
          label={t("org.settings.profile.avatarUrlLabel")}
          hint={t("org.settings.profile.avatarUrlHint")}
        >
          <Input
            value={avatarUrl}
            onChange={(e) => edit(setAvatarUrl, e.target.value)}
            type="url"
            placeholder="https://example.com/logo.png"
          />
        </Field>
      </Card>

      <Card className="flex flex-col gap-4">
        <CardHeader>
          <CardTitle>{t("org.settings.policy.title")}</CardTitle>
        </CardHeader>
        <p className="-mt-2 text-sm text-fg-subtle">{t("org.settings.policy.description")}</p>

        <Field
          label={t("org.settings.policy.membersVisibilityLabel")}
          hint={t("org.settings.policy.membersVisibilityHint")}
        >
          <Select
            value={membersVisibility}
            onChange={(e) =>
              edit(setMembersVisibility, e.target.value === "public" ? "public" : "members")
            }
          >
            <option value="members">{t("org.settings.policy.membersVisibilityMembers")}</option>
            <option value="public">{t("org.settings.policy.membersVisibilityPublic")}</option>
          </Select>
        </Field>
      </Card>

      <Button type="submit" variant="primary" disabled={saving} className="self-start px-4 py-2">
        {saving ? t("org.settings.profile.saving") : t("org.settings.profile.save")}
      </Button>

      {/* Below the save button so an error/success result never pushes it
          down right before the next click (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
      {saved && <Alert tone="positive">{t("org.settings.profile.saved")}</Alert>}
    </form>
  );
}
