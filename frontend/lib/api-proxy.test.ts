import { afterEach, describe, expect, it, vi } from "vitest";

import {
  bodyWasDecoded,
  checkSameOrigin,
  forwardRequestHeaders,
  forwardResponseHeaders,
  isConfinedUpstreamPath,
  proxyRequest,
  rewriteLocation,
  upstreamBase,
  upstreamUrl,
  webEdgeFromEnv,
} from "@/lib/api-proxy";

const BASE = "http://api:8080";

function headers(init: Record<string, string>): Headers {
  return new Headers(init);
}

describe("upstreamBase", () => {
  it("reads API_URL, dropping a trailing slash", () => {
    expect(upstreamBase({ API_URL: "http://api:8080/" })).toBe("http://api:8080");
  });

  it("falls back to localhost:8080 when unset or blank", () => {
    expect(upstreamBase({})).toBe("http://localhost:8080");
    expect(upstreamBase({ API_URL: " " })).toBe("http://localhost:8080");
  });
});

describe("upstreamUrl", () => {
  it("keeps the path's encoding and the query string exactly", () => {
    expect(
      upstreamUrl(
        "http://web:3000/api/v1/repos/model/a/b/tree/feature%2Fx/dir%20x?recursive=1&x=a%2Bb",
        BASE,
      ),
    ).toBe("http://api:8080/api/v1/repos/model/a/b/tree/feature%2Fx/dir%20x?recursive=1&x=a%2Bb");
  });
});

describe("isConfinedUpstreamPath", () => {
  it("allows the Web UI + HF-compatible API surface", () => {
    expect(isConfinedUpstreamPath("/api/v1/auth/login")).toBe(true);
    expect(isConfinedUpstreamPath("/api/v1/repos/dataset/a/b/tree/feature%2Fx")).toBe(true);
    expect(isConfinedUpstreamPath("/api")).toBe(true);
  });

  it("allows the resolve shapes the two resolve route handlers proxy", () => {
    expect(isConfinedUpstreamPath("/models/a/b/resolve/main/README.md")).toBe(true);
    expect(isConfinedUpstreamPath("/datasets/a/b/resolve/main/data/t.parquet")).toBe(true);
  });

  it("refuses a path outside both shapes, including one only reachable via dot-segment escape", () => {
    // What `/api/%2e%2e/datasets/a/b/git-receive-pack` normalises to.
    expect(isConfinedUpstreamPath("/datasets/a/b/git-receive-pack")).toBe(false);
    expect(isConfinedUpstreamPath("/healthz")).toBe(false);
    expect(isConfinedUpstreamPath("/models/a/b/tree/main")).toBe(false);
  });
});

