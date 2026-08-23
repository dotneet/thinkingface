"use client";

import { Archive, ArchiveRestore, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { archiveRepo, deleteRepo, unarchiveRepo } from "@/lib/repo-admin";
import type { RepoKind } from "@/types/api";

/**
 * The two destructive operations on a repository, side by side because the
 * reversible one is almost always the right answer: archiving freezes the
 * repository read-only and can be undone, deleting removes the git history
 * for good (object storage is reclaimed later by GC).
 *
 * Both confirm through `ConfirmDialog` (see [S13] in
 * todo/security-audit-findings.md): deletion asks the user to type the
 * repository id first, the way HuggingFace and GitHub do, since it can't be
 * undone; archiving only needs a plain yes/no, since it can. Unarchiving is
 * harmless (it only restores write access) and stays a single click.
 */
export function RepoDangerZone({
  kind,
  ns,
  name,
  archived,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  /** Server-rendered starting state; the toggle keeps its own after that. */
  archived: boolean;
}) {
  const t = useT();
  const router = useRouter();

  const [isArchived, setIsArchived] = useState(archived);
  const [archiving, setArchiving] = useState(false);
  const [archiveError, setArchiveError] = useState<string | null>(null);
  const [confirmArchiveOpen, setConfirmArchiveOpen] = useState(false);

  const [confirmDeleteOpen, setConfirmDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const repoId = `${ns}/${name}`;

  async function doArchive() {
    setArchiving(true);
    setArchiveError(null);
    const result = await archiveRepo(kind, ns, name);
    setArchiving(false);
    if (!result.ok) {
      setArchiveError(errorMessage(t, result));
      return;
    }
    setConfirmArchiveOpen(false);
    setIsArchived(result.data.repo.archived);
    // Everything else on the page reads `archived` / `can_write` from the
    // server, so refresh rather than trying to patch the tree by hand.
    router.refresh();
  }

  async function doUnarchive() {
    setArchiving(true);
    setArchiveError(null);
    const result = await unarchiveRepo(kind, ns, name);
    setArchiving(false);
    if (!result.ok) {
      setArchiveError(errorMessage(t, result));
      return;
    }
    setIsArchived(result.data.repo.archived);
    router.refresh();
  }

  async function handleDelete() {
    setDeleting(true);
    setDeleteError(null);
    const result = await deleteRepo(kind, ns, name);
    if (!result.ok) {
      setDeleting(false);
      setDeleteError(errorMessage(t, result));
      return;
    }
    // The repository is gone: nothing on this route can render any more, so
    // leave for the listing ("/models" or "/datasets") instead of refreshing
    // a page that would now 404.
    router.push(`/${kind}s`);
    router.refresh();
  }

  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">{t("repo.settings.archive.title")}</h3>
        <p className="text-sm text-fg-subtle">
          {isArchived
            ? t("repo.settings.archive.descriptionArchived")
            : t("repo.settings.archive.description")}
        </p>
        <Button
          variant="secondary"
          disabled={archiving}
          onClick={() => {
            if (isArchived) {
              // Unarchiving only restores write access — harmless, no confirmation.
              doUnarchive();
              return;
            }
            setArchiveError(null);
            setConfirmArchiveOpen(true);
          }}
          className="self-start"
        >
          {isArchived ? <ArchiveRestore size={16} /> : <Archive size={16} />}
          {archiving
            ? t("repo.settings.archive.working")
            : isArchived
              ? t("repo.settings.archive.unarchive")
              : t("repo.settings.archive.archive")}
        </Button>
        {/* Below the button, not above — a stale error left over from a
            closed confirm dialog must never push it down (DESIGN.md §8). */}
        {archiveError && !confirmArchiveOpen && <Alert tone="negative">{archiveError}</Alert>}
      </section>

      <section className="flex flex-col gap-2 border-t border-border pt-6">
        <h3 className="text-sm font-semibold text-negative">{t("repo.settings.delete.title")}</h3>
        <p className="text-sm text-fg-subtle">{t("repo.settings.delete.description")}</p>
        <Button
          variant="danger"
          onClick={() => {
            setDeleteError(null);
            setConfirmDeleteOpen(true);
          }}
          className="self-start"
        >
          <Trash2 size={16} />
          {t("repo.settings.delete.button")}
        </Button>
        {/* Below the button for the same reason as the archive error above. */}
        {deleteError && !confirmDeleteOpen && <Alert tone="negative">{deleteError}</Alert>}
      </section>

      <ConfirmDialog
        open={confirmArchiveOpen}
        onClose={() => setConfirmArchiveOpen(false)}
        onConfirm={doArchive}
        title={t("repo.settings.archive.confirmTitle")}
        description={
          <p className="text-sm text-fg-muted">{t("repo.settings.archive.confirmBody")}</p>
        }
        confirmLabel={t("repo.settings.archive.confirmSubmit")}
        confirmingLabel={t("repo.settings.archive.working")}
        cancelLabel={t("repo.settings.archive.confirmCancel")}
        tone="danger"
        confirming={archiving}
        error={archiveError}
      />

      <ConfirmDialog
        open={confirmDeleteOpen}
        onClose={() => setConfirmDeleteOpen(false)}
        onConfirm={handleDelete}
        title={t("repo.settings.delete.confirmTitle")}
        description={
          <Alert tone="negative" title={t("repo.settings.delete.confirmWarningTitle")}>
            {t("repo.settings.delete.confirmWarning", { repo: repoId })}
          </Alert>
        }
        requireText={repoId}
        confirmLabel={t("repo.settings.delete.confirmSubmit")}
        confirmingLabel={t("repo.settings.delete.deleting")}
        cancelLabel={t("repo.settings.delete.confirmCancel")}
        tone="danger"
        confirming={deleting}
        error={deleteError}
      />
    </div>
  );
}
