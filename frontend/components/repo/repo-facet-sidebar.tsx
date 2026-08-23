"use client";

import { X } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/field";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";
import { listFlagOn } from "@/lib/repos";
import type { RepoFacetItem, RepoFacets, RepoKind } from "@/types/api";

/** Tri-state archive filter, spelled the way `?archived=` is. */
type ArchiveMode = "all" | "active" | "archived";

/**
 * Filters for the /datasets and /models listings: the card facets
 * (tag/license/task) with counts from the current result set, the lineage
 * axis (base model, relation, dataset, "base only"), and the archive
 * tri-state.
 *
 * The URL query string is the source of truth (see repo-list-page.tsx): every
 * control here pushes a new URL rather than holding local state.
 */
export function RepoFacetSidebar({
  basePath,
  clearHref,
  kind,
  facets,
}: {
  basePath: string;
  /** Where "clear filters" goes; defaults to basePath (see RepoListPage). */
  clearHref?: string;
  kind: RepoKind;
  facets: RepoFacets;
}) {
  const t = useT();
  const router = useRouter();
  const searchParams = useSearchParams();

  const selectedTags = new Set(searchParams.getAll("tags"));
  const legacyTag = searchParams.get("tag");
  if (legacyTag) selectedTags.add(legacyTag);
  const selectedLicense = searchParams.get("license") ?? "";
  const selectedTask = searchParams.get("task") ?? "";
  const selectedRelation = searchParams.get("relation") ?? "";
  const baseModel = searchParams.get("base_model") ?? "";
  const dataset = searchParams.get("dataset") ?? "";
  const baseOnly = listFlagOn(searchParams.get("base_only"));
  const archiveMode = archiveModeOf(searchParams.get("archived"));

  const searchTerm = searchParams.get("search") ?? searchParams.get("q") ?? "";
  const hasActiveFilters =
    selectedTags.size > 0 ||
    selectedLicense !== "" ||
    selectedTask !== "" ||
    selectedRelation !== "" ||
    baseModel !== "" ||
    dataset !== "" ||
    baseOnly ||
    archiveMode !== "all" ||
    searchTerm !== "";

  function push(mutate: (params: URLSearchParams) => void) {
    const params = new URLSearchParams(searchParams.toString());
    // A stale singular `tag=` would otherwise keep re-adding itself to
    // selectedTags after every toggle; folding it into `tags=` up front lets
    // the checkbox list behave like a normal multi-select from here on.
    const tag = params.get("tag");
    params.delete("tag");
    if (tag) {
      const tags = new Set(params.getAll("tags"));
      tags.add(tag);
      params.delete("tags");
      for (const each of tags) params.append("tags", each);
    }
    params.delete("offset");
    mutate(params);
    router.push(params.toString() ? `${basePath}?${params.toString()}` : basePath);
  }

  function toggleTag(tag: string) {
    push((params) => {
      const tags = new Set(params.getAll("tags"));
      if (tags.has(tag)) tags.delete(tag);
      else tags.add(tag);
      params.delete("tags");
      for (const each of tags) params.append("tags", each);
    });
  }

  function toggleSingle(key: "license" | "task" | "relation", value: string) {
    push((params) => {
      if (params.get(key) === value) params.delete(key);
      else params.set(key, value);
      // A relation only exists on a base_model edge, so the two are mutually
      // exclusive: picking one clears the other rather than returning zero
      // results and looking broken.
      if (key === "relation" && params.get(key)) params.delete("base_only");
    });
  }

  function toggleBaseOnly() {
    push((params) => {
      if (listFlagOn(params.get("base_only"))) {
        params.delete("base_only");
        return;
      }
      params.set("base_only", "true");
      for (const key of ["base_model", "relation"]) params.delete(key);
    });
  }

  function setArchiveMode(mode: ArchiveMode) {
    push((params) => {
      if (mode === "all") params.delete("archived");
      else params.set("archived", String(mode === "archived"));
    });
  }

  // "Base only" is a model-tree idea: a dataset never declares a base model,
  // so on /datasets the toggle would be a no-op that always matches.
  const showBaseOnly = kind === "model";

  const controls = (
    <>
      {/* A fixed-height row, not `{hasActiveFilters && <Button/>}` on its own:
          letting the clear button appear at the top of the column pushed every
          facet checkbox below it down 46px the moment the first filter went on,
          which is more than the 29px row pitch — so the next click landed on
          the wrong row (DESIGN.md §8). The row is always here; only its button
          comes and goes. */}
      <div className="flex h-7 shrink-0 items-center justify-between gap-2">
        <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
          {t("repoList.filtersTitle")}
        </span>
        {hasActiveFilters && (
          <Button variant="ghost" size="sm" onClick={() => router.push(clearHref ?? basePath)}>
            {t("repoList.clearFilters")}
          </Button>
        )}
      </div>

      {(showBaseOnly || baseModel !== "" || dataset !== "") && (
        <div>
          <GroupTitle>{t("repoList.lineage.title")}</GroupTitle>
          <div className="flex flex-col gap-2">
            {baseModel !== "" && (
              <ActiveRef
                label={t("repoList.lineage.derivedFrom", { ref: baseModel })}
                removeLabel={t("repoList.lineage.remove")}
                onRemove={() =>
                  push((params) => {
                    params.delete("base_model");
                    params.delete("relation");
                  })
                }
              />
            )}
            {dataset !== "" && (
              <ActiveRef
                label={t("repoList.lineage.trainedOn", { ref: dataset })}
                removeLabel={t("repoList.lineage.remove")}
                onRemove={() => push((params) => params.delete("dataset"))}
              />
            )}
            {showBaseOnly && (
              <label className="flex cursor-pointer items-start gap-2 text-xs">
                <Checkbox checked={baseOnly} onChange={toggleBaseOnly} className="mt-0.5" />
                <span className="min-w-0">
                  <span className="text-fg-muted">{t("repoList.lineage.baseOnly")}</span>
                  <span className="block text-fg-subtle">{t("repoList.lineage.baseOnlyHint")}</span>
                </span>
              </label>
            )}
          </div>
        </div>
      )}

      <div>
        <GroupTitle>{t("repoList.archive.title")}</GroupTitle>
        <SegmentedControl<ArchiveMode>
          label={t("repoList.archive.title")}
          value={archiveMode}
          onChange={setArchiveMode}
          options={[
            { value: "all", label: t("repoList.archive.all") },
            { value: "active", label: t("repoList.archive.active") },
            { value: "archived", label: t("repoList.archive.archived") },
          ]}
        />
      </div>

      <FacetGroup
        title={t("repoList.facets.relation")}
        items={facets.relations}
        selectedValues={selectedRelation === "" ? [] : [selectedRelation]}
        onToggle={(v) => toggleSingle("relation", v)}
      />
      <FacetGroup
        title={t("repoList.facets.tags")}
        items={facets.tags}
        selectedValues={Array.from(selectedTags)}
        onToggle={toggleTag}
      />
      <FacetGroup
        title={t("repoList.facets.license")}
        items={facets.licenses}
        selectedValues={selectedLicense === "" ? [] : [selectedLicense]}
        onToggle={(v) => toggleSingle("license", v)}
      />
      <FacetGroup
        title={t("repoList.facets.task")}
        items={facets.tasks}
        selectedValues={selectedTask === "" ? [] : [selectedTask]}
        onToggle={(v) => toggleSingle("task", v)}
      />
    </>
  );

  // Counts the search term too, so the collapsed panel's badge never reads a
  // plain "Filters" while the clear control inside it is live —
  // `hasActiveFilters` above decides that from the same set.
  const activeCount =
    (searchTerm === "" ? 0 : 1) +
    selectedTags.size +
    (selectedLicense === "" ? 0 : 1) +
    (selectedTask === "" ? 0 : 1) +
    (selectedRelation === "" ? 0 : 1) +
    (baseModel === "" ? 0 : 1) +
    (dataset === "" ? 0 : 1) +
    (baseOnly ? 1 : 0) +
    (archiveMode === "all" ? 0 : 1);

  // Two renderings of the same controls, one visible per breakpoint (the same
  // arrangement components/repo/readme-toc.tsx uses): below `lg` the panel is
  // collapsed, because expanded it pushed the first result card roughly a
  // screen and a half down the page on a phone. Only one is ever displayed, so
  // the duplicate never reaches the accessibility tree.
  return (
    <aside className="w-full lg:w-64 lg:shrink-0">
      <details className="rounded-lg border border-border bg-bg-raised lg:hidden">
        <summary className="cursor-pointer px-3 py-2 text-sm font-medium text-fg-muted">
          {activeCount === 0
            ? t("repoList.filtersToggle")
            : t("repoList.filtersToggleWithCount", { count: activeCount })}
        </summary>
        <div className="flex flex-col gap-5 border-t border-border p-3">{controls}</div>
      </details>
      <div className="hidden flex-col gap-5 lg:flex">{controls}</div>
    </aside>
  );
}

