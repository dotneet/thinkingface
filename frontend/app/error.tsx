"use client";

import { RotateCw } from "lucide-react";
import Link from "next/link";
import { useEffect } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { useT } from "@/lib/i18n/client";

/**
 * App Router error boundary for everything under the root layout (see
 * [S14] in todo/security-audit-findings.md). `apiFetch` never throws
 * (CLAUDE.md invariant 3), so this only ever catches a *rendering*
 * exception — malformed Parquet values, a `uplot`/`react-markdown` edge
 * case, or similar — not an API failure, which pages already render as
 * their own `ErrorState`.
 *
 * Must be a Client Component (Next.js requirement for `error.tsx`). It
 * renders inside the root layout, so the header/nav and the `I18nProvider`
 * context `useT()` needs are still present — only the page content is
 * replaced.
 */
export default function RouteError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const t = useT();

  useEffect(() => {
    // Client-side only; nothing here reaches the user (see [S12] — the same
    // "never show backend/internal detail on screen" rule applies here).
    console.error(error);
  }, [error]);

  return (
    <div className="py-16">
      <ErrorState
        title={t("ui.unexpectedError.title")}
        message={t("ui.unexpectedError.description")}
        action={
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Button variant="primary" onClick={reset}>
              <RotateCw size={14} />
              {t("ui.unexpectedError.retry")}
            </Button>
            <Link href="/" className={buttonClass()}>
              {t("ui.unexpectedError.goHome")}
            </Link>
          </div>
        }
      />
    </div>
  );
}
