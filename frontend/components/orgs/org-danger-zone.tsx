"use client";

import { Trash2 } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { namespaceHref } from "@/lib/namespace";
import { deleteOrg, orgErrorKey } from "@/lib/orgs";
import type { Org } from "@/types/api";

/**
 * Deleting an organisation, with the two brakes the design calls for (§5):
 * it is refused outright while repositories remain (the server answers 409
 * `has_repositories`; the button is disabled here so the reason is visible
 * before clicking), and the confirmation dialog asks for the name in full via
 * `ConfirmDialog`'s `requireText` (the same primitive `RepoDangerZone` uses,
 * see components/repo/repo-danger-zone.tsx). A failed delete stays open with
 * the error shown inline, like every other `ConfirmDialog` caller, instead of
 * closing and losing the typed name.
 */
export function OrgDangerZone({ org }: { org: Org }) {
  const t = useT();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const blocked = org.num_repos > 0;

  async function handleDelete() {
    setDeleting(true);
    setError(null);
    const result = await deleteOrg(org.name);
    setDeleting(false);
    if (!result.ok) {
      const key = orgErrorKey(result);
      setError(key ? t(key) : errorMessage(t, result));
      return;
    }
    router.push("/settings/organizations");
    router.refresh();
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h2 className="text-sm font-semibold text-fg">{t("org.settings.danger.title")}</h2>
        <p className="mt-1 text-sm text-fg-subtle">{t("org.settings.danger.description")}</p>
      </div>

      {blocked && (
        <Alert
          tone="warning"
          role="presentation"
          title={t(
            org.num_repos === 1
              ? "org.settings.danger.blockedTitleOne"
              : "org.settings.danger.blockedTitleOther",
            { count: org.num_repos, name: org.name },
          )}
        >
          <p>{t("org.settings.danger.blockedDescription")}</p>
          <Link href={namespaceHref(org.name)} className="mt-1 w-fit text-accent hover:underline">
            {t("org.settings.danger.viewRepositories")}
          </Link>
        </Alert>
      )}

      <Button
        variant="danger"
        disabled={blocked || deleting}
        onClick={() => {
          setError(null);
          setOpen(true);
        }}
        className="self-start border-negative/40 px-4 py-2"
      >
        <Trash2 size={15} />
        {deleting ? t("org.settings.danger.deleting") : t("org.settings.danger.delete")}
      </Button>

      <ConfirmDialog
        open={open}
        onClose={() => setOpen(false)}
        onConfirm={handleDelete}
        title={t("org.settings.danger.confirmTitle", { name: org.name })}
        description={
          <Alert tone="negative" title={t("org.settings.danger.confirmWarningTitle")}>
            {t("org.settings.danger.confirmWarning", { name: org.name })}
          </Alert>
        }
        requireText={org.name}
        confirmLabel={t("org.settings.danger.confirmSubmit")}
        confirmingLabel={t("org.settings.danger.deleting")}
        cancelLabel={t("org.settings.danger.confirmCancel")}
        confirming={deleting}
        error={error}
      />
    </div>
  );
}
