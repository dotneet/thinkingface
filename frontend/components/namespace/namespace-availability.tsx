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
 * or organisation creation (docs/namespace-design.md §5.1, §5.2, §8.2).
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

  if (!trimmed) return null;

  if (localError) {
    return (
      <p role="status" className="text-xs text-negative">
        {t(errorKeys[localError])}
      </p>
    );
  }

  if (status === "checking") {
    return (
      <p role="status" className="flex items-center gap-1.5 text-xs font-medium text-fg-subtle">
        <Spinner size={12} />
        {t("auth.availability.checking")}
      </p>
    );
  }

  if (status === "available") {
    return (
      <p role="status" className="text-xs text-positive">
        {t("auth.availability.available", { name: trimmed })}
      </p>
    );
  }

  if (status === "taken") {
    return (
      <p role="status" className="text-xs text-negative">
        {t("auth.availability.taken", { name: trimmed })}
      </p>
    );
  }

  return null;
}
