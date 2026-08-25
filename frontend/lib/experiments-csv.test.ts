import { describe, expect, it } from "vitest";
import { csvFilename, metricSeriesCsv, runTableCsv } from "@/components/experiments/run-csv";
import type { ExpMetricSeries, ExpRun, RunStatus } from "@/types/api";

// As with experiments-live.test.ts: the module under test sits in
// components/experiments/, the suite here because vitest only collects
// lib/**/*.test.ts.

function run(name: string, over: Partial<ExpRun> = {}): ExpRun {
  return {
    name,
    status: "finished" as RunStatus,
    last_step: 100,
    num_points: 100,
    started_at: "2026-08-25T10:00:00Z",
    updated_at: "2026-08-25T11:00:00Z",
    config: {},
    metric_keys: [],
    summary: {},
    group: "",
    job_type: "",
    tags: [],
    archived: false,
    is_baseline: false,
    note: "",
    models: [],
    ...over,
  };
}

function rows(csv: string): string[][] {
  return csv.split("\n").map((line) => line.split(","));
}

describe("runTableCsv", () => {
  it("writes a header plus one row per run, in the order given", () => {
    const csv = runTableCsv([run("b"), run("a")], []);
    const [header, first, second] = rows(csv);
    expect(header).toEqual([
      "run",
      "group",
      "status",
      "tags",
      "last_step",
      "started_at",
      "updated_at",
    ]);
    // The caller passes the runs already filtered and sorted the way the table
    // shows them, so the export must not reorder them.
    expect(first?.[0]).toBe("b");
    expect(second?.[0]).toBe("a");
  });

  it("adds a column per metric the table is showing, and no others", () => {
    const runs = [
      run("a", { summary: { loss: 0.5, accuracy: 0.9, "system/gpu": 42 } }),
      run("b", { summary: { loss: 0.25, accuracy: 0.95, "system/gpu": 43 } }),
    ];
    // Only the keys the table decided to show are passed in, so the hidden
    // ones (a sixth metric, anything under system/) stay out.
    const csv = runTableCsv(runs, ["loss"]);
    const [header, first] = rows(csv);
    expect(header).toContain("loss");
    expect(header).not.toContain("accuracy");
    expect(header).not.toContain("system/gpu");
    expect(first?.at(-1)).toBe("0.5");
  });

  it("leaves an unlogged metric blank rather than zero", () => {
    const csv = runTableCsv([run("a", { summary: {} })], ["loss"]);
    expect(rows(csv)[1]?.at(-1)).toBe("");
  });

  it("joins tags with a semicolon so they read as one column", () => {
    const csv = runTableCsv([run("a", { tags: ["lr-sweep", "seed-1"] })], []);
    expect(csv).toContain("lr-sweep; seed-1");
    // No quoting was needed, i.e. no comma was introduced.
    expect(csv).not.toContain('"');
  });

  it("escapes a value that carries a comma, a quote or a newline", () => {
    const csv = runTableCsv([run('we"ird, name\nrun')], []);
    expect(csv.split("\n")[1]).toContain('"we""ird, name');
  });

  it("carries the group so a flattened sweep is still readable", () => {
    const csv = runTableCsv([run("lr-0.1", { group: "lr-sweep" })], []);
    expect(rows(csv)[1]?.[1]).toBe("lr-sweep");
  });

  it("reports a stale status as the API derived it", () => {
    const csv = runTableCsv([run("a", { status: "stale" as RunStatus })], []);
    const row = rows(csv)[1];
    expect(row?.[2]).toBe("stale");
    // updated_at rides along: outside the UI there is no badge to hover, so
    // the timestamp is the only thing that makes "stale" mean anything.
    expect(row?.at(-1)).toBe("2026-08-25T11:00:00Z");
  });

  it("writes a blank started_at for a run that never recorded one", () => {
    const csv = runTableCsv([run("a", { started_at: null })], []);
    expect(rows(csv)[1]?.[5]).toBe("");
  });

  it("appends the checkpoint column only when the table shows it", () => {
    const withModels = runTableCsv([run("a")], [], {
      includeModels: true,
      modelsByRun: { a: ["alice/bert-ja", "alice/bert-en"] },
    });
    expect(rows(withModels)[0]?.at(-1)).toBe("checkpoints");
    expect(withModels).toContain("alice/bert-ja; alice/bert-en");

    expect(rows(runTableCsv([run("a")], []))[0]).not.toContain("checkpoints");
  });

  it("writes a header even with no runs", () => {
    // An empty export is an empty table, not a broken file.
    expect(rows(runTableCsv([], ["loss"]))).toHaveLength(1);
  });
});

describe("metricSeriesCsv", () => {
  const series: ExpMetricSeries[] = [
    { run: "a", key: "loss", points: [[1, 0.5]] },
    {
      run: "b",
      key: "loss",
      points: [
        [1, 0.4],
        [2, 0.3],
      ],
    },
  ];

  it("writes one long-form row per point", () => {
    const out = rows(metricSeriesCsv(series, false));
    expect(out[0]).toEqual(["run", "metric", "step", "value"]);
    expect(out).toHaveLength(4);
    expect(out[1]).toEqual(["a", "loss", "1", "0.5"]);
    expect(out[3]).toEqual(["b", "loss", "2", "0.3"]);
  });

  it("names the x column after the axis the chart was plotting", () => {
    expect(rows(metricSeriesCsv(series, true))[0]?.[2]).toBe("timestamp_ms");
  });

  it("keeps runs with different axis lengths side by side without inventing points", () => {
    // The long form is the reason: a wide layout would have to fill run "a"'s
    // missing step 2 with something nobody measured.
    const out = rows(metricSeriesCsv(series, false));
    expect(out.filter((r) => r[0] === "a")).toHaveLength(1);
    expect(out.filter((r) => r[0] === "b")).toHaveLength(2);
  });

  it("writes a header even with nothing plotted", () => {
    expect(rows(metricSeriesCsv([], false))).toHaveLength(1);
  });
});

describe("csvFilename", () => {
  it("joins the parts with dashes and ends in .csv", () => {
    expect(csvFilename(["alice", "exp", "proj", "runs"])).toBe("alice-exp-proj-runs.csv");
  });

  it("reduces anything a filesystem or a header would choke on", () => {
    expect(csvFilename(["alice", "sweep/lr 0.1", "metrics"])).toBe(
      "alice-sweep-lr-0.1-metrics.csv",
    );
  });

  it("never produces a nameless file", () => {
    expect(csvFilename(["///", ""])).toBe("export.csv");
  });
});