describe("checkSameOrigin", () => {
  it("lets every safe method through, whatever the origin", () => {
    for (const method of ["GET", "HEAD", "OPTIONS"]) {
      expect(
        checkSameOrigin(
          method,
          headers({
            host: "web:3000",
            origin: "https://evil.example",
            "sec-fetch-site": "cross-site",
          }),
        ),
      ).toEqual({ ok: true });
    }
  });

  it("accepts an Origin that names the request's own host", () => {
    expect(
      checkSameOrigin("POST", headers({ host: "localhost:3111", origin: "http://localhost:3111" })),
    ).toEqual({ ok: true });
  });

  it("refuses an Origin on another host or port", () => {
    expect(
      checkSameOrigin("POST", headers({ host: "localhost:3111", origin: "http://localhost:3000" }))
        .ok,
    ).toBe(false);
    expect(
      checkSameOrigin("DELETE", headers({ host: "hub.example", origin: "https://evil.example" }))
        .ok,
    ).toBe(false);
  });

  it("refuses an opaque or unparsable Origin", () => {
    expect(checkSameOrigin("POST", headers({ host: "hub.example", origin: "null" })).ok).toBe(
      false,
    );
    expect(checkSameOrigin("POST", headers({ host: "hub.example", origin: "garbage" })).ok).toBe(
      false,
    );
  });

  it("falls back to the Referer's origin when there is no Origin", () => {
    expect(
      checkSameOrigin("PUT", headers({ host: "hub.example", referer: "https://hub.example/x?y" })),
    ).toEqual({ ok: true });
    expect(
      checkSameOrigin("PUT", headers({ host: "hub.example", referer: "https://evil.example/x" }))
        .ok,
    ).toBe(false);
  });

  it("passes a request with neither header, like the API does (not a browser)", () => {
    expect(checkSameOrigin("POST", headers({ host: "hub.example" }))).toEqual({ ok: true });
    expect(checkSameOrigin("POST", headers({ host: "hub.example", referer: "::" }))).toEqual({
      ok: true,
    });
  });

  it("refuses Sec-Fetch-Site: cross-site on an unsafe method even with a matching Origin", () => {
    expect(
      checkSameOrigin(
        "POST",
        headers({
          host: "hub.example",
          origin: "https://hub.example",
          "sec-fetch-site": "cross-site",
        }),
      ).ok,
    ).toBe(false);
    expect(
      checkSameOrigin(
        "POST",
        headers({
          host: "hub.example",
          origin: "https://hub.example",
          "sec-fetch-site": "same-origin",
        }),
      ),
    ).toEqual({ ok: true });
  });

  it("ignores the scheme's default port on either side, and case", () => {
    expect(
      checkSameOrigin("POST", headers({ host: "Hub.Example:443", origin: "https://hub.example" })),
    ).toEqual({ ok: true });
    expect(
      checkSameOrigin("POST", headers({ host: "hub.example", origin: "http://hub.example:80" })),
    ).toEqual({ ok: true });
  });

  it("accepts X-Forwarded-Host for a front proxy that rewrote Host", () => {
    expect(
      checkSameOrigin(
        "PATCH",
        headers({
          host: "web:3000",
          "x-forwarded-host": "hub.example",
          origin: "https://hub.example",
        }),
      ),
    ).toEqual({ ok: true });
    expect(
      checkSameOrigin(
        "PATCH",
        headers({
          host: "web:3000",
          "x-forwarded-host": "hub.example",
          origin: "https://evil.example",
        }),
      ).ok,
    ).toBe(false);
  });
});

describe("forwardRequestHeaders", () => {
  function request(method: string, init: Record<string, string>): Request {
    return new Request("http://localhost:3111/api/v1/x", { method, headers: init });
  }

  describe("the browser's address for the API's rate limiter", () => {
    const pinned = {
      host: "localhost:3000",
      "x-tf-client-addr": "198.51.100.20",
      "x-tf-proxy-secret": "sent-by-the-client",
    };

    it("is dropped when this process is not the pinning server (next dev / next start)", () => {
      const out = forwardRequestHeaders(request("GET", pinned), {
        pinned: false,
        proxySecret: "s",
      });
      expect(out.get("x-tf-client-addr")).toBeNull();
      expect(out.get("x-tf-proxy-secret")).toBeNull();
    });

    it("is forwarded with the configured secret behind server.ts, never the client's secret", () => {
      const out = forwardRequestHeaders(request("POST", pinned), {
        pinned: true,
        proxySecret: "the-real-secret",
      });
      expect(out.get("x-tf-client-addr")).toBe("198.51.100.20");
      expect(out.get("x-tf-proxy-secret")).toBe("the-real-secret");
    });

    it("sends no secret when none is configured", () => {
      const out = forwardRequestHeaders(request("GET", pinned), { pinned: true });
      expect(out.get("x-tf-client-addr")).toBe("198.51.100.20");
      expect(out.get("x-tf-proxy-secret")).toBeNull();
    });

    it("reads the pinning flag and the secret from the environment", () => {
      expect(webEdgeFromEnv({})).toEqual({ pinned: false, proxySecret: undefined });
      expect(webEdgeFromEnv({ TF_WEB_EDGE: "1", TF_WEB_PROXY_SECRET: "x" })).toEqual({
        pinned: true,
        proxySecret: "x",
      });
    });
  });

  it("drops hop-by-hop headers, Connection-nominated ones, Host and Expect", () => {
    const out = forwardRequestHeaders(
      request("GET", {
        host: "localhost:3111",
        connection: "keep-alive, x-secret-hop",
        "keep-alive": "timeout=5",
        te: "trailers",
        trailer: "x",
        upgrade: "h2c",
        "proxy-authorization": "Basic Zm9v",
        "x-secret-hop": "1",
        expect: "100-continue",
        cookie: "tf_session=abc",
        range: "bytes=0-9",
        "if-none-match": '"etag"',
      }),
    );
    for (const name of [
      "host",
      "connection",
      "keep-alive",
      "te",
      "trailer",
      "upgrade",
      "proxy-authorization",
      "x-secret-hop",
      "expect",
    ]) {
      expect(out.has(name), name).toBe(false);
    }
    expect(out.get("cookie")).toBe("tf_session=abc");
    expect(out.get("range")).toBe("bytes=0-9");
    expect(out.get("if-none-match")).toBe('"etag"');
  });

  it("asks the API for an unencoded body", () => {
    const out = forwardRequestHeaders(request("GET", { "accept-encoding": "gzip, br" }));
    expect(out.get("accept-encoding")).toBe("identity");
  });

  it("drops Origin and Referer on unsafe methods only", () => {
    const init = { origin: "http://localhost:3111", referer: "http://localhost:3111/settings" };
    const unsafe = forwardRequestHeaders(request("POST", init));
    expect(unsafe.has("origin")).toBe(false);
    expect(unsafe.has("referer")).toBe(false);
    const safe = forwardRequestHeaders(request("OPTIONS", init));
    expect(safe.get("origin")).toBe("http://localhost:3111");
  });

  it("forwards the X-Forwarded-* values Next.js computed, deriving missing ones", () => {
    const kept = forwardRequestHeaders(
      request("GET", {
        host: "localhost:3111",
        "x-forwarded-for": "203.0.113.9, 10.0.0.2",
        "x-forwarded-proto": "https",
        "x-forwarded-host": "hub.example",
      }),
    );
    expect(kept.get("x-forwarded-for")).toBe("203.0.113.9, 10.0.0.2");
    expect(kept.get("x-forwarded-proto")).toBe("https");
    expect(kept.get("x-forwarded-host")).toBe("hub.example");

    const derived = forwardRequestHeaders(request("GET", { host: "localhost:3111" }));
    expect(derived.get("x-forwarded-proto")).toBe("http");
    expect(derived.get("x-forwarded-host")).toBe("localhost:3111");
  });
});

