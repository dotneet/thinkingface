"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FlaskConical } from "lucide-react";
import { useMemo, useState } from "react";
import { ConfigDiffTable } from "@/components/experiments/config-diff-table";
import {
  hasLiveRun,
  LIVE_REFRESH_INTERVAL_MS,
  liveRefetchInterval,
} from "@/components/experiments/live-refresh";
import { MetricsCharts } from "@/components/experiments/metrics-charts";
import { MetricsChartsSkeleton } from "@/components/experiments/metrics-charts-skeleton";
import { MetricsToolbar } from "@/components/experiments/metrics-toolbar";
import { ParallelCoordinates } from "@/components/experiments/parallel-coordinates";
import { csvFilename, metricSeriesCsv } from "@/components/experiments/run-csv";
import { RunDeleteDialog } from "@/components/experiments/run-delete-dialog";
import { RunFilterBar } from "@/components/experiments/run-filter-bar";
import { RunScatter } from "@/components/experiments/run-scatter";
import { RunTable } from "@/components/experiments/run-table";
import { RunTagsDialog } from "@/components/experiments/run-tags-dialog";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { useChartOptions } from "@/hooks/use-chart-options";
import { useRunFilters } from "@/hooks/use-run-filters";
import { useRunSelection } from "@/hooks/use-run-selection";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import {
  annotationClosesTagEditor,
  deleteRun,
  getMetrics,
  listRuns,
  updateRunAnnotations,
} from "@/lib/experiments";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { RunModels } from "@/lib/lineage";
import { allTags, filterRuns } from "@/lib/run-compare";
import {
  buildMetricFilter,
  filterByMetric,
  metricColumns,
  type RunSort,
  type RunSortColumn,
  toggleSort,
} from "@/lib/run-grouping";
import type { ExpRun, ExpRunAnnotationRequest } from "@/types/api";

const VIEWS: { id: "metrics" | "config" | "scatter" | "parallel"; labelKey: MessageKey }[] = [
  { id: "metrics", labelKey: "experiments.dashboard.viewMetrics" },
  { id: "config", labelKey: "experiments.dashboard.viewConfigDiff" },
  { id: "scatter", labelKey: "experiments.dashboard.viewScatter" },
  { id: "parallel", labelKey: "experiments.dashboard.viewParallel" },
];

type ViewId = (typeof VIEWS)[number]["id"];

/** How many runs are pre-selected when the page opens. */
const DEFAULT_SELECTION = 5;

