"use client";

import { ChevronLeft, ChevronRight, ListFilter } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";

import { RunPagePicker } from "@/components/experiments/run-page-picker";
import { Button, buttonClass } from "@/components/ui/button";
import type { MetricGoals } from "@/lib/exp-goals";
import { isTypingTarget, runNeighbours, runPageShortcut } from "@/lib/exp-runpage-order";
import { expRunHref } from "@/lib/experiments";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { ExpRun } from "@/types/api";

/**
 * Moving between runs without going back to the project: previous / next in
 * the project's order, the position in it, and "Jump to run…".
 *
 * `[` / `k` and `]` / `j` step through runs from anywhere on the page, except
 * while the reader is typing (a field, the note editor) or a dialog is open.
 * Previous / next are real links, so the neighbours are prefetched and a
 * middle-click opens them in a tab.
 */
export function RunPageNav({
  ns,
  repo,
  project,
  current,
  orderedRuns,
  primaryMetric,
  goals,
  colorIndex,
}: {
  ns: string;
  repo: string;
  project: string;
  current: string;
  /** The project's runs in the run page's order (lib/exp-runpage-order.ts). */
  orderedRuns: ExpRun[];
  primaryMetric: string | undefined;
  goals: MetricGoals;
  colorIndex: ReadonlyMap<string, number>;
}) {
  const t = useT();
  const router = useRouter();
  const [pickerOpen, setPickerOpen] = useState(false);

  const order = useMemo(() => orderedRuns.map((run) => run.name), [orderedRuns]);
  const { prev, next, index, total } = runNeighbours(order, current);
  const prevHref = prev === undefined ? undefined : expRunHref(ns, repo, project, prev);
  const nextHref = next === undefined ? undefined : expRunHref(ns, repo, project, next);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.defaultPrevented || e.repeat) return;
      if (isTypingTarget(e.target)) return;
      // A modal (the picker, tags, delete) owns the keyboard while it is up.
      if (document.querySelector("dialog[open]")) return;
      const action = runPageShortcut(e);
      const href = action === "prev" ? prevHref : action === "next" ? nextHref : undefined;
      if (!href) return;
      e.preventDefault();
      router.push(href);
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [prevHref, nextHref, router]);

  const orderHint = primaryMetric
    ? t("experiments.runPage.orderHint", { metric: primaryMetric })
    : t("experiments.runPage.orderHintNewest");
  const prevTitle = prev
    ? t("experiments.runPage.prevRunHint", { name: prev })
    : t("experiments.runPage.noPrevRun");
  const nextTitle = next
    ? t("experiments.runPage.nextRunHint", { name: next })
    : t("experiments.runPage.noNextRun");

  return (
    <nav
      aria-label={t("experiments.runPage.jumpAria")}
      className="flex shrink-0 items-center gap-1"
    >
      <StepLink
        href={prevHref}
        label={t("experiments.runPage.prevRun")}
        title={prevTitle}
        icon={<ChevronLeft size={16} />}
      />
      {index >= 0 && (
        <span
          className="min-w-[4.5rem] text-center text-xs font-medium tabular-nums text-fg-subtle"
          title={orderHint}
          aria-label={t("experiments.runPage.positionAria", {
            index: formatNumber(index + 1),
            total: formatNumber(total),
          })}
        >
          {t("experiments.runPage.position", {
            index: formatNumber(index + 1),
            total: formatNumber(total),
          })}
        </span>
      )}
      <StepLink
        href={nextHref}
        label={t("experiments.runPage.nextRun")}
        title={nextTitle}
        icon={<ChevronRight size={16} />}
      />
      <Button
        size="sm"
        variant="secondary"
        className="ml-1 h-7"
        onClick={() => setPickerOpen(true)}
        aria-label={t("experiments.runPage.jumpAria")}
        title={t("experiments.runPage.jumpAria")}
      >
        <ListFilter size={14} />
        <span className="hidden sm:inline">{t("experiments.runPage.jump")}</span>
      </Button>

      <RunPagePicker
        open={pickerOpen}
        onClose={() => setPickerOpen(false)}
        ns={ns}
        repo={repo}
        project={project}
        runs={orderedRuns}
        current={current}
        primaryMetric={primaryMetric}
        goals={goals}
        colorIndex={colorIndex}
      />
    </nav>
  );
}

/**
 * One step button. A link when there is somewhere to go; a disabled button at
 * either end, so the control keeps its place and says why it does nothing.
 */
function StepLink({
  href,
  label,
  title,
  icon,
}: {
  href: string | undefined;
  label: string;
  title: string;
  icon: React.ReactNode;
}) {
  if (!href) {
    return (
      <Button
        size="sm"
        variant="secondary"
        disabled
        aria-label={label}
        title={title}
        className="h-7 w-7 px-0"
      >
        {icon}
      </Button>
    );
  }
  return (
    <Link
      href={href}
      aria-label={label}
      title={title}
      className={buttonClass({ variant: "secondary", size: "sm", className: "h-7 w-7 px-0" })}
    >
      {icon}
    </Link>
  );
}
