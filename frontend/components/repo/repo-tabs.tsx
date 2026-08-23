import Link from "next/link";
import { getT } from "@/lib/i18n/server";
import { repoBase, repoTreeHref, repoViewerHref } from "@/lib/paths";
import type { ParquetSummary, RepoDetail, RepoKind } from "@/types/api";

/**
 * Picks which parquet file the "Viewer" tab jumps to when the caller hasn't
 * already got one open. `repo.parquet_files` arrives in path order, so the
 * naive `[0]` picks whichever file sorts first alphabetically — a
 * `data/empty.parquet` next to a `data/rich.parquet` lands the reader on an
 * empty grid. Row count is the best proxy we have for "the file worth
 * looking at" without adding a new backend field; ties keep the first path
 * in array order so the choice stays stable across renders.
 */
function defaultParquetPath(files: ParquetSummary[]): string | undefined {
  if (files.length === 0) return undefined;
  let best = files[0]!;
  for (const file of files) {
    if (file.num_rows > best.num_rows) best = file;
  }
  return best.path;
}

export async function RepoTabs({
  kind,
  ns,
  name,
  repo,
  active,
  viewerPath,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  repo: RepoDetail;
  active: "card" | "files" | "viewer" | "experiments" | "settings";
  viewerPath?: string;
}) {
  const t = await getT();
  const base = repoBase(kind, ns, name);
  const rev = repo.default_branch;
  const parquetPath = viewerPath || defaultParquetPath(repo.parquet_files);

  const tabs: { key: typeof active; label: string; href: string }[] = [
    { key: "card", label: t("repo.tabs.card"), href: base },
    { key: "files", label: t("repo.tabs.files"), href: repoTreeHref(kind, ns, name, rev) },
  ];
  if (parquetPath) {
    tabs.push({
      key: "viewer",
      label: t("repo.tabs.viewer"),
      href: repoViewerHref(kind, ns, name, rev, parquetPath),
    });
  }
  if (repo.is_experiment) {
    tabs.push({
      key: "experiments",
      label: t("repo.tabs.experiments"),
      href: `/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`,
    });
  }
  // `can_admin`, not `can_write`: archiving clears can_write, and gating the
  // tab on it hid the only route to the page that can unarchive again — an
  // owner had to type the URL by hand to undo their own archive.
  if (repo.can_admin) {
    tabs.push({ key: "settings", label: t("repo.tabs.settings"), href: `${base}/settings` });
  }

  return (
    <div className="flex gap-1 border-b border-border">
      {tabs.map((tab) => (
        <Link
          key={tab.key}
          href={tab.href}
          className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
            active === tab.key
              ? "border-accent text-fg"
              : "border-transparent text-fg-subtle hover:text-fg"
          }`}
        >
          {tab.label}
        </Link>
      ))}
    </div>
  );
}
