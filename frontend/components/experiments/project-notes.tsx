"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { NotebookPen, RotateCw } from "lucide-react";
import { useMemo, useState } from "react";

import { Section } from "@/components/experiments/run-section";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Markdown } from "@/components/ui/markdown";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import { SkeletonLines } from "@/components/ui/skeleton";
import { SpinnerSlot } from "@/components/ui/spinner";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { rewriteRunLinks } from "@/lib/exp-notes";
import { expRunHref, getProjectNotes, saveProjectNotes } from "@/lib/experiments";
import { useT } from "@/lib/i18n/client";
import { publicApiBase } from "@/lib/paths";
import { resolveFileUrl } from "@/lib/repos";
import type { ExpNotesResponse } from "@/types/api";

/** The notebook's path when the server has not told us yet (docs/dev/agent-features.md §2.7). */
function defaultNotesPath(project: string): string {
  return `${project}/NOTES.md`;
}

/**
 * The project's experiment notebook: `{project}/NOTES.md` in the dataset
 * repository, rendered as Markdown with `[text](run:<name>)` links pointed at
 * the run pages, and editable by anyone with write access.
 *
 * Saves are optimistic-locked: the edit carries the `blob_sha` it started
 * from, and a 409 means somebody (a person or an agent) saved in between.
 * Nothing is overwritten then — the editor keeps the draft, offers to copy it,
 * and loads the latest version only when asked.
 */
