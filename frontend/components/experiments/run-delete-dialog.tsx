"use client";

import { useEffect, useId, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { useT } from "@/lib/i18n/client";

/**
 * Confirmation for `DELETE .../runs/{run}`.
 *
 * The run name has to be typed out, the way repo deletion asks for the
 * repository id (components/repo/repo-danger-zone.tsx): the alternative to
 * deleting is archiving, which hides the run and can be undone, so the
 * irreversible path should be the one that takes deliberate effort.
 */
export function RunDeleteDialog({
  run,
  open,
  deleting,
  error,
  onClose,
  onConfirm,
}: {
  /** Run to delete; null closes the dialog. */
  run: string | null;
  open: boolean;
  deleting: boolean;
  error?: string;
  onClose: () => void;
  onConfirm: (run: string) => void;
}) {
  const t = useT();
  const [confirmText, setConfirmText] = useState("");
  const formId = useId();

  // Clear the field whenever a different run is put up for deletion, so a
  // previously typed name can never confirm the next dialog.
  useEffect(() => {
    setConfirmText("");
  }, [run]);

  if (!run) return null;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("experiments.deleteRun.confirmTitle", { name: run })}
      footer={
        <>
          <Button onClick={onClose} disabled={deleting}>
            {t("experiments.deleteRun.cancel")}
          </Button>
          <Button
            type="submit"
            form={formId}
            variant="danger"
            disabled={confirmText !== run || deleting}
          >
            {deleting ? t("experiments.deleteRun.deleting") : t("experiments.deleteRun.submit")}
          </Button>
        </>
      }
      // Below the action row: an Alert in the body grows the panel, which
      // moves the buttons even though the footer itself is pinned (§8).
      footerNote={error ? <Alert tone="negative">{error}</Alert> : undefined}
    >
      <form
        id={formId}
        className="flex flex-col gap-4 px-4 py-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (confirmText === run && !deleting) onConfirm(run);
        }}
      >
        <Alert tone="negative" title={t("experiments.deleteRun.warningTitle")}>
          {t("experiments.deleteRun.warning", { name: run })}
        </Alert>
        <p className="text-sm text-fg-subtle">{t("experiments.deleteRun.parquetNote")}</p>
        <Field label={t("experiments.deleteRun.confirmInputLabel", { value: run })}>
          <Input
            autoFocus
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            placeholder={run}
          />
        </Field>
      </form>
    </Dialog>
  );
}
