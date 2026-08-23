import { describe, expect, it } from "vitest";
import {
  dependentBucket,
  groupDependents,
  groupUpstream,
  lineageRefHref,
  lineageRefLabel,
  toRunModels,
} from "@/lib/lineage";
import type { LineageDependent, LineageRef, RepoSummary } from "@/types/api";

function ref(over: Partial<LineageRef>): LineageRef {
  return {
    kind: "dataset",
    raw: "team/imdb-ja",
    target_kind: "dataset",
    namespace: "team",
    name: "imdb-ja",
    full_name: "team/imdb-ja",
    rev: "",
    project: "",
    run: "",
    relation: "",
    exists: true,
    ...over,
  };
}

describe("lineageRefHref", () => {
  it("links a resolved dataset to its overview page", () => {
    expect(lineageRefHref(ref({}))).toBe("/datasets/team/imdb-ja");
  });

  it("opens the file browser at a pinned revision", () => {
    expect(lineageRefHref(ref({ rev: "v1" }))).toBe("/datasets/team/imdb-ja/tree/v1");
  });

  it("links a base model to the model route", () => {
    const href = lineageRefHref(
      ref({ kind: "base_model", target_kind: "model", name: "bert-base", full_name: "team/bert" }),
    );
    expect(href).toBe("/models/team/bert-base");
  });

  it("links a run edge to that run's own page", () => {
    const href = lineageRefHref(
      ref({
        kind: "run",
        name: "trackio-metrics",
        full_name: "team/trackio-metrics",
        project: "sentiment",
        run: "run-42",
      }),
    );
    expect(href).toBe("/experiments/team/trackio-metrics/sentiment/run-42");
  });

  it("escapes a run name, slashes included", () => {
    const href = lineageRefHref(
      ref({
        kind: "run",
        name: "trackio-metrics",
        full_name: "team/trackio-metrics",
        project: "sentiment",
        run: "sweep/lr-0.1",
      }),
    );
    expect(href).toBe("/experiments/team/trackio-metrics/sentiment/sweep%2Flr-0.1");
  });

  it("falls back to the project page when the card named no run", () => {
    const href = lineageRefHref(
      ref({
        kind: "run",
        name: "trackio-metrics",
        full_name: "team/trackio-metrics",
        project: "sentiment",
        run: "",
      }),
    );
    expect(href).toBe("/experiments/team/trackio-metrics/sentiment");
  });

  it("returns null for a dangling reference so the UI renders plain text", () => {
    expect(lineageRefHref(ref({ exists: false }))).toBeNull();
  });

  it("escapes namespace and name", () => {
    expect(lineageRefHref(ref({ namespace: "a b", name: "c/d", rev: "" }))).toBe(
      "/datasets/a%20b/c%2Fd",
    );
  });
});

describe("lineageRefLabel", () => {
  it("shows the raw string when the reference did not parse", () => {
    expect(lineageRefLabel(ref({ raw: "not a ref", full_name: "", exists: false }))).toBe(
      "not a ref",
    );
  });

  it("appends the pinned revision", () => {
    expect(lineageRefLabel(ref({ rev: "a1b2c3d" }))).toBe("team/imdb-ja@a1b2c3d");
  });

  it("spells a run out in full", () => {
    expect(
      lineageRefLabel(
        ref({ kind: "run", full_name: "team/metrics", project: "proj", run: "run-1" }),
      ),
    ).toBe("team/metrics/proj/run-1");
  });
});

describe("groupUpstream", () => {
  it("splits edges by kind and keeps their order", () => {
    const refs = [
      ref({ raw: "a" }),
      ref({ kind: "base_model", raw: "b" }),
      ref({ raw: "c" }),
      ref({ kind: "run", raw: "d" }),
      ref({ kind: "eval_dataset", raw: "e" }),
    ];
    const grouped = groupUpstream(refs);
    expect(grouped.datasets.map((r) => r.raw)).toEqual(["a", "c"]);
    expect(grouped.baseModels.map((r) => r.raw)).toEqual(["b"]);
    expect(grouped.runs.map((r) => r.raw)).toEqual(["d"]);
    // Evaluated on, not trained from: it must not land in `datasets`.
    expect(grouped.evalDatasets.map((r) => r.raw)).toEqual(["e"]);
  });
});

