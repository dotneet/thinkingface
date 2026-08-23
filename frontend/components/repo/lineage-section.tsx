import type { LucideIcon } from "lucide-react";
import { ArrowUpRight, Database, FlaskConical, Layers, Link2Off, Ruler } from "lucide-react";
import Link from "next/link";
import { LineageDependents } from "@/components/repo/lineage-dependents";
import { Alert } from "@/components/ui/alert";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { expRunHref } from "@/lib/experiments";
import type { Translator } from "@/lib/i18n";
import { getT } from "@/lib/i18n/server";
import { getRepoLineage, groupUpstream, lineageRefHref, lineageRefLabel } from "@/lib/lineage";
import { authHeaders } from "@/lib/server-auth";
import type { ExpRunProducer, LineageRef, RepoKind } from "@/types/api";

/**
 * Where a repository came from and what came out of it, as declared by the
 * `lineage:` block of README.md's front matter (see docs/api-contract.md §12).
 *
 * A reference the server could not resolve -- a typo, something not pushed
 * yet, or a repository that never existed -- is rendered as plain
 * text with a note rather than as a broken link.
 *
 * Downstream is the model tree: it is grouped by relation and folded by
 * <LineageDependents>, which is where the interactive half of this section
 * lives.
 */
export async function LineageSection({
  kind,
  ns,
  name,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
}) {
  // Forward the tf_session cookie so the request is authenticated for a viewer who
  // is allowed to see them (see lib/server-auth.ts).
  const [result, t] = await Promise.all([
    getRepoLineage(kind, ns, name, { headers: await authHeaders() }),
    getT(),
  ]);

  if (!result.ok) {
    return (
      <Alert tone="warning" title={t("repo.lineage.unavailable")}>
        {result.message}
      </Alert>
    );
  }

  const { upstream, downstream, new_version: successor, produced_by: producedBy } = result.data;
  const { datasets, baseModels, evalDatasets, runs } = groupUpstream(upstream);
  // The successor is shown here as well as in the banner at the top of the
  // page: this card is the one place that claims to list a repository's whole
  // lineage, and "no lineage declared" would be a lie next to a `new_version:`.
  const successors = successor ? [successor.latest] : [];

  // Split the template so only the `lineage:` code snippet renders as <code>.
  const [emptyBefore = "", emptyAfter = ""] = t("repo.lineage.empty").split("{code}");

  return (
    <Card className="flex flex-col gap-4">
      <CardHeader>
        <CardTitle>{t("repo.lineage.title")}</CardTitle>
      </CardHeader>

      {upstream.length === 0 &&
      downstream.length === 0 &&
      successors.length === 0 &&
      producedBy.length === 0 ? (
        <p className="text-sm text-fg-subtle">
          {emptyBefore}
          <code className="font-mono text-xs">lineage:</code>
          {emptyAfter}
        </p>
      ) : (
        <div className="flex flex-col gap-4">
          <RefGroup
            icon={ArrowUpRight}
            label={t("repo.lineage.newVersionTitle")}
            refs={successors}
            t={t}
          />
          <RefGroup icon={Database} label={t("repo.lineage.trainedOn")} refs={datasets} t={t} />
          <RefGroup icon={Layers} label={t("repo.lineage.baseModel")} refs={baseModels} t={t} />
          <RefGroup icon={Ruler} label={t("repo.lineage.evaluatedOn")} refs={evalDatasets} t={t} />
          <RefGroup icon={FlaskConical} label={t("repo.lineage.trainingRun")} refs={runs} t={t} />
          <ProducedByGroup producers={producedBy} t={t} />
          <LineageDependents dependents={downstream} />
        </div>
      )}
    </Card>
  );
}

function GroupLabel({ icon: Icon, label }: { icon: LucideIcon; label: string }) {
  return (
    <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-fg-subtle">
      <Icon size={13} strokeWidth={1.5} />
      {label}
    </div>
  );
}

function RefGroup({
  icon,
  label,
  refs,
  t,
}: {
  icon: LucideIcon;
  label: string;
  refs: LineageRef[];
  t: Translator;
}) {
  if (refs.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <GroupLabel icon={icon} label={label} />
      <ul className="flex flex-col gap-1">
        {refs.map((ref) => (
          <li key={`${ref.kind}:${ref.raw}`}>
            <RefLink refItem={ref} t={t} />
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * "A training run says it built this."
 *
 * Separate from the `run` edges above on purpose: those are what *this*
 * repository's card declares about its origin, while these come from the other
 * end — a run that called `trackio.log_model` and named this repository. The
 * claim is stored with the run rather than in `repo_lineage`, because that
 * index is rebuilt from the card on every push and would drop anything written
 * from outside it (docs/api-contract.md §12).
 */
function ProducedByGroup({ producers, t }: { producers: ExpRunProducer[]; t: Translator }) {
  if (producers.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <GroupLabel icon={FlaskConical} label={t("repo.lineage.producedByRun")} />
      <ul className="flex flex-col gap-1">
        {producers.map((p) => (
          <li
            key={`${p.repo.full_name}/${p.project}/${p.run}`}
            className="flex flex-wrap items-center gap-2"
          >
            <Link
              href={expRunHref(p.repo.namespace, p.repo.name, p.project, p.run)}
              className="font-mono text-sm text-accent hover:underline"
            >
              {p.repo.full_name}/{p.project}/{p.run}
            </Link>
            {p.revision && (
              <span className="font-mono text-xs font-medium text-fg-subtle">
                {t("repo.lineage.producedRevision", { rev: p.revision.slice(0, 12) })}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

function RefLink({ refItem, t }: { refItem: LineageRef; t: Translator }) {
  const href = lineageRefHref(refItem);
  const label = lineageRefLabel(refItem);

  if (href === null) {
    return (
      <span className="flex flex-wrap items-center gap-1.5 text-sm">
        <span className="font-mono text-fg-muted">{label}</span>
        <span className="flex items-center gap-1 text-xs font-medium text-fg-subtle">
          <Link2Off size={11} strokeWidth={1.5} />
          {t("repo.lineage.notFound")}
        </span>
      </span>
    );
  }
  return (
    <Link href={href} className="font-mono text-sm text-accent hover:underline">
      {label}
    </Link>
  );
}
