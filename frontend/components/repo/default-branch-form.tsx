"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Field, Select } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { updateRepo } from "@/lib/repo-admin";
import type { RepoKind } from "@/types/api";

/**
 * Switches which branch clone, the file list, README and lineage read from
 * (docs/dev/api-contract.md "Changing the default branch"). Shown on the
 * settings page only when `repo.can_admin` -- the same bar as transfer and
 * archive -- and, like transfer, replaced by a plain message while the
 * repository is archived rather than rendered disabled: the backend refuses
 * the request either way, so there is nothing a disabled control would let
 * the reader do.
 */
export function DefaultBranchForm({
  kind,
  ns,
  name,
  branches,
  defaultBranch,
  archived,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  branches: string[];
  defaultBranch: string;
  archived: boolean;
}) {
  const t = useT();
  const router = useRouter();

  const [selected, setSelected] = useState(defaultBranch);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Deleting a branch the picker was sitting on (RefsManager + router.refresh)
  // leaves `selected` naming something that is gone: the <select> renders
  // blank (no matching option) while still holding the old value, so Save
  // posts a revision the server no longer has. Same class as the tag-rev
  // fallback in refs-manager.tsx.
  const effective = branches.includes(selected)
    ? selected
    : branches.includes(defaultBranch)
      ? defaultBranch
      : (branches[0] ?? "");

  async function handleSave() {
    setSaving(true);
    setError(null);
    setSaved(false);
    const result = await updateRepo(kind, ns, name, { default_branch: effective });
    setSaving(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    setSaved(true);
    // Everything else on the page (branches shown elsewhere, the README
    // card, file counts) reads default_branch from the server, so refresh
    // rather than trying to patch the tree by hand (matches RepoDangerZone).
    router.refresh();
  }

  if (archived) {
    return (
      <p className="text-sm text-fg-subtle">{t("repo.settings.defaultBranch.blockedByArchive")}</p>
    );
  }

  if (branches.length === 0) {
    // An empty repository (no commits yet) has no branch to switch to.
    return <p className="text-sm text-fg-subtle">{t("repo.settings.defaultBranch.noBranches")}</p>;
  }

  return (
    <div className="flex max-w-sm flex-col gap-3">
      <Field
        label={t("repo.settings.defaultBranch.label")}
        hint={t("repo.settings.defaultBranch.hint")}
      >
        <Select
          value={effective}
          onChange={(e) => {
            setSelected(e.target.value);
            setSaved(false);
          }}
        >
          {branches.map((b) => (
            <option key={b} value={b}>
              {b}
            </option>
          ))}
        </Select>
      </Field>
      <Button
        variant="secondary"
        // Saving the branch that is already the default is not a no-op: the
        // API re-queues the reindex for it, which is how a switch whose
        // enqueue failed gets repaired (docs/dev/api-contract.md "Changing
        // the default branch"). Disabling the button here would make that
        // documented repair unreachable from the UI.
        disabled={saving}
        onClick={handleSave}
        className="self-start"
      >
        {saving ? t("repo.settings.defaultBranch.saving") : t("repo.settings.defaultBranch.save")}
      </Button>
      {/* Below the button, not above -- a stale error/success message must
          never push it down (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
      {saved && !error && <Alert tone="positive">{t("repo.settings.defaultBranch.saved")}</Alert>}
    </div>
  );
}
