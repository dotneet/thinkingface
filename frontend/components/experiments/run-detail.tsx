"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LineChart } from "lucide-react";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";

import {
  isLiveRun,
  LIVE_REFRESH_INTERVAL_MS,
  liveRefetchInterval,
} from "@/components/experiments/live-refresh";
import { MetricsCharts } from "@/components/experiments/metrics-charts";
import { MetricsChartsSkeleton } from "@/components/experiments/metrics-charts-skeleton";
import { MetricsToolbar } from "@/components/experiments/metrics-toolbar";
import { RunArtifactsCard } from "@/components/experiments/run-artifacts-card";
import { csvFilename, metricSeriesCsv } from "@/components/experiments/run-csv";
import { RunDeleteDialog } from "@/components/experiments/run-delete-dialog";
import { RunEnvCard } from "@/components/experiments/run-env-card";
import { RunHeader } from "@/components/experiments/run-header";
import { RunModelsCard } from "@/components/experiments/run-models-card";
import { RunNoteCard } from "@/components/experiments/run-note-card";
import { RunPageActions } from "@/components/experiments/run-page-actions";
import { RunPageBreadcrumb } from "@/components/experiments/run-page-breadcrumb";
import { RunPageConfig } from "@/components/experiments/run-page-config";
import { RunPageNav } from "@/components/experiments/run-page-nav";
import { Section } from "@/components/experiments/run-section";
import { RunSummaryCards } from "@/components/experiments/run-summary-cards";
import { RunTagsDialog } from "@/components/experiments/run-tags-dialog";
import { Alert } from "@/components/ui/alert";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { useChartOptions } from "@/hooks/use-chart-options";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { runColorIndex } from "@/lib/chart-utils";
import { type BestRuns, type MetricGoals, runListBest, runListGoals } from "@/lib/exp-goals";
import { projectHref, runPageOrder, runPagePrimaryMetric } from "@/lib/exp-runpage-order";
import {
  annotationClosesTagEditor,
  deleteRun,
  getMetrics,
  listRuns,
  updateRunAnnotations,
} from "@/lib/experiments";
import { metricsQueryKey, metricsQueryKeyXMode } from "@/lib/experiments-query-keys";
import { useT } from "@/lib/i18n/client";
import { splitRunConfig } from "@/lib/run-config";
import type { ExpRun, ExpRunAnnotationRequest } from "@/types/api";

/**
 * Everything about one run, in the order a reader reaches for it:
 *
 * 1. where it is and how to move on — breadcrumb back to the project, and
 *    previous / next / "jump to run" in the project's order (RunPageNav);
 * 2. the header — status and facts on one line, the toolbar (compare, tags,
 *    baseline, archive, delete);
 * 3. the summary cards, goal metrics first;
 * 4. the config beside the note — "what was this run?";
 * 5. the charts;
 * 6. outputs and environment, each one line when empty.
 *
 * This component owns the queries, the two mutations and the two dialogs.
 *
 * The whole project's run list is passed in rather than just this run: the run
 * keeps the colour it has on the dashboard (which is assigned from the
 * project's run order), the switcher needs its neighbours, the config diff
 * needs the baseline, and the annotation mutations can invalidate the same
 * query key the dashboard uses.
 */
