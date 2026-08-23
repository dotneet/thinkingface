"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { SearchInput } from "@/components/ui/search-input";
import { useT } from "@/lib/i18n/client";

/** Search box for the /orgs directory; submits as `?search=` and resets paging. */
export function OrgSearch() {
  const t = useT();
  const router = useRouter();
  const searchParams = useSearchParams();
  const paramSearch = searchParams.get("search") ?? "";

  function go(q: string) {
    router.push(q ? `/orgs?search=${encodeURIComponent(q)}` : "/orgs");
  }

  return (
    <SearchInput
      activeValue={paramSearch}
      onSearch={go}
      placeholder={t("org.directory.searchPlaceholder")}
      label={t("org.directory.search")}
      formClassName="flex-1"
      className="py-2"
    />
  );
}
