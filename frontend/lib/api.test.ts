import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiFetch, isRepoMoved } from "@/lib/api";

// buildUrl is not exported, so it is exercised indirectly through apiFetch,
// with global fetch mocked to capture the URL it was actually called with.
function mockFetchOnce(): { calls: string[] } {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) => {
      calls.push(url);
      return new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
    }),
  );
  return { calls };
}

function mockFetchResponse(status: number, body: unknown): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
}

beforeEach(() => {
  // apiFetch resolves the base URL from API_URL first on the server side
  // (see lib/api.ts's apiBaseUrl); pin both so the test is independent of
  // whatever the ambient environment happens to have set.
  vi.stubEnv("API_URL", "http://localhost:8080");
  vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("apiFetch query building", () => {
  it("sends a scalar value once", async () => {
    const { calls } = mockFetchOnce();
    await apiFetch("/api/v1/repos", { query: { kind: "dataset" } });
    expect(calls[0]).toBe("http://localhost:8080/api/v1/repos?kind=dataset");
  });

  it("sends an array value as one key per entry, e.g. tags=a&tags=b", async () => {
    const { calls } = mockFetchOnce();
    await apiFetch("/api/v1/repos", { query: { tags: ["nlp", "pytorch"] } });
    expect(calls[0]).toBe("http://localhost:8080/api/v1/repos?tags=nlp&tags=pytorch");
  });

  it("omits undefined, null, empty-string, and empty-array-element values", async () => {
    const { calls } = mockFetchOnce();
    await apiFetch("/api/v1/repos", {
      query: { q: undefined, author: null, search: "", tags: ["", "nlp"] },
    });
    expect(calls[0]).toBe("http://localhost:8080/api/v1/repos?tags=nlp");
  });

  it("omits the query string entirely when every value is filtered out", async () => {
    const { calls } = mockFetchOnce();
    await apiFetch("/api/v1/repos", { query: { q: undefined, tags: [] } });
    expect(calls[0]).toBe("http://localhost:8080/api/v1/repos");
  });

  it("combines scalar and array params in the same request", async () => {
    const { calls } = mockFetchOnce();
    await apiFetch("/api/v1/repos", {
      query: { kind: "model", tags: ["a", "b"], license: "mit" },
    });
    expect(calls[0]).toBe(
      "http://localhost:8080/api/v1/repos?kind=model&tags=a&tags=b&license=mit",
    );
  });
});

// docs/dev/repo-transfer-design.md §9: a repository resolved by its former name
// answers 404 with `{"error":{"type":"repo_moved","moved_to":{...}}}`, and
// apiFetch must surface that as `ApiResult.movedTo` without throwing — see
// CLAUDE.md invariant 3.
describe("apiFetch repo_moved handling", () => {
  it("carries moved_to into the result as movedTo", async () => {
    mockFetchResponse(404, {
      error: {
        type: "repo_moved",
        message: "moved",
        moved_to: { namespace: "bob", name: "bar" },
      },
    });
    const result = await apiFetch("/api/v1/repos/model/alice/foo");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.status).toBe(404);
    expect(result.movedTo).toEqual({ namespace: "bob", name: "bar" });
    expect(isRepoMoved(result)).toBe(true);
  });

  it("leaves movedTo undefined for a plain 404", async () => {
    mockFetchResponse(404, { error: { type: "not_found", message: "no such repo" } });
    const result = await apiFetch("/api/v1/repos/model/alice/foo");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.movedTo).toBeUndefined();
    expect(isRepoMoved(result)).toBe(false);
  });

  it("does not flag a non-404 error even if it somehow carries moved_to", async () => {
    mockFetchResponse(409, {
      error: { type: "conflict", message: "taken", moved_to: { namespace: "bob", name: "bar" } },
    });
    const result = await apiFetch("/api/v1/repos/model/alice/foo/transfer");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(isRepoMoved(result)).toBe(false);
  });
});

// [S12]: `ApiResult.type` carries the backend's `error.type` so
// lib/api-error-message.ts can translate it instead of showing the raw
// English `message` on screen.
describe("apiFetch error type handling", () => {
  it("surfaces error.type as result.type", async () => {
    mockFetchResponse(403, {
      error: { type: "repository_archived", message: "alice/foo is archived and read-only" },
    });
    const result = await apiFetch("/api/v1/repos/model/alice/foo/tree");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.type).toBe("repository_archived");
    expect(result.message).toBe("alice/foo is archived and read-only");
  });

  it("leaves type undefined for a network failure (status 0)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new Error("fetch failed");
      }),
    );
    const result = await apiFetch("/api/v1/repos/model/alice/foo");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.status).toBe(0);
    expect(result.type).toBeUndefined();
  });

  it("leaves type undefined when the error body has no type (non-JSON body)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("plain text", { status: 500 })),
    );
    const result = await apiFetch("/api/v1/repos/model/alice/foo");
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected a failure result");
    expect(result.type).toBeUndefined();
  });
});