describe("toRunModels", () => {
  it("keys the response by run name", () => {
    const models = toRunModels({
      items: [
        { run: "run-1", models: [] },
        { run: "run-2", models: [] },
      ],
    });
    expect(Object.keys(models)).toEqual(["run-1", "run-2"]);
    expect(models["run-3"]).toBeUndefined();
  });
});

function dependent(over: Partial<LineageDependent> & { name?: string }): LineageDependent {
  const { name = "derived", ...rest } = over;
  return {
    // Only the fields the grouping reads are meaningful here; RepoSummary is
    // wide and the cast keeps the fixture to the point.
    repo: {
      kind: "model",
      namespace: "team",
      name,
      full_name: `team/${name}`,
    } as RepoSummary,
    kind: "base_model",
    raw: `team/${name}`,
    rev: "",
    project: "",
    run: "",
    relation: "",
    ...rest,
  };
}

describe("dependentBucket", () => {
  it("files a known relation under its own bucket", () => {
    expect(dependentBucket(dependent({ relation: "quantized" }))).toBe("quantized");
  });

  it("reads a base model edge with no relation as a fine-tune", () => {
    expect(dependentBucket(dependent({ relation: "" }))).toBe("finetune");
  });

  it("files an unknown relation under other", () => {
    expect(dependentBucket(dependent({ relation: "distillation" }))).toBe("other");
  });

  it("leaves dataset and run edges ungrouped", () => {
    expect(dependentBucket(dependent({ kind: "dataset" }))).toBe("");
    expect(dependentBucket(dependent({ kind: "run", run: "run-1" }))).toBe("");
  });

  it("gives successor and evaluation edges buckets of their own", () => {
    expect(dependentBucket(dependent({ kind: "new_version" }))).toBe("new_version");
    expect(dependentBucket(dependent({ kind: "eval_dataset" }))).toBe("eval_dataset");
  });
});

describe("groupDependents", () => {
  it("orders the buckets and drops the empty ones", () => {
    const groups = groupDependents([
      dependent({ name: "merged", relation: "merge" }),
      dependent({ name: "gguf", relation: "quantized" }),
      dependent({ name: "lora", relation: "adapter" }),
      dependent({ name: "weird", relation: "distillation" }),
    ]);
    expect(groups.map((g) => g.bucket)).toEqual(["adapter", "quantized", "merge", "other"]);
  });

  it("keeps the server order inside a bucket", () => {
    const groups = groupDependents([
      dependent({ name: "first", relation: "quantized" }),
      dependent({ name: "second", relation: "quantized" }),
    ]);
    expect(groups[0]?.items.map((d) => d.repo.name)).toEqual(["first", "second"]);
  });

  it("puts relation-less dependents in a bucket of their own, last", () => {
    const groups = groupDependents([
      dependent({ name: "consumer", kind: "dataset" }),
      dependent({ name: "tuned", relation: "finetune" }),
    ]);
    expect(groups.map((g) => g.bucket)).toEqual(["finetune", ""]);
  });

  it("leads with the versions this repository supersedes", () => {
    const groups = groupDependents([
      dependent({ name: "trained", relation: "finetune" }),
      dependent({ name: "evaluator", kind: "eval_dataset" }),
      dependent({ name: "v1", kind: "new_version" }),
    ]);
    expect(groups.map((g) => g.bucket)).toEqual(["new_version", "finetune", "eval_dataset"]);
  });

  it("returns nothing for an empty list", () => {
    expect(groupDependents([])).toEqual([]);
  });
});
