"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FlaskConical } from "lucide-react";
import { useMemo, useState } from "react";
import { ConfigDiffTable } from "@/components/experiments/config-diff-table";
import { CsvDownloadButton } from "@/components/experiments/csv-download-button";
import {
  hasLiveRun,
  LIVE_REFRESH_INTERVAL_MS,
  liveRefetchInterval,
} from "@/components/experiments/live-refresh";
import { MetricsCharts } from "@/components/experiments/metrics-charts";
import { ParallelCoordinates } from "@/components/experiments/parallel-coordinates";
import { csvFilename, metricSeriesCsv } from "@/components/experiments/run-csv";
import { RunDeleteDialog } from "@/components/experiments/run-delete-dialog";
import { RunScatter } from "@/components/experiments/run-scatter";
import { RunTable } from "@/components/experiments/run-table";
import { RunTagsDialog } from "@/components/experiments/run-tags-dialog";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Checkbox, Input, Select, Slider } from "@/components/ui/field";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Skeleton } from "@/components/ui/skeleton";
import { SpinnerSlot } from "@/components/ui/spinner";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { deleteRun, getMetrics, listRuns, updateRunAnnotations } from "@/lib/experiments";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { RunModels } from "@/lib/lineage";
import { allTags, filterRuns } from "@/lib/run-compare";
import {
  buildMetricFilter,
  filterByMetric,
  METRIC_FILTER_OPS,
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

  const [selected, setSelected] = useState<Set<string>>(
    () =>
      new Set(
        initialRuns
          .filter((r) => !r.archived)
          .slice(0, DEFAULT_SELECTION)
          .map((r) => r.name),
      ),
  );
  const [view, setView] = useState<ViewId>("metrics");
  const [showArchived, setShowArchived] = useState(false);
  const [tagFilter, setTagFilter] = useState("");
  // Metric filter: three controls rather than a query language, so the metric
  // names on offer are the ones this project actually logged.
  const [filterMetric, setFilterMetric] = useState("");
  const [filterOp, setFilterOp] = useState<string>("<");
  const [filterValue, setFilterValue] = useState("");
  // null is "the order the server returned" (started_at, then name), which is
  // what the table showed before it was sortable.
  const [sort, setSort] = useState<RunSort | null>(null);
  const [tagsFor, setTagsFor] = useState<string | null>(null);
  const [deleteFor, setDeleteFor] = useState<string | null>(null);
  const [xMode, setXMode] = useState<"step" | "time">("step");
  const [smoothing, setSmoothing] = useState(0);
  const [logScale, setLogScale] = useState(false);
  const [syncZoom, setSyncZoom] = useState(true);

  const tags = useMemo(() => allTags(runs), [runs]);
  const filterKeys = useMemo(() => metricColumns(runs, Number.POSITIVE_INFINITY), [runs]);
  const metricFilter = useMemo(
    () => buildMetricFilter(filterMetric, filterOp, filterValue),
    [filterMetric, filterOp, filterValue],
  );
  const visibleRuns = useMemo(
    () =>
      filterByMetric(filterRuns(runs, { showArchived, tag: tagFilter || undefined }), metricFilter),
    [runs, showArchived, tagFilter, metricFilter],
  );
  // Colours are assigned from the project's full run order so a run keeps the
  // same colour when the filters change what is on screen.
  const runOrder = useMemo(() => runs.map((r) => r.name), [runs]);
  const baseline = useMemo(() => runs.find((r) => r.is_baseline)?.name, [runs]);

  // Only runs that are both selected and visible get plotted: a run hidden by
  // the archive filter should not keep drawing a line.
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
    onSuccess: () => {
      // Refetch rather than patching one row: marking a baseline clears the
      // flag on whichever run held it before, which only the server knows.
      void queryClient.invalidateQueries({ queryKey: runsKey });
      setTagsFor(null);
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
      setSelected((prev) => {
        const next = new Set(prev);
        next.delete(run);
        return next;
      });
      setDeleteFor(null);
    },
  });

  function toggle(name: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }

  function toggleMany(names: string[], select: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const name of names) {
        if (select) next.add(name);
        else next.delete(name);
      }
      return next;
    });
  }

  /** Clears every run filter (archived / tag / metric threshold) at once, for
   * the table's empty state — the fastest way back to "something is showing"
   * when a filter combination matches nothing. */
  function resetFilters() {
    setShowArchived(false);
    setTagFilter("");
    setFilterMetric("");
    setFilterOp("<");
    setFilterValue("");
  }

  function toggleAll() {
    setSelected((prev) => {
      const everyVisible = visibleRuns.every((r) => prev.has(r.name));
      const next = new Set(prev);
      for (const run of visibleRuns) {
        if (everyVisible) next.delete(run.name);
        else next.add(run.name);
      }
      return next;
    });
  }

  const { data, isFetching, isPending, isError, error } = useQuery({
    queryKey: ["exp-metrics", ns, repo, project, selectedNames.join(","), xMode],
    queryFn: async () => {
      const result = await getMetrics(ns, repo, project, {
        runs: selectedNames,
        x: xMode,
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

      <div className="flex flex-wrap items-center gap-5 rounded-lg border border-border bg-bg-sunken px-4 py-3 text-sm">
        <span className="text-fg-subtle">
          {t("experiments.dashboard.selectedCount", {
            selected: selectedRuns.length,
            total: visibleRuns.length,
          })}
        </span>

        {tags.length > 0 && (
          <label className="flex items-center gap-2">
            <span className="text-fg-subtle">{t("experiments.dashboard.tagLabel")}</span>
            <Select
              value={tagFilter}
              onChange={(e) => setTagFilter(e.target.value)}
              className="w-auto bg-bg-raised px-2 py-1"
            >
              <option value="">{t("experiments.dashboard.allTags")}</option>
              {tags.map((tag) => (
                <option key={tag} value={tag}>
                  {tag}
                </option>
              ))}
            </Select>
          </label>
        )}

        {filterKeys.length > 0 && (
          <div className="flex items-center gap-2">
            <span className="text-fg-subtle">{t("experiments.dashboard.metricFilterLabel")}</span>
            <Select
              value={filterMetric}
              onChange={(e) => setFilterMetric(e.target.value)}
              aria-label={t("experiments.dashboard.metricFilterMetricAria")}
              className="w-auto bg-bg-raised px-2 py-1 font-mono text-xs"
            >
              <option value="">{t("experiments.dashboard.metricFilterNone")}</option>
              {filterKeys.map((key) => (
                <option key={key} value={key}>
                  {key}
                </option>
              ))}
            </Select>
            <Select
              value={filterOp}
              onChange={(e) => setFilterOp(e.target.value)}
              aria-label={t("experiments.dashboard.metricFilterOpAria")}
              disabled={!filterMetric}
              className="w-auto bg-bg-raised px-2 py-1"
            >
              {METRIC_FILTER_OPS.map((op) => (
                <option key={op} value={op}>
                  {op}
                </option>
              ))}
            </Select>
            <Input
              value={filterValue}
              onChange={(e) => setFilterValue(e.target.value)}
              inputMode="decimal"
              disabled={!filterMetric}
              placeholder={t("experiments.dashboard.metricFilterValuePlaceholder")}
              aria-label={t("experiments.dashboard.metricFilterValueAria")}
              className="w-24 bg-bg-raised px-2 py-1 tabular-nums"
            />
          </div>
        )}

        <label className="flex items-center gap-2">
          <Checkbox checked={showArchived} onChange={(e) => setShowArchived(e.target.checked)} />
          <span className="text-fg-subtle">
            {t("experiments.dashboard.showArchived", { count: archivedCount })}
          </span>
        </label>

        <SpinnerSlot
          active={annotate.isPending}
          size={14}
          label={t("experiments.dashboard.savingAnnotation")}
        />
      </div>

      <SegmentedControl
        value={view}
        onChange={setView}
        label={t("experiments.dashboard.viewSwitchAria")}
        options={VIEWS.map((v) => ({ value: v.id, label: t(v.labelKey) }))}
      />

      {view === "metrics" && (
        <>
          <div className="flex flex-wrap items-center gap-5 rounded-lg border border-border bg-bg-sunken px-4 py-3 text-sm">
            <label className="flex items-center gap-2">
              <span className="text-fg-subtle">{t("experiments.dashboard.xAxis")}</span>
              <Select
                value={xMode}
                onChange={(e) => setXMode(e.target.value as "step" | "time")}
                className="w-auto bg-bg-raised px-2 py-1"
              >
                <option value="step">{t("experiments.dashboard.xStep")}</option>
                <option value="time">{t("experiments.dashboard.xTime")}</option>
              </Select>
            </label>

            <label className="flex flex-1 items-center gap-2 min-w-[200px]">
              <span className="whitespace-nowrap text-fg-subtle">
                {t("experiments.dashboard.smoothing")}
              </span>
              <Slider
                min={0}
                max={0.95}
                step={0.05}
                value={smoothing}
                onChange={(e) => setSmoothing(Number(e.target.value))}
                aria-label={t("experiments.dashboard.smoothingAria")}
                className="flex-1"
              />
              <span className="w-10 tabular-nums text-fg-subtle">{smoothing.toFixed(2)}</span>
            </label>

            <label className="flex items-center gap-2">
              <Checkbox checked={logScale} onChange={(e) => setLogScale(e.target.checked)} />
              <span className="text-fg-subtle">{t("experiments.dashboard.logScale")}</span>
            </label>

            <label className="flex items-center gap-2">
              <Checkbox checked={syncZoom} onChange={(e) => setSyncZoom(e.target.checked)} />
              <span className="text-fg-subtle">{t("experiments.dashboard.syncZoom")}</span>
            </label>

            <SpinnerSlot
              active={isFetching}
              size={14}
              label={t("experiments.dashboard.loadingMetrics")}
            />

            <CsvDownloadButton
              label={t("experiments.dashboard.exportMetricsCsv")}
              filename={csvFilename([ns, repo, project, "metrics"])}
              disabled={(data?.series.length ?? 0) === 0}
              build={() => metricSeriesCsv(data?.series ?? [], xMode === "time")}
            />
          </div>

          {selectedRuns.length === 0 ? (
            <p className="text-sm text-fg-subtle">{t("experiments.dashboard.selectPrompt")}</p>
          ) : isError ? (
            <ErrorState
              title={t("experiments.errorTitle")}
              message={queryErrorMessage(t, error, t("experiments.dashboard.metricsLoadFailed"))}
            />
          ) : isPending ? (
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
              {Array.from({ length: 2 }, (_, i) => `chart-${i}`).map((key) => (
                <Skeleton key={key} className="h-56 w-full" />
              ))}
            </div>
          ) : (
            <MetricsCharts
              series={data?.series ?? []}
              runOrder={runOrder}
              xIsTime={xMode === "time"}
              smoothing={smoothing}
              logScale={logScale}
              baseline={baseline}
              syncZoom={syncZoom}
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
            onToggle={toggle}
            onToggleAll={toggleAll}
            onToggleMany={toggleMany}
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
