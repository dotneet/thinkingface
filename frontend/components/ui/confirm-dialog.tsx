"use client";

import { useEffect, useId, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button, type ButtonVariant } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { useT } from "@/lib/i18n/client";

/**
 * The one confirmation dialog for every destructive action (see [S13] in
 * todo/security-audit-findings.md). Replaces the previous three-way split
 * between `window.confirm` (unthemed, no focus management, outside
 * `components/ui/`), a bespoke type-to-confirm `Dialog` per call site
 * (components/repo/repo-danger-zone.tsx, the experiments run-delete dialog),
 * and no confirmation at all (archive/unarchive).
 *
 * Pass `requireText` for the operations serious enough to want typed
 * confirmation (deleting a repository, a run); omit it for a plain
 * Cancel/Confirm dialog (deleting a token, an SSH key, a webhook).
 */
export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  description,
  confirmLabel,
  confirmingLabel,
  cancelLabel,
  tone = "danger",
  requireText,
  confirming = false,
  error,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  /** Usually an <Alert tone="negative"> for a destructive action, or plain text. */
  description?: React.ReactNode;
  confirmLabel?: string;
  /** Shown on the confirm button instead of `confirmLabel` while `confirming` is true. */
  confirmingLabel?: string;
  cancelLabel?: string;
  /** Confirm button variant. Defaults to "danger" — every current caller deletes something. */
  tone?: ButtonVariant;
  /**
   * When set, the confirm button stays disabled until the typed value
   * matches exactly (HuggingFace/GitHub-style "type the name to confirm").
   * Omit for a lighter-weight yes/no dialog.
   */
  requireText?: string;
  confirming?: boolean;
  error?: string | null;
}) {
  const t = useT();
  const [text, setText] = useState("");
  // Links the footer's submit button back to this form: the footer is a
  // sibling rendered by Dialog, not a descendant, so a plain nested <button>
  // can't reach the form's onSubmit — the `form` attribute bridges that.
  const formId = useId();

  // Clear any previously typed confirmation text whenever the dialog opens
  // (covers both re-opening for the same target and opening for a new one),
  // so a stale value can never satisfy `requireText` for the next open.
  useEffect(() => {
    if (open) setText("");
  }, [open]);

  const canConfirm = requireText === undefined || text === requireText;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      footer={
        <>
          <Button type="button" onClick={onClose} disabled={confirming}>
            {cancelLabel ?? t("ui.confirmDialog.defaultCancel")}
          </Button>
          <Button type="submit" form={formId} variant={tone} disabled={!canConfirm || confirming}>
            {confirming
              ? (confirmingLabel ?? confirmLabel ?? t("ui.confirmDialog.defaultConfirm"))
              : (confirmLabel ?? t("ui.confirmDialog.defaultConfirm"))}
          </Button>
        </>
      }
      // Below the action row, not inside the body: an Alert in the body grows
      // the panel upward from the footer, and even with a pinned footer the
      // panel's own growth would move the buttons (DESIGN.md §8).
      footerNote={error ? <Alert tone="negative">{error}</Alert> : undefined}
    >
      <form
        id={formId}
        className="flex flex-col gap-4 px-4 py-4"
        onSubmit={(e) => {
          e.preventDefault();
          if (canConfirm && !confirming) onConfirm();
        }}
      >
        {description}
        {requireText !== undefined && (
          <Field label={t("ui.confirmDialog.typeToConfirm", { value: requireText })}>
            <Input
              autoFocus
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={requireText}
            />
          </Field>
        )}
      </form>
    </Dialog>
  );
}
