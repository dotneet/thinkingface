"use client";

import { useQuery } from "@tanstack/react-query";
import {
  Boxes,
  FileArchive,
  FileCode,
  FileText,
  Image as ImageIcon,
  type LucideIcon,
  Table2,
} from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { expArtifactHref, listRunArtifacts } from "@/lib/experiments";
import { formatBytes } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { PreviewKind } from "@/types/api";

/** Icon per preview kind, so a plot and a JSON dump are told apart at a glance. */
const ICONS: Record<PreviewKind, LucideIcon> = {
  image: ImageIcon,
  parquet: Table2,
  markdown: FileText,
  text: FileCode,
  model: Boxes,
  binary: FileArchive,
  // PreviewKindNone is the empty string (a directory), which never reaches
  // this list — the listing only carries files.
  "": FileArchive,
};

/**
 * The files a run produced: whatever `trackio.log_artifact` committed under
 * `{project}/artifacts/{run}` in the same dataset repository as the metrics
 * (docs/dev/api-contract.md §7).
 *
 * They are plain repository files, which is the whole point of the design —
 * so every row links into the file browser that already exists rather than
 * into a viewer of its own, and the same bytes are reachable through
 * `git clone`, the resolve API, or the repository's GCS access script.
 */
export function RunArtifactsCard({
  ns,
  repo,
  project,
  runName,
}: {
  ns: string;
  repo: string;
  project: string;
  runName: string;
}) {
  const t = useT();
  const artifacts = useQuery({
    queryKey: ["exp-artifacts", ns, repo, project, runName],
    queryFn: async () => {
      const result = await listRunArtifacts(ns, repo, project, runName);
      if (!result.ok) throw new Error(result.message);
      return result.data;
    },
  });

  if (artifacts.isPending) {
    return (
      <div className="flex flex-col gap-2">
        {["a", "b", "c"].map((key) => (
          <Skeleton key={key} className="h-10 w-full" />
        ))}
      </div>
    );
  }

  if (artifacts.isError) {
    return (
      <ErrorState
        title={t("experiments.errorTitle")}
        message={
          artifacts.error instanceof Error
            ? artifacts.error.message
            : t("experiments.artifacts.loadFailed")
        }
      />
    );
  }

  const { artifacts: items, rev } = artifacts.data;
  if (items.length === 0) {
    return (
      <EmptyState
        icon={FileArchive}
        title={t("experiments.artifacts.emptyTitle")}
        description={t("experiments.artifacts.emptyDescription")}
      />
    );
  }

  return (
    <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-lg border border-border">
      {items.map((artifact) => {
        const Icon = ICONS[artifact.preview] ?? FileArchive;
        return (
          <li key={artifact.path}>
            <Link
              href={expArtifactHref(ns, repo, rev, artifact)}
              className="flex items-center gap-3 px-3 py-2 text-sm hover:bg-bg-sunken"
            >
              <Icon size={15} strokeWidth={1.5} className="shrink-0 text-fg-subtle" />
              <span className="min-w-0 flex-1 truncate font-mono text-xs">{artifact.name}</span>
              {artifact.lfs && <Badge>{t("experiments.artifacts.lfsBadge")}</Badge>}
              <span className="shrink-0 tabular-nums text-xs font-medium text-fg-subtle">
                {formatBytes(artifact.size)}
              </span>
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
