"use client";

import { useRouter } from "next/navigation";
import { useEffect, useId, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { repoTreeHref } from "@/lib/paths";
import { createBranch } from "@/lib/refs";
import type { RepoKind } from "@/types/api";

/**
 * "New branch", reached from the ref switcher.
 *
 * The backend has answered `POST /api/{kind}s/{ns}/{name}/branch/{branch}`
 * since `HfApi.create_branch` was supported; nothing in the web UI ever called
 * it, so branching required a shell.
 *
 * The new branch starts at whatever revision the reader is currently on, which
 * is the same default `git switch -c` gives and the one thing the page can
 * state truthfully — the server's own default is the repository's default
 * branch, which would silently differ from what is on screen.
 */
export function CreateBranchDialog({
  kind,
  ns,
  name,
  startingPoint,
  open,
  onClose,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  /** Revision the new branch is cut from — the one currently being viewed. */
  startingPoint: string;
  open: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const router = useRouter();
  const formId = useId();
  const [branch, setBranch] = useState("");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // A reopen must not inherit the previous attempt's half-typed name or its
  // error; both belong to the attempt that is over.
  useEffect(() => {
    if (open) {
      setBranch("");
      setError(null);
    }
  }, [open]);

  async function submit() {
    const trimmed = branch.trim();
    if (trimmed === "" || creating) return;
    setCreating(true);
    setError(null);
    const result = await createBranch(kind, ns, name, trimmed, startingPoint);
    setCreating(false);
    if (!result.ok) {
      setError(errorMessage(t, result));
      return;
    }
    onClose();
    // refresh() so the sidebar's branch list and every ref switcher on the
    // page pick the new branch up; push() so the reader lands on it.
    router.push(repoTreeHref(kind, ns, name, trimmed));
    router.refresh();
  }

  if (!open) return null;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("repo.refs.newBranchTitle")}
      footer={
        <>
          <Button type="button" onClick={onClose} disabled={creating}>
            {t("repo.refs.cancel")}
          </Button>
          <Button
            type="submit"
            form={formId}
            variant="primary"
            disabled={creating || branch.trim() === ""}
          >
            {creating ? t("repo.refs.creating") : t("repo.refs.createBranch")}
          </Button>
        </>
      }
      // Below the action row, so an error never shifts the button that
      // produced it out from under the pointer (DESIGN.md §8).
      footerNote={error && <Alert tone="negative">{error}</Alert>}
    >
      <form
        id={formId}
        className="flex flex-col gap-3 px-4 py-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <p className="text-sm text-fg-muted">
          {t("repo.refs.newBranchBody", { rev: startingPoint })}
        </p>
        <Field label={t("repo.refs.branchNameLabel")}>
          <Input
            autoFocus
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            placeholder={t("repo.refs.branchNamePlaceholder")}
            disabled={creating}
          />
        </Field>
      </form>
    </Dialog>
  );
}
