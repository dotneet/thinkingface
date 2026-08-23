"use client";

import { useEffect, useState } from "react";
import { formatDate, formatDateTime, formatRelativeTime } from "@/lib/format";
import { useLocale } from "@/lib/i18n/client";

type TimeStyle = "date" | "dateTime" | "relative";

/**
 * A timestamp rendered for the reader's own time zone, without the hydration
 * mismatch that costs.
 *
 * The server renders in UTC — the one zone both sides can agree on before the
 * page is interactive — and so does the first client render, so the markup
 * matches. A mount effect then re-renders in whatever zone the browser is in,
 * which is what the reader actually wants to read. `suppressHydrationWarning`
 * covers the relative style, where the two sides disagree about "now" rather
 * than about the zone.
 *
 * Formatting a date inline (`formatDateTime(iso, locale)` straight into JSX) is
 * what this component exists to replace: it is server-rendered in the
 * container's zone and hydrated in the browser's, which React reports as
 * error #418 on every page that shows a timestamp.
 */
export function TimeText({
  iso,
  style = "date",
  className,
  title,
}: {
  iso: string | null | undefined;
  style?: TimeStyle;
  className?: string;
  /** Overrides the tooltip, which is otherwise the full date and time. */
  title?: string;
}) {
  const locale = useLocale();
  // False until the mount effect has run, i.e. for the server render and the
  // hydrating render — the two that have to produce identical markup.
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);

  const timeZone = mounted ? undefined : "UTC";
  const text =
    style === "relative"
      ? formatRelativeTime(iso, locale)
      : style === "dateTime"
        ? formatDateTime(iso, locale, timeZone)
        : formatDate(iso, locale, timeZone);

  if (!iso) return <span className={className}>{text}</span>;

  return (
    <time
      dateTime={iso}
      className={className}
      title={title ?? formatDateTime(iso, locale, timeZone)}
      suppressHydrationWarning
    >
      {text}
    </time>
  );
}

/**
 * The same two-pass formatting as {@link TimeText}, for the callers that need
 * the string itself — a date interpolated into a translated sentence, say.
 * Client Components only.
 */
export function useFormattedTime(
  iso: string | null | undefined,
  style: Exclude<TimeStyle, "relative"> = "dateTime",
): string {
  const locale = useLocale();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  const timeZone = mounted ? undefined : "UTC";
  return style === "dateTime"
    ? formatDateTime(iso, locale, timeZone)
    : formatDate(iso, locale, timeZone);
}
