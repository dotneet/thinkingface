"use client";

import Link from "next/link";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { useT } from "@/lib/i18n/client";

/**
 * What a settings screen shows when its first read came back 401: a signed-out
 * visitor, not a broken backend, so it says so and points at the fix.
 *
 * Eight screens rendered this by hand with three dictionary keys each, and the
 * wording differences between them were incidental rather than meaningful. One
 * of the eight also forgot to percent-encode the return path, so a `next=`
 * carrying a query string or a slash-heavy path was truncated at the first
 * `&`; encoding here is not optional.
 */
export function LoginRequiredState({
  /** Path to return to after signing in, unencoded. */
  next,
}: {
  next: string;
}) {
  const t = useT();
  return (
    <ErrorState
      title={t("settings.loginRequiredTitle")}
      message={t("settings.loginRequiredMessage")}
      action={
        <Link
          href={`/login?next=${encodeURIComponent(next)}`}
          className={buttonClass({ variant: "primary" })}
        >
          {t("settings.login")}
        </Link>
      }
    />
  );
}
