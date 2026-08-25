import { toCsv } from "@/lib/tabular";
import type { ExpMetricSeries, ExpRun } from "@/types/api";

/**
 * CSV for the two things an experiment page holds that people want to take
 * elsewhere: the run comparison table and the metric series behind the charts.
 *
 * Until now the only way out was to open the project's metrics.parquet in the
 * file browser and copy rows out of the SQL console, which loses every
 * annotation and every filter the table was showing.
 *
 * The serialising itself is `toCsv` from lib/tabular.ts — the same RFC 4180
 * writer the SQL console's "copy as CSV" uses, so a value with a comma, a
 * quote or a newline in it escapes identically wherever it is exported from.
 * Everything here is data-in / data-out so the column layout is testable.
 */

/** Column headers of the run table export, in order, before the metrics. */
const RUN_COLUMNS = ["run", "group", "status", "tags", "last_step", "started_at"] as const;

/**
 * The run table as CSV, in the order and with the columns currently on screen.
 *
 * `runs` must already be filtered and sorted the way the table shows them —
 * exporting the unfiltered project would quietly answer a different question
 * from the one on screen. `metricKeys` is the table's own metric column list
 * (`metricColumns` in lib/run-grouping.ts), so a metric the table dropped for
 * width is dropped here too.
 *
 * `group` is included even though the table renders it as nesting rather than
 * as a column: flattening the sweep rows into a CSV would otherwise lose which
 * sweep a run belonged to, which is the one thing the nesting was showing.
 * `updated_at` joins them because it is what makes a "stale" status readable
 * outside the UI, where there is no badge to hover.
 */
export function runTableCsv(
  runs: readonly ExpRun[],
  metricKeys: readonly string[],
  options: { includeModels?: boolean; modelsByRun?: Record<string, string[]> } = {},
): string {
  const columns = [
    ...RUN_COLUMNS,
    "updated_at",
    ...metricKeys,
    ...(options.includeModels ? ["checkpoints"] : []),
  ];
  const rows = runs.map((run) => {
    const row: Record<string, unknown> = {
      run: run.name,
      group: run.group,
      status: run.status,
      // Semicolons, not commas: a comma would be escaped correctly by toCsv
      // but reads as a second column to every human skimming the file.
      tags: run.tags.join("; "),
      last_step: run.last_step,
      started_at: run.started_at ?? "",
      updated_at: run.updated_at,
    };
    for (const key of metricKeys) {
      const value = run.summary?.[key];
      // A metric this run never logged stays blank rather than becoming 0:
      // "not measured" and "measured zero" are different claims.
      row[key] = typeof value === "number" && Number.isFinite(value) ? value : "";
    }
    if (options.includeModels) {
      row.checkpoints = (options.modelsByRun?.[run.name] ?? []).join("; ");
    }
    return row;
  });
  return toCsv(columns, rows);
}

/**
 * The plotted metric series as CSV: one row per point, long-form
 * (`run,metric,x,y`) rather than a column per run.
 *
 * Long-form because the runs of a sweep rarely share an x axis — one may have
 * stopped at step 400 and another at 12000, and one may log every step while
 * the next logs every epoch. A wide layout would have to invent a shared axis
 * and fill the gaps, and every gap it filled would be a number nobody
 * measured. Long-form is also what a spreadsheet pivot and every plotting
 * library want as input.
 *
 * `xIsTime` names the x column after what the chart is actually plotting, so
 * a file exported from the time axis cannot be mistaken for one exported from
 * the step axis.
 */
export function metricSeriesCsv(series: readonly ExpMetricSeries[], xIsTime: boolean): string {
  const xColumn = xIsTime ? "timestamp_ms" : "step";
  const columns = ["run", "metric", xColumn, "value"];
  const rows: Record<string, unknown>[] = [];
  for (const trace of series) {
    for (const [x, y] of trace.points) {
      rows.push({ run: trace.run, metric: trace.key, [xColumn]: x, value: y });
    }
  }
  return toCsv(columns, rows);
}

/**
 * A file name for one of these exports: the identifying parts, each reduced to
 * characters that survive every filesystem and every Content-Disposition
 * header, joined with dashes.
 */
export function csvFilename(parts: readonly string[]): string {
  const slug = parts
    .map((part) => part.replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, ""))
    .filter((part) => part !== "")
    .join("-");
  return `${slug === "" ? "export" : slug}.csv`;
}
