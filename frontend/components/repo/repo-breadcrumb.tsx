import Link from "next/link";
import { getT } from "@/lib/i18n/server";
import { namespaceHref } from "@/lib/namespace";
import { repoBase } from "@/lib/paths";
import type { RepoKind } from "@/types/api";

export async function RepoBreadcrumb({
  kind,
  ns,
  name,
  trail,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  trail?: { label: string; href?: string }[];
}) {
  const t = await getT();
  const base = repoBase(kind, ns, name);
  // Users and organisations share one profile page (docs/namespace-design.md
  // §4.1), so the namespace segment always links there.
  const nsBase = namespaceHref(ns);
  return (
    <nav className="flex flex-wrap items-center gap-1.5 text-sm text-fg-subtle">
      <Link href={`/${kind}s`} className="hover:text-fg hover:underline">
        {kind === "dataset" ? t("repo.breadcrumb.datasets") : t("repo.breadcrumb.models")}
      </Link>
      <span>/</span>
      <Link href={nsBase} className="text-fg-muted hover:text-fg hover:underline">
        {ns}
      </Link>
      <span>/</span>
      <Link href={base} className="font-medium text-fg hover:underline">
        {name}
      </Link>
      {trail?.map((item, i) => (
        <span
          // biome-ignore lint/suspicious/noArrayIndexKey: path segments can repeat (a/b/a) but are never reordered
          key={i}
          className="flex items-center gap-1.5"
        >
          <span>/</span>
          {item.href ? (
            <Link href={item.href} className="hover:text-fg hover:underline">
              {item.label}
            </Link>
          ) : (
            <span className="text-fg">{item.label}</span>
          )}
        </span>
      ))}
    </nav>
  );
}
