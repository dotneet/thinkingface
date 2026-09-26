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

import { LIVE_REFRESH_INTERVAL_MS } from "@/components/experiments/live-refresh";
import { RunPageOutputRow } from "@/components/experiments/run-page-output-row";
import { Badge } from "@/components/ui/badge";
import { ErrorState } from "@/components/ui/error-state";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { expArtifactHref, listRunArtifacts } from "@/lib/experiments";
import { formatBytes, formatNumber } from "@/lib/format";
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
  live = false,
}: {
  ns: string;
  repo: string;
  project: string;
  runName: string;
  /**
   * The run is still training, so it may still be committing artifacts. The
   * caller decides this from the run's derived status (see live-refresh.ts);
   * a finished, failed or stale run never grows a new file, so its listing is
   * fetched once and left alone.
   */
  live?: boolean;
}) {
  const t = useT();
  const artifacts = useQuery({
    queryKey: ["exp-artifacts", ns, repo, project, runName],
    queryFn: async () => {
      const result = await listRunArtifacts(ns, repo, project, runName);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    refetchInterval: live ? LIVE_REFRESH_INTERVAL_MS : false,
    refetchIntervalInBackground: false,
  });

  const row = {
    icon: FileArchive,
    title: t("experiments.artifacts.title"),
    description: t("experiments.artifacts.description", { project, run: runName }),
  };

  if (artifacts.isPending) {
    return (
      <RunPageOutputRow {...row}>
        <Skeleton className="h-9 w-full" />
      </RunPageOutputRow>
    );
  }

  if (artifacts.isError) {
    return (
      <RunPageOutputRow {...row}>
        <ErrorState
          title={t("experiments.errorTitle")}
          message={queryErrorMessage(t, artifacts.error, t("experiments.artifacts.loadFailed"))}
        />
      </RunPageOutputRow>
    );
  }

  const { artifacts: items, rev } = artifacts.data;
  if (items.length === 0) {
    return <RunPageOutputRow {...row} empty={t("experiments.runPage.artifactsNone")} />;
  }

  return (
    <RunPageOutputRow {...row} count={formatNumber(items.length)}>
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
    </RunPageOutputRow>
  );
}