function archiveModeOf(value: string | null): ArchiveMode {
  if (value === null || value === "") return "all";
  return listFlagOn(value) ? "archived" : "active";
}

function GroupTitle({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-2 text-xs font-medium uppercase tracking-wide text-fg-subtle">
      {children}
    </div>
  );
}

/**
 * A lineage filter that arrived from a repository page rather than from this
 * sidebar. It has no facet list to sit in — "ns/name" is free text — so it is
 * shown as a removable chip instead.
 */
function ActiveRef({
  label,
  removeLabel,
  onRemove,
}: {
  label: string;
  removeLabel: string;
  onRemove: () => void;
}) {
  return (
    <div className="flex items-center justify-between gap-2 rounded-lg border border-border px-2.5 py-1.5 text-xs">
      <span className="min-w-0 truncate font-mono text-fg-muted">{label}</span>
      <Button variant="ghost" size="sm" aria-label={removeLabel} onClick={onRemove}>
        <X size={13} />
      </Button>
    </div>
  );
}

function FacetGroup({
  title,
  items,
  selectedValues,
  onToggle,
}: {
  title: string;
  items: RepoFacetItem[];
  /** Values currently filtering the listing, from the URL — not from `items`. */
  selectedValues: string[];
  onToggle: (value: string) => void;
}) {
  const selected = new Set(selectedValues);
  // The facets for one field are computed with the *other* fields' filters
  // still applied, so a tag that is selected can vanish from `items` once a
  // license narrows the set — and vanish with it goes the only checkbox that
  // could turn it off. Selected values are always listed, at count 0 if the
  // response no longer offers them (DESIGN.md §8-4).
  const missing = selectedValues
    .filter((value) => !items.some((item) => item.value === value))
    .map((value) => ({ value, count: 0 }));
  const rows = [...items, ...missing];
  if (rows.length === 0) return null;

  return (
    <div>
      <GroupTitle>{title}</GroupTitle>
      <div className="flex max-h-48 flex-col overflow-y-auto rounded-lg border border-border">
        {rows.map((item) => (
          <label
            key={item.value}
            className="flex cursor-pointer items-center justify-between gap-2 border-b border-border px-2.5 py-1.5 text-xs last:border-0 hover:bg-bg-hover"
          >
            <span className="flex min-w-0 items-center gap-2">
              <Checkbox
                checked={selected.has(item.value)}
                onChange={() => onToggle(item.value)}
                className="shrink-0"
              />
              <span
                className={cn("truncate", item.count === 0 ? "text-fg-subtle" : "text-fg-muted")}
              >
                {item.value}
              </span>
            </span>
            <span className="shrink-0 tabular-nums text-fg-subtle">{item.count}</span>
          </label>
        ))}
      </div>
    </div>
  );
}
