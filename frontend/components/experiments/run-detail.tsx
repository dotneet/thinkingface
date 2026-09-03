"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LineChart } from "lucide-react";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { ConfigEntryTable } from "@/components/experiments/config-entry-table";
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
import { RunDangerZone } from "@/components/experiments/run-danger-zone";
import { RunDeleteDialog } from "@/components/experiments/run-delete-dialog";
import { RunEnvCard } from "@/components/experiments/run-env-card";
import { RunHeader } from "@/components/experiments/run-header";
import { RunModelsCard } from "@/components/experiments/run-models-card";
import { RunNoteCard } from "@/components/experiments/run-note-card";
import { Section } from "@/components/experiments/run-section";
import { RunSummaryCards } from "@/components/experiments/run-summary-cards";
import { RunTagsDialog } from "@/components/experiments/run-tags-dialog";
import { Alert } from "@/components/ui/alert";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { useChartOptions } from "@/hooks/use-chart-options";
import { ApiResultError, queryErrorMessage } from "@/lib/api-error-message";
import { runColorIndex } from "@/lib/chart-utils";
import { deleteRun, getMetrics, listRuns, updateRunAnnotations } from "@/lib/experiments";
import { metricsQueryKey } from "@/lib/experiments-query-keys";
import { useT } from "@/lib/i18n/client";
import { splitRunConfig } from "@/lib/run-config";
import type { ExpRun, ExpRunAnnotationRequest } from "@/types/api";

/**
 * Everything about one run: its annotations, its final metric values, its own
 * charts, the hyperparameters and TrainingArguments it was given, the
 * environment it ran in, the note someone left on it, and the delete button.
 *
 * This component owns the queries, the two mutations and the two dialogs; each
 * section of the page is its own component below `Section`, so the shape of
 * the page reads as the list of sections it is.
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
  const colorIndex = useMemo(() => runColorIndex(runOrder), [runOrder]);
  const baseline = useMemo(() => runs.find((r) => r.is_baseline)?.name, [runs]);

  const { options, setOptions } = useChartOptions();
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
    // Keep the previous x mode's series on screen while the new one loads,
    // rather than unmounting every chart down to MetricsChartsSkeleton on a
    // step/time toggle — the same reasoning as the dashboard's identical
    // query (experiment-dashboard.tsx), and DESIGN.md §4 (Skeleton is for
    // first paint; the toolbar's `fetching={metrics.isFetching}` spinner
    // below already covers an in-place refresh).
    placeholderData: keepPreviousData,
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
    <div className="flex flex-col gap-8">
      {/* Suppressed while the tags dialog is up: it renders the same failure in
          its own footer, and two copies read as two failures (the danger zone
          below does the same with the delete error). A note-save failure is
          never shown here at all — RunNoteCard renders its own copy right
          below the note it belongs to. */}
      {bannerAnnotateError && !tagsOpen && (
        <Alert tone="negative" title={t("experiments.dashboard.annotateErrorTitle")}>
          {bannerAnnotateError}{" "}
          {t("experiments.dashboard.writeAccessRequired", { repo: `${ns}/${repo}` })}
        </Alert>
      )}
      {runsFailed && (
        <Alert tone="warning" title={t("experiments.dashboard.staleTitle")}>
          {t("experiments.dashboard.staleBody")}
        </Alert>
      )}

      <RunHeader
        run={run}
        colorIndex={colorIndex}
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
      />

      <Section title={t("experiments.run.summaryTitle")}>
        <RunSummaryCards entries={summaryEntries} />
      </Section>

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
        <RunDangerZone
          // Hidden while the dialog is up: the dialog renders the same failure
          // in its own footer, and two copies of it read as two failures.
          error={deleteOpen ? undefined : deleteError}
          onRequestDelete={() => {
            remove.reset();
            setDeleteOpen(true);
          }}
        />
      )}

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
