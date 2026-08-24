import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { uploadFiles } from "@/lib/upload";

/**
 * Minimal XMLHttpRequest stand-in. `uploadFiles` uses XHR rather than fetch
 * because only XHR reports upload progress, so the test has to drive the same
 * shape: open/send, an `upload` object carrying onprogress, and one of
 * onload/onerror/onabort finishing the promise.
 */
class FakeXHR {
  static last: FakeXHR | null = null;

  method = "";
  url = "";
  withCredentials = false;
  body: FormData | null = null;
  status = 200;
  statusText = "OK";
  responseText = "{}";
  aborted = false;

  upload: {
    onprogress: ((e: { loaded: number; total: number; lengthComputable: boolean }) => void) | null;
  } = {
    onprogress: null,
  };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;

  constructor() {
    FakeXHR.last = this;
  }

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }

  send(body: FormData) {
    this.body = body;
    // Deferred so the caller has returned its promise before it settles.
    queueMicrotask(() => {
      this.upload.onprogress?.({ loaded: 512, total: 1024, lengthComputable: true });
      this.onload?.();
    });
  }

  abort() {
    this.aborted = true;
    this.onabort?.();
  }
}

function install(configure?: (xhr: FakeXHR) => void) {
  vi.stubGlobal(
    "XMLHttpRequest",
    class extends FakeXHR {
      constructor() {
        super();
        configure?.(this);
      }
    },
  );
}

function textFile(name: string, content = "hello"): File {
  return new File([content], name, { type: "text/plain" });
}

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_API_URL", "http://localhost:8080");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  FakeXHR.last = null;
});

describe("uploadFiles", () => {
  it("posts to the upload endpoint with credentials", async () => {
    install();
    await uploadFiles("model", "alice", "my repo", "main", [
      { path: "a.txt", file: textFile("a.txt") },
    ]);
    const xhr = FakeXHR.last;
    expect(xhr?.method).toBe("POST");
    expect(xhr?.url).toBe("http://localhost:8080/api/v1/upload/model/alice/my%20repo/main");
    expect(xhr?.withCredentials).toBe(true);
  });

  it("interleaves each path field before the file it names", async () => {
    install();
    await uploadFiles(
      "dataset",
      "alice",
      "ds",
      "main",
      [
        { path: "data/a.txt", file: textFile("a.txt") },
        { path: "data/b.txt", file: textFile("b.txt") },
      ],
      { message: "Add data" },
    );
    const entries = [...(FakeXHR.last?.body?.entries() ?? [])].map(([key, value]) => [
      key,
      value instanceof File ? value.name : value,
    ]);
    expect(entries).toEqual([
      ["message", "Add data"],
      ["path", "data/a.txt"],
      ["file", "a.txt"],
      ["path", "data/b.txt"],
      ["file", "b.txt"],
    ]);
  });

  it("reports progress while sending", async () => {
    install();
    const seen: Array<[number, number]> = [];
    await uploadFiles("model", "a", "b", "main", [{ path: "a.txt", file: textFile("a.txt") }], {
      onProgress: (loaded, total) => seen.push([loaded, total]),
    });
    expect(seen).toEqual([[512, 1024]]);
  });

  it("returns the parsed body on success", async () => {
    install((xhr) => {
      xhr.responseText = JSON.stringify({ commit_oid: "abc", paths: ["a.txt"] });
    });
    const result = await uploadFiles("model", "a", "b", "main", [
      { path: "a.txt", file: textFile("a.txt") },
    ]);
    expect(result).toEqual({ ok: true, data: { commit_oid: "abc", paths: ["a.txt"] } });
  });

  it("maps an error body onto the ApiResult failure shape, type included", async () => {
    install((xhr) => {
      xhr.status = 413;
      xhr.statusText = "Payload Too Large";
      xhr.responseText = JSON.stringify({
        error: { message: "a.bin is too large", type: "payload_too_large" },
      });
    });
    const result = await uploadFiles("model", "a", "b", "main", [
      { path: "a.bin", file: textFile("a.bin") },
    ]);
    expect(result).toEqual({
      ok: false,
      status: 413,
      message: "a.bin is too large",
      type: "payload_too_large",
    });
  });

  it("falls back to the status line when the error body is not JSON", async () => {
    install((xhr) => {
      xhr.status = 502;
      xhr.statusText = "Bad Gateway";
      xhr.responseText = "<html>nope</html>";
    });
    const result = await uploadFiles("model", "a", "b", "main", [
      { path: "a.txt", file: textFile("a.txt") },
    ]);
    expect(result).toMatchObject({ ok: false, status: 502, message: "502 Bad Gateway" });
  });

  it("never throws on a network failure", async () => {
    vi.stubGlobal(
      "XMLHttpRequest",
      class extends FakeXHR {
        override send() {
          queueMicrotask(() => this.onerror?.());
        }
      },
    );
    const result = await uploadFiles("model", "a", "b", "main", [
      { path: "a.txt", file: textFile("a.txt") },
    ]);
    expect(result).toEqual({ ok: false, status: 0, message: "Network error" });
  });

  it("resolves rather than hanging when the caller aborts", async () => {
    vi.stubGlobal(
      "XMLHttpRequest",
      class extends FakeXHR {
        override send() {
          // Never settles on its own: only the abort below finishes it.
        }
      },
    );
    const controller = new AbortController();
    const promise = uploadFiles(
      "model",
      "a",
      "b",
      "main",
      [{ path: "a.txt", file: textFile("a.txt") }],
      { signal: controller.signal },
    );
    controller.abort();
    expect(await promise).toEqual({ ok: false, status: 0, message: "Upload cancelled" });
  });
});
