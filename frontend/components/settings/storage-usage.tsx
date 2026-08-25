"use client";

import { HardDrive } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { buttonClass } from "@/components/ui/button";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { isUnauthorized } from "@/lib/api";
import { errorMessage, type FailedApiResult } from "@/lib/api-error-message";
import { formatBytes, formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { repoBase } from "@/lib/paths";
import { getUsage } from "@/lib/usage";
import type { UsageResponse } from "@/types/api";

/**
 * Keeps only the rows belonging to `namespace`. `/api/v1/usage` answers with
 * every namespace the viewer can see, so an organisation's own storage screen
 * narrows it here rather than asking the API for a slice it does not offer.
 */
function narrowToNamespace(usage: UsageResponse, namespace: string): UsageResponse {
  return {
    namespaces: usage.namespaces.filter((ns) => ns.namespace === namespace),
    repos: usage.repos.filter((repo) => repo.namespace === namespace),
  };
}

export function StorageUsage({
  /** Show only this namespace. Omitted on /settings/storage, which shows all. */
  namespace,
  /** Where to send an unauthenticated visitor back to after logging in. */
  loginNext = "/settings/storage",
  emptyTitle,
  emptyDescription,
}: {
  namespace?: string;
  loginNext?: string;
  emptyTitle?: string;
  emptyDescription?: string;
} = {}) {
  const t = useT();
  const [usage, setUsage] = useState<UsageResponse | null>(null);
  // The raw failed result (not a translated string) so the message can be
  // computed at render time with the current `t` — keeping `t` out of the
  // effect's dependencies avoids refetching on every locale change.
  const [error, setError] = useState<FailedApiResult | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const result = await getUsage();
      if (cancelled) return;
      if (!result.ok) {
        setNeedsLogin(isUnauthorized(result));
        setError(result);
        setUsage(null);
        return;
      }
      setNeedsLogin(false);
      setError(null);
      setUsage(namespace ? narrowToNamespace(result.data, namespace) : result.data);
    })();
    return () => {
      cancelled = true;
    };
  }, [namespace]);

  if (usage === null && !error) {
    return <SkeletonLines lines={5} />;
  }

  if (usage === null && needsLogin) {
    return (
      <ErrorState
        title={t("settings.storage.loginRequiredTitle")}
        message={t("settings.storage.loginRequiredMessage")}
        action={
          <Link
            href={`/login?next=${encodeURIComponent(loginNext)}`}
            className={buttonClass({ variant: "primary" })}
          >
            {t("settings.storage.login")}
          </Link>
        }
      />
    );
  }

  if (usage === null) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={error ? errorMessage(t, error) : t("settings.storage.loadFailed")}
        hint={t("settings.storage.loadFailedHint")}
      />
    );
  }

  if (usage.namespaces.length === 0) {
    return (
      <EmptyState
        icon={HardDrive}
        title={emptyTitle ?? t("settings.storage.emptyTitle")}
        description={emptyDescription ?? t("settings.storage.emptyDescription")}
      />
    );
  }

  return (
    <div className="flex flex-col gap-6">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {usage.namespaces.map((ns) => (
          <Card key={ns.namespace}>
            <CardHeader>
              <CardTitle>{ns.namespace}</CardTitle>
              <HardDrive size={14} className="text-fg-subtle" />
            </CardHeader>
            <div className="mt-3 flex flex-col gap-1 text-sm">
              <div className="flex items-center justify-between">
                <span className="text-fg-subtle">{t("settings.storage.lfsStorage")}</span>
                <span className="tabular-nums text-fg">{formatBytes(ns.lfs_size)}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-fg-subtle">{t("settings.storage.files")}</span>
                <span className="tabular-nums text-fg">{formatNumber(ns.num_files)}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-fg-subtle">{t("settings.storage.repositories")}</span>
                <span className="tabular-nums text-fg">{formatNumber(ns.num_repos)}</span>
              </div>
              <div className="flex items-center justify-between">
                <span className="text-fg-subtle">{t("settings.storage.quota")}</span>
                {/* A null quota is "unlimited", which is a different fact
                    from a quota of zero -- printing 0 B for it would be a
                    lie in the alarming direction. */}
                <span className="tabular-nums text-fg">
                  {ns.effective_quota_bytes === null
                    ? t("settings.storage.quotaUnlimited")
                    : formatBytes(ns.effective_quota_bytes)}
                </span>
              </div>
              {ns.effective_quota_bytes !== null && ns.lfs_size > ns.effective_quota_bytes ? (
                <p className="text-xs font-medium text-negative-strong">
                  {t("settings.storage.quotaExceeded")}
                </p>
              ) : null}
            </div>
          </Card>
        ))}
      </div>

      {usage.repos.length === 0 ? (
        <EmptyState
          icon={HardDrive}
          title={t("settings.storage.reposEmptyTitle")}
          description={t("settings.storage.reposEmptyDescription")}
        />
      ) : (
        <div className="scroll-x rounded-lg border border-border">
          <table className="w-full min-w-[560px] border-collapse text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
                <th className="px-3 py-2 font-medium">{t("settings.storage.colRepository")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.storage.colKind")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.storage.colFiles")}</th>
                <th className="px-3 py-2 text-right font-medium">
                  {t("settings.storage.colLfsSize")}
                </th>
              </tr>
            </thead>
            <tbody>
              {usage.repos.map((repo) => (
                <tr key={repo.full_name} className="border-b border-border last:border-0">
                  <td className="px-3 py-2">
                    <Link
                      href={repoBase(repo.kind, repo.namespace, repo.name)}
                      className="font-medium text-accent hover:underline"
                    >
                      {repo.full_name}
                    </Link>
                  </td>
                  <td className="px-3 py-2 capitalize text-fg-muted">{repo.kind}</td>
                  <td className="px-3 py-2 text-fg-subtle">{formatNumber(repo.num_files)}</td>
                  <td className="px-3 py-2 text-right tabular-nums text-fg">
                    {formatBytes(repo.lfs_size)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
