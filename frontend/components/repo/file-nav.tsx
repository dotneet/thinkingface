import { GitBranch } from "lucide-react";
import Link from "next/link";
import { RefSwitcher } from "@/components/repo/ref-switcher";
import { CopyButton } from "@/components/ui/copy-button";
import { getT } from "@/lib/i18n/server";
import { repoTreeHref } from "@/lib/paths";
import type { RefsResponseUI, RepoKind } from "@/types/api";

const SHA_RE = /^[0-9a-f]{7,40}$/i;

/** Short form of a commit SHA; leaves branch/tag names alone. */
function shortRev(rev: string): string {
  return SHA_RE.test(rev) ? rev.slice(0, 7) : rev;
}

/** Static rev chip shown when refs couldn't be loaded — same shape as RefSwitcher's trigger. */
function StaticRevChip({ rev }: { rev: string }) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-sm font-medium text-fg-muted">
      <GitBranch size={14} />
      <span className="max-w-[12rem] truncate font-mono">{shortRev(rev)}</span>
    </span>
  );
}

/**
 * HuggingFace-style nav row shown above a file tree, blob or commit history:
 * [ref ▾] repo-name / dir / file ⧉
 *
 * `refs` degrades to a static, non-interactive rev chip when the refs fetch
 * failed or the caller didn't ask for it — the row itself is never omitted.
 */
export async function FileNav({
  kind,
  ns,
  name,
  rev,
  path,
  refs,
  target = "tree",
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
  refs?: RefsResponseUI;
  /** Where selecting a ref in the switcher navigates to. Defaults to the file tree. */
  target?: "tree" | "commits" | "blob";
}) {
  const t = await getT();
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-sm">
      {refs ? (
        <RefSwitcher
          kind={kind}
          ns={ns}
          name={name}
          currentRev={rev}
          path={path}
          refs={refs}
          target={target}
        />
      ) : (
        <StaticRevChip rev={rev} />
      )}
      <span className="text-fg-subtle">/</span>
      <Link
        href={repoTreeHref(kind, ns, name, rev)}
        className="font-medium text-fg hover:underline"
      >
        {name}
      </Link>
      {path.map((seg, i) => {
        const isLast = i === path.length - 1;
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: path segments can repeat (a/b/a) but are never reordered
          <span key={i} className="flex items-center gap-1.5">
            <span className="text-fg-subtle">/</span>
            {isLast ? (
              <span className="font-medium text-fg">{seg}</span>
            ) : (
              <Link
                href={repoTreeHref(kind, ns, name, rev, path.slice(0, i + 1).join("/"))}
                className="text-fg-muted hover:text-fg hover:underline"
              >
                {seg}
              </Link>
            )}
          </span>
        );
      })}
      {path.length > 0 && (
        <CopyButton
          value={path.join("/")}
          label={t("repo.fileNav.copyPath")}
          iconOnly
          className="ml-1"
        />
      )}
    </div>
  );
}
