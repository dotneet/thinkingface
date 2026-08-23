"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { SegmentedControl } from "@/components/ui/segmented-control";
import type { Locale, LocalePreference } from "@/lib/i18n";
import { setLocalePreference, useT } from "@/lib/i18n/client";

/**
 * User setting for display language. Defaults to "auto" (follows the browser
 * setting); the choice is saved to the tf_locale cookie and applied everywhere
 * via router.refresh().
 */
export function LanguageSettings({
  initialPref,
  browserLocale,
}: {
  initialPref: LocalePreference;
  /** The language resolved from Accept-Language when the preference is "auto" (for the hint text). */
  browserLocale: Locale;
}) {
  const t = useT();
  const router = useRouter();
  const [pref, setPref] = useState<LocalePreference>(initialPref);

  function handleChange(next: LocalePreference) {
    setPref(next);
    setLocalePreference(next);
    router.refresh();
  }

  return (
    <div className="flex flex-col gap-2">
      <SegmentedControl<LocalePreference>
        value={pref}
        onChange={handleChange}
        label={t("language.groupLabel")}
        className="self-start"
        options={[
          { value: "auto", label: t("language.auto") },
          { value: "en", label: t("language.en") },
          { value: "ja", label: t("language.ja") },
        ]}
      />
      {pref === "auto" && (
        <p className="text-xs font-medium text-fg-subtle">
          {t("language.autoHint", { resolved: t(`language.${browserLocale}`) })}
        </p>
      )}
    </div>
  );
}