describe("bodyWasDecoded", () => {
  it("is true only for codings fetch decodes on its own", () => {
    expect(bodyWasDecoded(null)).toBe(false);
    expect(bodyWasDecoded("identity")).toBe(false);
    expect(bodyWasDecoded("gzip")).toBe(true);
    expect(bodyWasDecoded("deflate, br")).toBe(true);
    // One unknown coding and fetch hands the bytes over untouched.
    expect(bodyWasDecoded("gzip, x-custom")).toBe(false);
  });
});

describe("rewriteLocation", () => {
  it("turns an absolute URL on the API's origin into a path on this one", () => {
    expect(rewriteLocation("http://api:8080/api/v1/x?y=1#z", BASE)).toBe("/api/v1/x?y=1#z");
  });

  it("leaves relative URLs and other origins alone", () => {
    expect(rewriteLocation("/login", BASE)).toBe("/login");
    expect(rewriteLocation("../x", BASE)).toBe("../x");
    const signed = "https://storage.googleapis.com/bucket/lfs/ab?X-Goog-Signature=1";
    expect(rewriteLocation(signed, BASE)).toBe(signed);
    expect(rewriteLocation("http://api:9090/x", BASE)).toBe("http://api:9090/x");
  });

  it("rewrites a protocol-relative URL on the API's host", () => {
    expect(rewriteLocation("//api:8080/x", BASE)).toBe("/x");
  });

  it("collapses a rewritten path starting with // to avoid an open redirect", () => {
    // Without collapsing, this would rewrite to "//evil.com/x" — itself a
    // protocol-relative Location that sends the browser to evil.com.
    expect(rewriteLocation("http://api:8080//evil.com/x", BASE)).toBe("/evil.com/x");
    expect(rewriteLocation("http://api:8080///evil.com/x", BASE)).toBe("/evil.com/x");
  });
});

