"use client";

import type { LucideIcon } from "lucide-react";
import { GitBranch, GitFork, GitMerge, History, Minimize2, Puzzle, Ruler } from "lucide-react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { MessageKey, Translator } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import { type DependentBucket, type DependentGroup, groupDependents } from "@/lib/lineage";
import { repoBase } from "@/lib/paths";
import type { LineageDependent } from "@/types/api";

/**
 * How many repositories one group shows before it has to be opened. A model
 * with two hundred quantizations must not push the rest of the overview page
 * off the screen, and the first few (most recently updated first) answer
 * "is anything happening downstream?" on their own.
 */
const COLLAPSED = 5;

const BUCKETS: Record<DependentBucket, { icon: LucideIcon; label: MessageKey }> = {
  finetune: { icon: GitBranch, label: "repo.lineage.relationFinetune" },
  adapter: { icon: Puzzle, label: "repo.lineage.relationAdapter" },
  quantized: { icon: Minimize2, label: "repo.lineage.relationQuantized" },
  merge: { icon: GitMerge, label: "repo.lineage.relationMerge" },
  other: { icon: GitFork, label: "repo.lineage.relationOther" },
  // The reverse of this repository's own `new_version:` edge: the older
  // versions it replaces.
  new_version: { icon: History, label: "repo.lineage.supersededVersions" },
  // Evaluated on this dataset, not trained from it -- a weaker claim than the
  // flat bucket below, so it gets a heading of its own.
  eval_dataset: { icon: Ruler, label: "repo.lineage.evaluatedBy" },
  // The flat bucket: dataset and run dependents have no relation to group by,
  // so they keep the section's own heading.
  "": { icon: GitFork, label: "repo.lineage.derivedFromThis" },
};

/**
 * The model tree: every repository whose card points back at this one, grouped
 * by how it says it relates (fine-tune / adapter / quantization / merge), with
 * a count per group.
 *
 * A client component because the groups fold: the data itself is fetched on
 * the server by LineageSection and handed down as a prop.
 */
export function LineageDependents({ dependents }: { dependents: LineageDependent[] }) {
  const t = useT();
  // The repository this tree belongs to, taken from the route rather than a
  // prop: this component is only ever mounted under /{models,datasets}/[ns]/[name].
  const params = useParams<{ ns?: string; name?: string }>();
  const groups = groupDependents(dependents);
  if (groups.length === 0) return null;

  return (
    <div className="flex flex-col gap-3">
      {groups.map((group) => (
        <DependentBucketGroup
          key={group.bucket}
          group={group}
          listHref={bucketListHref(group.bucket, params.ns, params.name)}
          t={t}
        />
      ))}
    </div>
  );
}

/**
 * Where a group's "see all" goes: the model listing filtered to the same set,
 * which unlike this section is paginated rather than capped at 100 rows.
 *
 * Null when the group has no single filter that reproduces it: "other" holds
 * whatever relations cards invented, so no one `relation=` covers it, and the
 * new_version / eval_dataset buckets are edge kinds the listing does not filter
 * on (docs/api-contract.md §2).
 */
function bucketListHref(
  bucket: DependentBucket,
  ns: string | undefined,
  name: string | undefined,
): string | null {
  if (!ns || !name) return null;
  const ref = encodeURIComponent(`${ns}/${name}`);
  // The flat bucket only occurs on a dataset repository (dataset and run
  // edges), where "derived from this" means the models trained on it.
  if (bucket === "") return `/models?dataset=${ref}`;
  if (bucket === "other" || bucket === "new_version" || bucket === "eval_dataset") return null;
  return `/models?base_model=${ref}&relation=${bucket}`;
}

function DependentBucketGroup({
  group,
  listHref,
  t,
}: {
  group: DependentGroup;
  listHref: string | null;
  t: Translator;
}) {
  const [expanded, setExpanded] = useState(false);
  const { icon: Icon, label } = BUCKETS[group.bucket];
  const hidden = group.items.length - COLLAPSED;
  const shown = expanded ? group.items : group.items.slice(0, COLLAPSED);

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-fg-subtle">
        <Icon size={13} strokeWidth={1.5} />
        {t(label)}
        <Badge>{group.items.length}</Badge>
        {listHref && (
          <Link href={listHref} className="ml-auto normal-case text-accent hover:underline">
            {t("repoList.lineage.seeAll")}
          </Link>
        )}
      </div>
      <ul className="flex flex-col gap-1">
        {shown.map((d) => (
          <li key={`${d.kind}:${d.repo.full_name}:${d.raw}`} className="text-sm">
            <Link
              href={repoBase(d.repo.kind, d.repo.namespace, d.repo.name)}
              className="font-mono text-accent hover:underline"
            >
              {d.repo.full_name}
            </Link>
            {d.kind === "run" && d.run && (
              <span className="ml-1.5 text-xs font-medium text-fg-subtle">
                {t("repo.lineage.fromRun", { run: d.run })}
              </span>
            )}
            {group.bucket === "other" && d.relation && (
              // The card wrote a relation nobody knows. Showing it is the
              // point of keeping it: "other" alone would lose what it said.
              <span className="ml-1.5 font-mono text-xs font-medium text-fg-subtle">
                {d.relation}
              </span>
            )}
          </li>
        ))}
      </ul>
      {hidden > 0 && (
        <div>
          <Button variant="ghost" size="sm" onClick={() => setExpanded((v) => !v)}>
            {expanded
              ? t("repo.lineage.showFewerDerived")
              : t("repo.lineage.showAllDerived", { count: group.items.length })}
          </Button>
        </div>
      )}
    </div>
  );
}
