"use client";

import { Check, ChevronDown, GitBranch, Plus, Tag } from "lucide-react";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { CreateBranchDialog } from "@/components/repo/create-branch-dialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/field";
import { useT } from "@/lib/i18n/client";
import { repoBlobHref, repoCommitsHref, repoTreeHref } from "@/lib/paths";
import type { RefsResponseUI, RepoKind } from "@/types/api";

/** Short form of a commit SHA, matching the 7-char OIDs shown elsewhere. */
function shortOid(oid: string): string {
  return oid.slice(0, 7);
}

/** Below this many combined branches + tags, scrolling alone is enough to find a ref. */
const FILTER_THRESHOLD = 8;

export function RefSwitcher({
  kind,
  ns,
  name,
  currentRev,
  path,
  refs,
  target = "tree",
  canCreateBranch = false,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  currentRev: string;
  path: string[];
  refs: RefsResponseUI;
  /** Where selecting a ref navigates to. Defaults to the file tree. */
  target?: "tree" | "commits" | "blob";
  /**
   * Offers "New branch" at the bottom of the menu. The caller decides, because
   * only it knows whether the viewer has write access and whether the
   * repository is archived — the server refuses both, and an action that is
   * always refused should not be on screen.
   */
  canCreateBranch?: boolean;
}) {
  const t = useT();
  const router = useRouter();
  const pathStr = path.join("/");
  const [filter, setFilter] = useState("");
  const [createOpen, setCreateOpen] = useState(false);

  const currentBranch = refs.branches.find((b) => b.name === currentRev);
  const currentTag = refs.tags.find((t) => t.name === currentRev);
  const currentLabel = currentBranch?.name ?? currentTag?.name ?? shortOid(currentRev);

  const showFilter = refs.branches.length + refs.tags.length > FILTER_THRESHOLD;
  const query = filter.trim().toLowerCase();
  const filteredBranches = query
    ? refs.branches.filter((r) => r.name.toLowerCase().includes(query))
    : refs.branches;
  const filteredTags = query
    ? refs.tags.filter((r) => r.name.toLowerCase().includes(query))
    : refs.tags;
  const noMatches = query !== "" && filteredBranches.length === 0 && filteredTags.length === 0;

  function selectRef(ref: string) {
    let href: string;
    switch (target) {
      case "commits":
        href = repoCommitsHref(kind, ns, name, ref, pathStr);
        break;
      case "blob":
        // Keep showing the same file on the selected revision; the blob page
        // 404s if the file doesn't exist there, matching direct navigation.
        href = repoBlobHref(kind, ns, name, ref, pathStr);
        break;
      default:
        href = repoTreeHref(kind, ns, name, ref, pathStr);
    }
    router.push(href);
  }

  return (
    <>
      <DropdownMenu
        trigger={({ open, toggle, triggerProps }) => (
          <Button
            variant="secondary"
            size="sm"
            onClick={() => {
              if (!open) setFilter("");
              toggle();
            }}
            className="gap-1.5"
            {...triggerProps}
          >
            {currentTag && !currentBranch ? <Tag size={14} /> : <GitBranch size={14} />}
            <span className="max-w-[12rem] truncate font-mono" title={currentLabel}>
              {currentLabel}
            </span>
            <ChevronDown size={14} />
          </Button>
        )}
      >
        {({ close }) => (
          <>
            {showFilter && (
              <div className="sticky top-0 z-10 -mx-1 -mt-1 mb-1 bg-bg-raised px-1 pb-1 pt-1">
                <Input
                  autoFocus
                  type="text"
                  value={filter}
                  onChange={(e) => setFilter(e.target.value)}
                  placeholder={t("repo.refSwitcher.filterLabel")}
                  aria-label={t("repo.refSwitcher.filterLabel")}
                  className="h-8 py-1 text-sm"
                />
              </div>
            )}
            {!currentBranch && !currentTag && (
              <>
                <DropdownMenuLabel>{t("repo.refSwitcher.viewingCommit")}</DropdownMenuLabel>
                <div className="px-3 pb-1.5 font-mono text-xs text-fg">{shortOid(currentRev)}</div>
                <DropdownMenuSeparator />
              </>
            )}
            {noMatches ? (
              <p className="px-3 py-1.5 text-xs font-medium text-fg-subtle">
                {t("repo.refSwitcher.noMatches")}
              </p>
            ) : (
              <>
                <DropdownMenuLabel>{t("repo.refSwitcher.branches")}</DropdownMenuLabel>
                {filteredBranches.length === 0 ? (
                  <p className="px-3 py-1.5 text-xs font-medium text-fg-subtle">
                    {t("repo.refSwitcher.noBranches")}
                  </p>
                ) : (
                  filteredBranches.map((ref) => (
                    <DropdownMenuItem
                      key={ref.name}
                      active={ref.name === currentRev}
                      onClick={() => {
                        close();
                        selectRef(ref.name);
                      }}
                    >
                      <GitBranch size={14} className="shrink-0 text-fg-subtle" />
                      <span className="truncate" title={ref.name}>
                        {ref.name}
                      </span>
                      <RefTarget oid={ref.target_oid} />
                      {ref.name === currentRev && <Check size={14} className="shrink-0" />}
                    </DropdownMenuItem>
                  ))
                )}
                {filteredTags.length > 0 && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuLabel>{t("repo.refSwitcher.tags")}</DropdownMenuLabel>
                    {filteredTags.map((ref) => (
                      <DropdownMenuItem
                        key={ref.name}
                        active={ref.name === currentRev}
                        onClick={() => {
                          close();
                          selectRef(ref.name);
                        }}
                      >
                        <Tag size={14} className="shrink-0 text-fg-subtle" />
                        <span className="truncate" title={ref.name}>
                          {ref.name}
                        </span>
                        <RefTarget oid={ref.target_oid} />
                        {ref.name === currentRev && <Check size={14} className="shrink-0" />}
                      </DropdownMenuItem>
                    ))}
                  </>
                )}
              </>
            )}
            {canCreateBranch && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={() => {
                    close();
                    setCreateOpen(true);
                  }}
                >
                  <Plus size={14} className="shrink-0 text-fg-subtle" />
                  <span className="truncate">{t("repo.refs.newBranch")}</span>
                </DropdownMenuItem>
              </>
            )}
          </>
        )}
      </DropdownMenu>
      {canCreateBranch && (
        <CreateBranchDialog
          kind={kind}
          ns={ns}
          name={name}
          startingPoint={currentRev}
          open={createOpen}
          onClose={() => setCreateOpen(false)}
        />
      )}
    </>
  );
}

/**
 * The commit a ref points at, which `RefUI.target_oid` has always carried and
 * this menu never showed — two branches sitting on the same tip were
 * indistinguishable. `ml-auto` pushes it (and the check mark after it) to the
 * right of the name.
 */
function RefTarget({ oid }: { oid: string }) {
  const t = useT();
  if (oid === "") return null;
  return (
    <span
      className="ml-auto shrink-0 font-mono text-xs font-medium text-fg-subtle"
      title={t("repo.refSwitcher.targetTitle", { oid })}
    >
      {shortOid(oid)}
    </span>
  );
}
