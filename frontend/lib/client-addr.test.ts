import { describe, expect, it } from "vitest";

import {
  CLIENT_ADDR_HEADER,
  PROXY_SECRET_HEADER,
  pinClientAddr,
  resolveClientAddr,
  trustedHops,
} from "./client-addr";

describe("trustedHops", () => {
  it("reads a non-negative integer", () => {
    expect(trustedHops("2")).toBe(2);
    expect(trustedHops(" 1 ")).toBe(1);
  });

  it("treats anything else as no trusted proxy", () => {
    for (const raw of [undefined, "", "-1", "1.5", "two", "1,2"]) {
      expect(trustedHops(raw)).toBe(0);
    }
  });
});

describe("resolveClientAddr", () => {
  it("is the socket peer when nothing is trusted, whatever the client sent", () => {
    expect(resolveClientAddr("203.0.113.66", "198.51.100.20", 0)).toBe("198.51.100.20");
  });

  it("reads the entry `hops` places from the right behind appending proxies", () => {
    // browser 198.51.100.20 -> nginx 10.0.0.2 (appends) -> this server
    expect(resolveClientAddr("198.51.100.20", "10.0.0.2", 1)).toBe("198.51.100.20");
    // a forged prefix cannot displace the entry the trusted proxy appended
    expect(resolveClientAddr("203.0.113.66, 198.51.100.20", "10.0.0.2", 1)).toBe("198.51.100.20");
    // two proxies, one header line each
    expect(resolveClientAddr(["198.51.100.20", "10.0.0.1"], "10.0.0.2", 2)).toBe("198.51.100.20");
  });

  it("falls back to the peer when the chain is shorter than the trusted hops", () => {
    expect(resolveClientAddr(undefined, "198.51.100.20", 1)).toBe("198.51.100.20");
    expect(resolveClientAddr("", "198.51.100.20", 2)).toBe("198.51.100.20");
  });

  it("is undefined when the socket has no address", () => {
    expect(resolveClientAddr(undefined, undefined, 0)).toBeUndefined();
  });
});

describe("pinClientAddr", () => {
  it("replaces whatever the client sent", () => {
    const headers: Record<string, string | string[] | undefined> = {
      [CLIENT_ADDR_HEADER]: "1.2.3.4",
      [PROXY_SECRET_HEADER]: "guess",
      "x-forwarded-for": "203.0.113.66",
    };
    pinClientAddr(headers, "198.51.100.20", 0);
    expect(headers[CLIENT_ADDR_HEADER]).toBe("198.51.100.20");
    expect(headers[PROXY_SECRET_HEADER]).toBeUndefined();
  });

  it("leaves no header when the address is unknown", () => {
    const headers: Record<string, string | string[] | undefined> = {
      [CLIENT_ADDR_HEADER]: "1.2.3.4",
    };
    pinClientAddr(headers, undefined, 0);
    expect(CLIENT_ADDR_HEADER in headers).toBe(false);
  });
});
