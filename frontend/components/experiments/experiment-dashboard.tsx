"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FlaskConical, LineChart, ListChecks, X } from "lucide-react";
import { useSearchParams } from "next/navigation";
import { useEffect, useMemo, useRef, useState } from "react";

import { ConfigDiffTable } from "@/components/experiments/config-diff-table";
import {
  hasLiveRun,
  LIVE_REFRESH_INTERVAL_MS,
  liveRefetchInterval,
} from "@/components/experiments/live-refresh";
import { MetricGoalDialog } from "@/components/experiments/metric-goal-dialog";
import { MetricGoalMarker } from "@/components/experiments/metric-goal-marker";
import { MetricsCharts } from "@/components/experiments/metrics-charts";
import { MetricsChartsSkeleton } from "@/components/experiments/metrics-charts-skeleton";
import { MetricsToolbar } from "@/components/experiments/metrics-toolbar";
import { ParallelCoordinates } from "@/components/experiments/parallel-coordinates";
import { ProjectNotes } from "@/components/experiments/project-notes";
import { csvFilename, metricSeriesCsv } from "@/components/experiments/run-csv";
import { RunDeleteDialog } from "@/components/experiments/run-delete-dialog";
import { RunScatter } from "@/components/experiments/run-scatter";
import { RunSidebar, type RunSidebarProps, TOP_N } from "@/components/experiments/run-sidebar";
import { RunTable } from "@/components/experiments/run-table";
import { RunTagsDialog } from "@/components/experiments/run-tags-dialog";
import { WorkspaceHeader } from "@/components/experiments/workspace-header";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { useChartOptions } from "@/hooks/use-chart-options";
import { dropGoneRunFilters, useRunFilters } from "@/hooks/use-run-filters";
import { useRunSelection } from "@/hooks/use-run-selection";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { runColorIndex } from "@/lib/chart-utils";
import {
  type BestRuns,
  hasAnyGoal,
  type MetricGoals,
  metricGoal,
  projectRunSummaries,
  runListBest,
  runListGoals,
  type SummaryMode,
  toggleSortWithGoals,
} from "@/lib/exp-goals";
import {
  bestOf,
  bestValue,
  countByStatus,
  defaultRunSelection,
  defaultSidebarSort,
  orderedGoalMetrics,
  parseRunsParam,
  parseView,
  primaryGoalMetric,
  rankRunsByMetric,
  runMatchesQuery,
  runMatchesStatus,
  type SidebarSort,
  type StatusFilter,
  type WorkspaceView,
  workspaceSearch,
} from "@/lib/exp-workspace";
import {
  annotationClosesTagEditor,
  deleteDialogTargetAfterRunsChange,
  deleteRun,
  getMetrics,
  getProjectNotes,
  listRuns,
  tagEditorTargetAfterRunRemoved,
  tagEditorTargetAfterRunsChange,
  updateProjectGoals,
  updateRunAnnotations,
} from "@/lib/experiments";
import { metricsQueryKey, metricsQueryKeyXMode } from "@/lib/experiments-query-keys";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { RunModels } from "@/lib/lineage";
import { allTags, filterRuns } from "@/lib/run-compare";
import {
  buildMetricFilter,
  filterByMetric,
  metricColumns,
  metricSortColumn,
  type RunSort,
  type RunSortColumn,
} from "@/lib/run-grouping";
import type { ExpNotesResponse, ExpRun, ExpRunAnnotationRequest, MetricGoal } from "@/types/api";
import { RunStatusFailed, RunStatusRunning, RunStatusStale } from "@/types/api";

const VIEWS: { id: WorkspaceView; labelKey: MessageKey }[] = [
  { id: "charts", labelKey: "experiments.workspace.viewCharts" },
  { id: "table", labelKey: "experiments.workspace.viewTable" },
  { id: "config", labelKey: "experiments.workspace.viewConfig" },
  { id: "scatter", labelKey: "experiments.workspace.viewScatter" },
  { id: "parallel", labelKey: "experiments.workspace.viewParallel" },
  { id: "notes", labelKey: "experiments.workspace.viewNotes" },
];

