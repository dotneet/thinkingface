"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, Textarea } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { updateRepo } from "@/lib/repo-admin";
import type { RepoKind } from "@/types/api";

/** The server's own ceiling (maxDescriptionRunes in backend/internal/api). */
const MAX_DESCRIPTION_CHARS = 1024;

/**
 * Edits the one-line description shown in listings and at the top of the
 * repository page. It was previously typed once at creation and never again.
 *
 * The card note under the field is not decoration: the README's YAML
 * frontmatter is still the source of truth whenever it carries a
 * `description`, and the post-push indexer overwrites this field with it on
 * every push. What changed is that a *cardless* README no longer wipes what
 * was typed here — an empty card description now means "the card said
 * nothing" rather than "the description is empty". Somebody who fills this in
 * and then pushes a README with its own description needs to know which of the
 * two they will see afterwards.
 */
export function RepoDescriptionForm({
  kind,
  ns,
  name,
  description,
  archived,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  description: string;
  archived: boolean;
}) {
  const t = useT();
  const router = useRouter();

  const [value, setValue] = useState(description);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const trimmed = value.trim();

  async function handleSave() {
    setSaving(true);
    setError(null);
    setSaved(false);
    const result = await updateRepo(kind, ns, name, { description: trimmed });
    setSaving(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    setSaved(true);
    // The description is shown by the header and the tabs on this same page,
    // so let the server re-render them rather than patching them by hand.
    router.refresh();
  }

  if (archived) {
    return (
      <p className="text-sm text-fg-subtle">{t("repo.settings.description.blockedByArchive")}</p>
    );
  }

  return (
    <div className="flex max-w-xl flex-col gap-3">
      <Field
        label={t("repo.settings.description.label")}
        hint={t("repo.settings.description.cardNote")}
      >
        <Textarea
          rows={3}
          value={value}
          maxLength={MAX_DESCRIPTION_CHARS}
          onChange={(e) => {
            setValue(e.target.value);
            setSaved(false);
          }}
          placeholder={t("repo.settings.description.placeholder")}
          disabled={saving}
        />
      </Field>
      <Button
        variant="secondary"
        // Not disabled when the value is unchanged: saving the same text is a
        // harmless no-op, and the alternative is a button that looks broken to
        // anyone who edited the field and edited it back.
        disabled={saving}
        onClick={handleSave}
        className="self-start"
      >
        {saving ? t("repo.settings.description.saving") : t("repo.settings.description.save")}
      </Button>
      {/* Below the button, so neither message pushes it down (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
      {saved && !error && <Alert tone="positive">{t("repo.settings.description.saved")}</Alert>}
    </div>
  );
}
