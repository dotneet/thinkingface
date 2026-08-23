import { describe, expect, it } from "vitest";
import type { ApiResult } from "@/lib/api";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";

// next/navigation's permanentRedirect() signals the redirect by throwing an
// Error whose `digest` encodes "NEXT_REDIRECT;<type>;<url>;<statusCode>;" —
// there is no request context to inspect in a plain unit test, so this reads
// the thrown digest instead of relying on a rendered response.
function capturedRedirectUrl(fn: () => void): string {
  try {
    fn();
  } catch (err) {
    const digest = (err as { digest?: string }).digest;
    if (!digest?.startsWith("NEXT_REDIRECT")) throw err;
    // digest = "NEXT_REDIRECT;replace;/the/url;308;" — url is everything
    // between the second and second-to-last ";", which also tolerates a
    // query string containing ";".
    return digest.split(";").slice(2, -2).join(";");
  }
  throw new Error("expected redirectIfRepoMoved to throw a redirect");
}

function movedResult(namespace: string, name: string): ApiResult<unknown> {
  return { ok: false, status: 404, message: "moved", movedTo: { namespace, name } };
}

describe("redirectIfRepoMoved", () => {
  it("does nothing on success", () => {
    expect(() => redirectIfRepoMoved({ ok: true, data: {} }, "model")).not.toThrow();
  });

  it("does nothing on a plain 404 without movedTo", () => {
    expect(() =>
      redirectIfRepoMoved({ ok: false, status: 404, message: "not found" }, "model"),
    ).not.toThrow();
  });

  it("does nothing on a non-404 failure that happens to carry movedTo", () => {
    // Not a real backend response shape, just guards the status check.
    expect(() =>
      redirectIfRepoMoved(
        { ok: false, status: 500, message: "boom", movedTo: { namespace: "a", name: "b" } },
        "model",
      ),
    ).not.toThrow();
  });

  it("redirects to the plain repo base when given a RepoKind", () => {
    const url = capturedRedirectUrl(() => redirectIfRepoMoved(movedResult("bob", "bar"), "model"));
    expect(url).toBe("/models/bob/bar");
  });

  it("encodes the moved namespace and name", () => {
    const url = capturedRedirectUrl(() =>
      redirectIfRepoMoved(movedResult("a b", "c/d"), "dataset"),
    );
    expect(url).toBe("/datasets/a%20b/c%2Fd");
  });

  it("preserves the rest of the path and query string via the builder callback", () => {
    const url = capturedRedirectUrl(() =>
      redirectIfRepoMoved(
        movedResult("bob", "bar"),
        (ns, name) => `/models/${ns}/${name}/tree/main/dir?after=x`,
      ),
    );
    expect(url).toBe("/models/bob/bar/tree/main/dir?after=x");
  });
});
