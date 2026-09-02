import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  expArtifactHref,
  expRunHref,
  expRunModelHref,
  formatMetricValue,
  getMetrics,
} from "@/lib/experiments";
import { decodeRouteParams } from "@/lib/paths";
import type { ExpArtifact, ExpRunModelRef, PreviewKind } from "@/types/api";

// The network call itself belongs to lib/api.ts, so apiFetch is a spy here and
// the assertions are about the request this module builds (same split as
// lib/parquet.test.ts).
const { apiFetch } = vi.hoisted(() => ({
  apiFetch: vi.fn(async () => ({ ok: true as const, data: {} })),
}));
vi.mock("@/lib/api", () => ({ apiFetch }));

function artifact(path: string, preview: PreviewKind): ExpArtifact {
  return { name: path.split("/").pop() ?? path, path, size: 1, lfs: false, preview };
}

function model(overrides: Partial<ExpRunModelRef> = {}): ExpRunModelRef {
  return { repo_id: "alice/bert-ja", revision: "", exists: true, ...overrides };
}

describe("expRunHref", () => {
  it("escapes a run name containing a slash", () => {
    const href = expRunHref("alice", "exp", "proj", "sweep/lr-0.1");
    expect(href).toBe("/experiments/alice/exp/proj/sweep%2Flr-0.1");
    expect(decodeRouteParams({ run: "sweep%2Flr-0.1" }).run).toBe("sweep/lr-0.1");
  });
});

describe("expArtifactHref", () => {
  it("sends a parquet artifact to the dedicated viewer", () => {
    expect(
      expArtifactHref(
        "alice",
        "exp",
        "main",
        artifact("proj/artifacts/run-1/eval.parquet", "parquet"),
      ),
    ).toBe("/datasets/alice/exp/viewer/main/proj/artifacts/run-1/eval.parquet");
  });

  it("sends everything else to the blob page the file browser already has", () => {
    expect(
      expArtifactHref("alice", "exp", "main", artifact("proj/artifacts/run-1/cm.png", "image")),
    ).toBe("/datasets/alice/exp/blob/main/proj/artifacts/run-1/cm.png");
  });

  it("escapes each path segment without losing the directory structure", () => {
    const href = expArtifactHref(
      "alice",
      "exp",
      "main",
      artifact("proj/artifacts/run 1/a b.png", "image"),
    );
    expect(href).toBe("/datasets/alice/exp/blob/main/proj/artifacts/run%201/a%20b.png");
  });
});

describe("expRunModelHref", () => {
  it("opens the file browser at the revision the run recorded", () => {
    expect(expRunModelHref(model({ revision: "abc123" }))).toBe(
      "/models/alice/bert-ja/tree/abc123",
    );
  });

  it("falls back to the repository overview when no revision was resolved", () => {
    expect(expRunModelHref(model())).toBe("/models/alice/bert-ja");
  });

  it("refuses to link a model that is not on this server", () => {
    // The declaration is still kept and rendered as text: a typo, an unpushed
    // model and one that was never pushed are indistinguishable from here, and dropping
    // the record would lose provenance the run really did claim.
    expect(expRunModelHref(model({ exists: false, revision: "abc123" }))).toBeNull();
  });

  it("refuses to link a malformed repo id", () => {
    expect(expRunModelHref(model({ repo_id: "bert-ja" }))).toBeNull();
    expect(expRunModelHref(model({ repo_id: "alice/" }))).toBeNull();
  });
});

describe("formatMetricValue", () => {
  it("keeps a plain value at six significant digits", () => {
    expect(formatMetricValue(-86.5)).toBe("-86.5");
    expect(formatMetricValue(0)).toBe("0");
    expect(formatMetricValue(3.14159265)).toBe("3.14159");
  });

  it("falls back to exponential rather than rounding a tiny value to zero", () => {
    expect(formatMetricValue(2.3e-10)).toBe("2.3000e-10");
  });

  it("falls back to exponential rather than printing a wall of digits", () => {
    expect(formatMetricValue(1.85e13)).toBe("1.8500e+13");
  });

  it("adds thousands separators to an in-range integer", () => {
    expect(formatMetricValue(1234567)).toBe("1,234,567");
  });

  it("adds thousands separators to an in-range fraction", () => {
    expect(formatMetricValue(1234.5678)).toBe("1,234.57");
  });

  it("passes non-finite values through unchanged", () => {
    expect(formatMetricValue(Number.NaN)).toBe("NaN");
    expect(formatMetricValue(Number.POSITIVE_INFINITY)).toBe("Infinity");
  });
});

describe("getMetrics", () => {
  type Call = [string, { query?: Record<string, unknown> }];
  const lastCall = () => apiFetch.mock.calls.at(-1) as unknown as Call;

  beforeEach(() => {
    apiFetch.mockClear();
  });

  it("sends runs and keys as repeated parameters, never comma-joined", async () => {
    await getMetrics("alice", "exp", "proj", { runs: ["a", "b"], keys: ["loss"], x: "step" });
    const [path, opts] = lastCall();
    expect(path).toBe("/api/v1/experiments/alice/exp/proj/metrics");
    expect(opts.query?.run).toEqual(["a", "b"]);
    expect(opts.query?.key).toEqual(["loss"]);
    expect(opts.query?.runs).toBeUndefined();
    expect(opts.query?.keys).toBeUndefined();
  });

  // A comma-joined list split `lr=0.1,bs=32` into two names: at best the run
  // matched nothing, at worst a fragment matched a different run and plotted
  // a series nobody selected.
  it("keeps a run name or metric key containing a comma intact", async () => {
    await getMetrics("alice", "exp", "proj", {
      runs: ["lr=0.1,bs=32", " spaced "],
      keys: ["eval/f1,macro"],
    });
    expect(lastCall()[1].query?.run).toEqual(["lr=0.1,bs=32", " spaced "]);
    expect(lastCall()[1].query?.key).toEqual(["eval/f1,macro"]);
  });

  it("omits both parameters when the lists are empty", async () => {
    await getMetrics("alice", "exp", "proj", { runs: [], keys: [] });
    expect(lastCall()[1].query?.run).toBeUndefined();
    expect(lastCall()[1].query?.key).toBeUndefined();
  });
});
