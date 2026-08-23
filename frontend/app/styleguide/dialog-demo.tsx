"use client";

import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { Dialog } from "@/components/ui/dialog";

export function StyleguideDialogDemo() {
  const [open, setOpen] = useState(false);
  const [footerOpen, setFooterOpen] = useState(false);
  const [showError, setShowError] = useState(false);
  return (
    <div className="flex flex-wrap items-center gap-3">
      <Button variant="primary" onClick={() => setOpen(true)}>
        Open dialog
      </Button>
      <span className="text-xs font-medium text-fg-subtle">
        Escape and backdrop clicks both close it.
      </span>
      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        title="Dialog title"
        headerAction={<CopyButton value="Dialog body text" />}
      >
        <div className="p-4 text-sm text-fg-muted">
          <p>
            Built on the native <code className="font-mono text-xs">&lt;dialog&gt;</code> element,
            so the browser supplies the focus trap, the top layer, and the inert background.
          </p>
        </div>
      </Dialog>

      <Button
        variant="secondary"
        onClick={() => {
          setShowError(false);
          setFooterOpen(true);
        }}
      >
        Open dialog with footer
      </Button>
      <span className="text-xs font-medium text-fg-subtle">
        Click Confirm to see the body grow without moving Cancel/Confirm (DESIGN.md §8).
      </span>
      <Dialog
        open={footerOpen}
        onClose={() => setFooterOpen(false)}
        title="Dialog with a pinned footer"
        footer={
          <>
            <Button onClick={() => setFooterOpen(false)}>Cancel</Button>
            <Button variant="primary" onClick={() => setShowError(true)}>
              Confirm
            </Button>
          </>
        }
        footerNote={
          showError ? <Alert tone="negative">Something went wrong — try again.</Alert> : undefined
        }
      >
        <div className="flex flex-col gap-3 p-4 text-sm text-fg-muted">
          <p>
            The action row lives in <code className="font-mono text-xs">footer</code>, pinned below
            a horizontal rule, while this body scrolls independently. The error the Confirm button
            raises lands in <code className="font-mono text-xs">footerNote</code>, below the action
            row, so Cancel/Confirm never move out from under the pointer.
          </p>
        </div>
      </Dialog>
    </div>
  );
}
