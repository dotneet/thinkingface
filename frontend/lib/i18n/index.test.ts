import { describe, expect, it } from "vitest";
import {
  createTranslator,
  dictionaries,
  matchAcceptLanguage,
  parseLocalePreference,
  resolveLocale,
} from "@/lib/i18n";

describe("parseLocalePreference", () => {
  it("returns supported locales as-is", () => {
    expect(parseLocalePreference("ja")).toBe("ja");
    expect(parseLocalePreference("en")).toBe("en");
  });

  it("falls back to auto for missing or invalid values", () => {
    expect(parseLocalePreference(undefined)).toBe("auto");
    expect(parseLocalePreference(null)).toBe("auto");
    expect(parseLocalePreference("")).toBe("auto");
    expect(parseLocalePreference("fr")).toBe("auto");
    expect(parseLocalePreference("auto")).toBe("auto");
  });
});

describe("matchAcceptLanguage", () => {
  it("picks the first supported language in descending q-value order", () => {
    expect(matchAcceptLanguage("ja,en-US;q=0.9,en;q=0.8")).toBe("ja");
    expect(matchAcceptLanguage("en-US,en;q=0.9,ja;q=0.8")).toBe("en");
    expect(matchAcceptLanguage("fr;q=0.9,ja;q=0.8")).toBe("ja");
  });

  it("matches region-tagged locales by primary subtag", () => {
    expect(matchAcceptLanguage("ja-JP")).toBe("ja");
    expect(matchAcceptLanguage("en-GB")).toBe("en");
  });

  it("falls back to the default (en) for unsupported or missing values", () => {
    expect(matchAcceptLanguage("fr,de;q=0.9")).toBe("en");
    expect(matchAcceptLanguage(null)).toBe("en");
    expect(matchAcceptLanguage("")).toBe("en");
  });

  it("excludes q=0", () => {
    expect(matchAcceptLanguage("ja;q=0,en;q=0.5")).toBe("en");
  });
});

describe("resolveLocale", () => {
  it("explicit preference takes priority over Accept-Language", () => {
    expect(resolveLocale("en", "ja")).toBe("en");
    expect(resolveLocale("ja", "en-US")).toBe("ja");
  });

  it("resolves auto from the header", () => {
    expect(resolveLocale("auto", "ja")).toBe("ja");
    expect(resolveLocale("auto", null)).toBe("en");
  });
});

describe("createTranslator", () => {
  it("looks up the dictionary for each locale", () => {
    expect(createTranslator("en")("nav.datasets")).toBe("Datasets");
    expect(createTranslator("ja")("nav.datasets")).toBe("データセット");
  });

  it("substitutes placeholders", () => {
    const t = createTranslator("ja");
    expect(t("userMenu.accountMenu", { username: "alice" })).toBe("alice のアカウントメニュー");
  });

  it("leaves placeholders that aren't in params untouched", () => {
    const t = createTranslator("en");
    expect(t("userMenu.accountMenu", {})).toBe("Account menu for {username}");
  });
});

describe("dictionary consistency", () => {
  function flatten(node: unknown, prefix: string, out: Map<string, string>) {
    if (typeof node === "string") {
      out.set(prefix, node);
      return;
    }
    if (typeof node === "object" && node !== null) {
      for (const [k, v] of Object.entries(node)) {
        flatten(v, prefix ? `${prefix}.${k}` : k, out);
      }
    }
  }

  it("en and ja have matching key sets", () => {
    const en = new Map<string, string>();
    const ja = new Map<string, string>();
    flatten(dictionaries.en, "", en);
    flatten(dictionaries.ja, "", ja);
    expect([...ja.keys()].sort()).toEqual([...en.keys()].sort());
  });

  it("placeholder sets match between en and ja", () => {
    const en = new Map<string, string>();
    const ja = new Map<string, string>();
    flatten(dictionaries.en, "", en);
    flatten(dictionaries.ja, "", ja);
    const params = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
    for (const [key, enValue] of en) {
      const jaValue = ja.get(key);
      expect(jaValue, key).toBeDefined();
      expect(params(jaValue ?? ""), key).toEqual(params(enValue));
    }
  });
});
