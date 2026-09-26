"use client";

import { NotebookPen } from "lucide-react";
import { useState } from "react";

import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Markdown } from "@/components/ui/markdown";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import { SpinnerSlot } from "@/components/ui/spinner";
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

  const header = (
    <div className="flex min-h-8 items-center justify-between gap-2">
      <h2 className="text-sm font-semibold" title={t("experiments.note.description")}>
        {t("experiments.note.title")}
      </h2>
      {/* Always rendered once there is a note, so a save's spinner or the
          Edit button never shifts the note below (DESIGN.md §8.3). */}
      {!editing && note.trim() !== "" && (canWrite || saving) && (
        <div className="flex items-center gap-2">
          <SpinnerSlot active={saving} size={14} label={t("experiments.note.saving")} />
          {canWrite && (
            <Button size="sm" variant="secondary" onClick={startEditing} disabled={saving}>
              <NotebookPen size={14} />
              {t("experiments.note.edit")}
            </Button>
          )}
        </div>
      )}
    </div>
  );

  if (editing) {
    return (
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        {header}
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
    <section className="flex min-w-0 flex-col gap-2">
      {header}
      {note.trim() === "" ? (
        // One quiet line, not a full EmptyState: an absent note is the
        // common case and must not outweigh the config beside it.
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed border-border px-3 py-2.5">
          <span className="text-sm text-fg-subtle">
            {canWrite
              ? t("experiments.runPage.noteEmpty")
              : t("experiments.runPage.noteEmptyReadOnly")}
          </span>
          {canWrite && (
            <Button size="sm" variant="secondary" onClick={startEditing} disabled={saving}>
              <NotebookPen size={14} />
              {t("experiments.runPage.noteAdd")}
            </Button>
          )}
        </div>
      ) : (
        <div className="max-h-[32rem] overflow-y-auto rounded-lg border border-border bg-bg-raised px-4 py-3">
          <Markdown source={note} />
        </div>
      )}

      {/* Below the content and the Edit/Add button, not above — a failed
          save must never push either of them down (DESIGN.md §8). */}
      {error && <Alert tone="negative">{error}</Alert>}
    </section>
  );
}
