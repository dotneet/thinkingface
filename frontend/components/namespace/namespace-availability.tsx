"use client";

import { useEffect, useState } from "react";
import { Spinner } from "@/components/ui/spinner";
import { isNotFound } from "@/lib/api";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { getNamespace } from "@/lib/namespace";
import { type NamespaceNameError, validateNamespaceName } from "@/lib/validation";

const DEBOUNCE_MS = 400;

/**
 * Live availability indicator for a namespace name being claimed by sign-up
 * or organisation creation (docs/dev/namespace-design.md §5.1, §5.2, §8.2).
 *
 * A grammar or reserved-name violation is judged locally and instantly by
 * `validateNamespaceName` — `errorKeys` lets each caller translate that with
 * its own domain's wording ("username" vs. "organization ID"), reusing the
 * same map it already applies at submit time. A syntactically valid name is
 * checked against the server (`GET /api/v1/namespaces/{name}`) once typing
 * pauses. A failed lookup renders nothing: the server is the final judge at
 * submit time regardless, so there is nothing useful to show here.
 */
export function NamespaceAvailability({
  name,
  errorKeys,
}: {
  name: string;
  errorKeys: Record<NamespaceNameError, MessageKey>;
}) {
  const t = useT();
  const [status, setStatus] = useState<"idle" | "checking" | "available" | "taken">("idle");

  const trimmed = name.trim();
  const localError = trimmed ? validateNamespaceName(trimmed) : null;

  useEffect(() => {
    if (!trimmed || localError) {
      setStatus("idle");
      return;
    }
    setStatus("checking");
    let cancelled = false;
    const timer = setTimeout(async () => {
      const result = await getNamespace(trimmed);
      if (cancelled) return;
      if (result.ok) {
        setStatus("taken");
      } else if (isNotFound(result)) {
        setStatus("available");
      } else {
        // Network/server failure: say nothing, don't block submission.
        setStatus("idle");
      }
    }, DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [trimmed, localError]);

  let message: React.ReactNode = null;
  if (!trimmed) {
    message = null;
  } else if (localError) {
    message = <span className="text-xs text-negative">{t(errorKeys[localError])}</span>;
  } else if (status === "checking") {
    message = (
      <span className="flex items-center gap-1.5 text-xs font-medium text-fg-subtle">
        {/* No `label`: the live region around it already says what is being
            checked, and a second announcement mixed the primitive's old
            hardcoded English into the Japanese one. */}
        <Spinner size={12} />
        {t("auth.availability.checking")}
      </span>
    );
  } else if (status === "available") {
    message = (
      <span className="text-xs text-positive">
        {t("auth.availability.available", { name: trimmed })}
      </span>
    );
  } else if (status === "taken") {
    message = (
      <span className="text-xs text-negative">
        {t("auth.availability.taken", { name: trimmed })}
      </span>
    );
  }

  // Always rendered, at the height of one `text-xs` line (12px × the 1.5
  // leading globals.css sets), even while empty. This row sits directly above
  // Email / Password / Sign up, and letting it appear 400ms after typing
  // stopped pushed all three down mid-reach — DESIGN.md §8.3, "a control that
  // appears on a condition reserves its space up front". `min-h` rather than a
  // fixed height so a name long enough to wrap is not clipped.
  //
  // The live region is the container, not the message: `role="status"` on an
  // element that mounts at the same moment its text appears is unreliably
  // announced, whereas a region that is already in the tree announces every
  // change to it.
  return (
    <div role="status" className="min-h-[1.125rem]">
      {message}
    </div>
  );
}
