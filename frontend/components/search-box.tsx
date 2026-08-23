"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { SearchInput } from "@/components/ui/search-input";
import { useT } from "@/lib/i18n/client";

type Scope = "models" | "datasets" | "experiments";

/**
 * Which listing this box searches, and where it navigates to.
 *
 * Datasets is also the fallback for every route this box doesn't recognize
 * (home, orgs, settings, a repo page, …) — not "search everything": the box
 * only ever queries one listing endpoint, so the placeholder must say which
 * one, and silently defaulting to datasets keeps that promise even off the
 * three listing routes themselves.
 */
function resolveScope(pathname: string): { scope: Scope; target: string } {
  if (pathname.startsWith("/models")) return { scope: "models", target: "/models" };
  if (pathname.startsWith("/experiments")) return { scope: "experiments", target: "/experiments" };
  return { scope: "datasets", target: "/datasets" };
}

export function SearchBox() {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const t = useT();
  const { scope, target } = resolveScope(pathname);
  // Only mirrors the URL on the listing this box actually drives. Elsewhere
  // a `search=` in the URL belongs to some other control — /orgs has its own
  // directory search, and this box targets /datasets from there — so showing
  // its term here would put a value in the field that the field cannot
  // change, and clearing it would navigate away from the page the term came
  // from. `q` as well as `search`, since a bookmarked pre-facet-search URL
  // still carries the legacy spelling.
  const onTargetListing = pathname === target;
  const paramSearch = onTargetListing
    ? (searchParams.get("search") ?? searchParams.get("q") ?? "")
    : "";

  const placeholder =
    scope === "models"
      ? t("header.searchModels")
      : scope === "experiments"
        ? t("header.searchExperiments")
        : t("header.searchDatasets");

  function go(q: string) {
    if (onTargetListing) {
      // Already on the listing this scope targets: keep every other active
      // param (tags, license, sort, …) and only swap the search term, the
      // same contract RepoListFilters' `update()` follows. Jumping scopes
      // (e.g. from /orgs) starts a fresh query instead, since a /models
      // facet has no meaning on /datasets.
      const params = new URLSearchParams(searchParams.toString());
      if (q) params.set("search", q);
      else params.delete("search");
      params.delete("q"); // legacy alias this box used to emit
      params.delete("offset");
      const qs = params.toString();
      router.push(qs ? `${target}?${qs}` : target);
      return;
    }
    router.push(q ? `${target}?search=${encodeURIComponent(q)}` : target);
  }

  return <SearchInput activeValue={paramSearch} onSearch={go} placeholder={placeholder} />;
}
