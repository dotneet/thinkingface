"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";

import { RunColorDot } from "@/components/experiments/run-color-dot";
import { RunStatusIcon } from "@/components/experiments/run-status-badge";
import { Dialog } from "@/components/ui/dialog";
import { FilterInput } from "@/components/ui/search-input";
import { cn } from "@/lib/cn";
import { type MetricGoals, summaryValue } from "@/lib/exp-goals";
import { filterRunsForPicker } from "@/lib/exp-runpage-order";
import { expRunHref, metricCellText } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { ExpRun } from "@/types/api";

/**
 * "Jump to run…": every run of the project in the run page's order, one
 * compact line each (status glyph, colour, name, group, the primary goal
 * metric's best value), filterable as you type.
 *
 * ↑ / ↓ move the highlight and Enter opens it, so a reader who knows the name
 * never has to touch the mouse. Each row is also a real link, so a middle-
 * click opens it in a new tab.
 */
export function RunPagePicker({
  open,
  onClose,
  ns,
  repo,
  project,
  runs,
  current,
  primaryMetric,
  goals,
  colorIndex,
}: {
  open: boolean;
  onClose: () => void;
  ns: string;
  repo: string;
  project: string;
  /** In the run page's order (lib/exp-runpage-order.ts). */
  runs: ExpRun[];
  current: string;
  /** The metric the order ranks by, shown on each row. */
  primaryMetric: string | undefined;
  goals: MetricGoals;
  colorIndex: ReadonlyMap<string, number>;
}) {
  const t = useT();
  const router = useRouter();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const listRef = useRef<HTMLUListElement>(null);
  const wrapperRef = useRef<HTMLDivElement>(null);

  const matches = useMemo(() => filterRunsForPicker(runs, query), [runs, query]);
  // A live refetch can shorten the list under the highlight; never point past it.
  const activeIndex = Math.min(active, matches.length - 1);

  // Every time the dialog opens it starts from a clean filter with the
  // highlight on the run being viewed, so ↓ / ↑ step from "here". Adjusted
  // during render on the open *transition* only: `runs` is a new array on
  // every live refetch of a running project, and resetting on that would
  // wipe what the reader is typing every few seconds.
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setQuery("");
      setActive(
        Math.max(
          0,
          runs.findIndex((run) => run.name === current),
        ),
      );
    }
  }

  // The dialog exists to type a run name, so the field takes focus. This
  // effect runs after Dialog's own (a child's effects run first), so
  // `showModal()` has already moved focus to the header's close button and
  // this takes it from there.
  useEffect(() => {
    if (open) wrapperRef.current?.querySelector<HTMLInputElement>("input")?.focus();
  }, [open]);

  // Keep the highlighted row in view while arrowing through a long list.
  useEffect(() => {
    if (!open) return;
    const el = listRef.current?.querySelector<HTMLElement>(`[data-index="${activeIndex}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, open]);

  const hrefFor = (name: string) => expRunHref(ns, repo, project, name);

  function go(name: string) {
    onClose();
    if (name !== current) router.push(hrefFor(name));
  }

  const orderHint = primaryMetric
    ? t("experiments.runPage.pickerOrderGoal", { metric: primaryMetric })
    : t("experiments.runPage.pickerOrderNewest");
  const total = runs.length;
  const countText = t(
    total === 1 ? "experiments.runPage.pickerCountOne" : "experiments.runPage.pickerCountOther",
    { shown: formatNumber(matches.length), total: formatNumber(total) },
  );

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("experiments.runPage.pickerTitle")}
      className="max-w-xl"
      footer={
        <span className="mr-auto text-xs font-medium text-fg-subtle">
          {t("experiments.runPage.pickerKeysHint")}
        </span>
      }
    >
      {/* The keydown handler sits on the wrapper because FilterInput does not
          forward key events; arrows and Enter bubble up from the field. */}
      {/* oxlint-disable-next-line jsx-a11y/no-static-element-interactions -- keyboard shortcuts for the field inside it */}
      <div
        ref={wrapperRef}
        className="flex min-h-0 flex-col"
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setActive(Math.min(matches.length - 1, activeIndex + 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive(Math.max(0, activeIndex - 1));
          } else if (e.key === "Enter" && e.target instanceof HTMLInputElement) {
            const run = matches[activeIndex];
            if (run) {
              e.preventDefault();
              go(run.name);
            }
          }
        }}
      >
        <div className="sticky top-0 z-10 flex flex-col gap-1.5 border-b border-border bg-bg-raised px-4 py-3">
          <FilterInput
            value={query}
            onChange={(value) => {
              setQuery(value);
              setActive(0);
            }}
            placeholder={t("experiments.runPage.pickerFilter")}
          />
          <div className="flex items-center justify-between gap-2 text-xs font-medium text-fg-subtle">
            <span className="truncate" title={orderHint}>
              {orderHint}
            </span>
            <span className="shrink-0 tabular-nums">{countText}</span>
          </div>
        </div>

        {matches.length === 0 ? (
          <p className="px-4 py-6 text-center text-sm text-fg-subtle">
            {t("experiments.runPage.pickerNoMatch", { query })}
          </p>
        ) : (
          <ul ref={listRef} className="flex flex-col p-1">
            {matches.map((run, i) => {
              const value =
                primaryMetric === undefined
                  ? undefined
                  : summaryValue(run, primaryMetric, "best", goals);
              const isCurrent = run.name === current;
              return (
                <li key={run.name} data-index={i}>
                  <Link
                    href={hrefFor(run.name)}
                    aria-current={isCurrent ? "page" : undefined}
                    onClick={(e) => {
                      if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
                      e.preventDefault();
                      go(run.name);
                    }}
                    onMouseMove={() => setActive(i)}
                    className={cn(
                      "flex h-8 items-center gap-2 rounded-md px-2 text-sm whitespace-nowrap text-fg-muted",
                      i === activeIndex && "bg-bg-hover text-fg",
                    )}
                  >
                    <RunStatusIcon status={run.status} size={13} />
                    <RunColorDot run={run.name} colorIndex={colorIndex} />
                    <span
                      className={cn("min-w-0 truncate", isCurrent && "font-semibold text-fg")}
                      title={run.name}
                    >
                      {run.name}
                    </span>
                    {isCurrent && (
                      <span className="shrink-0 text-xs font-medium text-fg-subtle">
                        {t("experiments.runPage.pickerCurrent")}
                      </span>
                    )}
                    {run.group && (
                      <span className="hidden shrink-0 truncate text-xs font-medium text-fg-subtle sm:inline">
                        {run.group}
                      </span>
                    )}
                    {primaryMetric !== undefined && (
                      <span
                        className="ml-auto shrink-0 pl-2 tabular-nums text-fg"
                        title={primaryMetric}
                      >
                        {metricCellText(value)}
                      </span>
                    )}
                  </Link>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Dialog>
  );
}
