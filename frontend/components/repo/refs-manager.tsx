"use client";

import { GitBranch, Tag, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Field, Input, Select } from "@/components/ui/field";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import { createTag, deleteBranch, deleteTag } from "@/lib/refs";
import type { RefUI, RepoKind } from "@/types/api";

/** Short form of a commit SHA, matching the 7-char OIDs shown elsewhere. */
function shortOid(oid: string): string {
  return oid.slice(0, 7);
}

type PendingDelete = { kind: "branch" | "tag"; name: string };

/**
 * Branch and tag management for the repository's Settings tab.
 *
 * All four operations have existed on the HF-compatible API from the start
 * (`handleHFCreateBranch` / `handleHFDeleteBranch` / `handleHFCreateTag` /
 * `handleHFDeleteTag`); the web UI called none of them, so tagging a release
 * needed a shell even though the ref switcher happily *displayed* tags.
 *
 * The refs arrive server-rendered, so there is no loading state to show; what
 * this component owns is the outcome of each mutation. The lists are kept in
 * local state as well so a deletion disappears immediately rather than
 * waiting for the server round trip of `router.refresh()`.
 */
export function RefsManager({
  kind,
  ns,
  name,
  defaultBranch,
  branches: initialBranches,
  tags: initialTags,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  defaultBranch: string;
  branches: RefUI[];
  tags: RefUI[];
}) {
  const t = useT();
  const router = useRouter();
  const repoId = `${ns}/${name}`;

  const [branches, setBranches] = useState(initialBranches);
  const [tags, setTags] = useState(initialTags);

  const [pending, setPending] = useState<PendingDelete | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const [tagName, setTagName] = useState("");
  const [tagRev, setTagRev] = useState(defaultBranch);
  const [tagMessage, setTagMessage] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);

  async function confirmDelete() {
    if (!pending || deleting) return;
    setDeleting(true);
    setDeleteError(null);
    const result =
      pending.kind === "branch"
        ? await deleteBranch(kind, ns, name, pending.name)
        : await deleteTag(kind, ns, name, pending.name);
    setDeleting(false);
    if (!result.ok) {
      setDeleteError(errorMessage(t, result));
      return;
    }
    if (pending.kind === "branch") {
      setBranches((current) => current.filter((b) => b.name !== pending.name));
    } else {
      setTags((current) => current.filter((tag) => tag.name !== pending.name));
    }
    setPending(null);
    router.refresh();
  }

  async function submitTag(event: React.FormEvent) {
    event.preventDefault();
    const trimmed = tagName.trim();
    if (trimmed === "" || creating) return;
    setCreating(true);
    setCreateError(null);
    const result = await createTag(kind, ns, name, tagRev, trimmed, tagMessage.trim());
    setCreating(false);
    if (!result.ok) {
      setCreateError(errorMessage(t, result));
      return;
    }
    setTags((current) =>
      [...current, { name: result.data.name, target_oid: result.data.targetCommit }].sort((a, b) =>
        a.name.localeCompare(b.name),
      ),
    );
    setTagName("");
    setTagMessage("");
    router.refresh();
  }

  // Both lists feed the "revision to tag" picker: a tag may point at another
  // tag's commit just as well as at a branch tip.
  const revOptions = [...branches.map((b) => b.name), ...tags.map((tag) => tag.name)];

  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">{t("repo.refs.branchesTitle")}</h3>
        {branches.length === 0 ? (
          <p className="text-sm text-fg-subtle">{t("repo.refs.noBranches")}</p>
        ) : (
          <ul className="flex flex-col rounded-lg border border-border">
            {branches.map((branch) => {
              const isDefault = branch.name === defaultBranch;
              return (
                <li
                  key={branch.name}
                  className="flex items-center gap-2 border-b border-border px-3 py-2 last:border-0"
                >
                  <GitBranch size={14} className="shrink-0 text-fg-subtle" />
                  <span className="min-w-0 truncate font-mono text-sm text-fg">{branch.name}</span>
                  {isDefault && <Badge tone="accent">{t("repo.refs.defaultBadge")}</Badge>}
                  <span className="ml-auto shrink-0 font-mono text-xs font-medium text-fg-subtle">
                    {shortOid(branch.target_oid)}
                  </span>
                  {/* The server refuses to delete the default branch (409), so
                      the control is absent rather than present-and-failing;
                      the reason is spelled out where the button would be. */}
                  {isDefault ? (
                    <span className="shrink-0 text-xs font-medium text-fg-subtle">
                      {t("repo.refs.defaultUndeletable")}
                    </span>
                  ) : (
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={t("repo.refs.deleteBranchAction", { name: branch.name })}
                      onClick={() => {
                        setDeleteError(null);
                        setPending({ kind: "branch", name: branch.name });
                      }}
                    >
                      <Trash2 size={13} />
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold">{t("repo.refs.tagsTitle")}</h3>
        {tags.length === 0 ? (
          <p className="text-sm text-fg-subtle">{t("repo.refs.noTags")}</p>
        ) : (
          <ul className="flex flex-col rounded-lg border border-border">
            {tags.map((tag) => (
              <li
                key={tag.name}
                className="flex items-center gap-2 border-b border-border px-3 py-2 last:border-0"
              >
                <Tag size={14} className="shrink-0 text-fg-subtle" />
                <span className="min-w-0 truncate font-mono text-sm text-fg">{tag.name}</span>
                <span className="ml-auto shrink-0 font-mono text-xs font-medium text-fg-subtle">
                  {shortOid(tag.target_oid)}
                </span>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={t("repo.refs.deleteTagAction", { name: tag.name })}
                  onClick={() => {
                    setDeleteError(null);
                    setPending({ kind: "tag", name: tag.name });
                  }}
                >
                  <Trash2 size={13} />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <form className="flex flex-col gap-3" onSubmit={submitTag}>
        <h3 className="text-sm font-semibold">{t("repo.refs.newTagTitle")}</h3>
        <div className="flex flex-wrap gap-3">
          <Field label={t("repo.refs.tagNameLabel")} className="min-w-48 flex-1">
            <Input
              value={tagName}
              onChange={(e) => setTagName(e.target.value)}
              placeholder={t("repo.refs.tagNamePlaceholder")}
              disabled={creating}
            />
          </Field>
          <Field label={t("repo.refs.tagRevLabel")} className="min-w-48 flex-1">
            <Select
              value={tagRev}
              onChange={(e) => setTagRev(e.target.value)}
              disabled={creating || revOptions.length === 0}
            >
              {revOptions.map((rev) => (
                <option key={rev} value={rev}>
                  {rev}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <Field label={t("repo.refs.tagMessageLabel")} hint={t("repo.refs.tagMessageHint")}>
          <Input
            value={tagMessage}
            onChange={(e) => setTagMessage(e.target.value)}
            disabled={creating}
          />
        </Field>
        {/* The button sits above its own error, so a failed attempt never
            moves the control that produced it (DESIGN.md §8). */}
        <div className="flex">
          <Button
            type="submit"
            variant="primary"
            disabled={creating || tagName.trim() === "" || revOptions.length === 0}
          >
            {creating ? t("repo.refs.creating") : t("repo.refs.createTag")}
          </Button>
        </div>
        {createError && <Alert tone="negative">{createError}</Alert>}
      </form>

      {pending && (
        <ConfirmDialog
          open={pending !== null}
          onClose={() => {
            if (!deleting) setPending(null);
          }}
          onConfirm={() => void confirmDelete()}
          title={t(
            pending.kind === "branch" ? "repo.refs.deleteBranchTitle" : "repo.refs.deleteTagTitle",
          )}
          description={
            <Alert tone="negative">
              {t(
                pending.kind === "branch"
                  ? "repo.refs.deleteBranchBody"
                  : "repo.refs.deleteTagBody",
                { name: pending.name, repo: repoId },
              )}
            </Alert>
          }
          confirmLabel={t("repo.refs.confirmDelete")}
          confirmingLabel={t("repo.refs.deleting")}
          cancelLabel={t("repo.refs.cancel")}
          confirming={deleting}
          error={deleteError}
        />
      )}
    </div>
  );
}
