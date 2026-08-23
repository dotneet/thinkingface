"use client";

import { useEffect, useState } from "react";
import { useT } from "@/lib/i18n/client";

/**
 * Live preview of the URLs a namespace name will claim: sign-up
 * (docs/dev/namespace-design.md §5.1) and organisation creation (§5.2) share it
 * (§8.2). `window.location.origin` is only known in the browser, so the
 * origin renders empty on first paint and fills in after mount rather than
 * risking a server/client markup mismatch.
 *
 * Git commands and URLs are not translated (DESIGN.md §7) — only the two
 * row labels are.
 */
export function NamespaceUrlPreview({
  name,
  placeholder,
}: {
  /** The name field's current (untrimmed) value. */
  name: string;
  /** Shown instead of `name` while the field is empty. */
  placeholder: string;
}) {
  const t = useT();
  const [origin, setOrigin] = useState("");

  useEffect(() => {
    setOrigin(window.location.origin);
  }, []);

  const value = name.trim() || placeholder;

  return (
    <div className="flex flex-col gap-1 rounded-md border border-border bg-bg-sunken px-3 py-2 font-mono text-xs">
      <div className="flex gap-2">
        <span className="w-24 shrink-0 text-fg-subtle">{t("auth.preview.profileLabel")}</span>
        <span className="min-w-0 truncate text-fg-muted">
          {origin}/{value}
        </span>
      </div>
      <div className="flex gap-2">
        <span className="w-24 shrink-0 text-fg-subtle">{t("auth.preview.repositoriesLabel")}</span>
        <span className="min-w-0 truncate text-fg-muted">{value}/&lt;repo-name&gt;</span>
      </div>
      <div className="min-w-0 truncate text-fg-subtle">
        git clone {origin}/{value}/&lt;repo-name&gt;
      </div>
    </div>
  );
}
