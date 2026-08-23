"use client";

import { Search } from "lucide-react";
import { useEffect, useState } from "react";
import { Input } from "@/components/ui/field";
import { cn } from "@/lib/cn";

/**
 * Search field whose clear control actually clears the results.
 *
 * A native `type="search"` input renders a "×" that empties the value and
 * fires `change` — but never submits the form. Every hand-rolled search box
 * in this app got that wrong at least once: the field read as cleared while
 * the URL and the result list stayed on the old term. Three separate PRs
 * fixed the same bug in three different places (header search, org
 * directory, experiments listing), each with its own workaround — one added
 * an onChange short-circuit, another downgraded to `type="text"` to hide the
 * × entirely.
 *
 * So the semantics live here instead of in every caller:
 *
 * - Enter submits the trimmed value.
 * - Emptying the field while a search is *active* fires `onSearch("")`
 *   immediately — that covers the native ×, ⌘⌫, and select-all-delete alike.
 * - Ordinary typing still waits for submit, so a listing is not re-queried
 *   on every keystroke.
 * - `activeValue` is mirrored into the field whenever it changes. These
 *   boxes never unmount across a client-side navigation, so without that a
 *   browser back — or a `FilterChip` clearing the term elsewhere on the page
 *   — would leave a stale term on screen.
 *
 * `activeValue` is the term currently in effect (usually from the URL), not
 * an initial value: pass "" when this box does not drive the listing being
 * shown, and the field will read as empty, which is the honest state — see
 * SearchBox's scope handling.
 *
 * For a box that filters client-side as you type, use {@link FilterInput}.
 */
export function SearchInput({
  activeValue,
  onSearch,
  placeholder,
  label,
  className,
  formClassName,
}: {
  activeValue: string;
  onSearch: (query: string) => void;
  placeholder: string;
  /** Accessible name; defaults to `placeholder`. */
  label?: string;
  /** Extra classes for the `<input>`. */
  className?: string;
  /** Extra classes for the wrapping `<form>`. */
  formClassName?: string;
}) {
  const [value, setValue] = useState(activeValue);

  useEffect(() => {
    setValue(activeValue);
  }, [activeValue]);

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSearch(value.trim());
      }}
      className={cn("relative", formClassName)}
    >
      <Search
        size={15}
        className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
      />
      <Input
        value={value}
        onChange={(e) => {
          const next = e.target.value;
          setValue(next);
          // Only an *active* search short-circuits to a query; clearing an
          // already-empty listing would push a redundant navigation.
          if (next.trim() === "" && activeValue) onSearch("");
        }}
        type="search"
        placeholder={placeholder}
        aria-label={label ?? placeholder}
        className={cn("py-1.5 pl-8 pr-3 text-sm", className)}
      />
    </form>
  );
}

/**
 * Search-shaped field that filters something already on screen.
 *
 * No form and no submit: every keystroke — including the native × — reports
 * through `onChange`, so there is no state the field can get out of sync
 * with. Use it for in-page filtering (a tensor table, a column list); use
 * {@link SearchInput} when the term has to travel to the server or the URL.
 */
export function FilterInput({
  value,
  onChange,
  placeholder,
  label,
  className,
  wrapperClassName,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  /** Accessible name; defaults to `placeholder`. */
  label?: string;
  className?: string;
  wrapperClassName?: string;
}) {
  return (
    <div className={cn("relative", wrapperClassName)}>
      <Search
        size={15}
        className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
      />
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        type="search"
        placeholder={placeholder}
        aria-label={label ?? placeholder}
        className={cn("py-1.5 pl-8 pr-3 text-sm", className)}
      />
    </div>
  );
}
