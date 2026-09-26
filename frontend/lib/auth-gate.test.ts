import { describe, expect, it, vi } from "vitest";

import {
  createServerInfoGate,
  isGateExempt,
  isNavigationMethod,
  loginRedirectPath,
  serverInfoUrl,
} from "./auth-gate";
import { safeRedirectPath } from "./validation";

describe("isGateExempt", () => {
  it("leaves the sign-in page, the API proxy, Next internals and assets alone", () => {
    for (const path of [
      "/login",
      "/login/",
      "/api",
      "/api/v1/me",
      "/api/v1/repos",
      "/_next/static/chunks/main.js",
      "/_next/image",
      "/_next/data/x.json",
      "/favicon.ico",
      "/icon.svg",
      "/robots.txt",
      "/duckdb/duckdb-eh.wasm",
      "/models/alice/bert/resolve/main/model.safetensors",
      "/datasets/alice/squad/resolve/main/data/train.parquet",
      "/models/alice/bert/resolve",
    ]) {
      expect(isGateExempt(path), path).toBe(true);
    }
  });

  it("gates every page, including file pages whose path has a dot", () => {
    for (const path of [
      "/",
      "/models",
      "/datasets",
      "/experiments",
      "/alice",
      "/alice/bert",
      "/alice/bert/blob/main/config.json",
      "/datasets/alice/squad/viewer",
      "/settings/tokens",
      "/loginx",
      "/apix",
      "/new",
      "/models/alice/bert/blob/main/resolve",
      "/models/alice/resolve",
      "/alice/bert/resolve/main/x",
    ]) {
      expect(isGateExempt(path), path).toBe(false);
    }
  });
});

describe("isNavigationMethod", () => {
  it("redirects page loads only", () => {
    expect(isNavigationMethod("GET")).toBe(true);
    expect(isNavigationMethod("HEAD")).toBe(true);
    expect(isNavigationMethod("POST")).toBe(false);
    expect(isNavigationMethod("OPTIONS")).toBe(false);
  });
});

describe("loginRedirectPath", () => {
  it("carries the requested page, query included, as next", () => {
    expect(loginRedirectPath("/datasets", "?search=bert&tags=nlp")).toBe(
      "/login?next=%2Fdatasets%3Fsearch%3Dbert%26tags%3Dnlp",
    );
    expect(loginRedirectPath("/", "")).toBe("/login?next=%2F");
  });

  it("round-trips through the login form's safeRedirectPath", () => {
    const cases: [string, string][] = [
      ["/alice/bert/blob/main/config.json", ""],
      ["/datasets", "?search=bert"],
      ["/experiments/alice/runs", "?project=p&run=r"],
    ];
    for (const [pathname, search] of cases) {
      const next = new URL(loginRedirectPath(pathname, search), "http://x").searchParams.get(
        "next",
      );
      expect(safeRedirectPath(next)).toBe(`${pathname}${search}`);
    }
  });

  it("never lets next leave the origin", () => {
    for (const hostile of [
      "//evil.example/x",
      "/\\evil.example",
      "https://evil.example/",
      "javascript:alert(1)",
    ]) {
      expect(safeRedirectPath(hostile), hostile).toBe("/");
    }
  });
});

describe("serverInfoUrl", () => {
  it("appends the path to the API base, with or without a trailing slash", () => {
    expect(serverInfoUrl("http://api:8080/")).toBe("http://api:8080/api/v1/server-info");
    expect(serverInfoUrl("https://api.example")).toBe("https://api.example/api/v1/server-info");
  });
});

describe("createServerInfoGate", () => {
  const info = (require_auth_for_read: boolean) => ({ require_auth_for_read, allow_signup: true });

  it("answers what the server says and reuses it within the TTL", async () => {
    let t = 0;
    const fetchInfo = vi.fn(async () => info(true));
    const gate = createServerInfoGate({ fetchInfo, now: () => t, ttlMs: 30_000 });
    expect(await gate()).toBe(true);
    t = 29_999;
    expect(await gate()).toBe(true);
    expect(fetchInfo).toHaveBeenCalledTimes(1);
    t = 30_000;
    fetchInfo.mockResolvedValueOnce(info(false));
    expect(await gate()).toBe(false);
    expect(fetchInfo).toHaveBeenCalledTimes(2);
  });

  it("never redirects when the server cannot be asked, and retries soon", async () => {
    let t = 0;
    const fetchInfo = vi.fn(async (): Promise<ReturnType<typeof info> | null> => null);
    const gate = createServerInfoGate({ fetchInfo, now: () => t, failureTtlMs: 5_000 });
    expect(await gate()).toBe(false);
    t = 4_999;
    expect(await gate()).toBe(false);
    expect(fetchInfo).toHaveBeenCalledTimes(1);
    t = 5_000;
    fetchInfo.mockResolvedValueOnce(info(true));
    expect(await gate()).toBe(true);
  });

  it("treats a throwing lookup as a failure", async () => {
    const gate = createServerInfoGate({
      fetchInfo: async () => {
        throw new Error("boom");
      },
    });
    expect(await gate()).toBe(false);
  });

  it("shares one in-flight lookup between concurrent callers", async () => {
    let resolve: (v: ReturnType<typeof info>) => void = () => {};
    const fetchInfo = vi.fn(
      () =>
        new Promise<ReturnType<typeof info>>((r) => {
          resolve = r;
        }),
    );
    const gate = createServerInfoGate({ fetchInfo });
    const a = gate();
    const b = gate();
    resolve(info(true));
    expect(await Promise.all([a, b])).toEqual([true, true]);
    expect(fetchInfo).toHaveBeenCalledTimes(1);
  });
});
