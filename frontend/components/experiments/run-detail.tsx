"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, ArchiveRestore, LineChart, Star, Tag, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { ConfigEntryTable } from "@/components/experiments/config-entry-table";
import { CsvDownloadButton } from "@/components/experiments/csv-download-button";
import {
  isLiveRun,
  LIVE_REFRESH_INTERVAL_MS,
  liveRefetchInterval,
} from "@/components/experiments/live-refresh";
import { MetricsCharts } from "@/components/experiments/metrics-charts";
import { RunArtifactsCard } from "@/components/experiments/run-artifacts-card";
import { csvFilename, metricSeriesCsv } from "@/components/experiments/run-csv";
import { RunDeleteDialog } from "@/components/experiments/run-delete-dialog";
import { RunEnvCard } from "@/components/experiments/run-env-card";
import { RunModelsCard } from "@/components/experiments/run-models-card";
import { RunNoteCard } from "@/components/experiments/run-note-card";
import { RunStatusBadge } from "@/components/experiments/run-status-badge";
import { RunTagsDialog } from "@/components/experiments/run-tags-dialog";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Checkbox, Select, Slider } from "@/components/ui/field";
import { Skeleton } from "@/components/ui/skeleton";
import { SpinnerSlot } from "@/components/ui/spinner";
import { TimeText } from "@/components/ui/time-text";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { colorForRun } from "@/lib/chart-utils";
import {
  deleteRun,
  formatMetricValue,
  getMetrics,
  listRuns,
  updateRunAnnotations,
} from "@/lib/experiments";
import { metricsQueryKey } from "@/lib/experiments-query-keys";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { splitRunConfig } from "@/lib/run-config";
import type { ExpRun, ExpRunAnnotationRequest } from "@/types/api";

/** Section wrapper: a heading, an optional blurb, and the content below it. */
function Section({
  title,
  description,
  action,
  children,
}: {
  title: string;
  description?: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-col gap-0.5">
          <h2 className="text-sm font-semibold">{title}</h2>
          {description && <p className="text-xs font-medium text-fg-subtle">{description}</p>}
        </div>
        {action}
      </div>
      {children}
    </section>
  );
}

/**
 * Everything about one run: its annotations, its final metric values, its own
 * charts, the hyperparameters and TrainingArguments it was given, the
 * environment it ran in, the note someone left on it, and the delete button.
 *
 * The whole project's run list is passed in rather than just this run: the run
 * keeps the colour it has on the dashboard (which is assigned from the
 * project's run order), and the annotation mutations can invalidate the same
 * query key the dashboard uses.
 */
