import { Archive, Boxes, Database, Download, FlaskConical } from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { TimeText } from "@/components/ui/time-text";
import { formatCompactNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { namespaceHref } from "@/lib/namespace";
import { repoBase } from "@/lib/paths";
import type { RepoSummary } from "@/types/api";

export async function RepoCard({ repo }: { repo: RepoSummary }) {
  const t = await getT();
  const href = repoBase(repo.kind, repo.namespace, repo.name);
  const Icon = repo.kind === "model" ? Boxes : Database;
  return (
    // The card is not itself a <Link>: the namespace needs its own link to
    // /{ns}, and links cannot nest. The repository link instead stretches
    // over the whole card with `after:absolute after:inset-0`, so clicking
    // anywhere still opens the repository while the namespace link — raised
    // above it with `relative z-10` — keeps its own target.
    <div className="group relative flex flex-col gap-2 rounded-lg border border-border bg-bg-raised p-4 transition-colors hover:border-border-strong hover:bg-bg-hover">
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-1.5">
          <Icon size={14} className="shrink-0 text-fg-subtle" />
          <span className="flex min-w-0 items-center text-sm font-medium">
            <Link
              href={namespaceHref(repo.namespace)}
              className="relative z-10 truncate text-fg-muted hover:text-fg hover:underline"
            >
              {repo.namespace}
            </Link>
            <span className="px-0.5 text-fg-subtle">/</span>
            <Link
              href={href}
              className="truncate text-fg after:absolute after:inset-0 group-hover:underline"
            >
              {repo.name}
            </Link>
          </span>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {repo.archived && (
            <Badge tone="warning">
              <Archive size={11} />
              {t("repo.archived.badge")}
            </Badge>
          )}
          {repo.is_experiment && (
            <Badge tone="accent">
              <FlaskConical size={11} />
              {t("repoList.card.experiment")}
            </Badge>
          )}
        </div>
      </div>

      {repo.description && (
        <p className="line-clamp-2 text-sm text-fg-subtle">{repo.description}</p>
      )}

      {repo.tags.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {repo.tags.slice(0, 4).map((tag) => (
            <Badge key={tag}>{tag}</Badge>
          ))}
          {repo.tags.length > 4 && <Badge>+{repo.tags.length - 4}</Badge>}
        </div>
      )}

      <div className="mt-1 flex items-center gap-3 text-xs font-medium text-fg-subtle">
        <span className="flex items-center gap-1 tabular-nums">
          <Download size={12} />
          {formatCompactNumber(repo.downloads)}
        </span>
        <TimeText iso={repo.updated_at} style="relative" />
        {repo.license && <span className="truncate">{repo.license}</span>}
      </div>
    </div>
  );
}
