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
import { nearestExistingDir } from "@/lib/repos";
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
    if (!result.ok) {
      setDeleting(false);
      setError(errorMessage(t, result));
      return;
    }
    // deleting stays true from here on: the page is about to be left, and
    // while nearestExistingDir is still looking for where to go, the button
    // must not offer to delete the (already deleted) file a second time.
    setOpen(false);
    // The file is gone, so this page no longer exists: go to the directory
    // that held it -- or, since a directory that becomes empty is dropped
    // from the tree entirely (and that can cascade up through its own now-empty
    // parents), the nearest ancestor that is still actually there.
    const dest = await nearestExistingDir(kind, ns, name, rev, path.slice(0, -1));
    router.push(repoTreeHref(kind, ns, name, rev, dest.join("/")));
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
        // No `if (!deleting)` guard here any more: `confirming` below is the
        // same flag, and ConfirmDialog forwards it to Dialog's `busy`, which
        // now shuts Escape, the backdrop *and* the header × (the × slipped
        // past the old call-site guard entirely).
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
