"use client";

import { ChevronDown, FilePlus2, Plus, Upload } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { UploadDialog } from "@/components/repo/upload-dialog";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { Field, Input } from "@/components/ui/field";
import { useT } from "@/lib/i18n/client";
import { repoEditHref } from "@/lib/paths";
import type { RepoKind } from "@/types/api";

/**
 * The "Add file" menu on the file tree: the two ways content gets into a
 * repository from a browser. Rendered only where a commit is actually
 * possible — the caller checks `repo.can_write` and that the revision is a
 * branch — so this component never has to reason about permissions.
 */
export function AddFileMenu({
  kind,
  ns,
  name,
  rev,
  path,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  /** The directory being browsed, as segments from the repository root. */
  path: string[];
}) {
  const t = useT();
  const router = useRouter();
  const [newFileOpen, setNewFileOpen] = useState(false);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [newPath, setNewPath] = useState("");

  const trimmed = newPath.trim().replace(/^\/+/, "");

  function createFile() {
    if (!trimmed) return;
    setNewFileOpen(false);
    setNewPath("");
    // ?new=1 tells the editor that a path with nothing at it is the point,
    // rather than a 404 — see components/repo-pages/repo-edit.tsx.
    router.push(`${repoEditHref(kind, ns, name, rev, [...path, trimmed].join("/"))}?new=1`);
  }

  return (
    <>
      <DropdownMenu
        align="end"
        trigger={({ toggle, triggerProps }) => (
          <Button variant="secondary" onClick={toggle} {...triggerProps}>
            <Plus size={14} />
            {t("repo.upload.menuLabel")}
            <ChevronDown size={14} />
          </Button>
        )}
      >
        {({ close }) => (
          <>
            <DropdownMenuItem
              onClick={() => {
                close();
                setNewFileOpen(true);
              }}
            >
              <FilePlus2 size={14} />
              {t("repo.upload.menuNewFile")}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => {
                close();
                setUploadOpen(true);
              }}
            >
              <Upload size={14} />
              {t("repo.upload.menuUpload")}
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenu>

      <Dialog
        open={newFileOpen}
        onClose={() => setNewFileOpen(false)}
        title={t("repo.upload.newFileTitle")}
        footer={
          <>
            <Button onClick={() => setNewFileOpen(false)}>{t("repo.editor.cancel")}</Button>
            <Button variant="primary" onClick={createFile} disabled={!trimmed}>
              {t("repo.upload.newFileConfirm")}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4 px-4 py-4">
          <p className="text-sm text-fg-muted">{t("repo.upload.newFileBody")}</p>
          <Field
            label={t("repo.upload.newFilePathLabel")}
            hint={[...path, t("repo.upload.newFilePathPlaceholder")].join("/")}
          >
            <Input
              autoFocus
              value={newPath}
              onChange={(e) => setNewPath(e.target.value)}
              placeholder={t("repo.upload.newFilePathPlaceholder")}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  createFile();
                }
              }}
            />
          </Field>
        </div>
      </Dialog>

      <UploadDialog
        kind={kind}
        ns={ns}
        name={name}
        rev={rev}
        dir={path}
        open={uploadOpen}
        onClose={() => setUploadOpen(false)}
      />
    </>
  );
}
