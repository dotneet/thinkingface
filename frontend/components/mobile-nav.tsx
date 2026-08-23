"use client";

import { Menu, Plus, X } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useRef, useState } from "react";
import { navItems } from "@/components/nav-items";
import { SearchBox } from "@/components/search-box";
import { Button, buttonClass } from "@/components/ui/button";
import { useOnClickOutside } from "@/hooks/use-on-click-outside";
import { useT } from "@/lib/i18n/client";

export function MobileNav() {
  const [open, setOpen] = useState(false);
  const pathname = usePathname();
  const t = useT();
  const ref = useRef<HTMLDivElement>(null);
  useOnClickOutside(ref, () => setOpen(false));

  return (
    <div className="md:hidden" ref={ref}>
      <Button
        onClick={() => setOpen((v) => !v)}
        className="relative h-8 w-8 px-0 before:absolute before:inset-[-6px] before:content-['']"
        aria-expanded={open}
        aria-label={open ? t("header.closeMenu") : t("header.openMenu")}
      >
        {open ? <X size={16} /> : <Menu size={16} />}
      </Button>
      {open && (
        <div className="absolute inset-x-0 top-14 z-40 border-b border-border bg-bg-raised px-4 py-3 shadow-lg">
          <nav className="flex flex-col gap-1">
            {navItems.map((item) => {
              const active = pathname === item.href || pathname.startsWith(`${item.href}/`);
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  onClick={() => setOpen(false)}
                  aria-current={active ? "page" : undefined}
                  className={`flex items-center gap-2 rounded-md px-3 py-2 text-sm ${
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
          <div className="mt-3">
            <SearchBox />
          </div>
          <Link
            href="/new"
            onClick={() => setOpen(false)}
            className={buttonClass({ variant: "secondary", className: "mt-3 w-full" })}
          >
            <Plus size={15} />
            {t("header.newRepository")}
          </Link>
        </div>
      )}
    </div>
  );
}
