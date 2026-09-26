"use client";

import {
  Archive,
  ArchiveRestore,
  GitCompare,
  MoreHorizontal,
  Star,
  Tag,
  Trash2,
} from "lucide-react";
import Link from "next/link";

import { Button, buttonClass } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { SpinnerSlot } from "@/components/ui/spinner";
import { useT } from "@/lib/i18n/client";
import type { ExpRun } from "@/types/api";

/**
 * The run page's toolbar: "Compare" for everyone, and for a writer the two
 * annotations people reach for most (baseline, tags) as buttons with the rest
 * (archive, delete) behind a "more" menu.
 *
 * The spinner slot is always there, after the buttons, so a save in flight
 * never shifts a control (DESIGN.md §8.3).
 */
export function RunPageActions({
  run,
  compareHref,
  baseline,
  canWrite,
  saving,
  onToggleBaseline,
  onEditTags,
  onToggleArchived,
  onDelete,
}: {
  run: ExpRun;
  /** The project page with this run (and the baseline, if any) selected. */
  compareHref: string;
  /** The project's baseline run, when it is another run. */
  baseline: string | undefined;
  canWrite: boolean;
  saving: boolean;
  onToggleBaseline: () => void;
  onEditTags: () => void;
  onToggleArchived: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  const compareTitle = baseline
    ? t("experiments.runPage.compareWithBaseline", { baseline })
    : t("experiments.runPage.compareAlone");

  return (
    <div className="flex shrink-0 items-center gap-1.5">
      <Link
        href={compareHref}
        title={compareTitle}
        aria-label={compareTitle}
        className={buttonClass({ variant: "secondary", size: "sm" })}
      >
        <GitCompare size={14} />
        {t("experiments.runPage.compare")}
      </Link>
      {canWrite && (
        <>
          <Button
            size="sm"
            variant="secondary"
            disabled={saving}
            aria-pressed={run.is_baseline}
            onClick={onToggleBaseline}
            title={
              run.is_baseline
                ? t("experiments.table.clearBaseline")
                : t("experiments.table.setBaseline")
            }
          >
            <Star size={14} className={run.is_baseline ? "fill-current text-accent" : undefined} />
            <span className="hidden md:inline">
              {run.is_baseline
                ? t("experiments.table.clearBaseline")
                : t("experiments.table.setBaseline")}
            </span>
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={saving}
            onClick={onEditTags}
            title={t("experiments.table.editTags")}
          >
            <Tag size={14} />
            <span className="hidden md:inline">{t("experiments.table.editTags")}</span>
          </Button>
          <DropdownMenu
            align="end"
            className="min-w-[12rem]"
            trigger={({ toggle, triggerProps }) => (
              <Button
                size="sm"
                variant="secondary"
                disabled={saving}
                className="h-7 w-7 px-0"
                onClick={toggle}
                aria-label={t("experiments.runPage.actions")}
                title={t("experiments.runPage.actions")}
                {...triggerProps}
              >
                <MoreHorizontal size={14} />
              </Button>
            )}
          >
            {({ close }) => (
              <>
                <DropdownMenuItem
                  onClick={() => {
                    close();
                    onToggleArchived();
                  }}
                >
                  {run.archived ? <ArchiveRestore size={14} /> : <Archive size={14} />}
                  {run.archived ? t("experiments.table.unarchive") : t("experiments.table.archive")}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  className="text-negative hover:text-negative-strong"
                  onClick={() => {
                    close();
                    onDelete();
                  }}
                >
                  <Trash2 size={14} />
                  {t("experiments.runPage.delete")}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenu>
          <SpinnerSlot
            active={saving}
            size={14}
            label={t("experiments.dashboard.savingAnnotation")}
          />
        </>
      )}
    </div>
  );
}
