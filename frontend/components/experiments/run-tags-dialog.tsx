"use client";

import { useEffect, useId, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { SpinnerSlot } from "@/components/ui/spinner";
import { useT } from "@/lib/i18n/client";
import { parseTagInput } from "@/lib/run-compare";
import type { ExpRun } from "@/types/api";

/**
 * Tag editor for one run. Tags are typed as a comma-separated list — the same
 * way they read in the table — and normalised on the way out, so the preview
 * below the field is exactly what will be stored.
 */
export function RunTagsDialog({
  run,
  open,
  saving,
  error,
  onClose,
  onSave,
}: {
  run: ExpRun | null;
  open: boolean;
  saving: boolean;
  /**
   * Failure of the save this dialog started. Required for the dialog to be
   * usable at all: it only closes from the mutation's onSuccess, so a failed
   * PATCH leaves it open with nothing to show — the page-level Alert that used
   * to be the only report sits behind the native <dialog>'s backdrop, and
   * re-clicking Save just fires the same doomed request again. Same contract
   * as ConfirmDialog's `error`.
   */
  error?: string;
  onClose: () => void;
  onSave: (run: string, tags: string[]) => void;
}) {
  const t = useT();
  const [value, setValue] = useState("");
  const formId = useId();

  // Reset the field every time the dialog is opened (and again when a
  // *different* run is opened), so it never shows the previous session's
  // unsaved edits or the raw text of a set of tags that has since been
  // normalised and stored.
  //
  // Both `open` and the run's name are needed, and neither alone is enough:
  //
  // - Keying on the `run` object is what this used to do, and a running run is
  //   re-fetched every LIVE_REFRESH_INTERVAL_MS with a new `updated_at` /
  //   `last_step` / `summary`, so its identity changes on every poll and the
  //   effect overwrote whatever the user had typed, mid-edit. `RunNoteCard`
  //   keeps its draft the same way.
  // - Keying on the name alone fixed that but broke the reset on `RunDetail`,
  //   whose `run` comes from `runs.find(r => r.name === runName)`: that name is
  //   fixed for the life of the page, so the effect ran once at mount and never
  //   again — edit, cancel, reopen and the abandoned text was still there.
  //
  // `open` is the piece that carries "this is a fresh session"; it is false
  // between two openings and never changes during a poll.
  useEffect(() => {
    setValue((run?.tags ?? []).join(", "));
    // Keyed on the dialog opening and on the run's identity (its name), not on
    // the object every poll re-creates.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, run?.name]);

  if (!run) return null;
  const tags = parseTagInput(value);

  return (
    <Dialog
      open={open}
      onClose={onClose}
      // The save is already announced by the SpinnerSlot in the footer; the
      // dialog stays until it lands so a rejection has somewhere to render.
      busy={saving}
      title={t("experiments.tagsDialog.title", { name: run.name })}
      className="max-w-lg"
      footer={
        <>
          <SpinnerSlot active={saving} size={14} label={t("experiments.tagsDialog.savingTags")} />
          <Button onClick={onClose} disabled={saving}>
            {t("experiments.tagsDialog.cancel")}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={saving}>
            {t("experiments.tagsDialog.save")}
          </Button>
        </>
      }
      // Below the action row, not inside the body: an Alert in the body grows
      // the panel and moves the buttons even though the footer is pinned (§8).
      footerNote={error ? <Alert tone="negative">{error}</Alert> : undefined}
    >
      <form
        id={formId}
        className="flex flex-col gap-4 px-4 py-4"
        onSubmit={(e) => {
          e.preventDefault();
          onSave(run.name, tags);
        }}
      >
        <Field
          label={t("experiments.tagsDialog.tagsLabel")}
          hint={t("experiments.tagsDialog.hint")}
        >
          {/* The dialog exists only to edit this one field, so focus starts here. */}
          <Input
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={t("experiments.tagsDialog.tagsPlaceholder")}
          />
        </Field>

        <div className="flex min-h-6 flex-wrap items-center gap-1.5">
          {tags.length === 0 ? (
            <span className="text-xs font-medium text-fg-subtle">
              {t("experiments.tagsDialog.noTags")}
            </span>
          ) : (
            tags.map((tag) => <Badge key={tag}>{tag}</Badge>)
          )}
        </div>
      </form>
    </Dialog>
  );
}
