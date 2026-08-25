"use client";

import { NotebookPen } from "lucide-react";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Markdown } from "@/components/ui/markdown";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import { Spinner } from "@/components/ui/spinner";
import { useT } from "@/lib/i18n/client";

/**
 * The run's note: free-form Markdown a person writes about what the run was
 * for and what it showed.
 *
 * It rides the same PATCH as tags / archived / baseline, which is what makes
 * it survive re-indexing — the ingest and parquet paths never write the
 * column, so a re-index of the project leaves the note in place.
 */
export function RunNoteCard({
  note,
  canWrite,
  saving,
  error,
  onSave,
}: {
  note: string;
  /** Viewer has write access to the backing dataset repository. */
  canWrite: boolean;
  saving: boolean;
  /** Message from the last failed save, if any. */
  error?: string;
  /**
   * Resolves once the save attempt has finished, to `true` on success. The
   * editor only leaves edit mode on `true` — mirrors `RunTagsDialog`, which
   * closes only from the mutation's `onSuccess` rather than the instant the
   * request is fired, so a failed save leaves the draft exactly as typed
   * (the same "keep the in-progress edit on failure" rule `file-editor.tsx`
   * follows for commits).
   */
  onSave: (note: string) => Promise<boolean>;
}) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(note);

  function startEditing() {
    setDraft(note);
    setEditing(true);
  }

  async function save() {
    const ok = await onSave(draft);
    if (ok) setEditing(false);
  }

  if (editing) {
    return (
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <MarkdownEditor
          value={draft}
          onChange={setDraft}
          onSubmit={() => {
            if (!saving) void save();
          }}
          // The card is opened specifically to write, so focus starts here.
          autoFocus
          markdown
          minHeightClassName="min-h-[40vh]"
          placeholder={t("experiments.note.placeholder")}
          ariaLabel={t("experiments.note.editAria")}
          disabled={saving}
        />
        <div className="flex items-center justify-end gap-2">
          <span className="mr-auto text-xs font-medium text-fg-subtle">
            {t("experiments.note.hint")}
          </span>
          <Button onClick={() => setEditing(false)} disabled={saving}>
            {t("experiments.note.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={saving}>
            {t("experiments.note.save")}
          </Button>
        </div>
        {/* Below the Save/Cancel row, not above it — a failed save must never
            push either control down (DESIGN.md §8). The draft above stays
            untouched, so a failure never costs what was typed. */}
        {error && <Alert tone="negative">{error}</Alert>}
      </form>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {note.trim() === "" ? (
        <EmptyState
          icon={NotebookPen}
          title={t("experiments.note.emptyTitle")}
          description={
            canWrite
              ? t("experiments.note.emptyDescription")
              : t("experiments.note.emptyReadOnlyDescription")
          }
          action={
            canWrite ? (
              <Button variant="secondary" onClick={startEditing}>
                <NotebookPen size={16} />
                {t("experiments.note.add")}
              </Button>
            ) : undefined
          }
        />
      ) : (
        <div className="rounded-lg border border-border bg-bg-raised p-4">
          <Markdown source={note} />
        </div>
      )}

      {(canWrite || saving) && note.trim() !== "" && (
        <div className="flex items-center justify-end gap-2">
          {saving && <Spinner size={14} label={t("experiments.note.saving")} />}
          {canWrite && (
            <Button size="sm" variant="secondary" onClick={startEditing} disabled={saving}>
              <NotebookPen size={14} />
              {t("experiments.note.edit")}
            </Button>
          )}
        </div>
      )}

      {/* Below the content and the Edit/Add button, not above — a failed
          save must never push either of them down (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
    </div>
  );
}
