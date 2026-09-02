import { describe, expect, it } from "vitest";
import nextConfig, { SECURITY_HEADERS } from "@/next.config";

/**
 * The Web UI shipped with no security headers at all while the API had a full
 * set — `/settings/tokens` was framable and leaked the repository path it was
 * viewing in outbound Referers. These assertions are what keeps
 * `next.config.ts`'s `headers()` from quietly going away again.
 */
function headerValue(key: string): string {
  const found = SECURITY_HEADERS.find((h) => h.key.toLowerCase() === key.toLowerCase());
  if (!found) throw new Error(`no ${key} header`);
  return found.value;
}

function cspDirective(name: string): string[] {
  const csp = headerValue("Content-Security-Policy");
  const directive = csp
    .split(";")
    .map((part) => part.trim())
    .find((part) => part === name || part.startsWith(`${name} `));
  if (!directive) throw new Error(`no ${name} directive in ${csp}`);
  return directive.split(/\s+/).slice(1);
}

describe("security headers", () => {
  it("are applied to every route", async () => {
    const headers = await nextConfig.headers?.();
    expect(headers).toEqual([{ source: "/:path*", headers: SECURITY_HEADERS }]);
  });

  it("carry the three flat headers the backend already sends", () => {
    expect(headerValue("X-Frame-Options")).toBe("DENY");
    expect(headerValue("Referrer-Policy")).toBe("strict-origin-when-cross-origin");
    expect(headerValue("X-Content-Type-Options")).toBe("nosniff");
  });

  it("locks down the directives the Markdown pipeline is the reason for", () => {
    // Raw HTML in a README is sanitised, so these are the second lock: an
    // injected <script src>, <base>, <object>/<embed> or <iframe> is refused
    // even if the sanitiser ever lets one through.
    expect(cspDirective("object-src")).toEqual(["'none'"]);
    expect(cspDirective("frame-src")).toEqual(["'none'"]);
    expect(cspDirective("base-uri")).toEqual(["'self'"]);
    expect(cspDirective("form-action")).toEqual(["'self'"]);
    expect(cspDirective("frame-ancestors")).toEqual(["'none'"]);
    expect(cspDirective("script-src")).toContain("'self'");
    expect(cspDirective("script-src")).not.toContain("*");
  });

  it("keeps the app-breaking directives permissive on purpose", () => {
    // NEXT_PUBLIC_API_URL is a deploy-time value that `headers()` cannot see,
    // and README images / Parquet image cells resolve against it. Pinning an
    // origin here would break every fetch on a split-host deployment.
    for (const directive of ["connect-src", "img-src", "media-src"]) {
      expect(cspDirective(directive), directive).toEqual(
        expect.arrayContaining(["https:", "http:"]),
      );
    }
    // DuckDB-WASM compiles a wasm module in the SQL console.
    expect(cspDirective("script-src")).toEqual(
      expect.arrayContaining(["'wasm-unsafe-eval'", "'unsafe-eval'"]),
    );
    expect(cspDirective("worker-src")).toEqual(expect.arrayContaining(["'self'", "blob:"]));
  });
});
