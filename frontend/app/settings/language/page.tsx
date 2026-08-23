import { headers } from "next/headers";
import { LanguageSettings } from "@/components/settings/language-settings";
import { matchAcceptLanguage } from "@/lib/i18n";
import { getLocalePreference, getT } from "@/lib/i18n/server";

export const dynamic = "force-dynamic";

export default async function LanguagePage() {
  const [t, pref, h] = await Promise.all([getT(), getLocalePreference(), headers()]);
  const browserLocale = matchAcceptLanguage(h.get("accept-language"));

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("language.title")}</h1>
        <p className="mt-1 text-sm text-fg-subtle">{t("language.description")}</p>
      </div>
      <LanguageSettings initialPref={pref} browserLocale={browserLocale} />
    </div>
  );
}
