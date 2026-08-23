import { History } from "lucide-react";
import Link from "next/link";
import { TimeText } from "@/components/ui/time-text";
import { cn } from "@/lib/cn";
import { getT } from "@/lib/i18n/server";
import type { CommitInfoUI } from "@/types/api";

/**
 * HuggingFace-style bar showing a single commit: the message, short oid,
 * author and relative date, with an optional link to that path's full
 * history. Used above the file tree (the listing's latest commit, nested
 * inside file-tree-table's own bordered box — pass `border-b` only) and
 * above a file preview (that file's last commit, standalone — pass a full
 * `rounded-lg border`).
 */
export async function CommitBar({
  commit,
  historyHref,
  className,
}: {
  commit: CommitInfoUI;
  historyHref?: string;
  className?: string;
}) {
  const t = await getT();
  return (
    <div
      className={cn(
        "flex items-center gap-3 border-border bg-bg-sunken px-3 py-2 text-sm",
        className,
      )}
    >
      <span className="min-w-0 flex-1 truncate text-fg">{commit.message}</span>
      <span className="shrink-0 font-mono text-xs font-medium text-fg-subtle">
        {commit.oid.slice(0, 7)}
      </span>
      <span className="shrink-0 text-fg-subtle">·</span>
      <span className="shrink-0 truncate text-fg-subtle">{commit.author}</span>
      <TimeText iso={commit.date} style="relative" className="shrink-0 text-fg-subtle" />
      {historyHref && (
        <Link
          href={historyHref}
          className="ml-1 flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-xs font-medium text-fg-muted hover:bg-bg-hover hover:text-fg"
        >
          <History size={12} />
          {t("repo.commitBar.history")}
        </Link>
      )}
    </div>
  );
}
