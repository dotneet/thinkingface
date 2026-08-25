"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { repoBase } from "@/lib/paths";
import { updateRepo } from "@/lib/repo-admin";
import { type NameError, validateName } from "@/lib/validation";
import type { RepoKind } from "@/types/api";

/** Maps validateName error codes onto this form's messages. */
const NAME_ERROR_KEYS: Partial<Record<NameError, MessageKey>> = {
  invalid: "repo.settings.rename.errors.nameInvalid",
  gitSuffix: "repo.settings.rename.errors.nameGitSuffix",
};

/**
 * Renames the repository inside the namespace it already lives in.
 *
 * It has its own section for a reason: the only way to rename a repository
 * used to be the optional "new name" field on the *transfer* form, so wanting
 * to fix a typo in a name took someone through a screen about giving the
 * repository away, complete with a typed confirmation. Renaming and handing
 * over ownership are different decisions and now look different.
 *
 * There is no typed confirmation here, and that is deliberate: a rename leaves
 * a redirect behind (exactly as a transfer does), so old URLs, clone remotes
 * and model-card references keep resolving. It is undone by renaming back.
 */
export function RenameRepoForm({
  kind,
  ns,
  name,
  archived,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  archived: boolean;
}) {
  const t = useT();
  const router = useRouter();

  const [newName, setNewName] = useState(name);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const trimmed = newName.trim();

  async function handleSave() {
    if (trimmed === "" || trimmed === name) return;
    // Checked here rather than on every keystroke: a name is invalid for most
    // of the time it is being typed, and a field that scolds mid-word is
    // noise. The server validates it again regardless.
    const nameError = validateName(trimmed);
    const key = nameError ? NAME_ERROR_KEYS[nameError] : undefined;
    if (key) {
      setError(t(key));
      return;
    }
    setSaving(true);
    setError(null);
    const result = await updateRepo(kind, ns, name, { name: trimmed });
    setSaving(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    // Every URL on this page still spells the old name, so the settings page
    // has to be re-entered at the new one rather than refreshed in place.
    router.push(`${repoBase(kind, ns, result.data.repo.name)}/settings`);
    router.refresh();
  }

  if (archived) {
    return <p className="text-sm text-fg-subtle">{t("repo.settings.rename.blockedByArchive")}</p>;
  }

  return (
    <div className="flex max-w-sm flex-col gap-3">
      <Field label={t("repo.settings.rename.label")} hint={t("repo.settings.rename.hint")}>
        <Input
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          placeholder={name}
          disabled={saving}
        />
      </Field>
      <Button
        variant="secondary"
        disabled={saving || trimmed === "" || trimmed === name}
        onClick={handleSave}
        className="self-start"
      >
        {saving ? t("repo.settings.rename.saving") : t("repo.settings.rename.save")}
      </Button>
      {/* Below the button, so a stale error never pushes it down
          (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
    </div>
  );
}
