import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { routeForPath } from "@/lib/preupload";

function mockPreupload(status: number, body: unknown): { calls: RequestInit[]; urls: string[] } {
  const calls: RequestInit[] = [];
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      urls.push(url);
      calls.push(init);
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return { calls, urls };
}

beforeEach(() => {
  vi.stubEnv("API_URL", "http://localhost:8080");
  vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("routeForPath", () => {
  it("asks the HF-compatible preupload endpoint for the repository kind", async () => {
    const { urls, calls } = mockPreupload(200, {
      files: [{ path: "a.md", uploadMode: "regular" }],
    });
    await routeForPath("dataset", "alice", "my ds", "main", "data/a.parquet");
    expect(urls[0]).toBe("http://localhost:8080/api/datasets/alice/my%20ds/preupload/main");
    // size 0 is what makes the answer depend on .gitattributes alone.
    expect(JSON.parse(String(calls[0]?.body))).toEqual({
      files: [{ path: "data/a.parquet", sample: "", size: 0 }],
    });
  });

  it("reports an LFS-managed path", async () => {
    mockPreupload(200, { files: [{ path: "m.safetensors", uploadMode: "lfs" }] });
    expect(await routeForPath("model", "a", "b", "main", "m.safetensors")).toBe("lfs");
  });

  it("reports an ordinary path", async () => {
    mockPreupload(200, { files: [{ path: "notes.md", uploadMode: "regular" }] });
    expect(await routeForPath("model", "a", "b", "main", "notes.md")).toBe("regular");
  });

  it("answers unknown — never 'regular' — when the check fails", async () => {
    mockPreupload(500, { error: { message: "boom", type: "internal_error" } });
    expect(await routeForPath("model", "a", "b", "main", "notes.md")).toBe("unknown");
  });

  it("answers unknown for a body it does not recognise", async () => {
    mockPreupload(200, { files: [] });
    expect(await routeForPath("model", "a", "b", "main", "notes.md")).toBe("unknown");
    mockPreupload(200, {});
    expect(await routeForPath("model", "a", "b", "main", "notes.md")).toBe("unknown");
    mockPreupload(200, { files: [{ path: "notes.md", uploadMode: "something-new" }] });
    expect(await routeForPath("model", "a", "b", "main", "notes.md")).toBe("unknown");
  });

  it("does not throw when the backend is unreachable", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("ECONNREFUSED");
      }),
    );
    expect(await routeForPath("model", "a", "b", "main", "notes.md")).toBe("unknown");
  });
});
