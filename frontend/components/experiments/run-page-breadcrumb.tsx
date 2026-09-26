"use client";

import { ArrowLeft } from "lucide-react";
import Link from "next/link";

import { useT } from "@/lib/i18n/client";

/**
 * Where the run lives, ending in the way back: `experiments / ns/repo /
 * ← project`. The project crumb is the "back to project" link, so it is drawn
 * as one (arrow, full-strength text) rather than as one more grey segment.
 *
 * `projectHref` may carry the project page's own state (`?runs=` with this
 * run), so the reader lands with the run they were just reading selected.
 */
export function RunPageBreadcrumb({
  ns,
  repo,
  project,
  projectHref,
}: {
  ns: string;
  repo: string;
  project: string;
  projectHref: string;
}) {
  const t = useT();
  const backLabel = t("experiments.runPage.backToProject", { project });
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-sm whitespace-nowrap text-fg-subtle">
      <Link href="/experiments" className="hidden hover:text-fg hover:underline sm:inline">
        {t("experiments.repo.breadcrumbRoot")}
      </Link>
      <span className="hidden sm:inline" aria-hidden>
        /
      </span>
      <Link
        href={`/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}`}
        className="hidden min-w-0 truncate hover:text-fg hover:underline sm:inline"
      >
        {ns}/{repo}
      </Link>
      <span className="hidden sm:inline" aria-hidden>
        /
      </span>
      <Link
        href={projectHref}
        title={backLabel}
        aria-label={backLabel}
        className="flex min-w-0 items-center gap-1 rounded-md font-medium text-fg-muted hover:text-fg hover:underline"
      >
        <ArrowLeft size={14} className="shrink-0" />
        <span className="truncate">{project}</span>
      </Link>
    </div>
  );
}
