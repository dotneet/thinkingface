import { ChartNoAxesCombined, CornerLeftUp } from "lucide-react";
import Link from "next/link";
import { CommitBar } from "@/components/repo/commit-bar";
import { EntryIcon } from "@/components/repo/file-icon";
import { RenameFileButton } from "@/components/repo/rename-file-button";
import { Badge } from "@/components/ui/badge";
import { TBody, Td, THead, Th, Tr } from "@/components/ui/table";
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
  canRename = false,
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
  /**
   * Whether to offer the per-row rename. False for a reader, and false on a
   * revision that is not a branch — there is no ref to advance on a tag or a
   * detached SHA, and the API refuses it.
   */
  canRename?: boolean;
}) {
  const t = await getT();
  const sorted = [...entries].sort((a, b) => {
    if (a.type !== b.type) return a.type === "directory" ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  const parentHref =
    path.length > 0 ? repoTreeHref(kind, ns, name, rev, path.slice(0, -1).join("/")) : null;

  return (
    // `max-h-[70vh] overflow-y-auto` alongside `scroll-x`, the same pairing
    // run-table.tsx uses: THead's `sticky` positions relative to the nearest
    // *scrolling* ancestor, and without a bounded one here that would be the
    // page itself — sticking the header at viewport y=0, directly under the
    // site header's own `sticky top-0` (site-header.tsx), rather than at the
    // top of this box.
    <div className="scroll-x max-h-[70vh] overflow-y-auto rounded-lg border border-border">
      {latestCommit && (
        <CommitBar commit={latestCommit} historyHref={commitsHref} className="border-b" />
      )}
      <table className="w-full min-w-[480px] border-collapse text-sm">
        <THead sticky>
          <Th scope="col">{t("repo.treeTable.name")}</Th>
          <Th scope="col">{t("repo.treeTable.lastCommit")}</Th>
          <Th scope="col">{t("repo.treeTable.size")}</Th>
          <Th scope="col" align="right">
            {t("repo.treeTable.updated")}
          </Th>
          <Th scope="col">
            <span className="sr-only">{t("repo.treeTable.actionsSr")}</span>
          </Th>
        </THead>
        <TBody>
          {parentHref && (
            <Tr className="hover:bg-bg-hover">
              <Td className="p-0" colSpan={5}>
                <Link
                  href={parentHref}
                  className="flex items-center gap-2 px-3 py-2 text-fg-subtle hover:text-fg"
                >
                  <CornerLeftUp size={14} />
                  {t("repo.treeTable.upDir")}
                </Link>
              </Td>
            </Tr>
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
              <Tr key={entry.path} className="relative hover:bg-bg-hover">
                <Td>
                  <Link
                    href={href}
                    className="flex items-center gap-2 text-fg after:absolute after:inset-0 after:content-['']"
                  >
                    <EntryIcon entry={entry} />
                    {/* A long file name is clipped by `truncate` with nothing
                        to reveal it — the row links to the file, not to its
                        name — so the full text lives in the tooltip. */}
                    <span className="truncate" title={entry.name}>
                      {entry.name}
                    </span>
                    {entry.lfs && <Badge tone="accent">LFS</Badge>}
                  </Link>
                </Td>
                {/* `max-w-0` lets this column give its width to the others,
                    which means the commit message is clipped at almost any
                    viewport width; the tooltip is the only way to read it. */}
                <Td className="max-w-0 truncate text-fg-subtle">
                  {entry.last_commit ? (
                    <Link
                      href={repoCommitsHref(kind, ns, name, rev, entry.path)}
                      className="relative z-10 hover:underline"
                      title={entry.last_commit.message}
                    >
                      {entry.last_commit.message}
                    </Link>
                  ) : (
                    "—"
                  )}
                </Td>
                <Td className="tabular-nums text-fg-subtle">
                  {entry.type === "file" ? formatBytes(entry.size) : "—"}
                </Td>
                <Td align="right" className="text-fg-subtle">
                  {entry.last_commit ? (
                    <TimeText iso={entry.last_commit.date} style="relative" />
                  ) : (
                    "—"
                  )}
                </Td>
                <Td align="right">
                  <div className="flex items-center justify-end gap-2">
                    {entry.is_parquet && (
                      <Link
                        href={repoViewerHref(kind, ns, name, rev, entry.path)}
                        className="relative z-10 inline-flex items-center gap-1 text-xs font-medium text-accent hover:underline"
                      >
                        <ChartNoAxesCombined size={12} />
                        {t("repo.treeTable.openInViewer")}
                      </Link>
                    )}
                    {/* Directories are left out: git has no directory objects
                        to move, so renaming one means rewriting every path
                        below it -- a different operation from this one, and
                        not one the single-file endpoint performs. */}
                    {canRename && entry.type === "file" && (
                      <RenameFileButton
                        kind={kind}
                        ns={ns}
                        name={name}
                        rev={rev}
                        path={entry.path.split("/")}
                        baseOid={entry.oid}
                        variant="link"
                      />
                    )}
                  </div>
                </Td>
              </Tr>
            );
          })}
        </TBody>
      </table>
    </div>
  );
}
