"use client";

import { Search, X } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/field";
import { useT } from "@/lib/i18n/client";

export function RepoListFilters({ basePath }: { basePath: string }) {
  const t = useT();
  const router = useRouter();
  const searchParams = useSearchParams();
  // `search` (tsquery-based full text) is the current param; `q` (substring
  // match) is kept alive only so a bookmarked pre-facet-search URL still
  // shows its term in the box.
  const paramSearch = searchParams.get("search") ?? searchParams.get("q") ?? "";
  const [search, setSearch] = useState(paramSearch);
  // The header SearchBox navigates within the same route tree (router.push),
  // so this component never unmounts and the useState initializer above only
  // runs once. Without this effect, a search triggered from the header
  // leaves this field showing stale (empty) text even though the results
  // below are already filtered by the new `search`/`q` param.
  useEffect(() => {
    setSearch(paramSearch);
  }, [paramSearch]);

  function update(patch: Record<string, string | null>) {
    const params = new URLSearchParams(searchParams.toString());
    for (const [key, value] of Object.entries(patch)) {
      if (value === null || value === "") params.delete(key);
      else params.set(key, value);
    }
    params.delete("offset");
    router.push(params.toString() ? `${basePath}?${params.toString()}` : basePath);
  }

  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          update({ search, q: null });
        }}
        className="relative flex-1"
      >
        <Search
          size={15}
          className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-fg-subtle"
        />
        <Input
          value={search}
          // `type="text"`, not `type="search"`: the browser's own clear "×"
          // empties the box without submitting, so the results kept matching a
          // term that was no longer on screen. The button below both clears and
          // applies. `enterKeyHint` restores the "search" key that the
          // on-screen keyboard would have labelled from the input type.
          // A *new* search box should use `SearchInput` (ui/search-input.tsx),
          // which owns that behaviour; this one keeps its own clear control
          // because it sits in a form that also carries the facet state.
          type="text"
          enterKeyHint="search"
          placeholder={t("repoList.searchPlaceholder")}
          aria-label={t("repoList.searchPlaceholder")}
          onChange={(e) => setSearch(e.target.value)}
          className="pl-8 pr-9 text-sm"
        />
        {search !== "" && (
          // Absolutely positioned, so appearing costs no layout: the input
          // reserves `pr-9` whether or not there is anything to clear.
          <Button
            variant="ghost"
            size="sm"
            aria-label={t("repoList.clearSearch")}
            title={t("repoList.clearSearch")}
            onClick={() => {
              setSearch("");
              update({ search: null, q: null });
            }}
            className="absolute right-1.5 top-1/2 -translate-y-1/2"
          >
            <X size={14} />
          </Button>
        )}
      </form>

      <Select
        value={searchParams.get("sort") ?? "updated"}
        onChange={(e) => update({ sort: e.target.value })}
        className="w-auto text-sm"
      >
        <option value="updated">{t("repoList.sort.updated")}</option>
        <option value="created">{t("repoList.sort.created")}</option>
        <option value="downloads">{t("repoList.sort.downloads")}</option>
        <option value="name">{t("repoList.sort.name")}</option>
      </Select>
    </div>
  );
}