describe("forwardResponseHeaders", () => {
  it("keeps every Set-Cookie as its own line", () => {
    const upstream = new Headers();
    upstream.append("set-cookie", "tf_session=a; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT");
    upstream.append("set-cookie", "tf_other=b; Path=/");
    const out = forwardResponseHeaders(upstream, BASE);
    expect(out.getSetCookie()).toEqual([
      "tf_session=a; Path=/; Expires=Wed, 21 Oct 2026 07:28:00 GMT",
      "tf_other=b; Path=/",
    ]);
  });

  it("drops hop-by-hop headers and passes the rest", () => {
    const out = forwardResponseHeaders(
      headers({
        connection: "close, x-hop",
        "x-hop": "1",
        "transfer-encoding": "chunked",
        "content-type": "application/octet-stream",
        "content-length": "10",
        "content-range": "bytes 0-9/100",
        etag: '"e"',
        "x-repo-commit": "abc",
      }),
      BASE,
    );
    expect(out.has("connection")).toBe(false);
    expect(out.has("x-hop")).toBe(false);
    expect(out.has("transfer-encoding")).toBe(false);
    expect(out.get("content-length")).toBe("10");
    expect(out.get("content-range")).toBe("bytes 0-9/100");
    expect(out.get("etag")).toBe('"e"');
    expect(out.get("x-repo-commit")).toBe("abc");
  });

  it("drops Content-Encoding and Content-Length of a body fetch decoded", () => {
    const out = forwardResponseHeaders(
      headers({ "content-encoding": "gzip", "content-length": "42", "content-type": "text/plain" }),
      BASE,
    );
    expect(out.has("content-encoding")).toBe(false);
    expect(out.has("content-length")).toBe(false);
    expect(out.get("content-type")).toBe("text/plain");
  });

  it("rewrites Location", () => {
    const out = forwardResponseHeaders(headers({ location: "http://api:8080/api/v1/y" }), BASE);
    expect(out.get("location")).toBe("/api/v1/y");
  });

  it("strips WWW-Authenticate so the browser never pops its native Basic-auth dialog on this origin", () => {
    const out = forwardResponseHeaders(
      headers({
        "www-authenticate": 'Basic realm="thinkingface"',
        "content-type": "application/json",
      }),
      BASE,
    );
    expect(out.has("www-authenticate")).toBe(false);
    expect(out.get("content-type")).toBe("application/json");
  });
});