/**
 * Height of the site header (`h-14` + its 1px border) and of the workspace's
 * own sticky view bar: the sticky offsets below are built from these, so the
 * run sidebar, the view bar and the chart toolbar stack under the site header
 * instead of sliding beneath it.
 */
const STICKY_BAR = "sticky top-[calc(3.5rem+1px)] z-30";
const STICKY_TOOLBAR = "lg:sticky lg:top-[calc(3.5rem+1px+3.5rem)] lg:z-20";

/**
 * The experiment project workspace: a run sidebar that stays on screen (a
 * sticky column at ≥lg, a sheet behind a sticky "Runs" button below that)
 * and, next to it, the project's facts and the view the selection feeds —
 * charts, the full run table, the config diff, scatter, parallel
 * coordinates, notes.
 *
 * The selection and the view live in the URL (`?view=`, repeated `?runs=`,
 * see lib/exp-workspace.ts), written with `history.replaceState` so changing
 * either never re-renders the server page.
 */
export function ExperimentDashboard({
  ns,
  repo,
  project,
  runs: initialRuns,
  metricGoals: initialGoals,
  best: initialBest,
  runModels,
  canWrite,
  initialNotes,
}: {
  ns: string;
  repo: string;
  project: string;
  runs: ExpRun[];
  /** The project's metric goals, from the same run listing. */
  metricGoals: MetricGoals;
  /** Metric → best non-archived run, from the same run listing. */
  best: BestRuns;
  /** Checkpoints each run produced, keyed by run name (see lib/lineage.ts). */
  runModels?: RunModels;
  /** Viewer has write access to the backing dataset repository (goals). */
  canWrite: boolean;
  /** What the server render read of the notebook, or null when that failed. */
  initialNotes: ExpNotesResponse | null;
}) {
  const t = useT();
  const queryClient = useQueryClient();
  const runsKey = ["exp-runs", ns, repo, project];
  // Read from the live URL, not from props the server rendered: going Back
  // to this page restores the router's cached render of it, whose props
  // predate every `replaceState` below — the URL is the only thing that
  // remembers the view and selection the reader left with.
  const searchParams = useSearchParams();

  // The server component already fetched the runs; this query seeds itself from
  // that list and owns it afterwards, so an annotation write refreshes the
  // workspace without a full page navigation.
  const { data: runsData, isError: runsFailed } = useQuery({
    queryKey: runsKey,
    queryFn: async () => {
      const result = await listRuns(ns, repo, project);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    initialData: { runs: initialRuns, metric_goals: { ...initialGoals }, best: { ...initialBest } },
    // Live training curves: while any run in this project is still logging,
    // the list re-reads itself so the sidebar values and the table move on
    // their own. The predicate reads the *response*, so a project whose runs
    // have all finished (or gone stale) settles back to no polling.
    refetchInterval: (query) => liveRefetchInterval(query.state.data?.runs ?? []),
    // A backgrounded tab is nobody watching a chart.
    refetchIntervalInBackground: false,
  });
  const runs = runsData.runs;
  // Memoised on the response: a fresh `{}` per render for a server without
  // goals would re-render every row.
  const goals = useMemo(() => runListGoals(runsData), [runsData]);
  const best = useMemo(() => runListBest(runsData), [runsData]);

  // The notebook, read once here only to mark the Notes tab; ProjectNotes
  // shares the cache entry when the tab opens.
  const notes = useQuery({
    queryKey: ["exp-notes", ns, repo, project],
    queryFn: async () => {
      const result = await getProjectNotes(ns, repo, project);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    initialData: initialNotes ?? undefined,
    staleTime: 60_000,
  });
  const hasNotes = Boolean(notes.data?.exists && notes.data.content.trim() !== "");

  // Which summary the table's metric columns (and the config diff's result
  // rows, the scatter and the parallel plot) read. "best" without a goal
  // reads as "last" rather than guessing for every metric.
  const [summaryMode, setSummaryMode] = useState<SummaryMode>("last");
  const effectiveMode: SummaryMode =
    summaryMode === "best" && !hasAnyGoal(goals) ? "last" : summaryMode;
  const displayRuns = useMemo(
    () => projectRunSummaries(runs, effectiveMode, goals),
    [runs, effectiveMode, goals],
  );

  const activeRuns = useMemo(() => runs.filter((r) => !r.archived), [runs]);
  const goalMetrics = useMemo(() => orderedGoalMetrics(goals, activeRuns), [goals, activeRuns]);
  const primary = goalMetrics[0] ?? primaryGoalMetric(goals, runs);

  const selection = useRunSelection(
    parseRunsParam(searchParams.has("runs") ? searchParams.getAll("runs") : undefined) ??
      defaultRunSelection(initialRuns, initialGoals),
  );
  const { filters, setFilters, reset: resetFilters } = useRunFilters();
  const { options: chartOptions, setOptions: setChartOptions } = useChartOptions();
  const [view, setView] = useState<WorkspaceView>(() =>
    parseView(searchParams.get("view") ?? undefined),
  );
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [sidebarSort, setSidebarSort] = useState<SidebarSort>(() => defaultSidebarSort(primary));
  // null is "follow the sidebar": the table opens sorted the way the run list
  // is, until a header is clicked.
  const [tableSort, setTableSort] = useState<RunSort | null>(null);
  const [tagsFor, setTagsFor] = useState<string | null>(null);
  const [deleteFor, setDeleteFor] = useState<string | null>(null);
  const [goalFor, setGoalFor] = useState<string | null>(null);
  const [sheetOpen, setSheetOpen] = useState(false);

  const tags = useMemo(() => allTags(runs), [runs]);
  const filterKeys = useMemo(
    () => metricColumns(displayRuns, Number.POSITIVE_INFINITY),
    [displayRuns],
  );
  const otherMetrics = useMemo(
    () => filterKeys.filter((key) => !goalMetrics.includes(key)),
    [filterKeys, goalMetrics],
  );
  // A tag or metric the last run just dropped must not keep filtering the
  // list: a Select with a gone value looks blank while every row stays hidden.
  const effectiveFilters = useMemo(
    () => dropGoneRunFilters(filters, tags, filterKeys),
    [filters, tags, filterKeys],
  );
  const metricFilter = useMemo(
    () => buildMetricFilter(effectiveFilters.metric, effectiveFilters.op, effectiveFilters.value),
    [effectiveFilters.metric, effectiveFilters.op, effectiveFilters.value],
  );
  const activeExtraFilters = (effectiveFilters.tag ? 1 : 0) + (metricFilter ? 1 : 0);

  // In scope: everything but archived runs (unless shown). The sidebar's
  // search, status, tag and metric filters narrow what is *listed*; they never
  // take a run off the charts — hunting for a run to add must not drop the
  // ones already being compared.
  const scopeRuns = useMemo(
    () => filterRuns(displayRuns, { showArchived: effectiveFilters.showArchived }),
    [displayRuns, effectiveFilters.showArchived],
  );
  const narrowedRuns = useMemo(
    () =>
      filterByMetric(
        filterRuns(scopeRuns, { showArchived: true, tag: effectiveFilters.tag || undefined }),
        metricFilter,
      ).filter((run) => runMatchesQuery(run, query)),
    [scopeRuns, effectiveFilters.tag, metricFilter, query],
  );
  const statusCounts = useMemo(() => countByStatus(narrowedRuns), [narrowedRuns]);
  const listedRuns = useMemo(
    () => narrowedRuns.filter((run) => runMatchesStatus(run, status)),
    [narrowedRuns, status],
  );
  const listedNames = useMemo(() => listedRuns.map((r) => r.name), [listedRuns]);

  // Colours come from the project's full run order so a run keeps its colour
  // whatever the filters show.
  const runOrder = useMemo(() => runs.map((r) => r.name), [runs]);
  const colorIndex = useMemo(() => runColorIndex(runOrder), [runOrder]);
  // A run that vanished on a live refetch (deleted in another tab) must not
  // leave the tag editor or the delete dialog pointing at it.
  useEffect(() => {
    setTagsFor((current) => tagEditorTargetAfterRunsChange(current, runOrder));
    setDeleteFor((current) => deleteDialogTargetAfterRunsChange(current, runOrder));
  }, [runOrder]);
  const baseline = useMemo(() => runs.find((r) => r.is_baseline)?.name, [runs]);

  const { selected } = selection;
  const selectedRuns = useMemo(
    () => scopeRuns.filter((r) => selected.has(r.name)),
    [scopeRuns, selected],
  );
  const selectedNames = useMemo(() => selectedRuns.map((r) => r.name).sort(), [selectedRuns]);
  const listedSet = useMemo(() => new Set(listedNames), [listedNames]);
  const hiddenSelectedCount = selectedRuns.filter((r) => !listedSet.has(r.name)).length;

  const valueMetric = sidebarSort.kind === "metric" ? sidebarSort.metric : primary;
  const runningNames = useMemo(
    () => listedRuns.filter((r) => r.status === RunStatusRunning).map((r) => r.name),
    [listedRuns],
  );
  const topNames = useMemo(
    () =>
      valueMetric
        ? rankRunsByMetric(listedRuns, valueMetric, goals)
            .slice(0, TOP_N)
            .map((r) => r.name)
        : [],
    [listedRuns, valueMetric, goals],
  );

  // The header's "best" fact follows the metric the list is ranked by when
  // that is a goal, and the primary goal otherwise. The server's pick wins
  // when it has one, so the trophy here and in the list always agree.
  const factMetric = valueMetric && metricGoal(goals, valueMetric) ? valueMetric : primary;
  const factBest = useMemo(() => {
    if (!factMetric) return null;
    const serverBest = best[factMetric];
    const run = serverBest ? runs.find((r) => r.name === serverBest) : undefined;
    const value = run ? bestValue(run, factMetric, goals) : undefined;
    if (run && value !== undefined) return { run: run.name, value };
    return bestOf(activeRuns, factMetric, goals);
  }, [factMetric, best, runs, activeRuns, goals]);

  // Write the view and (once the reader has changed it) the selection into
  // the URL. replaceState, not the router: the page is server-rendered on
  // every navigation, and a checkbox click must not refetch it.
  const initialSelectedRef = useRef(selected);
  useEffect(() => {
    const touched = selected !== initialSelectedRef.current;
    const search = workspaceSearch(
      window.location.search,
      view,
      touched ? Array.from(selected) : null,
      runOrder,
    );
    if (search !== window.location.search) {
      window.history.replaceState(
        null,
        "",
        `${window.location.pathname}${search}${window.location.hash}`,
      );
    }
  }, [view, selected, runOrder]);

  const annotate = useMutation({
    mutationFn: async ({ run, body }: { run: string; body: ExpRunAnnotationRequest }) => {
      const result = await updateRunAnnotations(ns, repo, project, run, body);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    onSuccess: (_data, variables) => {
      // Refetch rather than patching one row: marking a baseline clears the
      // flag on whichever run held it before, which only the server knows.
      void queryClient.invalidateQueries({ queryKey: runsKey });
      // Archive / baseline share this mutation; only a tag save closes the
      // tag editor (an in-progress draft must survive a star elsewhere).
      if (annotationClosesTagEditor(variables.body)) setTagsFor(null);
    },
  });

  const saveGoal = useMutation({
    mutationFn: async ({ metric, goal }: { metric: string; goal: MetricGoal | "" }) => {
      const result = await updateProjectGoals(ns, repo, project, {
        metric_goals: { [metric]: goal },
      });
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    onSuccess: () => {
      // The listing carries both the goals and the best run they pick, so
      // one refetch updates the markers, the trophies and the "Best" mode.
      void queryClient.invalidateQueries({ queryKey: runsKey });
      setGoalFor(null);
    },
  });

  const remove = useMutation({
    mutationFn: async (run: string) => {
      const result = await deleteRun(ns, repo, project, run);
      if (!result.ok) throw new ApiResultError(result);
      return run;
    },
    onSuccess: (run) => {
      void queryClient.invalidateQueries({ queryKey: runsKey });
      // Drop the deleted run from the plotted selection; nothing else on the
      // page knows it is gone until the run list comes back.
      selection.remove(run);
      setDeleteFor(null);
      setTagsFor((current) => tagEditorTargetAfterRunRemoved(current, run));
    },
  });

  // Memoised because RunTable memoises its row context on this object, and the
  // run list re-reads itself every 15 seconds while anything is training.
  const { mutate: annotateMutate, reset: annotateReset } = annotate;
  const { reset: removeReset } = remove;
  const runActions = useMemo(
    () => ({
      onEditTags: (run: ExpRun) => {
        // Drop a failure left over from an archive/baseline click, so the
        // dialog does not open already showing someone else's error.
        annotateReset();
        setTagsFor(run.name);
      },
      onToggleArchived: (run: ExpRun) =>
        annotateMutate({ run: run.name, body: { archived: !run.archived } }),
      onToggleBaseline: (run: ExpRun) =>
        annotateMutate({ run: run.name, body: { is_baseline: !run.is_baseline } }),
      onDelete: (run: ExpRun) => {
        removeReset();
        setDeleteFor(run.name);
      },
      pendingRun: annotate.isPending
        ? annotate.variables?.run
        : remove.isPending
          ? remove.variables
          : undefined,
    }),
    [
      annotateMutate,
      annotateReset,
      removeReset,
      annotate.isPending,
      annotate.variables?.run,
      remove.isPending,
      remove.variables,
    ],
  );

  const metrics = useQuery({
    // Keyed through the shared helper, which serializes the run list as JSON:
    // a run literally named `lr=0.1,bs=32` would otherwise collide with the
    // pair `lr=0.1` + `bs=32` under a comma-join.
    queryKey: metricsQueryKey(ns, repo, project, selectedNames, chartOptions.xMode),
    queryFn: async () => {
      const result = await getMetrics(ns, repo, project, {
        runs: selectedNames,
        x: chartOptions.xMode,
        max_points: 1000,
      });
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    enabled: selectedNames.length > 0,
    // Only the plotted runs decide this: a live run nobody selected is not
    // drawing a line.
    refetchInterval: hasLiveRun(selectedRuns) ? LIVE_REFRESH_INTERVAL_MS : false,
    refetchIntervalInBackground: false,
    // Keep the previous selection's series on screen while the new one loads
    // (a checkbox click must not drop every chart to a skeleton and lose the
    // zoom) — except with nothing selected, where the query never runs and
    // stale data would feed the CSV export, and across a step/time flip, where
    // kept points would land on the wrong kind of axis.
    placeholderData: (previousData, previousQuery) => {
      if (selectedNames.length === 0) return undefined;
      if (!previousQuery) return previousData;
      return metricsQueryKeyXMode(previousQuery.queryKey) === chartOptions.xMode
        ? previousData
        : undefined;
    },
  });

  if (runs.length === 0) {
    return (
      <div className="flex flex-col gap-6">
        <WorkspaceHeader
          ns={ns}
          repo={repo}
          project={project}
          totalRuns={0}
          running={0}
          stale={0}
          failed={0}
          metric={undefined}
          goals={goals}
          best={null}
          canWrite={false}
          onEditGoal={() => {}}
        />
        <EmptyState
          icon={FlaskConical}
          title={t("experiments.dashboard.emptyRunsTitle")}
          description={t("experiments.dashboard.emptyRunsDescription")}
        />
      </div>
    );
  }

  const archivedCount = runs.filter((r) => r.archived).length;
  const annotateError = annotate.isError
    ? queryErrorMessage(t, annotate.error, t("experiments.dashboard.updateFailed"))
    : undefined;
  const editGoal = (metric: string) => {
    saveGoal.reset();
    setGoalFor(metric);
  };
  const clearFilters = () => {
    setQuery("");
    setStatus("all");
    resetFilters();
  };

  const sidebarProps: Omit<RunSidebarProps, "variant"> = {
    ns,
    repo,
    project,
    listedRuns,
    statusCounts,
    query,
    onQueryChange: setQuery,
    status,
    onStatusChange: setStatus,
    sort: sidebarSort,
    onSortChange: setSidebarSort,
    goalMetrics,
    otherMetrics,
    valueMetric,
    showArchived: effectiveFilters.showArchived,
    onShowArchivedChange: (show) => setFilters({ showArchived: show }),
    archivedCount,
    filters: effectiveFilters,
    onFiltersChange: setFilters,
    activeExtraFilters,
    tags,
    metricKeys: filterKeys,
    onClearFilters: clearFilters,
    selected,
    onToggle: selection.toggle,
    onToggleMany: selection.toggleMany,
    onSelectOnly: selection.replace,
    selectedCount: selectedRuns.length,
    hiddenSelectedCount,
    runningNames,
    topNames,
    colorIndex,
    goals,
    best,
    canWrite,
    actions: runActions,
    saving: annotate.isPending,
  };

  const goalMarker = (metric: string) => (
    <MetricGoalMarker
      metric={metric}
      goal={metricGoal(goals, metric)}
      canWrite={canWrite}
      onEdit={() => editGoal(metric)}
    />
  );

  const noSelection = (
    <EmptyState
      icon={LineChart}
      title={t("experiments.workspace.noSelectionTitle")}
      description={t("experiments.workspace.noSelectionDescription")}
    />
  );

  return (
    <div
      // Read by `main:has([data-full-bleed])` in app/layout.tsx: this page
      // alone drops the site-wide max-w-7xl so the workspace can use the
      // screen, capped at 1920px so lines stay comparable on an ultrawide.
      data-full-bleed=""
      className="mx-auto w-full max-w-[1920px] lg:-mb-24 lg:grid lg:grid-cols-[19rem_minmax(0,1fr)] lg:gap-6 xl:grid-cols-[21rem_minmax(0,1fr)]"
    >
      <aside
        aria-label={t("experiments.sidebar.title")}
        className="hidden overflow-hidden rounded-lg border border-border bg-bg-raised lg:sticky lg:top-[calc(3.5rem+1px+1rem)] lg:block lg:h-[calc(100dvh-3.5rem-1px-2rem)] lg:self-start"
      >
        <RunSidebar {...sidebarProps} variant="pane" />
      </aside>

      <div className="flex min-w-0 flex-col gap-4 lg:pb-24">
        <WorkspaceHeader
          ns={ns}
          repo={repo}
          project={project}
          totalRuns={runs.length}
          running={activeRuns.filter((r) => r.status === RunStatusRunning).length}
          stale={activeRuns.filter((r) => r.status === RunStatusStale).length}
          failed={activeRuns.filter((r) => r.status === RunStatusFailed).length}
          metric={factMetric}
          goals={goals}
          best={factBest}
          canWrite={canWrite}
          onEditGoal={editGoal}
        />

        {/* The view switch stays reachable while the workspace scrolls. Below
            lg it also carries the way into the run list, so changing the
            selection never needs a scroll to the bottom of the page. */}
        <div
          className={`${STICKY_BAR} -mx-4 flex h-14 items-center gap-2 border-b border-border bg-bg/95 px-4 backdrop-blur lg:mx-0 lg:px-0`}
        >
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setSheetOpen(true)}
            className="lg:hidden"
            aria-haspopup="dialog"
          >
            <ListChecks size={14} />
            {t("experiments.workspace.runsButton")}
            <span className="tabular-nums text-fg-subtle">
              {t("experiments.workspace.runsButtonCount", { count: selectedRuns.length })}
            </span>
          </Button>
          <div className="scroll-x min-w-0 flex-1">
            <SegmentedControl
              value={view}
              onChange={setView}
              label={t("experiments.workspace.viewsAria")}
              options={VIEWS.map((v) => ({
                value: v.id,
                label: t(v.labelKey),
                indicator:
                  v.id === "notes" && hasNotes ? t("experiments.workspace.notesExist") : undefined,
              }))}
              className="flex-nowrap whitespace-nowrap"
            />
          </div>
        </div>

        {view === "charts" && (
          <div className="flex flex-col gap-3">
            <div className={`${STICKY_TOOLBAR} lg:-mt-1 lg:bg-bg lg:pb-1 lg:pt-1`}>
              <MetricsToolbar
                options={chartOptions}
                onChange={setChartOptions}
                showSyncZoom
                fetching={metrics.isFetching}
                csvFilename={csvFilename([ns, repo, project, "metrics"])}
                csvDisabled={(metrics.data?.series.length ?? 0) === 0}
                buildCsv={() =>
                  metricSeriesCsv(metrics.data?.series ?? [], chartOptions.xMode === "time")
                }
              />
            </div>
            {selectedRuns.length === 0 ? (
              noSelection
            ) : metrics.isError ? (
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
            ) : (
              <MetricsCharts
                series={metrics.data?.series ?? []}
                runOrder={runOrder}
                xIsTime={chartOptions.xMode === "time"}
                smoothing={chartOptions.smoothing}
                logScale={chartOptions.logScale}
                baseline={baseline}
                syncZoom={chartOptions.syncZoom}
                keyOrder={goalMetrics}
                systemLayout="section"
                titleAdornment={goalMarker}
              />
            )}
          </div>
        )}

        {view === "table" &&
          (listedRuns.length === 0 ? (
            <EmptyState
              icon={FlaskConical}
              title={t("experiments.sidebar.noMatchTitle")}
              description={t("experiments.sidebar.noMatchDescription")}
              action={
                <Button size="sm" variant="secondary" onClick={clearFilters}>
                  {t("experiments.sidebar.clearFilters")}
                </Button>
              }
            />
          ) : (
            <RunTable
              ns={ns}
              repo={repo}
              project={project}
              runs={listedRuns}
              sourceRuns={runs}
              runOrder={runOrder}
              selected={selected}
              onToggle={selection.toggle}
              onToggleAll={() => selection.toggleAll(listedNames)}
              onToggleMany={selection.toggleMany}
              runModels={runModels}
              sort={
                tableSort ??
                (sidebarSort.kind === "metric"
                  ? toggleSortWithGoals(null, metricSortColumn(sidebarSort.metric), goals)
                  : sidebarSort.kind === "name"
                    ? { column: "name", dir: "asc" }
                    : null)
              }
              onSort={(column: RunSortColumn) =>
                setTableSort((current) => toggleSortWithGoals(current, column, goals))
              }
              actions={runActions}
              goals={goals}
              best={best}
              canWrite={canWrite}
              onEditGoal={editGoal}
              summaryMode={effectiveMode}
              onSummaryModeChange={setSummaryMode}
            />
          ))}

        {view === "config" &&
          (selectedRuns.length === 0 ? (
            noSelection
          ) : (
            <ConfigDiffTable
              runs={selectedRuns}
              runOrder={runOrder}
              baseline={baseline}
              resultMetrics={goalMetrics}
              goals={goals}
              summaryMode={effectiveMode}
            />
          ))}

        {view === "scatter" && (
          <RunScatter
            runs={selectedRuns}
            runOrder={runOrder}
            baseline={baseline}
            primaryMetric={primary}
          />
        )}

        {view === "parallel" && (
          <ParallelCoordinates
            runs={selectedRuns}
            runOrder={runOrder}
            baseline={baseline}
            primaryMetric={primary}
          />
        )}

        {view === "notes" && (
          <ProjectNotes
            ns={ns}
            repo={repo}
            project={project}
            canWrite={canWrite}
            initial={initialNotes}
          />
        )}
      </div>

      {/* Messages that appear on their own (a failed archive, a failed live
          refresh) float over the corner instead of being inserted into the
          page, where they would move the row the reader was about to click
          (DESIGN.md §8.1). */}
      {((annotateError && !tagsFor) || runsFailed) && (
        <div className="fixed bottom-4 right-4 z-40 flex w-[min(28rem,calc(100vw-2rem))] flex-col gap-2">
          {annotateError && !tagsFor && (
            <Alert tone="negative" title={t("experiments.dashboard.annotateErrorTitle")}>
              <span className="flex items-start gap-2">
                <span className="flex-1">
                  {annotateError}{" "}
                  {t("experiments.dashboard.writeAccessRequired", { repo: `${ns}/${repo}` })}
                </span>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={annotateReset}
                  aria-label={t("ui.close")}
                  className="px-1"
                >
                  <X size={14} />
                </Button>
              </span>
            </Alert>
          )}
          {runsFailed && (
            <Alert tone="warning" title={t("experiments.dashboard.staleTitle")}>
              {t("experiments.dashboard.staleBody")}
            </Alert>
          )}
        </div>
      )}

      <Dialog
        open={sheetOpen}
        onClose={() => setSheetOpen(false)}
        title={t("experiments.workspace.runsDialogTitle")}
        footer={
          <Button variant="primary" onClick={() => setSheetOpen(false)}>
            {t("experiments.workspace.done")}
          </Button>
        }
      >
        {sheetOpen && <RunSidebar {...sidebarProps} variant="sheet" />}
      </Dialog>

      <RunTagsDialog
        run={runs.find((r) => r.name === tagsFor) ?? null}
        open={tagsFor !== null}
        saving={annotate.isPending}
        // The dialog reports the failure itself, and the floating banner is
        // suppressed while it is open: two copies read as two failures.
        error={annotateError}
        // Ignored while the PATCH is in flight: Escape would otherwise read as
        // a cancel for a write that is still on its way. Also drops a failed
        // save's error so it does not reappear as the banner.
        onClose={() => {
          if (annotate.isPending) return;
          annotateReset();
          setTagsFor(null);
        }}
        onSave={(run, tags) => annotate.mutate({ run, body: { tags } })}
      />

      <MetricGoalDialog
        metric={goalFor}
        goal={goalFor === null ? undefined : metricGoal(goals, goalFor)}
        open={goalFor !== null}
        saving={saveGoal.isPending}
        error={
          saveGoal.isError
            ? queryErrorMessage(t, saveGoal.error, t("experiments.goals.saveFailed"))
            : undefined
        }
        onClose={() => {
          if (!saveGoal.isPending) setGoalFor(null);
        }}
        onSave={(metric, goal) => saveGoal.mutate({ metric, goal })}
      />

      <RunDeleteDialog
        run={deleteFor}
        open={deleteFor !== null}
        deleting={remove.isPending}
        error={
          remove.isError
            ? queryErrorMessage(t, remove.error, t("experiments.deleteRun.failed"))
            : undefined
        }
        // Dismissing mid-DELETE reads as a cancel while the request keeps
        // running and still drops the run from the selection on success.
        onClose={() => {
          if (!remove.isPending) setDeleteFor(null);
        }}
        onConfirm={(run) => remove.mutate(run)}
      />
    </div>
  );
}
