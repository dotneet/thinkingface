import { Plus } from "lucide-react";
import Link from "next/link";
import { Suspense } from "react";
import { MobileNav } from "@/components/mobile-nav";
import { SearchBox } from "@/components/search-box";
import { SiteNav } from "@/components/site-nav";
import { ThemeToggle } from "@/components/theme-toggle";
import { buttonClass } from "@/components/ui/button";
import { UserMenu } from "@/components/user-menu";
import { getT } from "@/lib/i18n/server";
import { authHeaders } from "@/lib/server-auth";
import { getCurrentUser } from "@/lib/session";
import { listMyTransfers } from "@/lib/transfers";

export async function SiteHeader() {
  const [result, t] = await Promise.all([getCurrentUser(), getT()]);
  const user = result.ok ? result.data.user : null;
  // Only queried for signed-in visitors: the endpoint requires auth, and
  // this header renders on every page, so skip it entirely for the common
  // anonymous case. Any failure degrades silently to no badge.
  //
  // `incoming` is scoped server-side to namespaces this user actually writes
  // and capped per side (docs/dev/api-contract.md, "Transfer (for the Web
  // UI)"), so the badge means "waiting for you" — not "pending somewhere on
  // this server" — and one page render cannot pull an unbounded list.
  const pendingTransfersCount = user
    ? await listMyTransfers({ headers: await authHeaders() }).then((r) =>
        r.ok ? r.data.incoming.length : undefined,
      )
    : undefined;

  return (
    <header className="sticky top-0 z-40 border-b border-border bg-bg-raised/95 backdrop-blur">
      <div className="relative mx-auto flex h-14 max-w-7xl items-center gap-4 px-4">
        {/* MobileNav renders its own SearchBox instance, and both that box
            and the one below read the current search term via
            useSearchParams(). Wrapping each call site in its own Suspense
            keeps that hook from opting routes that aren't already
            force-dynamic (settings, styleguide) into full client-side
            rendering (see components/search-box.tsx). */}
        <Suspense>
          <MobileNav />
        </Suspense>
        <Link
          href="/"
          className="flex shrink-0 items-center gap-2 font-semibold tracking-tight"
          // The wordmark below is `hidden` (not just visually collapsed)
          // under `lg`, which removes it from the accessibility tree too —
          // without this, every page below 1024px has a home link with no
          // accessible name at all. The brand name is never translated
          // (DESIGN.md §7), so it's safe to hardcode here the same way
          // app/page-metadata.ts's SITE_NAME does.
          aria-label="Thinking Face"
        >
          <span className="text-xl leading-none" aria-hidden>
            🤔
          </span>
          {/* Hidden until `lg` (not `sm`): between `sm` and `lg` the desktop
              nav (`SiteNav`, `md:flex`) is already showing alongside the
              header search box, and at exactly 768px giving the wordmark its
              ~95px back is what keeps the search input's placeholder from
              being clipped to a couple of characters (measured: search
              shrinks to 82px wide there without this). */}
          <span className="hidden lg:inline" aria-hidden>
            Thinking Face
          </span>
        </Link>

        <SiteNav />

        <div className="ml-auto flex flex-1 items-center justify-end gap-3">
          <div className="hidden max-w-sm flex-1 sm:block">
            <Suspense>
              <SearchBox />
            </Suspense>
          </div>
          <Link
            href="/new"
            className={buttonClass({ variant: "secondary", className: "hidden sm:flex" })}
          >
            <Plus size={15} />
            {t("header.new")}
          </Link>
          <ThemeToggle />
          <UserMenu user={user} pendingTransfersCount={pendingTransfersCount} />
        </div>
      </div>
    </header>
  );
}
