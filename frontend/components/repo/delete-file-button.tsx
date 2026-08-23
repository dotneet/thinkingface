"use client";

import { Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { errorMessage } from "@/lib/api-error-message";
import { deleteFile } from "@/lib/edit";
import { useT } from "@/lib/i18n/client";
import { repoTreeHref } from "@/lib/paths";
import type { RepoKind } from "@/types/api";

/**
 * Deletes the file being viewed. Destructive and one click away from the
 * content, so it always goes through ConfirmDialog — never a bare button that
 * commits on the first click.
 *
 * `baseOid` is the blob the page was rendered from: the server refuses the
 * delete if the file has moved on since, so a stale tab cannot remove a
 * version its reader never saw.
 */
export function DeleteFileButton({
  kind,
  ns,
  name,
  rev,
  path,
  baseOid,
  lfs,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  /** File path segments from the repository root. */
  path: string[];
  baseOid: string;
  lfs: boolean;
}) {
  const t = useT();
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const filePath = path.join("/");

  async function confirm() {
    setError(null);
    setDeleting(true);
    const result = await deleteFile(kind, ns, name, rev, path, { base_oid: baseOid });
    setDeleting(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    setOpen(false);
    // The file is gone, so this page no longer exists: go to the directory
    // that held it.
    router.push(repoTreeHref(kind, ns, name, rev, path.slice(0, -1).join("/")));
    router.refresh();
  }

  return (
    <>
      <Button
        variant="danger"
        size="sm"
        onClick={() => {
          setError(null);
          setOpen(true);
        }}
      >
        <Trash2 size={14} />
        {t("repo.deleteFile.action")}
      </Button>
      <ConfirmDialog
        open={open}
        onClose={() => setOpen(false)}
        onConfirm={() => void confirm()}
        title={t("repo.deleteFile.title")}
        description={
          <div className="flex flex-col gap-3">
            <Alert tone="negative">{t("repo.deleteFile.body", { file: filePath, rev })}</Alert>
            {lfs && <p className="text-sm text-fg-muted">{t("repo.deleteFile.lfsNote")}</p>}
          </div>
        }
        confirmLabel={t("repo.deleteFile.confirm")}
        confirmingLabel={t("repo.deleteFile.deleting")}
        cancelLabel={t("repo.deleteFile.cancel")}
        confirming={deleting}
        error={error}
      />
    </>
  );
}