export function RunDetail({
  ns,
  repo,
  project,
  runName,
  runs: initialRuns,
  canWrite,
}: {
  ns: string;
  repo: string;
  project: string;
  runName: string;
  runs: ExpRun[];
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
    initialData: { runs: initialRuns },
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

  const runOrder = useMemo(() => runs.map((r) => r.name), [runs]);
  const baseline = useMemo(() => runs.find((r) => r.is_baseline)?.name, [runs]);

  const [xMode, setXMode] = useState<"step" | "time">("step");
  const [smoothing, setSmoothing] = useState(0);
  const [logScale, setLogScale] = useState(false);
  const [tagsOpen, setTagsOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  const annotate = useMutation({
    mutationFn: async (body: ExpRunAnnotationRequest) => {
      const result = await updateRunAnnotations(ns, repo, project, runName, body);
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    onSuccess: () => {
      // Refetch rather than patching the row: setting the baseline clears the
      // flag on whichever run held it before, which only the server knows.
      void queryClient.invalidateQueries({ queryKey: runsKey });
      setTagsOpen(false);
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
      router.push(
        `/experiments/${encodeURIComponent(ns)}/${encodeURIComponent(repo)}/${encodeURIComponent(project)}`,
      );
      router.refresh();
    },
  });

  const metrics = useQuery({
    // Same helper as the dashboard, so a single-run selection there and this
    // page share one cache entry instead of drifting apart.
    queryKey: metricsQueryKey(ns, repo, project, [runName], xMode),
    queryFn: async () => {
      const result = await getMetrics(ns, repo, project, {
        runs: [runName],
        x: xMode,
        max_points: 1000,
      });
      if (!result.ok) throw new ApiResultError(result);
      return result.data;
    },
    // The chart follows the same rule as the run list above: a live run redraws
    // itself, everything else is a static page.
    refetchInterval: isLiveRun(run) ? LIVE_REFRESH_INTERVAL_MS : false,
    refetchIntervalInBackground: false,
  });

  const config = useMemo(() => splitRunConfig(run?.config), [run]);
  const summaryEntries = useMemo(() => {
    const summary = run?.summary ?? {};
    return Object.entries(summary).sort(([a], [b]) => a.localeCompare(b));
  }, [run]);

  if (!run) {
    return (
      <ErrorState
        title={t("experiments.run.notFoundTitle")}
        message={t("experiments.run.notFoundDescription", { name: runName })}
      />
    );
  }

  const annotateError = annotate.isError
    ? queryErrorMessage(t, annotate.error, t("experiments.dashboard.updateFailed"))
    : undefined;
  const deleteError = remove.isError
    ? queryErrorMessage(t, remove.error, t("experiments.deleteRun.failed"))
    : undefined;

  return (
    <div className="flex flex-col gap-8">
      {annotateError && (
        <Alert tone="negative" title={t("experiments.dashboard.annotateErrorTitle")}>
          {annotateError}{" "}
          {t("experiments.dashboard.writeAccessRequired", { repo: `${ns}/${repo}` })}
        </Alert>
      )}
      {runsFailed && (
        <Alert tone="warning" title={t("experiments.dashboard.staleTitle")}>
          {t("experiments.dashboard.staleBody")}
        </Alert>
      )}

      <header className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <span
            className="h-3 w-3 shrink-0 rounded-full"
            style={{ background: colorForRun(runOrder.indexOf(run.name)) }}
          />
          <h1 className="text-2xl font-semibold tracking-tight break-all">{run.name}</h1>
          <RunStatusBadge status={run.status} updatedAt={run.updated_at} />
          {run.is_baseline && <Badge tone="accent">{t("experiments.table.baselineBadge")}</Badge>}
          {run.archived && <Badge>{t("experiments.table.archivedBadge")}</Badge>}
        </div>

        <dl className="flex flex-wrap items-center gap-x-6 gap-y-1 text-sm text-fg-subtle">
          <div className="flex items-center gap-1.5">
            <dt>{t("experiments.table.colStarted")}</dt>
            <dd className="text-fg-muted">
              <TimeText iso={run.started_at} style="dateTime" />
            </dd>
          </div>
          <div className="flex items-center gap-1.5">
            <dt>{t("experiments.run.updated")}</dt>
            <dd className="text-fg-muted">
              <TimeText iso={run.updated_at} style="dateTime" />
            </dd>
          </div>
          <div className="flex items-center gap-1.5">
            <dt>{t("experiments.table.colLastStep")}</dt>
            <dd className="tabular-nums text-fg-muted">{formatNumber(run.last_step)}</dd>
          </div>
          <div className="flex items-center gap-1.5">
            <dt>{t("experiments.run.points")}</dt>
            <dd className="tabular-nums text-fg-muted">{formatNumber(run.num_points)}</dd>
          </div>
          {run.group && (
            <div className="flex items-center gap-1.5">
              <dt>{t("experiments.run.group")}</dt>
              <dd className="text-fg-muted">{run.group}</dd>
            </div>
          )}
          {run.job_type && (
            <div className="flex items-center gap-1.5">
              <dt>{t("experiments.run.jobType")}</dt>
              <dd className="text-fg-muted">{run.job_type}</dd>
            </div>
          )}
        </dl>

        <div className="flex flex-wrap items-center gap-2">
          {run.tags.length === 0 ? (
            <span className="text-sm text-fg-subtle">{t("experiments.run.noTags")}</span>
          ) : (
            run.tags.map((tag) => <Badge key={tag}>{tag}</Badge>)
          )}
          {canWrite && (
            <div className="flex flex-wrap items-center gap-1.5">
              <Button
                size="sm"
                variant="secondary"
                disabled={annotate.isPending}
                aria-pressed={run.is_baseline}
                onClick={() => annotate.mutate({ is_baseline: !run.is_baseline })}
              >
                <Star size={14} className={run.is_baseline ? "text-accent" : undefined} />
                {run.is_baseline
                  ? t("experiments.table.clearBaseline")
                  : t("experiments.table.setBaseline")}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={annotate.isPending}
                onClick={() => setTagsOpen(true)}
              >
                <Tag size={14} />
                {t("experiments.table.editTags")}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={annotate.isPending}
                aria-pressed={run.archived}
                onClick={() => annotate.mutate({ archived: !run.archived })}
              >
                {run.archived ? <ArchiveRestore size={14} /> : <Archive size={14} />}
                {run.archived ? t("experiments.table.unarchive") : t("experiments.table.archive")}
              </Button>
              <SpinnerSlot
                active={annotate.isPending}
                size={14}
                label={t("experiments.dashboard.savingAnnotation")}
              />
            </div>
          )}
        </div>
      </header>

      <Section title={t("experiments.run.summaryTitle")}>
        {summaryEntries.length === 0 ? (
          <p className="text-sm text-fg-subtle">{t("experiments.run.summaryEmpty")}</p>
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            {summaryEntries.map(([key, value]) => (
              <Card key={key} className="flex flex-col gap-1">
                <span className="truncate text-xs font-medium text-fg-subtle" title={key}>
                  {key}
                </span>
                <span className="tabular-nums text-lg font-semibold">
                  {formatMetricValue(value)}
                </span>
              </Card>
            ))}
          </div>
        )}
      </Section>

      <Section title={t("experiments.run.metricsTitle")}>
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

          <label className="flex min-w-[200px] flex-1 items-center gap-2">
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

          <SpinnerSlot
            active={metrics.isFetching}
            size={14}
            label={t("experiments.dashboard.loadingMetrics")}
          />

          <CsvDownloadButton
            label={t("experiments.dashboard.exportMetricsCsv")}
            filename={csvFilename([ns, repo, project, runName, "metrics"])}
            disabled={(metrics.data?.series.length ?? 0) === 0}
            build={() => metricSeriesCsv(metrics.data?.series ?? [], xMode === "time")}
          />
        </div>

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
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {Array.from({ length: 2 }, (_, i) => `chart-${i}`).map((key) => (
              <Skeleton key={key} className="h-56 w-full" />
            ))}
          </div>
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
            xIsTime={xMode === "time"}
            smoothing={smoothing}
            logScale={logScale}
            baseline={baseline}
          />
        )}
      </Section>

      <Section
        title={t("experiments.artifacts.title")}
        description={t("experiments.artifacts.description")}
      >
        <RunArtifactsCard
          ns={ns}
          repo={repo}
          project={project}
          runName={runName}
          live={isLiveRun(run)}
        />
      </Section>

      <Section
        title={t("experiments.models.title")}
        description={t("experiments.models.description")}
      >
        <RunModelsCard models={run.models} />
      </Section>

      <Section title={t("experiments.note.title")} description={t("experiments.note.description")}>
        <RunNoteCard
          note={run.note}
          canWrite={canWrite}
          saving={annotate.isPending}
          error={annotateError}
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
      </Section>

      <Section title={t("experiments.run.paramsTitle")}>
        <ConfigEntryTable
          entries={config.params}
          emptyTitle={t("experiments.run.paramsEmptyTitle")}
          emptyDescription={t("experiments.run.paramsEmptyDescription")}
        />
      </Section>

      {config.args.length > 0 && (
        <Section
          title={t("experiments.run.argsTitle")}
          description={t("experiments.run.argsDescription")}
        >
          <ConfigEntryTable entries={config.args} emptyTitle={t("experiments.run.argsTitle")} />
        </Section>
      )}

      <Section title={t("experiments.env.title")} description={t("experiments.env.description")}>
        <RunEnvCard meta={config.meta} />
      </Section>

      {canWrite && (
        <Section
          title={t("experiments.deleteRun.dangerTitle")}
          description={t("experiments.deleteRun.description")}
        >
          {deleteError && !deleteOpen && <Alert tone="negative">{deleteError}</Alert>}
          <div>
            <Button
              variant="danger"
              onClick={() => {
                remove.reset();
                setDeleteOpen(true);
              }}
            >
              <Trash2 size={16} />
              {t("experiments.deleteRun.button")}
            </Button>
          </div>
        </Section>
      )}

      <RunTagsDialog
        run={run}
        open={tagsOpen}
        saving={annotate.isPending}
        onClose={() => setTagsOpen(false)}
        onSave={(_, tags) => annotate.mutate({ tags })}
      />

      <RunDeleteDialog
        run={deleteOpen ? run.name : null}
        open={deleteOpen}
        deleting={remove.isPending}
        error={deleteError}
        onClose={() => setDeleteOpen(false)}
        onConfirm={() => remove.mutate()}
      />
    </div>
  );
}
