"use client";

import { LoaderCircle } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Field, Input, Textarea } from "@/components/ui/field";
import type { MarkdownLinkContext } from "@/components/ui/markdown";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import { errorMessage } from "@/lib/api-error-message";
import { editFile } from "@/lib/edit";
import { useT } from "@/lib/i18n/client";
import type { RepoKind } from "@/types/api";

const MARKDOWN_EXT = /\.(md|markdown)$/i;

export function FileEditor({
  kind,
  ns,
  name,
  rev,
  path,
  fileName,
  initialContent,
  baseOid,
  blobHref,
  assetBaseUrl,
  repoRootUrl,
  linkContext,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
  fileName: string;
  initialContent: string;
  baseOid: string;
  blobHref: string;
  assetBaseUrl: string;
  repoRootUrl: string;
  linkContext?: MarkdownLinkContext;
}) {
  const t = useT();
  const router = useRouter();
  const isMarkdown = MARKDOWN_EXT.test(fileName);

  const [content, setContent] = useState(initialContent);
  const [message, setMessage] = useState("");
  const [description, setDescription] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);

  const dirty = content !== initialContent;

  // Warn on tab close / hard navigation away from an unsaved edit. The
  // listener is only attached while dirty so it doesn't interfere with normal
  // navigation once the content matches what's saved. It does NOT cover
  // in-app navigation — `beforeunload` never fires for a client-side route
  // change — which is why Cancel goes through ConfirmDialog below.
  useEffect(() => {
    if (!dirty) return;
    function handleBeforeUnload(e: BeforeUnloadEvent) {
      e.preventDefault();
      e.returnValue = "";
    }
    window.addEventListener("beforeunload", handleBeforeUnload);
    return () => window.removeEventListener("beforeunload", handleBeforeUnload);
  }, [dirty]);

  async function handleSubmit(e?: React.FormEvent) {
    e?.preventDefault();
    setError(null);
    setConflict(false);
    setSubmitting(true);
    const result = await editFile(kind, ns, name, rev, path, {
      content,
      // Send the message as typed, even if empty — the server applies its
      // own default commit message rather than us sending a placeholder.
      message,
      description: description.trim() || undefined,
      base_oid: baseOid,
    });
    setSubmitting(false);
    if (!result.ok) {
      if (result.status === 409) {
        setConflict(true);
        setError(t("repo.editor.conflict"));
      } else {
        setError(errorMessage(t, result));
      }
      // Keep the in-progress edit in place either way — don't clear content
      // on failure so the user doesn't lose their work.
      return;
    }
    router.push(blobHref);
    router.refresh();
  }

  return (
    <>
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <MarkdownEditor
          value={content}
          onChange={setContent}
          onSubmit={() => {
            if (dirty && !submitting) void handleSubmit();
          }}
          markdown={isMarkdown}
          previewProps={{
            assetBaseUrl,
            repoRootUrl,
            linkContext,
            stripFrontmatter: true,
          }}
          disabled={submitting}
          ariaLabel={t("repo.editor.editAria", { file: fileName })}
        />

        <Field label={t("repo.editor.commitMessageLabel")}>
          <Input
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder={t("repo.editor.commitMessagePlaceholder", { file: fileName })}
          />
        </Field>

        <Field label={t("repo.editor.descriptionLabel")}>
          <Textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            className="min-h-20"
            placeholder={t("repo.editor.descriptionPlaceholder")}
          />
        </Field>

        <div className="flex items-center gap-3">
          <Button
            type="submit"
            variant="primary"
            disabled={submitting || !dirty}
            className="px-4 py-2"
          >
            {submitting && <LoaderCircle size={14} className="animate-spin" />}
            {submitting ? t("repo.editor.committing") : t("repo.editor.commit")}
          </Button>
          {dirty ? (
            <Button
              variant="ghost"
              className="text-sm text-fg-subtle hover:text-fg"
              onClick={() => setConfirmDiscard(true)}
            >
              {t("repo.editor.cancel")}
            </Button>
          ) : (
            <Link href={blobHref} className="text-sm text-fg-subtle hover:text-fg hover:underline">
              {t("repo.editor.cancel")}
            </Link>
          )}
        </div>

        {/* Below the Commit/Cancel row, not above the editor — a conflict or
            commit failure here must never push the button just clicked down
            out from under the pointer (DESIGN.md §8). */}
        {error && <Alert tone={conflict ? "warning" : "negative"}>{error}</Alert>}
      </form>

      {/* Outside the form: the dialog renders its own <form> for the confirm
          buttons, and nested forms are invalid HTML (React refuses to
          hydrate them). */}
      <ConfirmDialog
        open={confirmDiscard}
        onClose={() => setConfirmDiscard(false)}
        onConfirm={() => {
          setConfirmDiscard(false);
          router.push(blobHref);
        }}
        title={t("repo.editor.discardTitle")}
        description={
          <Alert tone="warning">{t("repo.editor.discardBody", { file: fileName })}</Alert>
        }
        confirmLabel={t("repo.editor.discardConfirm")}
        cancelLabel={t("repo.editor.keepEditing")}
      />
    </>
  );
}
