"use client";

import { useEffect, useState } from "react";
import { cn } from "@/lib/cn";
import { useT } from "@/lib/i18n/client";
import type { TocEntry } from "@/lib/markdown-toc";

/**
 * Table of contents for a README's headings. The heading ids attached by the
 * README body (components/repo/readme-card.tsx, via rehype-slug) and the ids
 * numbered by `extractToc` in `lib/markdown-toc.ts` use the same
 * github-slugger algorithm, so all this component has to do is link to those
 * ids with same-page `#id` links.
 *
 * Below the `lg` breakpoint it collapses into a `<details>`; at `lg` and above
 * it stays always visible, tracking scroll with `sticky top-20` (offset down
 * by the site header's height). Both versions exist in the DOM, but whichever
 * one isn't the active breakpoint gets `display: none`, so the duplicate
 * never lingers in the accessibility tree (same pattern as
 * components/mobile-nav.tsx / site-nav.tsx).
 */
export function ReadmeToc({ entries, className }: { entries: TocEntry[]; className?: string }) {
  const t = useT();
  const [activeId, setActiveId] = useState<string | null>(null);

  // `entries` may be a freshly allocated array on every render from the
  // caller, so only the id order flattened into a string is fed into the
  // effect (the effect reconstructs the ids from this string, so it doesn't
  // depend on the array's identity and won't re-attach the observer needlessly).
  const entryIds = entries.map((entry) => entry.id).join("\0");

  useEffect(() => {
    const ids = entryIds === "" ? [] : entryIds.split("\0");
    if (ids.length < 3) return;
    const headingEls = ids
      .map((id) => document.getElementById(id))
      .filter((el): el is HTMLElement => el !== null);
    if (headingEls.length === 0) return;

    // Header (56px) + this nav's own sticky offset (top-20 = 80px) sit above
    // the viewport's top edge, so shrink the observation band from there and
    // treat anything past the top 30% of the viewport as "not yet reached".
    const observer = new IntersectionObserver(
      (observed) => {
        const visible = observed
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        const first = visible[0];
        if (first) setActiveId(first.target.id);
      },
      { rootMargin: "-88px 0px -70% 0px", threshold: 0 },
    );

    for (const el of headingEls) observer.observe(el);
    return () => observer.disconnect();
  }, [entryIds]);

  if (entries.length < 3) return null;

  const minDepth = Math.min(...entries.map((entry) => entry.depth));

  return (
    <nav aria-label={t("repo.readme.toc")} className={cn("text-sm", className)}>
      <details className="rounded-lg border border-border bg-bg-raised p-3 lg:hidden">
        <summary className="cursor-pointer text-xs font-medium uppercase tracking-wide text-fg-subtle">
          {t("repo.readme.toc")}
        </summary>
        <div className="mt-2 max-h-[70vh] overflow-y-auto">
          <TocList entries={entries} minDepth={minDepth} activeId={activeId} />
        </div>
      </details>
      <div className="sticky top-20 hidden max-h-[70vh] flex-col gap-2 overflow-y-auto rounded-lg border border-border bg-bg-raised p-3 lg:flex">
        <span className="text-xs font-medium uppercase tracking-wide text-fg-subtle">
          {t("repo.readme.toc")}
        </span>
        <TocList entries={entries} minDepth={minDepth} activeId={activeId} />
      </div>
    </nav>
  );
}

function TocList({
  entries,
  minDepth,
  activeId,
}: {
  entries: TocEntry[];
  minDepth: number;
  activeId: string | null;
}) {
  return (
    <ul className="flex flex-col gap-0.5">
      {entries.map((entry) => {
        const isActive = entry.id === activeId;
        return (
          <li key={entry.id} style={{ paddingLeft: `${(entry.depth - minDepth) * 0.75}rem` }}>
            <a
              href={`#${entry.id}`}
              aria-current={isActive ? "location" : undefined}
              className={cn(
                "block truncate rounded-md px-2 py-1 transition-colors",
                isActive ? "font-medium text-fg" : "text-fg-muted hover:text-fg",
              )}
            >
              {entry.text}
            </a>
          </li>
        );
      })}
    </ul>
  );
}
