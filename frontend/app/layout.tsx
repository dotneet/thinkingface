import type { Metadata } from "next";
import { Figtree, IBM_Plex_Mono } from "next/font/google";
import { pageTitle } from "@/app/page-metadata";
import { Providers } from "@/app/providers";
import { ThemeScript } from "@/app/theme-script";
import { SiteHeader } from "@/components/site-header";
import { I18nProvider } from "@/lib/i18n/client";
import { getLocale, getT } from "@/lib/i18n/server";
import "./globals.css";

const bodyFont = Figtree({
  subsets: ["latin"],
  variable: "--font-body",
  display: "swap",
});

const monoFont = IBM_Plex_Mono({
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  variable: "--font-mono-code",
  display: "swap",
});

export async function generateMetadata(): Promise<Metadata> {
  const t = await getT();
  return {
    title: pageTitle(),
    description: t("meta.description"),
  };
}

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const [locale, t] = await Promise.all([getLocale(), getT()]);
  return (
    <html lang={locale} suppressHydrationWarning>
      <head>
        <ThemeScript />
      </head>
      <body
        className={`${bodyFont.variable} ${monoFont.variable} min-h-screen font-sans antialiased`}
      >
        {/* First focusable element in the document: a keyboard user would
            otherwise have to tab through the entire header (logo, nav,
            search, new-repo, theme toggle, account menu) before reaching the
            page content on every single navigation. Visually hidden until
            focused (`sr-only focus:not-sr-only`); positioned fixed so it
            doesn't shift layout when it appears. */}
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded-md focus:bg-accent focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-accent-fg"
        >
          {t("ui.skipToContent")}
        </a>
        <I18nProvider locale={locale}>
          <Providers>
            <SiteHeader />
            <main id="main-content" className="mx-auto max-w-7xl px-4 pb-24 pt-6">
              {children}
            </main>
          </Providers>
        </I18nProvider>
      </body>
    </html>
  );
}
