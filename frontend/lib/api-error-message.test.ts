import { describe, expect, it } from "vitest";
import {
  ApiResultError,
  errorMessage,
  type FailedApiResult,
  queryErrorMessage,
} from "@/lib/api-error-message";
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

  it("keeps the backend detail for bad_request, interpolated into translated copy", () => {
    const result = failed({
      status: 400,
      type: "bad_request",
      message: "name must not contain spaces",
    });
    expect(errorMessage(en, result)).toBe("Invalid request: name must not contain spaces");
    expect(errorMessage(ja, result)).toBe("リクエストが不正です: name must not contain spaces");
  });

  it("falls back to the raw backend message for an unrecognized type", () => {
    const result = failed({ status: 418, type: "teapot", message: "I'm a teapot" });
    expect(errorMessage(en, result)).toBe("I'm a teapot");
    expect(errorMessage(ja, result)).toBe("I'm a teapot");
  });

  it("falls back to the raw backend message when type is missing", () => {
    const result = failed({ status: 500, type: undefined, message: "boom" });
    expect(errorMessage(en, result)).toBe("boom");
  });
});

describe("queryErrorMessage / ApiResultError", () => {
  it("unwraps an ApiResultError through errorMessage", () => {
    const result = failed({ status: 403, type: "forbidden" });
    const error = new ApiResultError(result);
    expect(queryErrorMessage(en, error, "fallback")).toBe(en("errors.forbidden"));
  });

  it("uses the fallback for a non-API error", () => {
    expect(queryErrorMessage(en, new Error("boom"), "fallback text")).toBe("fallback text");
    expect(queryErrorMessage(en, "not even an Error", "fallback text")).toBe("fallback text");
  });
});