export function ExperimentDashboard({
  ns,
  repo,
  project,
  runs: initialRuns,
  runModels,
}: {
  ns: string;
  repo: string;
  project: string;
  runs: ExpRun[];
  /** Checkpoints each run produced, keyed by run name (see lib/lineage.ts). */
  runModels?: RunModels;
}) {
  const t = useT();
  const queryClient = useQueryClient();
  const runsKey = ["exp-runs", ns, repo, project];

  // The server component already fetched the runs; this query seeds itself from
  // that list and owns it afterwards, so an annotation write refreshes the
  // table without a full page navigation.
  const { data: runsData, isError: runsFailed } = useQuery({
    queryKey: runsKey,
    queryFn: async () => {
      const result = await listRuns(ns, repo, project);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    initialData: { runs: initialRuns },
    // Live training curves: while any run in this project is still logging,
    // the list re-reads itself so the table's last-step and summary columns
    // move on their own. The predicate reads the *response*, so a project
    // whose runs have all finished (or gone stale) settles back to no polling
    // without the page having to be reloaded.
    refetchInterval: (query) => liveRefetchInterval(query.state.data?.runs ?? []),
    // A backgrounded tab is nobody watching a chart. Left on, every experiment
    // page anyone forgot to close would keep polling all day.
    refetchIntervalInBackground: false,
  });
  const runs = runsData.runs;

  const selection = useRunSelection(
    initialRuns
      .filter((r) => !r.archived)
      .slice(0, DEFAULT_SELECTION)
      .map((r) => r.name),
  );
  const { filters, setFilters, reset: resetFilters } = useRunFilters();
  const { options: chartOptions, setOptions: setChartOptions } = useChartOptions();
  const [view, setView] = useState<ViewId>("metrics");
  // null is "the order the server returned" (started_at, then name), which is
  // what the table showed before it was sortable.
  const [sort, setSort] = useState<RunSort | null>(null);
  const [tagsFor, setTagsFor] = useState<string | null>(null);
  const [deleteFor, setDeleteFor] = useState<string | null>(null);

  const tags = useMemo(() => allTags(runs), [runs]);
  const filterKeys = useMemo(() => metricColumns(runs, Number.POSITIVE_INFINITY), [runs]);
  const metricFilter = useMemo(
    () => buildMetricFilter(filters.metric, filters.op, filters.value),
    [filters.metric, filters.op, filters.value],
  );
  const visibleRuns = useMemo(
    () =>
      filterByMetric(
        filterRuns(runs, { showArchived: filters.showArchived, tag: filters.tag || undefined }),
        metricFilter,
      ),
    [runs, filters.showArchived, filters.tag, metricFilter],
  );
  const visibleNames = useMemo(() => visibleRuns.map((r) => r.name), [visibleRuns]);
  // Colours are assigned from the project's full run order so a run keeps the
  // same colour when the filters change what is on screen.
  const runOrder = useMemo(() => runs.map((r) => r.name), [runs]);
  const baseline = useMemo(() => runs.find((r) => r.is_baseline)?.name, [runs]);

  // Only runs that are both selected and visible get plotted: a run hidden by
  // the archive filter should not keep drawing a line.
  const { selected } = selection;
  const selectedRuns = useMemo(
    () => visibleRuns.filter((r) => selected.has(r.name)),
    [visibleRuns, selected],
  );
  const selectedNames = useMemo(() => selectedRuns.map((r) => r.name).sort(), [selectedRuns]);

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
      // Archive / baseline share this mutation. Closing the editor for those
      // would drop an in-progress tag draft — including one open on a
      // different row.
      if (annotationClosesTagEditor(variables.body)) setTagsFor(null);
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
    },
  });

  const { data, isFetching, isPending, isError, error } = useQuery({
    queryKey: ["exp-metrics", ns, repo, project, selectedNames.join(","), chartOptions.xMode],
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
    // drawing a line, so re-reading the series for it would buy nothing.
    refetchInterval: hasLiveRun(selectedRuns) ? LIVE_REFRESH_INTERVAL_MS : false,
    refetchIntervalInBackground: false,
  });

  if (runs.length === 0) {
    return (
      <EmptyState
        icon={FlaskConical}
        title={t("experiments.dashboard.emptyRunsTitle")}
        description={t("experiments.dashboard.emptyRunsDescription")}
      />
    );
  }

  const archivedCount = runs.filter((r) => r.archived).length;

  return (
    <div className="flex flex-col gap-6">
      {annotate.isError && (
        <Alert tone="negative" title={t("experiments.dashboard.annotateErrorTitle")}>
          {queryErrorMessage(t, annotate.error, t("experiments.dashboard.updateFailed"))}{" "}
          {t("experiments.dashboard.writeAccessRequired", { repo: `${ns}/${repo}` })}
        </Alert>
      )}
      {runsFailed && (
        <Alert tone="warning" title={t("experiments.dashboard.staleTitle")}>
          {t("experiments.dashboard.staleBody")}
        </Alert>
      )}

      <RunFilterBar
        filters={filters}
        onChange={setFilters}
        tags={tags}
        metricKeys={filterKeys}
        archivedCount={archivedCount}
        selectedCount={selectedRuns.length}
        visibleCount={visibleRuns.length}
        saving={annotate.isPending}
      />

      <SegmentedControl
        value={view}
        onChange={setView}
        label={t("experiments.dashboard.viewSwitchAria")}
        options={VIEWS.map((v) => ({ value: v.id, label: t(v.labelKey) }))}
      />

      {view === "metrics" && (
        <>
          <MetricsToolbar
            options={chartOptions}
            onChange={setChartOptions}
            showSyncZoom
            fetching={isFetching}
            csvFilename={csvFilename([ns, repo, project, "metrics"])}
            csvDisabled={(data?.series.length ?? 0) === 0}
            buildCsv={() => metricSeriesCsv(data?.series ?? [], chartOptions.xMode === "time")}
          />

          {selectedRuns.length === 0 ? (
            <p className="text-sm text-fg-subtle">{t("experiments.dashboard.selectPrompt")}</p>
          ) : isError ? (
            <ErrorState
              title={t("experiments.errorTitle")}
              message={queryErrorMessage(t, error, t("experiments.dashboard.metricsLoadFailed"))}
            />
          ) : isPending ? (
            <MetricsChartsSkeleton />
          ) : (
            <MetricsCharts
              series={data?.series ?? []}
              runOrder={runOrder}
              xIsTime={chartOptions.xMode === "time"}
              smoothing={chartOptions.smoothing}
              logScale={chartOptions.logScale}
              baseline={baseline}
              syncZoom={chartOptions.syncZoom}
            />
          )}
        </>
      )}

      {view === "config" && (
        <ConfigDiffTable runs={selectedRuns} runOrder={runOrder} baseline={baseline} />
      )}

      {view === "scatter" && (
        <RunScatter runs={selectedRuns} runOrder={runOrder} baseline={baseline} />
      )}

      {view === "parallel" && (
        <ParallelCoordinates runs={selectedRuns} runOrder={runOrder} baseline={baseline} />
      )}

      {/* A fixed floor keeps this region from collapsing to a sliver when the
          filters clear the table down to zero rows, and RunTable caps its own
          height with an internal scrollbar (see DESIGN.md §8) so a long run
          list can never push the region — or this switch and the chart above
          it — further than that floor plus a bit of scroll. */}
      <div className="min-h-[16rem]">
        {visibleRuns.length === 0 ? (
          <EmptyState
            icon={FlaskConical}
            title={t("experiments.dashboard.noMatchTitle")}
            description={t("experiments.dashboard.noMatchDescription")}
            action={
              <Button size="sm" variant="secondary" onClick={resetFilters}>
                {t("experiments.dashboard.noMatchClearFilters")}
              </Button>
            }
          />
        ) : (
          <RunTable
            ns={ns}
            repo={repo}
            project={project}
            runs={visibleRuns}
            runOrder={runOrder}
            selected={selected}
            onToggle={selection.toggle}
            onToggleAll={() => selection.toggleAll(visibleNames)}
            onToggleMany={selection.toggleMany}
            runModels={runModels}
            sort={sort}
            onSort={(column: RunSortColumn) => setSort((current) => toggleSort(current, column))}
            actions={{
              onEditTags: (run) => setTagsFor(run.name),
              onToggleArchived: (run) =>
                annotate.mutate({ run: run.name, body: { archived: !run.archived } }),
              onToggleBaseline: (run) =>
                annotate.mutate({ run: run.name, body: { is_baseline: !run.is_baseline } }),
              onDelete: (run) => {
                remove.reset();
                setDeleteFor(run.name);
              },
              pendingRun: annotate.isPending
                ? annotate.variables?.run
                : remove.isPending
                  ? remove.variables
                  : undefined,
            }}
          />
        )}
      </div>

      <RunTagsDialog
        run={runs.find((r) => r.name === tagsFor) ?? null}
        open={tagsFor !== null}
        saving={annotate.isPending}
        onClose={() => setTagsFor(null)}
        onSave={(run, tags) => annotate.mutate({ run, body: { tags } })}
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
        onClose={() => setDeleteFor(null)}
        onConfirm={(run) => remove.mutate(run)}
      />
    </div>
  );
}