export function RunDetail({
  ns,
  repo,
  project,
  runName,
  runs: initialRuns,
  metricGoals: initialGoals,
  best: initialBest,
  canWrite,
}: {
  ns: string;
  repo: string;
  project: string;
  runName: string;
  runs: ExpRun[];
  /** The project's metric goals, from the same run listing. */
  metricGoals: MetricGoals;
  /** Metric → best non-archived run, from the same run listing. */
  best: BestRuns;
  /** Viewer has write access to the backing dataset repository. */
  canWrite: boolean;
}) {
  const t = useT();
  const router = useRouter();
  const queryClient = useQueryClient();
  const runsKey = ["exp-runs", ns, repo, project];

  // Seeded from the server render and owned by the query afterwards, so an
  // annotation write refreshes this page without a navigation — the same
  // arrangement (and the same key) as the dashboard.
  const { data: runsData, isError: runsFailed } = useQuery({
    queryKey: runsKey,
    queryFn: async () => {
      const result = await listRuns(ns, repo, project);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    initialData: { runs: initialRuns, metric_goals: { ...initialGoals }, best: { ...initialBest } },
    // Polls only while *this* run is live. The page shows one run, so a sweep
    // sibling still training next door is no reason to keep re-reading here —
    // and a run that finished, failed or went stale stops the timer outright.
    refetchInterval: (query) =>
      liveRefetchInterval((query.state.data?.runs ?? []).filter((r) => r.name === runName)),
    // Nobody is watching a chart in a backgrounded tab.
    refetchIntervalInBackground: false,
  });
  const runs = runsData.runs;
  const run = runs.find((r) => r.name === runName);
  const goals = useMemo(() => runListGoals(runsData), [runsData]);
  const best = useMemo(() => runListBest(runsData), [runsData]);

  const runOrder = useMemo(() => runs.map((r) => r.name), [runs]);
  const colorIndex = useMemo(() => runColorIndex(runOrder), [runOrder]);
  const baseline = useMemo(() => runs.find((r) => r.is_baseline)?.name, [runs]);
  // The baseline as a run to compare against — only when it is another run.
  const baselineRun = useMemo(
    () => runs.find((r) => r.is_baseline && r.name !== runName),
    [runs, runName],
  );

  // Previous / next and the picker walk the project in the project page's
  // default order (lib/exp-runpage-order.ts documents the rule).
  const orderedRuns = useMemo(() => {
    const byName = new Map(runs.map((r) => [r.name, r]));
    return runPageOrder(runs, goals, runName).flatMap((name) => byName.get(name) ?? []);
  }, [runs, goals, runName]);
  const primaryMetric = useMemo(() => runPagePrimaryMetric(runs, goals), [runs, goals]);

  const { options, setOptions } = useChartOptions();
  const [tagsOpen, setTagsOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  const annotate = useMutation({
    mutationFn: async (body: ExpRunAnnotationRequest) => {
      const result = await updateRunAnnotations(ns, repo, project, runName, body);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    onSuccess: (_data, body) => {
      // Refetch rather than patching the row: setting the baseline clears the
      // flag on whichever run held it before, which only the server knows.
      void queryClient.invalidateQueries({ queryKey: runsKey });
      // Archive / baseline share this mutation and stay clickable while the
      // tag dialog is open (`saving` is only true mid-request). Closing here
      // for those writes would drop the draft.
      if (annotationClosesTagEditor(body)) setTagsOpen(false);
    },
  });

  const remove = useMutation({
    mutationFn: async () => {
      const result = await deleteRun(ns, repo, project, runName);
      if (!result.ok) throw new ApiResultError(result);
    },
    onSuccess: () => {
      // Nothing on this route can render any more, so leave for the project
      // dashboard rather than refreshing a page that would now 404.
      void queryClient.invalidateQueries({ queryKey: runsKey });
      router.push(projectHref(ns, repo, project));
      router.refresh();
    },
  });

  const metrics = useQuery({
    // Same helper as the dashboard, so a single-run selection there and this
    // page share one cache entry instead of drifting apart.
    queryKey: metricsQueryKey(ns, repo, project, [runName], options.xMode),
    queryFn: async () => {
      const result = await getMetrics(ns, repo, project, {
        runs: [runName],
        x: options.xMode,
        max_points: 1000,
      });
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    // The chart follows the same rule as the run list above: a live run redraws
    // itself, everything else is a static page.
    refetchInterval: isLiveRun(run) ? LIVE_REFRESH_INTERVAL_MS : false,
    refetchIntervalInBackground: false,
    // Keep the previous response on screen while the new one loads, rather
    // than unmounting every chart down to MetricsChartsSkeleton on a live
    // refetch — the same reasoning as the dashboard's identical query
    // (experiment-dashboard.tsx), and DESIGN.md §4 (Skeleton is for first
    // paint; the toolbar's `fetching={metrics.isFetching}` spinner below
    // already covers an in-place refresh).
    //
    // Restricted to a placeholder fetched under the *same* x-mode: `xIsTime`
    // below switches immediately with `options.xMode` on a step/time toggle,
    // so a plain `keepPreviousData` would plot the other mode's series —
    // step numbers on a time axis, or vice versa — until the new response
    // for the new mode lands.
    placeholderData: (previousData, previousQuery) => {
      if (!previousQuery) return previousData;
      return metricsQueryKeyXMode(previousQuery.queryKey) === options.xMode
        ? previousData
        : undefined;
    },
  });

  const config = useMemo(() => splitRunConfig(run?.config), [run]);

  if (!run) {
    return (
      <div className="flex flex-col gap-6">
        <RunPageBreadcrumb
          ns={ns}
          repo={repo}
          project={project}
          projectHref={projectHref(ns, repo, project)}
        />
        <ErrorState
          title={t("experiments.run.notFoundTitle")}
          message={t("experiments.run.notFoundDescription", { name: runName })}
        />
      </div>
    );
  }

  // The banner below, the tags dialog and RunNoteCard would otherwise all
  // render the same mutation's failure — but `annotate` is shared across
  // four unrelated writes (tags / archived / baseline / note), and only one
  // of them is ever in flight at a time. Failing to distinguish which one
  // used to mean: rejecting a baseline toggle for lack of write access also
  // painted "failed to save" under the run's note (which the click never
  // touched), and disabled the note's edit button — with a spinner — for as
  // long as that unrelated request was in flight. `annotate.variables` is
  // the body of whichever PATCH is (or was) last in flight, and every call
  // site sends exactly one field, so a `note` key on it identifies a
  // note-triggered mutation; tags/archived/baseline never do.
  const annotateIsNote = annotate.variables !== undefined && "note" in annotate.variables;
  const annotateError = annotate.isError
    ? queryErrorMessage(t, annotate.error, t("experiments.dashboard.updateFailed"))
    : undefined;
  const bannerAnnotateError = annotateError && !annotateIsNote ? annotateError : undefined;
  const noteError = annotateError && annotateIsNote ? annotateError : undefined;
  const noteSaving = annotate.isPending && annotateIsNote;
  const deleteError = remove.isError
    ? queryErrorMessage(t, remove.error, t("experiments.deleteRun.failed"))
    : undefined;

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-3">
          <RunPageBreadcrumb
            ns={ns}
            repo={repo}
            project={project}
            projectHref={projectHref(ns, repo, project, [runName])}
          />
          <RunPageNav
            ns={ns}
            repo={repo}
            project={project}
            current={runName}
            orderedRuns={orderedRuns}
            primaryMetric={primaryMetric}
            goals={goals}
            colorIndex={colorIndex}
          />
        </div>

        <RunHeader
          run={run}
          colorIndex={colorIndex}
          actions={
            <RunPageActions
              run={run}
              compareHref={projectHref(
                ns,
                repo,
                project,
                baselineRun ? [runName, baselineRun.name] : [runName],
              )}
              baseline={baselineRun?.name}
              canWrite={canWrite}
              saving={annotate.isPending}
              onToggleBaseline={() => annotate.mutate({ is_baseline: !run.is_baseline })}
              onEditTags={() => {
                // Drop a failure left over from a baseline/archive click so the
                // dialog does not open already showing someone else's error.
                annotate.reset();
                setTagsOpen(true);
              }}
              onToggleArchived={() => annotate.mutate({ archived: !run.archived })}
              onDelete={() => {
                remove.reset();
                setDeleteOpen(true);
              }}
            />
          }
        />

        {/* Below the header and its toolbar, never above them, so a failed
            write does not move the buttons that caused it (DESIGN.md §8.1).
            Suppressed while a dialog is up: the dialog renders the same
            failure in its own footer, and two copies read as two failures. A
            note-save failure is never shown here at all — RunNoteCard renders
            its own copy right below the note it belongs to. */}
        {bannerAnnotateError && !tagsOpen && (
          <Alert tone="negative" title={t("experiments.dashboard.annotateErrorTitle")}>
            {bannerAnnotateError}{" "}
            {t("experiments.dashboard.writeAccessRequired", { repo: `${ns}/${repo}` })}
          </Alert>
        )}
        {deleteError && !deleteOpen && <Alert tone="negative">{deleteError}</Alert>}
        {runsFailed && (
          <Alert tone="warning" title={t("experiments.dashboard.staleTitle")}>
            {t("experiments.dashboard.staleBody")}
          </Alert>
        )}
      </div>

      <RunSummaryCards run={run} goals={goals} best={best} />

      {/* Config and note side by side on a wide screen: both are "what was
          this run?", and neither is long enough to deserve the full width. */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <RunPageConfig run={run} baseline={baselineRun} />
        <RunNoteCard
          note={run.note}
          canWrite={canWrite}
          saving={noteSaving}
          error={noteError}
          onSave={async (note) => {
            // mutateAsync rather than mutate: the note card needs to know
            // whether the save landed before it leaves edit mode, or a
            // failed save would silently drop the draft (the bug this
            // fixes) — see the contract on RunNoteCard's onSave prop.
            try {
              await annotate.mutateAsync({ note });
              return true;
            } catch {
              return false;
            }
          }}
        />
      </div>

      <Section title={t("experiments.run.metricsTitle")}>
        <MetricsToolbar
          options={options}
          onChange={setOptions}
          fetching={metrics.isFetching}
          csvFilename={csvFilename([ns, repo, project, runName, "metrics"])}
          csvDisabled={(metrics.data?.series.length ?? 0) === 0}
          buildCsv={() => metricSeriesCsv(metrics.data?.series ?? [], options.xMode === "time")}
        />

        {metrics.isError ? (
          <ErrorState
            title={t("experiments.errorTitle")}
            message={queryErrorMessage(
              t,
              metrics.error,
              t("experiments.dashboard.metricsLoadFailed"),
            )}
          />
        ) : metrics.isPending ? (
          <MetricsChartsSkeleton />
        ) : (metrics.data?.series.length ?? 0) === 0 ? (
          // MetricsCharts' own empty state asks the reader to select runs,
          // which makes no sense on a page that is already one run.
          <EmptyState
            icon={LineChart}
            title={t("experiments.run.metricsEmptyTitle")}
            description={t("experiments.run.metricsEmptyDescription")}
          />
        ) : (
          <MetricsCharts
            series={metrics.data?.series ?? []}
            runOrder={runOrder}
            xIsTime={options.xMode === "time"}
            smoothing={options.smoothing}
            logScale={options.logScale}
            baseline={baseline}
          />
        )}
      </Section>

      <Section title={t("experiments.runPage.outputsTitle")}>
        <div className="flex flex-col divide-y divide-border rounded-lg border border-border bg-bg-raised">
          <RunArtifactsCard
            ns={ns}
            repo={repo}
            project={project}
            runName={runName}
            live={isLiveRun(run)}
          />
          <RunModelsCard models={run.models} />
          <RunEnvCard meta={config.meta} />
        </div>
      </Section>

      <RunTagsDialog
        run={run}
        open={tagsOpen}
        saving={annotate.isPending}
        // Same reason the danger zone hides its copy while its dialog is up:
        // the page-level Alert is behind the <dialog> backdrop, so without
        // this a failed save reported nothing the reader could see.
        error={annotateError}
        // Ignored while the PATCH is in flight (the pattern
        // components/repo/delete-file-button.tsx uses): Escape or a backdrop
        // click would otherwise read as a cancel for a write already sent.
        // Also drops a failed save's error: without this, dismissing the
        // dialog after a failed save left the same error to reappear as the
        // page-level banner the moment the dialog closed.
        onClose={() => {
          if (annotate.isPending) return;
          annotate.reset();
          setTagsOpen(false);
        }}
        onSave={(_, tags) => annotate.mutate({ tags })}
      />

      <RunDeleteDialog
        run={deleteOpen ? run.name : null}
        open={deleteOpen}
        deleting={remove.isPending}
        error={deleteError}
        // Dismissing mid-DELETE reads as a cancel, and the request keeps
        // running — then navigates the reader off the page on success.
        onClose={() => {
          if (!remove.isPending) setDeleteOpen(false);
        }}
        onConfirm={() => remove.mutate()}
      />
    </div>
  );
}
