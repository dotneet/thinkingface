import { describe, expect, it } from "vitest";
import {
  ApiResultError,
  errorMessage,
  type FailedApiResult,
  queryErrorMessage,
} from "@/lib/api-error-message";
import type { MessageKey } from "@/lib/i18n";
import { createTranslator } from "@/lib/i18n";

const en = createTranslator("en");
const ja = createTranslator("ja");

function failed(overrides: Partial<FailedApiResult>): FailedApiResult {
  return { ok: false, status: 404, message: "repository foo/bar not found", ...overrides };
}

describe("errorMessage", () => {
  it("translates a network failure (status 0) regardless of type", () => {
    const result = failed({ status: 0, message: "fetch failed", type: undefined });
    expect(errorMessage(en, result)).toBe(en("errors.networkError"));
    expect(errorMessage(ja, result)).toBe(ja("errors.networkError"));
  });

  it("maps every known backend error type to translated copy, in both locales", () => {
    const cases: Array<[string, number]> = [
      ["unauthorized", 401],
      ["forbidden", 403],
      ["not_found", 404],
      ["conflict", 409],
      ["payload_too_large", 413],
      ["internal_error", 500],
      ["repository_archived", 403],
      ["repo_moved", 404],
      ["transfer_not_pending", 409],
      ["method_not_allowed", 405],
      ["xet_not_supported", 400],
      ["rate_limited", 429],
    ];
    for (const [type, status] of cases) {
      const result = failed({ status, type, message: "backend English detail" });
      for (const t of [en, ja]) {
        const message = errorMessage(t, result);
        expect(message).not.toBe(result.message);
        expect(message.length).toBeGreaterThan(0);
      }
    }
  });

  it("keeps the backend reason for forbidden instead of flattening it", () => {
    // A sign-up on an instance with registration closed answers `forbidden`
    // with the actual reason; rendering the generic "you don't have
    // permission" made that read as a permissions bug.
    const result = failed({
      status: 403,
      type: "forbidden",
      message: "sign-up is disabled on this instance",
    });
    expect(errorMessage(en, result)).toBe("Not allowed: sign-up is disabled on this instance");
    expect(errorMessage(ja, result)).toBe(
      "許可されていません: sign-up is disabled on this instance",
    );
  });

  it("keeps the backend reason for conflict instead of contradicting it", () => {
    // "That already exists." was not just vague for these two, it was false.
    const defaultBranch = failed({
      status: 409,
      type: "conflict",
      message: "main is the default branch of acme/bert and cannot be deleted",
    });
    expect(errorMessage(en, defaultBranch)).toBe(
      "Conflict: main is the default branch of acme/bert and cannot be deleted",
    );
    expect(errorMessage(ja, defaultBranch)).toBe(
      "競合が発生しました: main is the default branch of acme/bert and cannot be deleted",
    );

    const notABranch = failed({
      status: 409,
      type: "conflict",
      message: "v1.0 is a tag, not a branch; uploads must target a branch",
    });
    for (const t of [en, ja]) {
      expect(errorMessage(t, notABranch)).toContain(
        "v1.0 is a tag, not a branch; uploads must target a branch",
      );
      expect(errorMessage(t, notABranch)).not.toBe(t("errors.conflict"));
    }
  });

  it("falls back to the generic wording when a detail type carries no message", () => {
    // The bug this guards: `t("errors.badRequest")` called with no params
    // returns its template verbatim (createTranslator's `if (!params) return
    // template`), so a bad_request with an empty message printed the literal
    // "Invalid request: {detail}" on screen. Assert the placeholder is gone,
    // not just that the sentence doesn't end in a colon — "…: {detail}"
    // passes both of those and is exactly the string we shipped.
    for (const type of ["forbidden", "bad_request", "conflict"]) {
      for (const message of ["", "   "]) {
        const result = failed({ status: 409, type, message });
        for (const t of [en, ja]) {
          const rendered = errorMessage(t, result);
          expect(rendered, `${type} / ${JSON.stringify(message)}`).not.toMatch(/\{\w+\}/);
          expect(rendered, type).not.toContain(":  ");
          expect(rendered, type).not.toMatch(/: *$/);
          expect(rendered.trim().length, type).toBeGreaterThan(0);
        }
      }
    }
  });

  it("never renders an unfilled placeholder for any known type", () => {
    // ERROR_TYPE_KEYS is rendered with `t(key)` — no params — so every key it
    // points at has to be a complete sentence. This is the general form of the
    // bad_request bug above: adding another `…Detail` template to that map
    // would leak "{detail}" onto the page the same way.
    const types = [
      "bad_request",
      "unauthorized",
      "forbidden",
      "not_found",
      "conflict",
      "payload_too_large",
      "internal_error",
      "repository_archived",
      "repo_moved",
      "transfer_not_pending",
      "method_not_allowed",
      "xet_not_supported",
      "account_disabled",
      "approval_pending",
      "account_pending",
      "rate_limited",
      "overloaded",
      "range_not_satisfiable",
      "timeout",
      "network_error",
      "upload_cancelled",
    ];
    for (const type of types) {
      // An empty message is what forces the generic branch for the detail
      // types; the rest ignore the message entirely.
      const result = failed({ status: 400, type, message: "" });
      for (const t of [en, ja]) {
        expect(errorMessage(t, result), type).not.toMatch(/\{\w+\}/);
      }
    }
  });

  it("translates the types added after the first audit pass", () => {
    // range_not_satisfiable (resolve.go) and timeout (server.go's
    // handlerTimeoutBody) reached the client before they were mapped, which
    // dropped the caller through to `result.message` — backend English.
    const cases: Array<[string, number, string]> = [
      ["range_not_satisfiable", 416, "the requested range starts at or past the end"],
      ["timeout", 504, "the server took too long to answer this request"],
    ];
    for (const [type, status, message] of cases) {
      const result = failed({ status, type, message });
      for (const t of [en, ja]) {
        const rendered = errorMessage(t, result);
        expect(rendered, type).not.toBe(message);
        expect(rendered, type).not.toContain(message);
        expect(rendered.length, type).toBeGreaterThan(0);
      }
      expect(errorMessage(en, result), type).not.toBe(errorMessage(ja, result));
    }
  });

  it("never leaks a server-side message for types written for an operator", () => {
    // The line DETAIL_KEYS draws: an internal error's text is written for
    // whoever reads the logs, not for the person on the page.
    for (const type of ["internal_error", "overloaded", "unauthorized", "not_found"]) {
      const result = failed({ status: 500, type, message: "pq: relation tokens does not exist" });
      expect(errorMessage(en, result), type).not.toContain("pq:");
      expect(errorMessage(ja, result), type).not.toContain("pq:");
    }
  });

  it("keeps the backend detail for bad_request, interpolated into translated copy", () => {
    const result = failed({
      status: 400,
      type: "bad_request",
      message: "name must not contain spaces",
    });
    expect(errorMessage(en, result)).toBe("Invalid request: name must not contain spaces");
    expect(errorMessage(ja, result)).toBe("リクエストが不正です: name must not contain spaces");
  });

  it("falls back to the generic failure for an unmapped status, never raw English", () => {
    const result = failed({ status: 418, type: "teapot", message: "I'm a teapot" });
    for (const t of [en, ja]) {
      const rendered = errorMessage(t, result);
      expect(rendered).toBe(t("errors.internalError"));
      expect(rendered).not.toContain("teapot");
    }
  });

  it("maps an unknown or missing type to the sentence for its status", () => {
    // A proxy/gateway failure carries no backend body (`type` undefined), and
    // a backend type this dictionary doesn't know yet arrives the same way.
    // Either used to degrade to `errors.internalError`, so even a 404 read as
    // a server failure. Now the status decides (via `typeForStatus` in
    // lib/error-status.ts, the same table lib/upload.ts synthesizes from).
    const cases: Array<[number, MessageKey]> = [
      [400, "errors.badRequestGeneric"],
      [401, "errors.unauthorized"],
      [403, "errors.forbidden"],
      [404, "errors.notFound"],
      [408, "errors.timeout"],
      [504, "errors.timeout"],
      [409, "errors.conflict"],
      [413, "errors.payloadTooLarge"],
      [415, "errors.unsupportedMediaType"],
      [503, "errors.overloaded"],
      [429, "errors.rateLimited"],
      [500, "errors.internalError"],
      [502, "errors.internalError"],
    ];
    for (const [status, key] of cases) {
      for (const type of ["mystery_type", undefined] as const) {
        const result = failed({ status, type, message: "proxy English detail" });
        for (const t of [en, ja]) {
          const rendered = errorMessage(t, result);
          expect(rendered, `${status} / ${type}`).toBe(t(key));
          expect(rendered, `${status} / ${type}`).not.toContain("proxy English detail");
        }
      }
    }
  });

  it("never interpolates a bare status line into translated copy", () => {
    // A bodyless proxy/gateway failure has no backend reason — only the
    // transport's status line (`400 Bad Request`). lib/upload.ts leaves `type`
    // undefined for detail-capable statuses (see `isDetailErrorType`), and the
    // status fallback above must render the generic sentence rather than
    // "Invalid request: 400 Bad Request".
    const cases: Array<[number, string, MessageKey]> = [
      [400, "400 Bad Request", "errors.badRequestGeneric"],
      [403, "403 Forbidden", "errors.forbidden"],
      [404, "404 Not Found", "errors.notFound"],
      [409, "409 Conflict", "errors.conflict"],
    ];
    for (const [status, message, key] of cases) {
      const result = failed({ status, type: undefined, message });
      for (const t of [en, ja]) {
        const rendered = errorMessage(t, result);
        expect(rendered, `${status}`).toBe(t(key));
        expect(rendered, `${status}`).not.toContain(message);
      }
    }
  });

  it("falls back to the generic failure when type is missing, never raw English", () => {
    const result = failed({ status: 500, type: undefined, message: "boom" });
    for (const t of [en, ja]) {
      const rendered = errorMessage(t, result);
      expect(rendered).toBe(t("errors.internalError"));
      expect(rendered).not.toContain("boom");
    }
  });

  it("translates client-synthesized upload types instead of raw XHR text", () => {
    const cancelled = failed({ status: 0, type: "upload_cancelled", message: "Upload cancelled" });
    expect(errorMessage(en, cancelled)).toBe(en("errors.uploadCancelled"));
    expect(errorMessage(ja, cancelled)).toBe(ja("errors.uploadCancelled"));

    const dead = failed({ status: 0, type: "network_error", message: "Network error" });
    expect(errorMessage(en, dead)).toBe(en("errors.networkError"));
    expect(errorMessage(ja, dead)).toBe(ja("errors.networkError"));

    const timedOut = failed({ status: 0, type: "timeout", message: "timeout" });
    expect(errorMessage(en, timedOut)).toBe(en("errors.timeout"));
    expect(errorMessage(ja, timedOut)).toBe(ja("errors.timeout"));
  });
});

describe("queryErrorMessage / ApiResultError", () => {
  it("unwraps an ApiResultError through errorMessage", () => {
    const result = failed({ status: 403, type: "forbidden", message: "this token is read-only" });
    const error = new ApiResultError(result);
    expect(queryErrorMessage(en, error, "fallback")).toBe(
      en("errors.forbiddenDetail", { detail: "this token is read-only" }),
    );
  });

  it("uses the fallback for a non-API error", () => {
    expect(queryErrorMessage(en, new Error("boom"), "fallback text")).toBe("fallback text");
    expect(queryErrorMessage(en, "not even an Error", "fallback text")).toBe("fallback text");
  });
});
