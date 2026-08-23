import { ChevronLeft, ChevronRight } from "lucide-react";
import Link from "next/link";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";

// Server Component: every current call site (RepoListPage, the /orgs
// listing, NamespaceOverview) renders this from a Server Component tree, so
// it resolves its own translator via getT() rather than taking labels as
// props. If a Client Component ever needs paging controls, add a sibling
// client variant instead of forcing this one to take a translator prop.
export async function Pagination({
  offset,
  limit,
  total,
  buildHref,
}: {
  offset: number;
  limit: number;
  total: number;
  buildHref: (offset: number) => string;
}) {
  if (total <= limit) return null;
  const t = await getT();
  const hasPrev = offset > 0;
  const hasNext = offset + limit < total;
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(offset + limit, total);

  return (
    <div className="mt-6 flex items-center justify-between text-sm text-fg-subtle">
      <span className="tabular-nums">
        {t("ui.pagination.range", { from, to, total: formatNumber(total) })}
      </span>
      <div className="flex gap-2">
        <Link
          href={hasPrev ? buildHref(Math.max(0, offset - limit)) : "#"}
          aria-disabled={!hasPrev}
          // `pointer-events-none` only blocks the mouse — a real <a> stays
          // focusable and Enter still navigates it, so a keyboard user could
          // activate a link that looks disabled. `tabIndex={-1}` removes it
          // from the tab order to match; the mouse-only `pointer-events-none`
          // still needs it too, since tabIndex doesn't stop a click.
          tabIndex={hasPrev ? undefined : -1}
          className={`flex items-center gap-1 rounded-md border border-border px-2.5 py-1.5 ${
            hasPrev ? "hover:bg-bg-hover" : "pointer-events-none opacity-40"
          }`}
        >
          <ChevronLeft size={14} />
          {t("ui.pagination.prev")}
        </Link>
        <Link
          href={hasNext ? buildHref(offset + limit) : "#"}
          aria-disabled={!hasNext}
          tabIndex={hasNext ? undefined : -1}
          className={`flex items-center gap-1 rounded-md border border-border px-2.5 py-1.5 ${
            hasNext ? "hover:bg-bg-hover" : "pointer-events-none opacity-40"
          }`}
        >
          {t("ui.pagination.next")}
          <ChevronRight size={14} />
        </Link>
      </div>
    </div>
  );
}
