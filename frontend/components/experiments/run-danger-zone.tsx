"use client";

import { Trash2 } from "lucide-react";
import { Section } from "@/components/experiments/run-section";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n/client";

/**
 * Deleting the run. Only rendered for a viewer who can write.
 *
 * The button opens a confirmation rather than deleting — the dialog lives on
 * the page above, since it has to survive this section unmounting — and the
 * failure message renders *below* the button so a failed attempt never moves
 * the button the reader is about to press again (DESIGN.md §8.1).
 */
export function RunDangerZone({
  error,
  onRequestDelete,
}: {
  /** A failed delete, already translated; the caller hides it while the dialog is up. */
  error?: string;
  onRequestDelete: () => void;
}) {
  const t = useT();

  return (
    <Section
      title={t("experiments.deleteRun.dangerTitle")}
      description={t("experiments.deleteRun.description")}
    >
      <div>
        <Button variant="danger" onClick={onRequestDelete}>
          <Trash2 size={16} />
          {t("experiments.deleteRun.button")}
        </Button>
      </div>
      {error && <Alert tone="negative">{error}</Alert>}
    </Section>
  );
}
