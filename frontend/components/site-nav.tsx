"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { navItems } from "@/components/nav-items";
import { useT } from "@/lib/i18n/client";

// Split out of SiteHeader (a Server Component) so the active section can be
// highlighted: usePathname() needs a Client Component. Mirrors the active
// check already used by MobileNav (components/mobile-nav.tsx).
export function SiteNav() {
  const pathname = usePathname();
  const t = useT();

  return (
    <nav className="hidden items-center gap-1 md:flex">
      {navItems.map((item) => {
        const active = pathname === item.href || pathname.startsWith(`${item.href}/`);
        return (
          <Link
            key={item.href}
            href={item.href}
            aria-current={active ? "page" : undefined}
            className={`flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm transition-colors ${
              active
                ? "bg-accent-muted text-accent-strong"
                : "text-fg-muted hover:bg-bg-hover hover:text-fg"
            }`}
          >
            <item.icon size={15} />
            {t(item.labelKey)}
          </Link>
        );
      })}
    </nav>
  );
}
