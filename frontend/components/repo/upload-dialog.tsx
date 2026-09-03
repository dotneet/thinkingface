"use client";

import { FileUp, X } from "lucide-react";
import { useRouter } from "next/navigation";
import { useRef, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Field, Input } from "@/components/ui/field";
import { FileDropZone } from "@/components/ui/file-drop";
import { ProgressBar } from "@/components/ui/progress-bar";
import { errorMessage } from "@/lib/api-error-message";
import { formatBytes } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { evaluateUploadSizes, uploadFiles } from "@/lib/upload";
import type { RepoKind } from "@/types/api";

/**
 * Mirrors maxUploadFiles in backend/internal/api/upload.go. Checked here as
 * well so picking 200 files fails in the dialog instead of after the first
 * one has already been streamed to the server.
 */
const MAX_FILES = 64;

/**
 * A picked file plus a stable id. Two files with the same name and size are
 * indistinguishable otherwise, and a list keyed by position re-mounts every
 * row below whichever one is removed.
 */
type PickedFile = { id: string; file: File };

let nextPickedId = 0;

export function UploadDialog({
  kind,
  ns,
  name,
  rev,
  dir,
  open,
  onClose,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  /** Directory the files land in, as path segments from the repository root. */
  dir: string[];
  open: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const router = useRouter();

  const [files, setFiles] = useState<PickedFile[]>([]);
  const [message, setMessage] = useState("");
  const [uploading, setUploading] = useState(false);
  const [sent, setSent] = useState(0);
  const [error, setError] = useState<string | null>(null);

  // The in-flight request, so Cancel can actually stop it instead of only
  // refusing to close while it runs -- this repository's files run to
  // gigabytes (lib/upload.ts), and closing the tab used to be the only way
  // out of one. `cancelledRef` distinguishes that deliberate abort from every
  // other way uploadFiles can resolve `{ ok: false }`: the abort's own
  // resolution arrives after `close()` has already reset and closed the
  // dialog, and without the flag it would go on to set an error message
  // nobody is looking at into a dialog that looks freshly opened next time.
  const abortRef = useRef<AbortController | null>(null);
  const cancelledRef = useRef(false);

  const total = files.reduce((sum, f) => sum + f.file.size, 0);
  const dirLabel = dir.length > 0 ? dir.join("/") : "/";

  function reset() {
    setFiles([]);
    setMessage("");
    setSent(0);
    setError(null);
  }

  function close() {
    if (uploading) {
      cancelledRef.current = true;
      abortRef.current?.abort();
    }
    reset();
    onClose();
  }

  function pathOf(file: File): string {
    return [...dir, file.name].join("/");
  }

  function addFiles(picked: File[]) {
    setError(null);
    setFiles((current) => {
      // Two picks landing on the same target path is not two files, it's one
      // file picked twice: the second part would just replace the first in
      // the commit (gitrepo/commit.go, last op wins), but silently sending
      // it twice doubles the upload and mislabels the commit ("Upload 2
      // files" for what is really one). Drop it here instead of sending it.
      const existingPaths = new Set(current.map(({ file }) => pathOf(file)));
      const additions: PickedFile[] = [];
      let duplicatePath: string | null = null;
      for (const file of picked) {
        const path = pathOf(file);
        if (existingPaths.has(path)) {
          duplicatePath = duplicatePath ?? path;
          continue;
        }
        existingPaths.add(path);
        additions.push({ id: `f${nextPickedId++}`, file });
      }
      if (additions.length === 0) {
        if (duplicatePath) setError(t("repo.upload.duplicatePath", { path: duplicatePath }));
        return current;
      }
      const next = [...current, ...additions];
      if (next.length > MAX_FILES) {
        setError(t("repo.upload.tooMany", { count: MAX_FILES }));
        return current;
      }
      // Mirrors backend/internal/api/upload.go's size limits so a request
      // that is certain to be refused fails here instead of after every byte
      // has been streamed to the server (see lib/upload.ts).
      const issue = evaluateUploadSizes(next.map(({ file }) => file));
      if (issue) {
        setError(
          issue.type === "fileTooLarge"
            ? t("repo.upload.fileTooLarge", {
                file: issue.fileName,
                limit: formatBytes(issue.limit),
              })
            : t("repo.upload.inlineTotalTooLarge", { limit: formatBytes(issue.limit) }),
        );
        return current;
      }
      if (duplicatePath) setError(t("repo.upload.duplicatePath", { path: duplicatePath }));
      return next;
    });
  }

  async function submit() {
    if (files.length === 0 || uploading) return;
    setError(null);
    setSent(0);
    setUploading(true);
    cancelledRef.current = false;
    const controller = new AbortController();
    abortRef.current = controller;
    const result = await uploadFiles(
      kind,
      ns,
      name,
      rev,
      files.map(({ file }) => ({ path: pathOf(file), file })),
      {
        // Sent as typed: an empty message means the server applies its own
        // default rather than us inventing a placeholder.
        message,
        onProgress: (loaded) => setSent(loaded),
        signal: controller.signal,
      },
    );
    abortRef.current = null;
    setUploading(false);
    if (cancelledRef.current) {
      // `close()` already reset and closed the dialog when Cancel was
      // clicked; this request settling afterwards has nothing left to do.
      cancelledRef.current = false;
      return;
    }
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    reset();
    onClose();
    router.refresh();
  }

  return (
    <Dialog
      open={open}
      onClose={close}
      // `close` already refuses while uploading; this states the same rule to
      // the primitive so the header × is disabled and reads as unavailable
      // rather than silently doing nothing.
      busy={uploading}
      title={t("repo.upload.title")}
      footer={
        <>
          {/* Not `disabled={uploading}` any more: while uploading, this is
              the only surviving way out (Escape/backdrop/× are all held by
              `busy` above), so it stays clickable and `close()` aborts the
              in-flight request before it resets and closes. */}
          <Button onClick={close}>
            {uploading
              ? t("repo.upload.cancelUploading")
              : /* Reuses the editor's "Cancel" — same word, same meaning, and
                   it keeps one translation of it rather than three. */
                t("repo.editor.cancel")}
          </Button>
          <Button
            variant="primary"
            onClick={() => void submit()}
            disabled={uploading || files.length === 0}
          >
            {uploading ? t("repo.upload.uploading") : t("repo.upload.submit")}
          </Button>
        </>
      }
      footerNote={error ? <Alert tone="negative">{error}</Alert> : undefined}
    >
      <div className="flex flex-col gap-4 px-4 py-4">
        <FileDropZone
          onFiles={addFiles}
          disabled={uploading}
          label={t("repo.upload.dropLabel")}
          hint={t("repo.upload.dropHint", { dir: dirLabel, rev })}
          browseHint={t("repo.upload.browseHint")}
        />
        {/* Always rendered, not only once a file is picked: this dialog never
            sees the directory's current contents (it would need the tree
            listing threaded in from the page that opens it), so there is no
            selection-by-selection check to run -- only this standing notice
            that a same-named file already in the repository will be
            replaced. */}
        <p className="text-xs font-medium text-fg-subtle">{t("repo.upload.overwriteNote")}</p>

        {files.length === 0 ? (
          <EmptyState
            icon={FileUp}
            title={t("repo.upload.emptyTitle")}
            description={t("repo.upload.emptyDescription")}
          />
        ) : (
          <div className="flex flex-col gap-2">
            <p className="text-xs font-medium text-fg-subtle">
              {(files.length === 1
                ? t("repo.upload.selectedOne", { count: files.length })
                : t("repo.upload.selectedOther", { count: files.length })) +
                " · " +
                t("repo.upload.totalSize", { size: formatBytes(total) })}
            </p>
            <ul className="flex flex-col rounded-lg border border-border">
              {files.map(({ id, file }) => (
                <li
                  key={id}
                  className="flex items-center gap-2 border-b border-border px-3 py-2 text-sm last:border-0"
                >
                  <span className="truncate font-mono text-fg">{pathOf(file)}</span>
                  <span className="tabular-nums ml-auto shrink-0 text-fg-subtle">
                    {formatBytes(file.size)}
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={t("repo.upload.remove", { file: file.name })}
                    disabled={uploading}
                    onClick={() => setFiles((current) => current.filter((f) => f.id !== id))}
                  >
                    <X size={14} />
                  </Button>
                </li>
              ))}
            </ul>
            <p className="text-xs font-medium text-fg-subtle">{t("repo.upload.lfsNote")}</p>
          </div>
        )}

        <Field label={t("repo.upload.commitMessageLabel")}>
          <Input
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder={t("repo.upload.commitMessagePlaceholder")}
            disabled={uploading}
          />
        </Field>

        {/* The progress row keeps its space whether or not an upload is
            running, so starting one never moves the buttons under the
            pointer (DESIGN.md §8). */}
        <div className="flex flex-col gap-1">
          <ProgressBar
            value={total > 0 ? sent / total : 0}
            label={t("repo.upload.progressLabel")}
            className={uploading ? undefined : "opacity-0"}
          />
          <p className="text-xs font-medium text-fg-subtle">
            {uploading
              ? t("repo.upload.progressCount", {
                  // `sent` is xhr.upload.loaded: bytes on the wire, which
                  // includes the multipart boundaries and the `path`/
                  // `message` fields alongside the file bytes `total` counts.
                  // It legitimately runs past `total` before the request
                  // finishes, which read as "12 B of 3.4 kB sent" once
                  // formatted — clamp what's shown, not what's tracked, so
                  // the count never claims to have sent more than there was.
                  done: formatBytes(total > 0 ? Math.min(sent, total) : sent),
                  total: formatBytes(total),
                })
              : " "}
          </p>
        </div>
      </div>
    </Dialog>
  );
}
