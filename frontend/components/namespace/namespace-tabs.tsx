import Link from "next/link";
import type { MessageKey } from "@/lib/i18n";
import { getT } from "@/lib/i18n/server";
import { type NamespaceTab, namespaceTabHref } from "@/lib/namespace";
import type { NamespaceKind } from "@/types/api";

const LABEL_KEYS: Record<NamespaceTab, MessageKey> = {
  models: "namespace.tabs.models",
  datasets: "namespace.tabs.datasets",
  experiments: "namespace.tabs.experiments",
  members: "namespace.tabs.members",
};

export async function NamespaceTabs({
  ns,
  kind,
  active,
  counts,
}: {
  ns: string;
  kind: NamespaceKind;
  active: NamespaceTab;
  counts: Record<NamespaceTab, number>;
}) {
  const t = await getT();
  // A user namespace has no member list (docs/namespace-design.md §4.1), so
  // the tab is not offered at all — `parseNamespaceTab` refuses ?tab=members
  // there for the same reason.
  const tabs: NamespaceTab[] =
    kind === "org"
      ? ["models", "datasets", "experiments", "members"]
      : ["models", "datasets", "experiments"];

  return (
    <div className="flex gap-1 overflow-x-auto border-b border-border">
      {tabs.map((tab) => (
        <Link
          key={tab}
          href={namespaceTabHref(ns, tab)}
          aria-current={active === tab ? "page" : undefined}
          className={`-mb-px flex shrink-0 items-center gap-1.5 border-b-2 px-3 py-2 text-sm font-medium transition-colors ${
            active === tab
              ? "border-accent text-fg"
              : "border-transparent text-fg-subtle hover:text-fg"
          }`}
        >
          {t(LABEL_KEYS[tab])}
          <span className="tabular-nums text-xs font-medium text-fg-subtle">{counts[tab]}</span>
        </Link>
      ))}
    </div>
  );
}
