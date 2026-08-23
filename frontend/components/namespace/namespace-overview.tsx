import { Boxes, Database, FlaskConical, Plus } from "lucide-react";
import Link from "next/link";
import { Suspense } from "react";
import { NamespaceExperimentCard } from "@/components/namespace/namespace-experiment-card";
import { NamespaceHeader } from "@/components/namespace/namespace-header";
import { NamespaceTabs } from "@/components/namespace/namespace-tabs";
import { OrgMembersPanel } from "@/components/orgs/org-members-panel";
import { RepoListPage } from "@/components/repo/repo-list-page";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Pagination } from "@/components/ui/pagination";
import { SkeletonLines } from "@/components/ui/skeleton";
import { errorMessage } from "@/lib/api-error-message";
import { listExperiments } from "@/lib/experiments";
import type { MessageKey } from "@/lib/i18n";
import { getT } from "@/lib/i18n/server";
import {
  canCreateInNamespace,
  type NamespaceTab,
  namespaceHref,
  namespaceTabHref,
  parseNamespaceTab,
} from "@/lib/namespace";
import type { RepoListSearch } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import { getCurrentUser } from "@/lib/session";
import type { NamespaceProfile, RepoKind } from "@/types/api";

const LIMIT = 30;

/** The listing's own filters (`RepoListPage` reads them) plus `tab`. */
export type NamespaceSearch = RepoListSearch;

const EMPTY_COPY: Record<
  RepoKind | "experiments",
  { icon: typeof Boxes; title: MessageKey; own: MessageKey }
> = {
  model: {
    icon: Boxes,
    title: "namespace.empty.models",
    own: "namespace.empty.ownModels",
  },
  dataset: {
    icon: Database,
    title: "namespace.empty.datasets",
    own: "namespace.empty.ownDatasets",
  },
  experiments: {
    icon: FlaskConical,
    title: "namespace.empty.experiments",
    own: "namespace.empty.ownExperiments",
  },
};

/**
 * The whole of `/{ns}` below the identity block: one tab per resource kind,
 * with the models and datasets tabs delegating to the same `RepoListPage`
 * the global `/models` and `/datasets` listings use, so search, facets,
 * sorting and paging behave identically inside a namespace
 * (docs/dev/namespace-design.md §4.3).
 *
 * The profile is fetched by the page (which needs it for the 404 and the
 * canonical-spelling redirect) and handed down rather than fetched twice.
 */
export async function NamespaceOverview({
  profile,
  searchParams,
}: {
  profile: NamespaceProfile;
  searchParams: Promise<NamespaceSearch>;
}) {
  const [sp, t, me] = await Promise.all([searchParams, getT(), getCurrentUser()]);
  const ns = profile.name;
  const tab = parseNamespaceTab(sp.tab, profile.kind);
  const counts: Record<NamespaceTab, number> = {
    models: profile.num_models,
    datasets: profile.num_datasets,
    experiments: profile.num_experiments,
    members: profile.num_members,
  };
  const canCreate = canCreateInNamespace(profile, me.ok ? me.data.user : null);
  // The Datasets tab lists with `experiment=false`, so its empty state and
  // `num_datasets` (which excludes experiment repositories) agree; experiment
  // repositories have their own tab.
  const hasAnyDataset = counts.datasets > 0;

  /**
   * Empty tabs are answered here rather than by `RepoListPage`: the counts
   * come from the profile, so "this namespace owns nothing of this kind" is
   * known without a listing round trip, and only that case earns the "create
   * the first repository" call to action. A tab that *has* repositories goes
   * through `RepoListPage`, whose own empty state covers "no match for these
   * filters".
   */
  function emptyTab(which: RepoKind | "experiments") {
    const copy = EMPTY_COPY[which];
    return (
      <EmptyState
        icon={copy.icon}
        title={t(canCreate ? copy.own : copy.title)}
        description={canCreate ? t("namespace.empty.createFirstDescription", { ns }) : undefined}
        action={
          canCreate ? (
            <Link
              href={`/new?ns=${encodeURIComponent(ns)}`}
              className={buttonClass({ variant: "primary", size: "sm" })}
            >
              <Plus size={14} />
              {t("namespace.empty.createFirst")}
            </Link>
          ) : undefined
        }
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <NamespaceHeader profile={profile} />
      <NamespaceTabs ns={ns} kind={profile.kind} active={tab} counts={counts} />

      {tab === "models" &&
        (counts.models === 0 ? (
          emptyTab("model")
        ) : (
          <RepoListPage
            kind="model"
            author={ns}
            basePath={namespaceHref(ns)}
            preserveParams={["tab"]}
            showHeading={false}
            searchParams={searchParams}
          />
        ))}

      {tab === "datasets" &&
        (!hasAnyDataset ? (
          emptyTab("dataset")
        ) : (
          <RepoListPage
            kind="dataset"
            author={ns}
            basePath={namespaceHref(ns)}
            preserveParams={["tab"]}
            experiment={false}
            showHeading={false}
            searchParams={searchParams}
          />
        ))}

      {tab === "experiments" &&
        (counts.experiments === 0 ? (
          emptyTab("experiments")
        ) : (
          <NamespaceExperiments ns={ns} offset={Number(sp.offset ?? 0) || 0} />
        ))}

      {tab === "members" && (
        // Its authorisation can fail on its own (a private member list is a
        // 403, not an error), so it streams in rather than blocking the page.
        <Suspense fallback={<SkeletonLines lines={2} />}>
          <OrgMembersPanel ns={ns} canAdmin={profile.can_edit} />
        </Suspense>
      )}
    </div>
  );
}

/** The experiments tab: dataset repositories flagged `is_experiment`. */
async function NamespaceExperiments({ ns, offset }: { ns: string; offset: number }) {
  const [t, headers] = await Promise.all([getT(), authHeaders()]);
  const result = await listExperiments({ author: ns, limit: LIMIT, offset }, { headers });

  if (!result.ok) {
    return (
      <ErrorState
        title={t("namespace.errorTitle")}
        message={errorMessage(t, result)}
        hint={t("namespace.backendHint")}
      />
    );
  }

  if (result.data.items.length === 0) {
    return (
      <EmptyState
        icon={FlaskConical}
        title={t("ui.pagination.outOfRangeTitle")}
        description={t("ui.pagination.outOfRangeDescription")}
        action={
          <Link
            href={namespaceTabHref(ns, "experiments")}
            className={buttonClass({ variant: "secondary", size: "sm" })}
          >
            {t("ui.pagination.backToFirstPage")}
          </Link>
        }
      />
    );
  }

  return (
    <>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {result.data.items.map((item) => (
          <NamespaceExperimentCard key={item.full_name} item={item} />
        ))}
      </div>
      <Pagination
        offset={offset}
        limit={LIMIT}
        total={result.data.total}
        buildHref={(o) => {
          const base = namespaceTabHref(ns, "experiments");
          return o > 0 ? `${base}&offset=${o}` : base;
        }}
      />
    </>
  );
}
