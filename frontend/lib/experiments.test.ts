import { describe, expect, it } from "vitest";
import { expArtifactHref, expRunHref, expRunModelHref, formatMetricValue } from "@/lib/experiments";
import { decodeRouteParams } from "@/lib/paths";
import type { ExpArtifact, ExpRunModelRef, PreviewKind } from "@/types/api";

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
