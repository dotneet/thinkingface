"use client";

import { useEffect, useId, useState } from "react";
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
  onClose,
  onSave,
}: {
  run: ExpRun | null;
  open: boolean;
  saving: boolean;
  onClose: () => void;
  onSave: (run: string, tags: string[]) => void;
}) {
  const t = useT();
  const [value, setValue] = useState("");
  const formId = useId();

  // Reset the field whenever a different run's tags are opened, so the dialog
  // never shows the previous run's edits.
  useEffect(() => {
    setValue((run?.tags ?? []).join(", "));
  }, [run]);

  if (!run) return null;
  const tags = parseTagInput(value);

  return (
    <Dialog
      open={open}
      onClose={onClose}
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
