import { AlertTriangle } from "lucide-react";

export function ErrorState({
  title,
  message,
  hint,
  action,
}: {
  /**
   * No default on purpose: this is a user-visible string and DESIGN.md §7
   * requires every one of those to come from the i18n dictionary. Server and
   * Client Components both render this primitive, and it cannot call
   * `useT()`/`getT()` itself (one is client-only, the other async-server-only),
   * so callers pass `t("ui.errorStateTitle")` explicitly — `title` being
   * required means a missing translation fails `bun run typecheck` instead of
   * silently falling back to English.
   */
  title: string;
  message: string;
  hint?: string;
  action?: React.ReactNode;
}) {
  return (
    // role="alert" so a failure that replaces content after first paint (a
    // client-side query, a re-render after a failed refetch) is announced
    // rather than silently swapping in.
    <div
      role="alert"
      className="flex flex-col items-center justify-center rounded-lg border border-border bg-bg-sunken px-6 py-16 text-center"
    >
      <AlertTriangle size={26} className="mb-3 text-negative" strokeWidth={1.5} />
      <p className="text-sm font-medium text-fg">{title}</p>
      <p className="mt-1 max-w-md text-sm text-fg-subtle">{message}</p>
      {hint && <p className="mt-3 max-w-md text-xs font-medium text-fg-subtle">{hint}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