export function ProjectNotes({
  ns,
  repo,
  project,
  canWrite,
  initial,
}: {
  ns: string;
  repo: string;
  project: string;
  /** Viewer has write access to the backing dataset repository. */
  canWrite: boolean;
  /**
   * What the server render read, or null when that read failed — the query
   * then fetches on its own, so a transient failure gets a second chance and
   * a persistent one reaches the error state with a retry.
   */
  initial: ExpNotesResponse | null;
}) {
  const t = useT();
  const queryClient = useQueryClient();
  const notesKey = ["exp-notes", ns, repo, project];

  const notes = useQuery({
    queryKey: notesKey,
    queryFn: async () => {
      const result = await getProjectNotes(ns, repo, project);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    initialData: initial ?? undefined,
    // The server render just read it; no need to read it again on mount.
    staleTime: 60_000,
  });

  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  // The blob the edit started from, sent back as `base_sha`. Captured when the
  // editor opens (and when the latest version is loaded after a conflict), not
  // read from the query at save time: a background refetch must never quietly
  // move the lock forward past a version this editor never showed.
  const [baseSha, setBaseSha] = useState("");
  const [reloadFailed, setReloadFailed] = useState(false);

  const save = useMutation({
    mutationFn: async (content: string) => {
      const result = await saveProjectNotes(ns, repo, project, {
        content,
        base_sha: baseSha,
      });
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(notesKey, data);
      setEditing(false);
    },
  });

  const conflict = save.error instanceof ApiResultError && save.error.result.status === 409;
  const path = notes.data?.path || defaultNotesPath(project);

  function startEditing(from: ExpNotesResponse) {
    save.reset();
    setReloadFailed(false);
    setDraft(from.content);
    setBaseSha(from.blob_sha);
    setEditing(true);
  }

  async function loadLatest() {
    setReloadFailed(false);
    const result = await notes.refetch();
    if (!result.data || result.isError) {
      setReloadFailed(true);
      return;
    }
    startEditing(result.data);
  }

  // Everything relative in the notebook resolves against its own directory in
  // the repository, pinned to the commit it was read from, so an image under
  // `{project}/plots/` renders. `run:` links are rewritten first; hrefs get no
  // link context because it would treat the rewritten `/experiments/…` route
  // as a path inside the repository.
  const rendered = useMemo(() => {
    const data = notes.data;
    if (!data) return null;
    const source = rewriteRunLinks(data.content, (name) => expRunHref(ns, repo, project, name));
    const rev = data.commit_sha;
    return {
      source,
      assetBaseUrl: rev
        ? resolveFileUrl("dataset", ns, repo, rev, [project], publicApiBase())
        : undefined,
      repoRootUrl: rev ? resolveFileUrl("dataset", ns, repo, rev, [], publicApiBase()) : undefined,
    };
  }, [notes.data, ns, repo, project]);

  const hasContent =
    notes.data !== undefined && notes.data.exists && notes.data.content.trim() !== "";

  const editAction =
    canWrite && !editing && hasContent && notes.data ? (
      <Button size="sm" variant="secondary" onClick={() => notes.data && startEditing(notes.data)}>
        <NotebookPen size={14} />
        {t("experiments.projectNotes.edit")}
      </Button>
    ) : undefined;

  return (
    <Section
      title={t("experiments.projectNotes.title")}
      description={t("experiments.projectNotes.description", { path })}
      action={editAction}
    >
      {editing ? (
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!save.isPending) save.mutate(draft);
          }}
        >
          <MarkdownEditor
            value={draft}
            onChange={setDraft}
            onSubmit={() => {
              if (!save.isPending) save.mutate(draft);
            }}
            autoFocus
            markdown
            minHeightClassName="min-h-[40vh]"
            placeholder={t("experiments.projectNotes.placeholder")}
            ariaLabel={t("experiments.projectNotes.editAria")}
            disabled={save.isPending}
          />
          <div className="flex flex-wrap items-center justify-end gap-2">
            <span className="mr-auto text-xs font-medium text-fg-subtle">
              {t("experiments.projectNotes.hint")}
            </span>
            <SpinnerSlot
              active={save.isPending}
              size={14}
              label={t("experiments.projectNotes.saving")}
            />
            <Button onClick={() => setEditing(false)} disabled={save.isPending}>
              {t("experiments.projectNotes.cancel")}
            </Button>
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t("experiments.projectNotes.save")}
            </Button>
          </div>
          {/* Below the action row, never above it (DESIGN.md §8.1): a failed
              save must not move Save out from under the pointer. */}
          {conflict ? (
            <Alert tone="warning" title={t("experiments.projectNotes.conflictTitle")}>
              <span>{t("experiments.projectNotes.conflictBody")}</span>
              <span className="mt-2 flex flex-wrap items-center gap-2">
                <CopyButton value={draft} label={t("experiments.projectNotes.copyDraft")} />
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => void loadLatest()}
                  disabled={notes.isFetching}
                >
                  <RotateCw size={14} />
                  {t("experiments.projectNotes.loadLatest")}
                </Button>
              </span>
              {reloadFailed && (
                <span className="mt-1 text-xs font-medium text-negative-strong">
                  {t("experiments.projectNotes.loadLatestFailed")}
                </span>
              )}
            </Alert>
          ) : save.isError ? (
            <Alert tone="negative">
              {queryErrorMessage(t, save.error, t("experiments.projectNotes.saveFailed"))}
            </Alert>
          ) : null}
        </form>
      ) : notes.isPending ? (
        <SkeletonLines lines={4} />
      ) : notes.isError && notes.data === undefined ? (
        <ErrorState
          title={t("experiments.errorTitle")}
          message={queryErrorMessage(t, notes.error, t("experiments.projectNotes.loadFailed"))}
          action={
            <Button size="sm" variant="secondary" onClick={() => void notes.refetch()}>
              <RotateCw size={14} />
              {t("experiments.projectNotes.retry")}
            </Button>
          }
        />
      ) : !hasContent ? (
        <EmptyState
          icon={NotebookPen}
          title={t("experiments.projectNotes.emptyTitle")}
          description={
            canWrite
              ? t("experiments.projectNotes.emptyDescription", { path })
              : t("experiments.projectNotes.emptyReadOnlyDescription")
          }
          action={
            canWrite && notes.data ? (
              <Button variant="secondary" onClick={() => notes.data && startEditing(notes.data)}>
                <NotebookPen size={16} />
                {t("experiments.projectNotes.write")}
              </Button>
            ) : undefined
          }
        />
      ) : (
        rendered && (
          <div className="rounded-lg border border-border bg-bg-raised p-4">
            <Markdown
              source={rendered.source}
              assetBaseUrl={rendered.assetBaseUrl}
              repoRootUrl={rendered.repoRootUrl}
            />
          </div>
        )
      )}
    </Section>
  );
}
