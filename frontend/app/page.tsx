import { ArrowRight, Boxes, Database, FlaskConical, HardDrive } from "lucide-react";
import Link from "next/link";
import { RepoCard } from "@/components/repo/repo-card";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { errorMessage } from "@/lib/api-error-message";
import { formatBytes, formatNumber } from "@/lib/format";
import type { Translator } from "@/lib/i18n";
import { getT } from "@/lib/i18n/server";
import { listRepos } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import { getStats } from "@/lib/stats";

export const dynamic = "force-dynamic";

export default async function HomePage() {
  // Forward the tf_session cookie so the request is authenticated
  // in "recently updated" alongside public ones (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [t, stats, datasets, models] = await Promise.all([
    getT(),
    getStats({ headers }),
    listRepos({ kind: "dataset", sort: "updated", limit: 6 }, { headers }),
    listRepos({ kind: "model", sort: "updated", limit: 6 }, { headers }),
  ]);

  const statItems = [
    {
      label: t("home.stats.datasets"),
      value: stats.ok ? stats.data.datasets : null,
      icon: Database,
    },
    { label: t("home.stats.models"), value: stats.ok ? stats.data.models : null, icon: Boxes },
    {
      label: t("home.stats.experiments"),
      value: stats.ok ? stats.data.experiments : null,
      icon: FlaskConical,
    },
    {
      label: t("home.stats.totalStorage"),
      value: stats.ok ? formatBytes(stats.data.total_size) : null,
      icon: HardDrive,
      raw: true,
    },
  ];

  return (
    <div className="flex flex-col gap-12">
      <section className="flex flex-col gap-2 py-6">
        <h1 className="text-3xl font-semibold tracking-tight">🤔 Thinking Face</h1>
        <p className="max-w-2xl text-fg-subtle">{t("home.tagline")}</p>
      </section>

      <section className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {statItems.map((item) => (
          <div key={item.label} className="rounded-lg border border-border bg-bg-raised p-4">
            <item.icon size={16} className="mb-2 text-fg-subtle" />
            <div className="text-2xl font-semibold tabular-nums">
              {item.value === null
                ? "—"
                : item.raw
                  ? item.value
                  : formatNumber(item.value as number)}
            </div>
            <div className="text-xs font-medium text-fg-subtle">{item.label}</div>
          </div>
        ))}
      </section>

      <RecentSection
        t={t}
        title={t("home.recentDatasets")}
        href="/datasets"
        icon={Database}
        result={datasets}
        emptyLabel={t("home.noDatasets")}
        viewAllLabel={t("home.viewAll")}
        errorTitle={t("ui.errorStateTitle")}
        errorHint={t("home.backendHint")}
      />

      <RecentSection
        t={t}
        title={t("home.recentModels")}
        href="/models"
        icon={Boxes}
        result={models}
        emptyLabel={t("home.noModels")}
        viewAllLabel={t("home.viewAll")}
        errorTitle={t("ui.errorStateTitle")}
        errorHint={t("home.backendHint")}
      />
    </div>
  );
}

function RecentSection({
  t,
  title,
  href,
  icon: Icon,
  result,
  emptyLabel,
  viewAllLabel,
  errorTitle,
  errorHint,
}: {
  t: Translator;
  title: string;
  href: string;
  icon: typeof Database;
  result: Awaited<ReturnType<typeof listRepos>>;
  emptyLabel: string;
  viewAllLabel: string;
  errorTitle: string;
  errorHint: string;
}) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="text-base font-semibold tracking-tight">{title}</h2>
        <Link href={href} className="flex items-center gap-1 text-sm text-accent hover:underline">
          {viewAllLabel}
          <ArrowRight size={14} />
        </Link>
      </div>
      {!result.ok ? (
        <ErrorState title={errorTitle} message={errorMessage(t, result)} hint={errorHint} />
      ) : result.data.items.length === 0 ? (
        <EmptyState icon={Icon} title={emptyLabel} />
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {result.data.items.map((repo) => (
            <RepoCard key={repo.id} repo={repo} />
          ))}
        </div>
      )}
    </section>
  );
}
