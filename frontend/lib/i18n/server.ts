import { cookies, headers } from "next/headers";
import { cache } from "react";
import {
  createTranslator,
  LOCALE_COOKIE,
  type Locale,
  type LocalePreference,
  parseLocalePreference,
  resolveLocale,
  type Translator,
} from "@/lib/i18n";

/**
 * Server-only: returns the user preference (tf_locale cookie). Falls back
 * to "auto" (follow the browser setting) if the cookie is missing or invalid.
 */
export const getLocalePreference = cache(async (): Promise<LocalePreference> => {
  const store = await cookies();
  return parseLocalePreference(store.get(LOCALE_COOKIE)?.value);
});

/**
 * Server-only: the display locale for the request. When the user
 * preference is "auto", resolves it from Accept-Language. Cached within
 * the request.
 */
export const getLocale = cache(async (): Promise<Locale> => {
  const pref = await getLocalePreference();
  if (pref !== "auto") return pref;
  const h = await headers();
  return resolveLocale("auto", h.get("accept-language"));
});

/** Server-only: returns a translator for the current locale. */
export async function getT(): Promise<Translator> {
  return createTranslator(await getLocale());
}
