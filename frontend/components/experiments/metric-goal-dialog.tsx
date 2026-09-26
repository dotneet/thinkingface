"use client";

import { useEffect, useId, useState } from "react";

import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { SpinnerSlot } from "@/components/ui/spinner";
import { useT } from "@/lib/i18n/client";
import type { MetricGoal } from "@/types/api";

type GoalChoice = MetricGoal | "none";

/**
 * Declare which direction is better for one metric. The goal is a project
 * setting (`PATCH /api/v1/experiments/{ns}/{repo}/{project}`), so it is shared
 * with everyone reading the project and with `tf experiments runs --sort
 * best:<metric>`.
 *
 * A dialog rather than a menu hanging off the column header: the run table
 * scrolls inside its own box, and a floating panel anchored in a sticky
 * header is clipped by it whenever the table holds only a few rows. The
 * native `<dialog>` sits in the top layer and is never clipped.
 */
export function MetricGoalDialog({
  metric,
  goal,
  open,
  saving,
  error,
  onClose,
  onSave,
}: {
  /** The metric being edited; null renders nothing. */
  metric: string | null;
  /** Its current goal, if any. */
  goal: MetricGoal | undefined;
  open: boolean;
  saving: boolean;
  error?: string;
  onClose: () => void;
  /** `""` clears the goal, as the API spells it. */
  onSave: (metric: string, goal: MetricGoal | "") => void;
}) {
  const t = useT();
  const formId = useId();
  const [choice, setChoice] = useState<GoalChoice>(goal ?? "none");

  // Start every opening from the stored goal, not from whatever was clicked
  // the last time the dialog was open (same rule as RunTagsDialog).
  useEffect(() => {
    setChoice(goal ?? "none");
    // Keyed on the dialog opening and the metric, not on the goal: a live
    // refetch landing mid-edit must not reset the choice being made.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, metric]);

  if (metric === null) return null;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      busy={saving}
      title={t("experiments.goals.dialogTitle", { metric })}
      className="max-w-md"
      footer={
        <>
          <SpinnerSlot active={saving} size={14} label={t("experiments.goals.saving")} />
          <Button onClick={onClose} disabled={saving}>
            {t("experiments.goals.cancel")}
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={saving}>
            {t("experiments.goals.save")}
          </Button>
        </>
      }
      // Below the action row, never in the body (DESIGN.md §8.2).
      footerNote={error ? <Alert tone="negative">{error}</Alert> : undefined}
    >
      <form
        id={formId}
        className="flex flex-col gap-4 px-4 py-4"
        onSubmit={(e) => {
          e.preventDefault();
          onSave(metric, choice === "none" ? "" : choice);
        }}
      >
        <p className="text-sm text-fg-muted">{t("experiments.goals.dialogDescription")}</p>
        <SegmentedControl<GoalChoice>
          value={choice}
          onChange={setChoice}
          label={t("experiments.goals.optionsAria")}
          options={[
            { value: "min", label: t("experiments.goals.optionMin") },
            { value: "max", label: t("experiments.goals.optionMax") },
            { value: "none", label: t("experiments.goals.optionNone") },
          ]}
        />
      </form>
    </Dialog>
  );
}
