"use client";

import { FileUp, X } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
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
import { uploadFiles } from "@/lib/upload";
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

  const total = files.reduce((sum, f) => sum + f.file.size, 0);
  const dirLabel = dir.length > 0 ? dir.join("/") : "/";

  function reset() {
    setFiles([]);
    setMessage("");
    setSent(0);
    setError(null);
  }

  function close() {
    if (uploading) return;
    reset();
    onClose();
  }

  function addFiles(picked: File[]) {
    setError(null);
    setFiles((current) => {
      const next = [...current, ...picked.map((file) => ({ id: `f${nextPickedId++}`, file }))];
      if (next.length > MAX_FILES) {
        setError(t("repo.upload.tooMany", { count: MAX_FILES }));
        return current;
      }
      return next;
    });
  }

  async function submit() {
    if (files.length === 0 || uploading) return;
    setError(null);
    setSent(0);
    setUploading(true);
    const result = await uploadFiles(
      kind,
      ns,
      name,
      rev,
      files.map(({ file }) => ({ path: [...dir, file.name].join("/"), file })),
      {
        // Sent as typed: an empty message means the server applies its own
        // default rather than us inventing a placeholder.
        message,
        onProgress: (loaded) => setSent(loaded),
      },
    );
    setUploading(false);
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
      title={t("repo.upload.title")}
      footer={
        <>
          <Button onClick={close} disabled={uploading}>
            {/* Reuses the editor's "Cancel" — same word, same meaning, and
                it keeps one translation of it rather than three. */}
            {t("repo.editor.cancel")}
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
                  <span className="truncate font-mono text-fg">
                    {[...dir, file.name].join("/")}
                  </span>
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
                  done: formatBytes(sent),
                  total: formatBytes(total),
                })
              : " "}
          </p>
        </div>
      </div>
    </Dialog>
  );
}
