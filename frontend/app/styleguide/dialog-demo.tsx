"use client";

import { useEffect, useRef, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { Dialog } from "@/components/ui/dialog";

export function StyleguideDialogDemo() {
  const [open, setOpen] = useState(false);
  const [footerOpen, setFooterOpen] = useState(false);
  const [showError, setShowError] = useState(false);
  const [busyOpen, setBusyOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [busyError, setBusyError] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // The whole point of `busy` is that the dialog outlives the request, so the
  // demo needs a request that actually takes a moment. Cleared on unmount so a
  // fast-navigating styleguide reader cannot land a setState on a dead tree.
  useEffect(() => () => (timer.current ? clearTimeout(timer.current) : undefined), []);

  function fakeRequest() {
    setBusy(true);
    setBusyError(false);
    timer.current = setTimeout(() => {
      setBusy(false);
      setBusyError(true);
    }, 2500);
  }

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

      <Button
        variant="secondary"
        onClick={() => {
          setBusy(false);
          setBusyError(false);
          setBusyOpen(true);
        }}
      >
        Open dialog that locks while busy
      </Button>
      <span className="text-xs font-medium text-fg-subtle">
        Press Submit, then try Escape, the backdrop and the × — all three are refused for 2.5s.
      </span>
      <Dialog
        open={busyOpen}
        onClose={() => setBusyOpen(false)}
        busy={busy}
        title="Dialog with busy"
        footer={
          <>
            <Button onClick={() => setBusyOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button variant="danger" onClick={fakeRequest} disabled={busy}>
              {busy ? "Submitting…" : "Submit"}
            </Button>
          </>
        }
        footerNote={
          busyError ? (
            <Alert tone="negative">The request failed — nothing was changed.</Alert>
          ) : null
        }
      >
        <div className="flex flex-col gap-3 p-4 text-sm text-fg-muted">
          <p>
            While <code className="font-mono text-xs">busy</code> is set, every way out is shut:
            Escape, a backdrop click and the header × alike. The × is disabled so the refusal is
            visible rather than a click that does nothing.
          </p>
          <p>
            Without it, dismissing mid-request threw away the only place the failure below is
            rendered, and the write looked like it had never run.{" "}
            <code className="font-mono text-xs">ConfirmDialog</code> passes its own{" "}
            <code className="font-mono text-xs">confirming</code> through automatically.
          </p>
        </div>
      </Dialog>
    </div>
  );
}
