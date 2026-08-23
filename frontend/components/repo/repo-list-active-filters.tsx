import Link from "next/link";
import { FilterChip } from "@/components/ui/filter-chip";
import { formatNumber } from "@/lib/format";
import type { Translator } from "@/lib/i18n";
import { getT } from "@/lib/i18n/server";
import {
  activeRepoFilters,
  type RepoFilterRef,
  type RepoListSearch,
  repoListHref,
} from "@/lib/repos";

/**
 * The row above the results: how many repositories matched, and one chip per
 * filter currently narrowing them.
 *
 * Always rendered, filters or not. It is the anchor the result grid sits under,
 * so making it appear only when a filter is on would shove the first row of
 * cards down at the moment the user applies one (DESIGN.md §8).
 *
 * Chips are built from the URL rather than from the facet response, which is
 * what makes them the reliable place to remove a filter: a selected tag can
 * drop out of the sidebar's own list once another facet narrows the set, and
 * without this row it would be stuck on with no control to remove it.
 */
export async function RepoListActiveFilters({
  basePath,
  sp,
  total,
  clearHref,
}: {
  basePath: string;
  sp: RepoListSearch;
  /** Matches for the current filters, from the listing response. */
  total: number;
  /** Where "clear filters" goes; keeps the host page's own params (e.g. `tab`). */
  clearHref: string;
}) {
  const t = await getT();
  const filters = activeRepoFilters(sp);

  // `min-h-7` so a chip appearing does not make the row taller than the bare
  // count did — the chip is 26px against the count line's 20px, and without
  // the floor the whole result grid slid 6px on every search.
  return (
    <div className="flex min-h-7 flex-wrap items-center gap-2">
      <span className="mr-1 shrink-0 text-sm tabular-nums text-fg-subtle">
        {t(total === 1 ? "repoList.results.countOne" : "repoList.results.countOther", {
          count: formatNumber(total),
        })}
      </span>
      {filters.map((ref) => {
        const { label, value } = chipText(t, ref);
        return (
          <FilterChip
            key={`${ref.key}:${ref.value}`}
            label={label}
            value={value}
            href={repoListHref(basePath, sp, { omit: ref })}
            removeLabel={t("repoList.results.remove", { value })}
          />
        );
      })}
      {filters.length > 0 && (
        <Link href={clearHref} className="shrink-0 text-xs font-medium text-accent hover:underline">
          {t("repoList.clearAll")}
        </Link>
      )}
    </div>
  );
}

/**
 * How one filter reads on its chip. The flags carry their whole meaning in the
 * label, so they get no separate value; everything else is "<field>: <value>".
 */
function chipText(t: Translator, ref: RepoFilterRef): { label?: string; value: string } {
  switch (ref.key) {
    case "search":
      return { label: t("repoList.results.search"), value: ref.value };
    case "tags":
      return { label: t("repoList.results.tag"), value: ref.value };
    case "license":
      return { label: t("repoList.results.license"), value: ref.value };
    case "task":
      return { label: t("repoList.results.task"), value: ref.value };
    case "relation":
      return { label: t("repoList.results.relation"), value: ref.value };
    case "base_model":
      return { label: t("repoList.results.baseModel"), value: ref.value };
    case "dataset":
      return { label: t("repoList.results.dataset"), value: ref.value };
    case "base_only":
      return { value: t("repoList.results.baseOnly") };
    // No `default`: the switch is exhaustive over RepoFilterRef, so adding a
    // filter kind without giving it a chip label fails typecheck here rather
    // than silently rendering as an archive chip.
    case "archived":
      return {
        value:
          ref.value === "true"
            ? t("repoList.results.archivedOnly")
            : t("repoList.results.activeOnly"),
      };
  }
}
