import { FileDiff, FileMinus, FilePen, FilePlus, GitCommitVertical } from "lucide-react";
import Link from "next/link";
import { IndexingBanner } from "@/components/repo/indexing-banner";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { Alert } from "@/components/ui/alert";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import { CopyButton } from "@/components/ui/copy-button";
import { DiffView } from "@/components/ui/diff-view";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { TimeText } from "@/components/ui/time-text";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { countsArePartial, noPatchReason } from "@/lib/diff";
import { formatBytes } from "@/lib/format";
import type { MessageKey, Translator } from "@/lib/i18n";
import { getT } from "@/lib/i18n/server";
import { repoBlobHref, repoCommitHref, repoCommitsHref, repoTreeHref } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getCommitDiff, getRepo } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { CommitDiffResponse, DiffFile, DiffStatus, RepoKind } from "@/types/api";

const STATUS_TONES: Record<DiffStatus, BadgeTone> = {
  added: "positive",
  modified: "accent",
  deleted: "negative",
};

const STATUS_ICONS: Record<DiffStatus, typeof FilePlus> = {
  added: FilePlus,
  modified: FilePen,
  deleted: FileMinus,
};

const STATUS_KEYS: Record<DiffStatus, MessageKey> = {
  added: "repo.diff.status.added",
  modified: "repo.diff.status.modified",
  deleted: "repo.diff.status.deleted",
};

const NO_PATCH_KEYS = {
  binary: "repo.diff.noPatch.binary",
  lfs: "repo.diff.noPatch.lfs",
  tooLarge: "repo.diff.noPatch.tooLarge",
  noTextChange: "repo.diff.noPatch.noTextChange",
  unsupported: "repo.diff.noPatch.unsupported",
  budgetSpent: "repo.diff.noPatch.budgetSpent",
} as const satisfies Record<string, MessageKey>;

/**
 * The size line for a file whose lines were never counted: the only factual
 * "how much changed" left once there is no patch. `old_size` / `size` are 0
 * on the side the path does not exist, which is why the status decides the
 * phrasing rather than the numbers.
 */
function sizeSummary(t: Translator, file: DiffFile): string {
  if (file.status === "added") return t("repo.diff.sizeAdded", { size: formatBytes(file.size) });
  if (file.status === "deleted") {
    return t("repo.diff.sizeDeleted", { size: formatBytes(file.old_size) });
  }
  return t("repo.diff.sizeChanged", {
    from: formatBytes(file.old_size),
    to: formatBytes(file.size),
  });
}

/** One file's row: the header strip, then either its patch or why there isn't one. */
function DiffFileCard({
  t,
  file,
  kind,
  ns,
  name,
  rev,
}: {
  t: Translator;
  file: DiffFile;
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
}) {
  const StatusIcon = STATUS_ICONS[file.status];
  const reason = noPatchReason(file);
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-bg-raised p-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <StatusIcon size={16} className="shrink-0 text-fg-subtle" strokeWidth={1.5} />
        {/* A deleted path has nothing to open at this revision — it is gone
            from this commit's tree — so it stays plain text rather than
            linking to a guaranteed 404. */}
        {file.status === "deleted" ? (
          <span className="min-w-0 break-all font-mono text-sm text-fg">{file.path}</span>
        ) : (
          <Link
            href={repoBlobHref(kind, ns, name, rev, file.path)}
            className="min-w-0 break-all font-mono text-sm text-accent hover:underline"
          >
            {file.path}
          </Link>
        )}
        <Badge tone={STATUS_TONES[file.status]}>{t(STATUS_KEYS[file.status])}</Badge>

        <div className="ml-auto flex shrink-0 items-center gap-2 text-xs font-medium">
          {/* Only ever rendered from `has_patch`: the counts are 0 for a file
              nothing counted, and "+0 −0" there would read as "unchanged"
              (docs/dev/api-contract.md §2). */}
          {file.has_patch ? (
            <>
              <span
                className="text-positive-strong tabular-nums"
                title={t("repo.diff.additionsTitle", { count: file.additions })}
              >
                {t("repo.diff.additions", { count: file.additions })}
              </span>
              <span
                className="text-negative-strong tabular-nums"
                title={t("repo.diff.deletionsTitle", { count: file.deletions })}
              >
                {t("repo.diff.deletions", { count: file.deletions })}
              </span>
            </>
          ) : (
            <span className="text-fg-subtle">{t("repo.diff.noPatch.linesNotCounted")}</span>
          )}
        </div>
      </div>

      {reason ? (
        <Alert tone="info" role="presentation">
          <span>{t(NO_PATCH_KEYS[reason])}</span>
          <span className="text-xs font-medium text-fg-subtle tabular-nums">
            {sizeSummary(t, file)}
          </span>
        </Alert>
      ) : (
        <DiffView
          patch={file.patch}
          emptyLabel={t("repo.diff.patchEmpty")}
          truncatedNote={file.patch_truncated ? t("repo.diff.patchTruncated") : undefined}
        />
      )}
    </div>
  );
}

/**
 * The commit's own metadata, its totals, and one block per changed file.
 *
 * Split out from `RepoCommitDiff` so that the success branch takes a
 * `CommitDiffResponse` rather than a `CommitDiffResponse | null` narrowed by
 * a conditional several lines above it.
 */
