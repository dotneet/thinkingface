"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { ThemeScript } from "@/app/theme-script";
import { Button, buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import {
  createTranslator,
  defaultLocale,
  isLocale,
  LOCALE_COOKIE,
  type Locale,
  matchAcceptLanguage,
} from "@/lib/i18n";
// global-error.tsx replaces the entire root layout (including its
// `<html>`/`<body>` and its `import "./globals.css"`), so it has to bring
// its own copy of the stylesheet — see the Next.js docs for this file.
import "./globals.css";

/**
 * Last-resort error boundary: only rendered when `app/layout.tsx` itself
 * throws (see [S14]). Ordinary page/rendering exceptions are caught by
 * `app/error.tsx`, which keeps the header and stays inside the normal
 * layout — this one can't, since the thing that broke is the layout.
 *
 * Because it replaces the root layout, none of that layout's context is
 * available: no `I18nProvider` (so no `useT()`), no `Providers`
 * (react-query), no header. Locale is resolved by hand from the same
 * `tf_locale` cookie / Accept-Language fallback the server uses
 * (`lib/i18n`'s `createTranslator`/`matchAcceptLanguage` are plain
 * functions, not tied to the provider), and the page renders its own
 * `<html>`/`<body>` plus `ThemeScript` so dark/light still resolves before
 * paint.
 */
function detectLocale(): Locale {
  if (typeof document !== "undefined") {
    const match = document.cookie.match(new RegExp(`(?:^|; )${LOCALE_COOKIE}=([^;]+)`));
    if (match?.[1]) {
      const value = decodeURIComponent(match[1]);
      if (isLocale(value)) return value;
    }
  }
  if (typeof navigator !== "undefined") {
    return matchAcceptLanguage(navigator.language);
  }
  return defaultLocale;
}

export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  // Resolved once per mount, not on every render — this page has no reason
  // to react to a cookie change after it's already up.
  const [locale] = useState(detectLocale);
  const t = createTranslator(locale);

  useEffect(() => {
    console.error(error);
  }, [error]);

  return (
    <html lang={locale} suppressHydrationWarning>
      <head>
        <ThemeScript />
      </head>
      <body className="min-h-screen bg-bg font-sans text-fg antialiased">
        <main className="mx-auto max-w-2xl px-4 py-24">
          <ErrorState
            title={t("ui.unexpectedError.title")}
            message={t("ui.unexpectedError.description")}
            action={
              <div className="flex flex-wrap items-center justify-center gap-2">
                <Button variant="primary" onClick={reset}>
                  {t("ui.unexpectedError.retry")}
                </Button>
                <Link href="/" className={buttonClass()}>
                  {t("ui.unexpectedError.goHome")}
                </Link>
              </div>
            }
          />
        </main>
      </body>
    </html>
  );
}
