import { type Dict, en } from "@/lib/i18n/dictionaries/en";
import { ja } from "@/lib/i18n/dictionaries/ja";

export const locales = ["en", "ja"] as const;
export type Locale = (typeof locales)[number];

/** "auto" = follow the browser's Accept-Language (the default user setting). */
export type LocalePreference = Locale | "auto";

export const defaultLocale: Locale = "en";
export const LOCALE_COOKIE = "tf_locale";

export function isLocale(value: string): value is Locale {
  return (locales as readonly string[]).includes(value);
}

export function parseLocalePreference(value: string | null | undefined): LocalePreference {
  if (value && isLocale(value)) return value;
  return "auto";
}

/**
 * Picks a supported locale from the Accept-Language header. Takes the
 * first matching language (primary subtag) in descending q-value order,
 * falling back to defaultLocale if nothing matches.
 */
export function matchAcceptLanguage(header: string | null | undefined): Locale {
  if (!header) return defaultLocale;
  const ranges = header
    .split(",")
    .map((part) => {
      const [tag = "", ...params] = part.trim().split(";");
      let q = 1;
      for (const p of params) {
        const m = p.trim().match(/^q=([0-9.]+)$/i);
        if (m?.[1]) q = Number.parseFloat(m[1]);
      }
      return { tag: tag.trim().toLowerCase(), q: Number.isFinite(q) ? q : 0 };
    })
    .filter((r) => r.tag && r.q > 0)
    .sort((a, b) => b.q - a.q);
  for (const { tag } of ranges) {
    const primary = tag.split("-")[0] ?? "";
    if (isLocale(primary)) return primary;
  }
  return defaultLocale;
}

export function resolveLocale(
  pref: LocalePreference,
  acceptLanguage: string | null | undefined,
): Locale {
  if (pref !== "auto") return pref;
  return matchAcceptLanguage(acceptLanguage);
}

export const dictionaries: Record<Locale, Dict> = { en, ja };

type DeepKey<T> = {
  [K in keyof T & string]: T[K] extends string ? K : `${K}.${DeepKey<T[K]>}`;
}[keyof T & string];

/** Dot-separated dictionary key (e.g. `"nav.datasets"`). */
export type MessageKey = DeepKey<Dict>;

export type TranslateParams = Record<string, string | number>;
export type Translator = (key: MessageKey, params?: TranslateParams) => string;

function lookup(dict: Dict, key: string): string | undefined {
  let node: unknown = dict;
  for (const part of key.split(".")) {
    if (typeof node !== "object" || node === null) return undefined;
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "string" ? node : undefined;
}

/**
 * Returns a translator that looks up the given locale's dictionary. Key
 * coverage is guaranteed by the type system, so the runtime fallback is
 * just a safety net: if a key is ever missing it degrades to en, then to
 * the key string itself, without throwing. Substitutes `{name}`-style
 * placeholders with params.
 */
export function createTranslator(locale: Locale): Translator {
  const dict = dictionaries[locale];
  return (key, params) => {
    const template = lookup(dict, key) ?? lookup(dictionaries[defaultLocale], key) ?? key;
    if (!params) return template;
    return template.replace(/\{(\w+)\}/g, (match, name: string) =>
      name in params ? String(params[name]) : match,
    );
  };
}
