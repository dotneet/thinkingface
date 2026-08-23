import type { Locale } from "@/lib/i18n";

// Display locale -> Intl locale tag. When the caller doesn't pass a locale,
// format as en-US as before (numbers and byte sizes barely differ across locales).
const intlLocales: Record<Locale, string> = { en: "en-US", ja: "ja-JP" };

function intlLocale(locale?: Locale): string {
  return locale ? intlLocales[locale] : "en-US";
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "-";
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / 1024 ** exp;
  const digits = value >= 100 || exp === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits)} ${units[exp]}`;
}

export function formatNumber(n: number): string {
  if (!Number.isFinite(n)) return "-";
  return new Intl.NumberFormat("en-US").format(n);
}

export function formatCompactNumber(n: number): string {
  if (!Number.isFinite(n)) return "-";
  return new Intl.NumberFormat("en-US", { notation: "compact" }).format(n);
}

/**
 * A calendar date is only meaningful in some time zone, and the server's is not
 * the reader's: rendering one on both sides of hydration produces two different
 * strings and React throws a mismatch. Pass a fixed `timeZone` (what `TimeText`
 * does until it has mounted) whenever the output has to be reproducible;
 * leaving it out formats in the runtime's own zone.
 */
export function formatDate(
  iso: string | null | undefined,
  locale?: Locale,
  timeZone?: string,
): string {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  return new Intl.DateTimeFormat(intlLocale(locale), {
    year: "numeric",
    month: "short",
    day: "numeric",
    timeZone,
  }).format(d);
}

/** {@link formatDate} with the clock time, and the same `timeZone` caveat. */
export function formatDateTime(
  iso: string | null | undefined,
  locale?: Locale,
  timeZone?: string,
): string {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  return new Intl.DateTimeFormat(intlLocale(locale), {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    timeZone,
  }).format(d);
}

export function formatRelativeTime(iso: string | null | undefined, locale?: Locale): string {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  const diffMs = d.getTime() - Date.now();
  const diffSec = Math.round(diffMs / 1000);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 60 * 60 * 24 * 365],
    ["month", 60 * 60 * 24 * 30],
    ["week", 60 * 60 * 24 * 7],
    ["day", 60 * 60 * 24],
    ["hour", 60 * 60],
    ["minute", 60],
  ];
  const rtf = new Intl.RelativeTimeFormat(intlLocale(locale), { numeric: "auto" });
  for (const [unit, secInUnit] of units) {
    if (Math.abs(diffSec) >= secInUnit) {
      return rtf.format(Math.round(diffSec / secInUnit), unit);
    }
  }
  return rtf.format(diffSec, "second");
}
