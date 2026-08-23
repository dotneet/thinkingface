import { describe, expect, it } from "vitest";
import { isEnvEmpty, runEnv, splitRunConfig } from "@/lib/run-config";

describe("splitRunConfig", () => {
  it("returns empty sections for a missing config", () => {
    expect(splitRunConfig(undefined)).toEqual({ params: [], args: [], meta: [] });
    expect(splitRunConfig(null)).toEqual({ params: [], args: [], meta: [] });
  });

  it("keeps user hyperparameters unflattened and sorted", () => {
    const { params, args, meta } = splitRunConfig({
      lr: 0.1,
      batch: 32,
      optimizer: { name: "adamw", betas: [0.9, 0.999] },
    });
    expect(params.map((e) => e.key)).toEqual(["batch", "lr", "optimizer"]);
    expect(params[2]?.value).toEqual({ name: "adamw", betas: [0.9, 0.999] });
    expect(args).toEqual([]);
    expect(meta).toEqual([]);
  });

  it("flattens the nested _meta the ingest path sends", () => {
    const { meta, params } = splitRunConfig({
      lr: 0.1,
      _meta: { git: { commit: "abc", dirty: false }, cmdline: ["train.py", "--lr", "0.1"] },
    });
    expect(meta).toEqual([
      { key: "cmdline", value: ["train.py", "--lr", "0.1"] },
      { key: "git.commit", value: "abc" },
      { key: "git.dirty", value: false },
    ]);
    expect(params.map((e) => e.key)).toEqual(["lr"]);
  });

  it("accepts the flattened _meta.* / _args.* columns the parquet path produces", () => {
    const { meta, args, params } = splitRunConfig({
      "_meta.git.commit": "abc",
      "_args.learning_rate": 5e-5,
      seed: 1,
    });
    expect(meta).toEqual([{ key: "git.commit", value: "abc" }]);
    expect(args).toEqual([{ key: "learning_rate", value: 5e-5 }]);
    expect(params).toEqual([{ key: "seed", value: 1 }]);
  });

  it("keeps a scalar sitting directly under a reserved key addressable", () => {
    const { meta } = splitRunConfig({ _meta: '{"python":"3.12"}' });
    expect(meta).toEqual([{ key: "_meta", value: '{"python":"3.12"}' }]);
  });

  it("treats an empty reserved object as nothing to show", () => {
    expect(splitRunConfig({ _meta: {} }).meta).toEqual([]);
  });
});

describe("runEnv", () => {
  it("pulls out the known fields and joins argv", () => {
    const { meta } = splitRunConfig({
      _meta: {
        git: { commit: "deadbeef", branch: "main", dirty: true },
        cmdline: ["train.py", "--token", "***"],
        python: "3.12.1",
        platform: "macOS-15",
        hostname: "gpu-01",
        gpu: { name: "H100", count: 8, cuda: "12.4" },
        requirements_sha256: "0f0f",
        unknown_future_field: 7,
      },
    });
    const env = runEnv(meta);
    expect(env).toMatchObject({
      gitCommit: "deadbeef",
      gitBranch: "main",
      gitDirty: true,
      cmdline: "train.py --token ***",
      python: "3.12.1",
      platform: "macOS-15",
      hostname: "gpu-01",
      gpuName: "H100",
      gpuCount: 8,
      cuda: "12.4",
      requirementsSha256: "0f0f",
    });
    expect(env.extra).toEqual([{ key: "unknown_future_field", value: 7 }]);
    expect(isEnvEmpty(env)).toBe(false);
  });

  it("reports an empty snapshot when there is no _meta", () => {
    expect(isEnvEmpty(runEnv(splitRunConfig({ lr: 1 }).meta))).toBe(true);
  });

  it("drops a gpu count that is not a number", () => {
    const env = runEnv([{ key: "gpu.count", value: "many" }]);
    expect(env.gpuCount).toBeUndefined();
  });
});