describe("proxyRequest", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("forwards method, URL, headers and a streamed body, and streams the answer back", async () => {
    const fetchImpl = vi.fn(async (_url: string | URL | Request, init?: RequestInit) => {
      const sent = await new Response(init?.body as BodyInit).text();
      const out = new Headers({ "content-type": "application/json" });
      out.append("set-cookie", "tf_session=s; Path=/; HttpOnly");
      return new Response(JSON.stringify({ sent }), { status: 201, headers: out });
    });
    const res = await proxyRequest(
      new Request("http://localhost:3111/api/v1/auth/login?x=1", {
        method: "POST",
        headers: {
          host: "localhost:3111",
          origin: "http://localhost:3111",
          "content-type": "application/json",
        },
        body: '{"username":"admin"}',
      }),
      { base: BASE, fetchImpl: fetchImpl as unknown as typeof fetch },
    );
    expect(res.status).toBe(201);
    expect(await res.json()).toEqual({ sent: '{"username":"admin"}' });
    expect(res.headers.getSetCookie()).toEqual(["tf_session=s; Path=/; HttpOnly"]);

    const [url, init] = fetchImpl.mock.calls[0] ?? [];
    expect(url).toBe("http://api:8080/api/v1/auth/login?x=1");
    expect(init?.method).toBe("POST");
    expect(init?.redirect).toBe("manual");
    expect((init as { duplex?: string }).duplex).toBe("half");
    const sentHeaders = init?.headers as Headers;
    expect(sentHeaders.has("origin")).toBe(false);
    expect(sentHeaders.get("content-type")).toBe("application/json");
  });

  it("answers 404 in the API's error shape for a path that escapes /api via dot-segments, without calling the API", async () => {
    const fetchImpl = vi.fn();
    const res = await proxyRequest(
      new Request("http://localhost:3111/api/%2e%2e/datasets/a/b/git-receive-pack", {
        method: "POST",
        headers: { host: "localhost:3111", origin: "http://localhost:3111" },
      }),
      { base: BASE, fetchImpl: fetchImpl as unknown as typeof fetch },
    );
    expect(res.status).toBe(404);
    const body = (await res.json()) as { error: { type: string; message: string } };
    expect(body.error.type).toBe("not_found");
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("still forwards an /api/v1 path with an encoded slash inside a segment unchanged", async () => {
    const fetchImpl = vi.fn(
      async (_url: string | URL | Request, _init?: RequestInit) =>
        new Response("{}", { status: 200 }),
    );
    await proxyRequest(
      new Request("http://localhost:3111/api/v1/repos/dataset/a/b/tree/feature%2Fx"),
      { base: BASE, fetchImpl: fetchImpl as unknown as typeof fetch },
    );
    const [url] = fetchImpl.mock.calls[0] ?? [];
    expect(url).toBe("http://api:8080/api/v1/repos/dataset/a/b/tree/feature%2Fx");
  });

  it("answers 403 in the API's error shape without calling the API", async () => {
    const fetchImpl = vi.fn();
    const res = await proxyRequest(
      new Request("http://localhost:3111/api/v1/tokens", {
        method: "POST",
        headers: { host: "localhost:3111", origin: "https://evil.example" },
      }),
      { base: BASE, fetchImpl: fetchImpl as unknown as typeof fetch },
    );
    expect(res.status).toBe(403);
    const body = (await res.json()) as { error: { type: string; message: string } };
    expect(body.error.type).toBe("forbidden");
    expect(body.error.message).toBeTruthy();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("maps a network failure to 502 bad_gateway without leaking the upstream address", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const fetchImpl = vi.fn(async () => {
      throw new TypeError("fetch failed: connect ECONNREFUSED api:8080");
    });
    const res = await proxyRequest(new Request("http://localhost:3111/api/v1/stats"), {
      base: BASE,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    expect(res.status).toBe(502);
    const body = (await res.json()) as { error: { type: string; message: string } };
    expect(body.error.type).toBe("bad_gateway");
    expect(body.error.message).not.toContain("api:8080");
  });

  it("passes a redirect through without following it", async () => {
    const fetchImpl = vi.fn(
      async () =>
        new Response(null, {
          status: 302,
          headers: { location: "https://storage.googleapis.com/b/o?sig=1" },
        }),
    );
    const res = await proxyRequest(
      new Request("http://localhost:3111/models/a/b/resolve/main/w.bin"),
      { base: BASE, fetchImpl: fetchImpl as unknown as typeof fetch },
    );
    expect(res.status).toBe(302);
    expect(res.headers.get("location")).toBe("https://storage.googleapis.com/b/o?sig=1");
  });

  it("returns no body for 204 and HEAD, keeping Content-Length on HEAD", async () => {
    const noContent = await proxyRequest(
      new Request("http://localhost:3111/api/v1/auth/logout", { method: "POST" }),
      {
        base: BASE,
        fetchImpl: (async () => new Response(null, { status: 204 })) as unknown as typeof fetch,
      },
    );
    expect(noContent.status).toBe(204);
    expect(noContent.body).toBeNull();

    const head = await proxyRequest(
      new Request("http://localhost:3111/datasets/a/b/resolve/main/t.parquet", { method: "HEAD" }),
      {
        base: BASE,
        fetchImpl: (async () =>
          new Response(null, {
            status: 200,
            headers: { "content-length": "1234" },
          })) as unknown as typeof fetch,
      },
    );
    expect(head.status).toBe(200);
    expect(head.headers.get("content-length")).toBe("1234");
  });

  it("does not send a body on GET", async () => {
    const fetchImpl = vi.fn(
      async (_url: string | URL | Request, _init?: RequestInit) =>
        new Response("{}", { status: 200 }),
    );
    await proxyRequest(new Request("http://localhost:3111/api/v1/stats"), {
      base: BASE,
      fetchImpl: fetchImpl as unknown as typeof fetch,
    });
    const init = fetchImpl.mock.calls[0]?.[1] as (RequestInit & { duplex?: string }) | undefined;
    expect(init?.method).toBe("GET");
    expect(init?.body).toBeUndefined();
    expect(init?.duplex).toBeUndefined();
  });
});
