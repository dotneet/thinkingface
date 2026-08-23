import { FlaskConical } from "lucide-react";
import Link from "next/link";
import { TimeText } from "@/components/ui/time-text";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import type { ExpProjectListItem } from "@/types/api";

/** Mirrors the card markup on `/experiments` (app/experiments/page.tsx) so an
 * experiment repository looks the same whether reached from the global list
 * or from a namespace's own page. */
export async function NamespaceExperimentCard({ item }: { item: ExpProjectListItem }) {
  const t = await getT();
  return (
    <Link
      href={`/experiments/${encodeURIComponent(item.namespace)}/${encodeURIComponent(item.name)}`}
      className="flex flex-col gap-2 rounded-lg border border-border bg-bg-raised p-4 transition-colors hover:border-border-strong hover:bg-bg-hover"
    >
      <div className="flex items-center gap-1.5 text-sm font-medium">
        <FlaskConical size={14} className="text-fg-subtle" />
        {item.full_name}
      </div>
      <div className="flex items-center gap-3 text-xs font-medium text-fg-subtle">
        <span className="tabular-nums">
          {t(
            item.num_projects === 1
              ? "experiments.index.projectsOne"
              : "experiments.index.projectsOther",
            { count: formatNumber(item.num_projects) },
          )}
        </span>
        <TimeText iso={item.updated_at} style="relative" />
      </div>
    </Link>
  );
}
