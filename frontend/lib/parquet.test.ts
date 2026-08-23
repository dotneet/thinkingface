import { beforeEach, describe, expect, it, vi } from "vitest";

// Only the request-building half of lib/parquet.ts is under test: the network
// call itself belongs to lib/api.ts, so apiFetch is replaced with a spy and we
// assert on the path/query/headers it is handed.
const apiFetch = vi.fn(async () => ({ ok: true as const, data: {} }));
vi.mock("@/lib/api", () => ({ apiFetch }));

const { getParquetRows, getParquetSchema } = await import("@/lib/parquet");

type Call = [string, { query?: Record<string, unknown>; headers?: unknown }];

function lastCall(): Call {
  return apiFetch.mock.calls.at(-1) as unknown as Call;
}

beforeEach(() => {
  apiFetch.mockClear();
});

describe("getParquetSchema", () => {
  it("builds the schema path from kind, repo, revision, and file path", async () => {
    await getParquetSchema("dataset", "acme", "squad", "main", ["data", "train.parquet"]);
    expect(lastCall()[0]).toBe("/api/v1/parquet/dataset/acme/squad/schema/main/data/train.parquet");
  });

  it("encodes every user-supplied segment", async () => {
    await getParquetSchema("model", "a b", "c/d", "feature/x", ["sub dir", "f#1.parquet"]);
    expect(lastCall()[0]).toBe(
      "/api/v1/parquet/model/a%20b/c%2Fd/schema/feature%2Fx/sub%20dir/f%231.parquet",
    );
  });

  it("leaves a trailing separator for an empty path", async () => {
    await getParquetSchema("dataset", "acme", "squad", "main", []);
    expect(lastCall()[0]).toBe("/api/v1/parquet/dataset/acme/squad/schema/main/");
  });

  it("forwards caller-supplied auth headers", async () => {
    await getParquetSchema("dataset", "acme", "squad", "main", ["a.parquet"], {
      headers: { cookie: "tf_session=x" },
    });
    expect(lastCall()[1].headers).toEqual({ cookie: "tf_session=x" });
  });

  it("passes undefined headers when no options are given", async () => {
    await getParquetSchema("dataset", "acme", "squad", "main", ["a.parquet"]);
    expect(lastCall()[1].headers).toBeUndefined();
  });
});

describe("getParquetRows", () => {
  it("uses the rows endpoint and forwards paging parameters", async () => {
    await getParquetRows("dataset", "acme", "squad", "main", ["a.parquet"], {
      offset: 100,
      limit: 50,
    });
    const [path, opts] = lastCall();
    expect(path).toBe("/api/v1/parquet/dataset/acme/squad/rows/main/a.parquet");
    expect(opts.query).toEqual({ offset: 100, limit: 50, columns: undefined });
  });

  it("joins the requested columns with commas", async () => {
    await getParquetRows("dataset", "acme", "squad", "main", ["a.parquet"], {
      columns: ["id", "text"],
    });
    expect(lastCall()[1].query?.columns).toBe("id,text");
  });

  it("omits the columns parameter when the list is empty", async () => {
    await getParquetRows("dataset", "acme", "squad", "main", ["a.parquet"], { columns: [] });
    expect(lastCall()[1].query?.columns).toBeUndefined();
  });

  it("keeps an explicit zero offset distinguishable from an absent one", async () => {
    await getParquetRows("dataset", "acme", "squad", "main", ["a.parquet"], { offset: 0 });
    expect(lastCall()[1].query?.offset).toBe(0);
    await getParquetRows("dataset", "acme", "squad", "main", ["a.parquet"], {});
    expect(lastCall()[1].query?.offset).toBeUndefined();
  });
});
