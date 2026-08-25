"use client";

import { FolderInput } from "lucide-react";
import { useRouter } from "next/navigation";
import { useEffect, useId, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import { renameFile } from "@/lib/edit";
import { useT } from "@/lib/i18n/client";
import { repoBlobHref } from "@/lib/paths";

import type { RepoKind } from "@/types/api";

/**
 * Renames — or moves — the file it is given, in a single commit.
 *
 * Renaming and moving are deliberately one control rather than two: the field
 * holds the file's whole path from the repository root, so editing the last
 * segment renames it and editing the directory part moves it. That is also why
 * there is no drag-and-drop here — a path is something you can read back,
 * correct and confirm, which a drop target is not.
 *
 * `baseOid` is the blob the page was rendered from. The server refuses the
 * rename if the file has moved on since, so a stale tab cannot move a version
 * its reader never saw — the same optimistic lock the edit and delete
 * endpoints take.
 */
export function RenameFileButton({
  kind,
  ns,
  name,
  rev,
  path,
  baseOid,
  variant = "button",
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  /** File path segments from the repository root. */
  path: string[];
  baseOid: string;
  /**
   * "button" is the file page's control, sitting beside Download and Delete;
   * "link" is the compact form the file table puts in its actions column,
   * where a full button in every row would out-shout the file names.
   */
  variant?: "button" | "link";
}) {
  const t = useT();
  const router = useRouter();
  const formId = useId();
  const currentPath = path.join("/");

  const [open, setOpen] = useState(false);
  const [newPath, setNewPath] = useState(currentPath);
  const [renaming, setRenaming] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // A reopen starts from the file's current path again, not from the previous
  // attempt's half-edited value or its error.
  useEffect(() => {
    if (open) {
      setNewPath(currentPath);
      setError(null);
    }
  }, [open, currentPath]);

  const trimmed = newPath.trim();
  const unchanged = trimmed === currentPath;

  async function submit() {
    if (trimmed === "" || unchanged || renaming) return;
    setRenaming(true);
    setError(null);
    const result = await renameFile(kind, ns, name, rev, path, {
      new_path: trimmed,
      base_oid: baseOid,
    });
    setRenaming(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    setOpen(false);
    // The old path no longer exists, so staying put would leave the reader on
    // a page that 404s on reload: follow the file to where it went.
    router.push(repoBlobHref(kind, ns, name, rev, result.data.path));
    router.refresh();
  }

  return (
    <>
      {variant === "link" ? (
        <Button
          variant="ghost"
          size="sm"
          // relative z-10: the table row is covered by the name link's
          // stretched ::after overlay, so a control inside it has to sit above
          // that to be clickable at all (see file-tree-table.tsx).
          className="relative z-10 text-accent"
          onClick={() => setOpen(true)}
        >
          <FolderInput size={12} />
          {t("repo.renameFile.action")}
        </Button>
      ) : (
        <Button variant="secondary" size="sm" onClick={() => setOpen(true)}>
          <FolderInput size={14} />
          {t("repo.renameFile.action")}
        </Button>
      )}
      {open && (
        <Dialog
          open={open}
          // Ignored while the rename is in flight, the same way the delete
          // dialog refuses to close mid-request: dismissing then would read as
          // a cancel for something that was never cancelled.
          onClose={() => {
            if (!renaming) setOpen(false);
          }}
          title={t("repo.renameFile.title")}
          footer={
            <>
              <Button type="button" onClick={() => setOpen(false)} disabled={renaming}>
                {t("repo.renameFile.cancel")}
              </Button>
              <Button
                type="submit"
                form={formId}
                variant="primary"
                disabled={renaming || trimmed === "" || unchanged}
              >
                {renaming ? t("repo.renameFile.renaming") : t("repo.renameFile.confirm")}
              </Button>
            </>
          }
          // Below the action row, so an error never shifts the button that
          // produced it out from under the pointer (DESIGN.md §8).
          footerNote={error && <Alert tone="negative">{error}</Alert>}
        >
          <form
            id={formId}
            className="flex flex-col gap-3 px-4 py-4"
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <p className="text-sm text-fg-muted">
              {t("repo.renameFile.body", { file: currentPath, rev })}
            </p>
            <Field label={t("repo.renameFile.pathLabel")} hint={t("repo.renameFile.pathHint")}>
              <Input
                autoFocus
                value={newPath}
                onChange={(e) => setNewPath(e.target.value)}
                placeholder={currentPath}
                disabled={renaming}
              />
            </Field>
          </form>
        </Dialog>
      )}
    </>
  );
}
