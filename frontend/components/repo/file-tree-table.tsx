import { ChartNoAxesCombined, CornerLeftUp } from "lucide-react";
import Link from "next/link";
import { CommitBar } from "@/components/repo/commit-bar";
import { EntryIcon } from "@/components/repo/file-icon";
import { Badge } from "@/components/ui/badge";
import { TimeText } from "@/components/ui/time-text";
import { formatBytes } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { repoBlobHref, repoCommitsHref, repoTreeHref, repoViewerHref } from "@/lib/paths";
import type { CommitInfoUI, RepoKind, TreeEntryUI } from "@/types/api";

export async function FileTreeTable({
  entries,
  kind,
  ns,
  name,
  rev,
  path,
  latestCommit,
  commitsHref,
}: {
  entries: TreeEntryUI[];
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  /** Current directory, as path segments from the repo root (empty at the root). */
  path: string[];
  latestCommit?: CommitInfoUI | null;
  commitsHref?: string;
}) {
  const t = await getT();
  const sorted = [...entries].sort((a, b) => {
    if (a.type !== b.type) return a.type === "directory" ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  const parentHref =
    path.length > 0 ? repoTreeHref(kind, ns, name, rev, path.slice(0, -1).join("/")) : null;

  return (
    <div className="scroll-x rounded-lg border border-border">
      {latestCommit && (
        <CommitBar commit={latestCommit} historyHref={commitsHref} className="border-b" />
      )}
      <table className="w-full min-w-[480px] border-collapse text-sm">
        <thead>
          <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
            <th scope="col" className="px-3 py-2 font-medium">
              {t("repo.treeTable.name")}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t("repo.treeTable.lastCommit")}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t("repo.treeTable.size")}
            </th>
            <th scope="col" className="px-3 py-2 text-right font-medium">
              {t("repo.treeTable.updated")}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              <span className="sr-only">{t("repo.treeTable.actionsSr")}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {parentHref && (
            <tr className="border-b border-border last:border-0 hover:bg-bg-hover">
              <td className="p-0" colSpan={5}>
                <Link
                  href={parentHref}
                  className="flex items-center gap-2 px-3 py-2 text-fg-subtle hover:text-fg"
                >
                  <CornerLeftUp size={14} />
                  {t("repo.treeTable.upDir")}
                </Link>
              </td>
            </tr>
          )}
          {sorted.map((entry) => {
            const href =
              entry.type === "directory"
                ? repoTreeHref(kind, ns, name, rev, entry.path)
                : repoBlobHref(kind, ns, name, rev, entry.path);
            return (
              // `relative` on a <tr> (so the name link's ::after below can
              // stretch over the whole row) is only defined from CSS Position
              // 3 — but every engine in this project's browser baseline
              // (Tailwind v4 requires Safari 16.4+ / Chrome 111+ / Firefox
              // 128+) implements it. The other cells' links sit above the
              // overlay with `relative z-10` instead of nesting inside it,
              // which would be invalid HTML.
              <tr
                key={entry.path}
                className="relative border-b border-border last:border-0 hover:bg-bg-hover"
              >
                <td className="px-3 py-2">
                  <Link
                    href={href}
                    className="flex items-center gap-2 text-fg after:absolute after:inset-0 after:content-['']"
                  >
                    <EntryIcon entry={entry} />
                    <span className="truncate">{entry.name}</span>
                    {entry.lfs && <Badge tone="accent">LFS</Badge>}
                  </Link>
                </td>
                <td className="max-w-0 truncate px-3 py-2 text-fg-subtle">
                  {entry.last_commit ? (
                    <Link
                      href={repoCommitsHref(kind, ns, name, rev, entry.path)}
                      className="relative z-10 hover:underline"
                    >
                      {entry.last_commit.message}
                    </Link>
                  ) : (
                    "—"
                  )}
                </td>
                <td className="tabular-nums px-3 py-2 text-fg-subtle">
                  {entry.type === "file" ? formatBytes(entry.size) : "—"}
                </td>
                <td className="px-3 py-2 text-right text-fg-subtle">
                  {entry.last_commit ? (
                    <TimeText iso={entry.last_commit.date} style="relative" />
                  ) : (
                    "—"
                  )}
                </td>
                <td className="px-3 py-2 text-right">
                  {entry.is_parquet && (
                    <Link
                      href={repoViewerHref(kind, ns, name, rev, entry.path)}
                      className="relative z-10 inline-flex items-center gap-1 text-xs font-medium text-accent hover:underline"
                    >
                      <ChartNoAxesCombined size={12} />
                      {t("repo.treeTable.openInViewer")}
                    </Link>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
