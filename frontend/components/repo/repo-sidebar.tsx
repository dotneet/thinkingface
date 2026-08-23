import {
  Building2,
  Download,
  GitBranch,
  HardDrive,
  Tag as TagIcon,
  User as UserIcon,
} from "lucide-react";
import Link from "next/link";
import { GcsAccessDialog } from "@/components/repo/gcs-access-dialog";
import { badgeClass } from "@/components/ui/badge";
import { CodeBlock } from "@/components/ui/code-block";
import { TimeText } from "@/components/ui/time-text";
import { formatBytes, formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { namespaceHref } from "@/lib/namespace";
import { repoTreeHref } from "@/lib/paths";
import type { RepoDetail } from "@/types/api";

export async function RepoSidebar({ repo }: { repo: RepoDetail }) {
  const t = await getT();
  return (
    <aside className="flex w-full flex-col gap-5 text-sm lg:w-72 lg:shrink-0">
      <div className="flex flex-col gap-2">
        {/* Users and organisations share one profile page
            (docs/namespace-design.md §4.1), so both owners link out; only the
            label and glyph tell the two kinds apart. */}
        <div className="flex items-center justify-between">
          <span className="flex items-center gap-1.5 text-fg-subtle">
            {repo.namespace_kind === "org" ? <Building2 size={13} /> : <UserIcon size={13} />}
            {t(
              repo.namespace_kind === "org"
                ? "repo.sidebar.organization"
                : "repo.sidebar.userNamespace",
            )}
          </span>
          <Link href={namespaceHref(repo.namespace)} className="text-accent hover:underline">
            {repo.namespace}
          </Link>
        </div>
        <MetaRow
          icon={Download}
          label={t("repo.sidebar.downloads")}
          value={formatNumber(repo.downloads)}
        />
        <MetaRow
          icon={Download}
          label={t("repo.sidebar.downloads30d")}
          value={formatNumber(repo.downloads_last_30_days)}
        />
        <MetaRow
          icon={HardDrive}
          label={t("repo.sidebar.size")}
          value={formatBytes(repo.total_size)}
        />
        <MetaRow
          icon={GitBranch}
          label={t("repo.sidebar.files")}
          value={formatNumber(repo.num_files)}
        />
        {repo.license && (
          <MetaRow icon={TagIcon} label={t("repo.sidebar.license")} value={repo.license} />
        )}
        <MetaRow
          icon={GitBranch}
          label={t("repo.sidebar.updated")}
          value={<TimeText iso={repo.updated_at} style="date" />}
        />
      </div>

      {repo.tags.length > 0 && (
        <div>
          <div className="mb-2 text-xs font-medium uppercase tracking-wide text-fg-subtle">
            {t("repo.sidebar.tags")}
          </div>
          <div className="flex flex-wrap gap-1.5">
            {repo.tags.map((tag) => (
              <Link
                key={tag}
                href={`/${repo.kind}s?tag=${encodeURIComponent(tag)}`}
                className={badgeClass({
                  className: "hover:border-border-strong hover:text-fg",
                })}
              >
                {tag}
              </Link>
            ))}
          </div>
        </div>
      )}

      <GcsAccessDialog
        kind={repo.kind}
        ns={repo.namespace}
        name={repo.name}
        rev={repo.default_branch}
      />

      <CodeBlock value={`git clone ${repo.clone_url}`} label="git clone" />

      {repo.branches.length > 0 && (
        <div>
          <div className="mb-2 text-xs font-medium uppercase tracking-wide text-fg-subtle">
            {t("repo.sidebar.branches")}
          </div>
          <div className="flex flex-wrap gap-1.5">
            {repo.branches.map((b) => (
              <Link
                key={b}
                href={repoTreeHref(repo.kind, repo.namespace, repo.name, b)}
                className={badgeClass({
                  tone: b === repo.default_branch ? "accent" : "neutral",
                  className:
                    b === repo.default_branch
                      ? "border-accent bg-transparent hover:border-accent"
                      : "bg-transparent hover:border-border-strong hover:text-fg",
                })}
              >
                {b}
              </Link>
            ))}
          </div>
        </div>
      )}
    </aside>
  );
}

function MetaRow({
  icon: Icon,
  label,
  value,
}: {
  icon: typeof Download;
  label: string;
  value: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between">
      <span className="flex items-center gap-1.5 text-fg-subtle">
        <Icon size={13} />
        {label}
      </span>
      <span className="tabular-nums text-fg">{value}</span>
    </div>
  );
}
