"use client";

import { createContext, useContext, useMemo } from "react";
import {
  createTranslator,
  defaultLocale,
  LOCALE_COOKIE,
  type Locale,
  type LocalePreference,
  type Translator,
} from "@/lib/i18n";

const LocaleContext = createContext<Locale>(defaultLocale);

/** Distributes the server-resolved locale from the root layout. */
export function I18nProvider({ locale, children }: { locale: Locale; children: React.ReactNode }) {
  return <LocaleContext.Provider value={locale}>{children}</LocaleContext.Provider>;
}

export function useLocale(): Locale {
  return useContext(LocaleContext);
}

/** Translator for use in Client Components. */
export function useT(): Translator {
  const locale = useLocale();
  return useMemo(() => createTranslator(locale), [locale]);
}

/**
 * Rewrites the user-preference cookie. "auto" (follow the browser setting)
 * is represented by deleting the cookie. Requires router.refresh() to take
 * effect.
 */
export function setLocalePreference(pref: LocalePreference) {
  if (pref === "auto") {
    // biome-ignore lint/suspicious/noDocumentCookie: Cookie Store API is not supported in Safari
    document.cookie = `${LOCALE_COOKIE}=; path=/; max-age=0; samesite=lax`;
  } else {
    const oneYear = 60 * 60 * 24 * 365;
    // biome-ignore lint/suspicious/noDocumentCookie: Cookie Store API is not supported in Safari
    document.cookie = `${LOCALE_COOKIE}=${pref}; path=/; max-age=${oneYear}; samesite=lax`;
  }
}
