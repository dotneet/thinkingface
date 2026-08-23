"use client";

import { RotateCw } from "lucide-react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useEffect } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { useT } from "@/lib/i18n/client";
import { repoBase } from "@/lib/paths";
import type { RepoKind } from "@/types/api";

/**
 * Shared body for the per-repository `error.tsx` boundaries
 * (`app/datasets/[ns]/[name]/error.tsx`, `app/models/[ns]/[name]/error.tsx`
 * — see [S14]). Scoped one level below the root boundary (`app/error.tsx`)
 * so a rendering exception anywhere under a repository (tree, blob,
 * commits, edit, viewer, settings) offers a way back to that specific
 * repository instead of only the homepage.
 *
 * `error.tsx` doesn't receive route params as props, so this reads them
 * from the URL via `useParams()` — safe here because Next.js requires
 * `error.tsx` to already be a Client Component.
 */
export function RepoErrorBoundary({
  kind,
  error,
  reset,
}: {
  kind: RepoKind;
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const t = useT();
  const params = useParams<{ ns?: string; name?: string }>();

  useEffect(() => {
    console.error(error);
  }, [error]);

  // Falls back to the kind's listing if the params are somehow missing —
  // still a sensible "go back" destination, just one level less specific.
  const backHref = params.ns && params.name ? repoBase(kind, params.ns, params.name) : `/${kind}s`;

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
            <Link href={backHref} className={buttonClass()}>
              {t("ui.unexpectedError.backToRepo")}
            </Link>
          </div>
        }
      />
    </div>
  );
}
