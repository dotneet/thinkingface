"use client";

import { ChevronDown, FilePlus2, Plus, Upload } from "lucide-react";
import { useRouter } from "next/navigation";
import { useId, useState } from "react";
import { UploadDialog } from "@/components/repo/upload-dialog";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { Field, Input } from "@/components/ui/field";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";
import { repoEditHref, resolveNewFilePath } from "@/lib/paths";
import type { RepoKind } from "@/types/api";

/**
 * The "Add file" menu on the file tree: the two ways content gets into a
 * repository from a browser. Rendered only where a commit is actually
 * possible — the caller checks `repo.can_write` and that the revision is a
 * branch — so this component never has to reason about permissions.
 */
/**
 * The example path shown before anything is typed. It goes through
 * resolveNewFilePath like a real entry would, so the preview cannot drift
 * from the behaviour it is previewing.
 */
function exampleNewFilePath(dir: string[], t: ReturnType<typeof useT>): string {
  const example = resolveNewFilePath(dir, t("repo.upload.newFilePathPlaceholder"));
  return example.status === "ok" ? example.path : t("repo.upload.newFilePathPlaceholder");
}

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
  const pathNoteId = useId();
  const [newFileOpen, setNewFileOpen] = useState(false);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [newPath, setNewPath] = useState("");

  // What will actually be created: the typed path is relative to the
  // directory being browsed (see resolveNewFilePath), and the dialog says so
  // and shows the result rather than leaving the user to work it out.
  const resolved = resolveNewFilePath(path, newPath);

  // One always-present line under the input, carrying whichever of the three
  // states applies. It never disappears, so nothing below it moves as the
  // user types (DESIGN.md §8), and an unusable path explains itself instead
  // of leaving a disabled button unexplained (§9).
  const pathNote =
    resolved.status === "invalid"
      ? {
          tone: "negative" as const,
          text:
            resolved.issue === "gitDirectory"
              ? t("repo.upload.newFileGitDirectory")
              : t("repo.upload.newFileRelativeSegment"),
        }
      : {
          tone: "subtle" as const,
          text: t("repo.upload.newFileResolved", {
            // Before anything is typed, preview the placeholder in the same
            // shape the real answer will take.
            path: resolved.status === "ok" ? resolved.path : exampleNewFilePath(path, t),
          }),
        };

  function createFile() {
    if (resolved.status !== "ok") return;
    setNewFileOpen(false);
    setNewPath("");
    // ?new=1 tells the editor that a path with nothing at it is the point,
    // rather than a 404 — see components/repo-pages/repo-edit.tsx.
    router.push(`${repoEditHref(kind, ns, name, rev, resolved.path)}?new=1`);
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
            <Button variant="primary" onClick={createFile} disabled={resolved.status !== "ok"}>
              {t("repo.upload.newFileConfirm")}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4 px-4 py-4">
          <p className="text-sm text-fg-muted">
            {path.length > 0
              ? t("repo.upload.newFileBodyIn", { dir: path.join("/") })
              : t("repo.upload.newFileBody")}
          </p>
          <Field label={t("repo.upload.newFilePathLabel")}>
            <Input
              autoFocus
              value={newPath}
              onChange={(e) => setNewPath(e.target.value)}
              placeholder={t("repo.upload.newFilePathPlaceholder")}
              aria-invalid={resolved.status === "invalid"}
              aria-describedby={pathNoteId}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  createFile();
                }
              }}
            />
          </Field>
          {/* Outside the Field so the message can carry a tone: Field's own
              hint slot is always subtle, and "this path can't be used" has to
              read as a refusal rather than as guidance. */}
          <p
            id={pathNoteId}
            className={cn(
              "text-xs font-medium",
              pathNote.tone === "negative" ? "text-negative" : "text-fg-subtle",
            )}
          >
            {pathNote.text}
          </p>
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