function CommitDiffBody({
  t,
  diff,
  kind,
  ns,
  name,
}: {
  t: Translator;
  diff: CommitDiffResponse;
  kind: RepoKind;
  ns: string;
  name: string;
}) {
  return (
    <>
      <div className="flex flex-col gap-3 rounded-lg border border-border bg-bg-raised p-4">
        <div className="flex items-start gap-2">
          <GitCommitVertical size={18} className="mt-0.5 shrink-0 text-fg-subtle" />
          <p className="min-w-0 break-words text-sm font-medium text-fg">{diff.commit.message}</p>
        </div>

        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs font-medium text-fg-subtle">
          <span className="text-fg-muted">{diff.commit.author}</span>
          <TimeText iso={diff.commit.date} style="dateTime" />
          <span className="break-all font-mono">{diff.commit.oid}</span>
          <CopyButton value={diff.commit.oid} label={t("repo.diff.copySha")} iconOnly />
          {diff.parent_oid !== null && (
            <Link
              href={repoCommitHref(kind, ns, name, diff.parent_oid)}
              className="text-accent hover:underline"
            >
              {t("repo.diff.parent", { oid: diff.parent_oid.slice(0, 7) })}
            </Link>
          )}
        </div>

        {/* Not a footnote: with no parent, every path below reads as "added"
            because there was nothing to compare against, not because this
            commit created all of them. */}
        {diff.parent_oid === null && (
          <p className="text-xs font-medium text-fg-subtle">{t("repo.diff.rootCommit")}</p>
        )}

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm font-medium">
          <span className="text-fg tabular-nums">
            {t(diff.num_files === 1 ? "repo.diff.filesChangedOne" : "repo.diff.filesChangedOther", {
              count: diff.num_files,
            })}
          </span>
          <span
            className="text-positive-strong tabular-nums"
            title={t("repo.diff.additionsTitle", { count: diff.additions })}
          >
            {t("repo.diff.additions", { count: diff.additions })}
          </span>
          <span
            className="text-negative-strong tabular-nums"
            title={t("repo.diff.deletionsTitle", { count: diff.deletions })}
          >
            {t("repo.diff.deletions", { count: diff.deletions })}
          </span>
        </div>

        {/* The totals are the sum of what was counted, which is not the same
            as what changed the moment any file went uncounted. */}
        {countsArePartial(diff.files, diff.files_truncated) && (
          <p className="text-xs font-medium text-fg-subtle">{t("repo.diff.countsPartial")}</p>
        )}
      </div>

      {/* Never truncate in silence: say how many of the total are on screen,
          and that the rest are absent rather than collapsed. */}
      {diff.files_truncated && (
        <Alert
          tone="warning"
          role="presentation"
          title={t("repo.diff.filesTruncatedTitle", {
            shown: diff.files.length,
            total: diff.num_files,
          })}
        >
          <span>{t("repo.diff.filesTruncated")}</span>
        </Alert>
      )}

      {diff.files.length === 0 ? (
        <EmptyState
          icon={FileDiff}
          title={t("repo.diff.empty")}
          description={t("repo.diff.emptyDescription")}
        />
      ) : (
        <div className="flex flex-col gap-3">
          {diff.files.map((file) => (
            <DiffFileCard
              key={file.path}
              t={t}
              file={file}
              kind={kind}
              ns={ns}
              name={name}
              rev={diff.commit.oid}
            />
          ))}
        </div>
      )}
    </>
  );
}

/**
 * One commit's diff against its first parent.
 *
 * Everything the response is unsure about is said out loud rather than
 * rounded off: a file with no patch names which of the three reasons applies,
 * a capped file list says how many of the total it is showing, a cut-off
 * patch says so, and a root commit says it has no parent. The alternative —
 * a silent `+0 −0` and a quietly short list — is the failure DESIGN.md §9
 * is about.
 */
export async function RepoCommitDiff({
  kind,
  ns,
  name,
  rev,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
}) {
  // Forward the tf_session cookie so the request is authenticated and
  // resolves instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [repoResult, diffResult, t] = await Promise.all([
    getRepo(kind, ns, name, { headers }),
    getCommitDiff(kind, ns, name, rev, { headers }),
    getT(),
  ]);

  redirectIfRepoMoved(repoResult, (toNs, toName) => repoCommitHref(kind, toNs, toName, rev));
  if (isNotFound(repoResult)) {
    return <RepoNotFoundOrLogin currentPath={repoCommitHref(kind, ns, name, rev)} />;
  }
  if (!repoResult.ok) {
    return <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, repoResult)} />;
  }
  const repo = repoResult.data.repo;

  const diff = diffResult.ok ? diffResult.data : null;
  // The commit list is keyed by the revision the reader came from, and this
  // page may have been reached by a branch name; once the diff has resolved,
  // link back at the commit's own oid so the history starts where they are.
  const historyRev = diff ? diff.commit.oid : rev;

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb kind={kind} ns={ns} name={name} />

      <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>

      <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="files" />

      {repo.indexing && <IndexingBanner />}

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs font-medium">
        <Link
          href={repoCommitsHref(kind, ns, name, historyRev)}
          className="text-accent hover:underline"
        >
          {t("repo.diff.backToHistory")}
        </Link>
        {diff && (
          <Link
            href={repoTreeHref(kind, ns, name, diff.commit.oid)}
            className="text-accent hover:underline"
          >
            {t("repo.diff.browseFiles")}
          </Link>
        )}
      </div>

      {!diffResult.ok ? (
        // A 404 here is specific: the backend answers one for an unresolvable
        // revision *and* for a repository with no commits at all, and neither
        // is "the repository is missing" — the repo fetch above already
        // succeeded. Anything else is the generic failure path.
        isNotFound(diffResult) ? (
          <ErrorState
            title={t("repo.diff.revisionNotFound")}
            message={t("repo.diff.revisionNotFoundMessage")}
          />
        ) : (
          <ErrorState
            title={t("ui.errorStateTitle")}
            message={errorMessage(t, diffResult)}
            hint={t("repo.diff.errorHint")}
          />
        )
      ) : (
        <CommitDiffBody t={t} diff={diffResult.data} kind={kind} ns={ns} name={name} />
      )}
    </div>
  );
}
