"use client";

import { Building2, Plus, Settings } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { NamespaceAvatar } from "@/components/namespace/namespace-avatar";
import { OrgRoleBadge, orgRoleLabelKey } from "@/components/orgs/org-role-badge";
import { Alert } from "@/components/ui/alert";
import { Button, buttonClass } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { SkeletonLines } from "@/components/ui/skeleton";
import { isUnauthorized } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getMe } from "@/lib/auth";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import { namespaceHref } from "@/lib/namespace";
import { canAdminOrg, listMyOrgs, orgErrorKey, orgSettingsHref, removeMember } from "@/lib/orgs";
import type { Org } from "@/types/api";

/**
 * The signed-in user's memberships. Leaving is `DELETE
 * /orgs/{org}/members/{self}` — the same endpoint an admin uses to remove
 * someone else (§5) — and is refused with `last_admin` when they are the only
 * admin left.
 */
export function OrganizationsManager() {
  const t = useT();
  const [orgs, setOrgs] = useState<Org[] | null>(null);
  const [username, setUsername] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  // Organisation the user is about to leave, pending the ConfirmDialog.
  const [confirmTarget, setConfirmTarget] = useState<Org | null>(null);

  async function refresh() {
    const result = await listMyOrgs();
    if (!result.ok) {
      setNeedsLogin(isUnauthorized(result));
      setError(errorMessage(t, result));
      setOrgs(null);
      return;
    }
    setError(null);
    setOrgs(result.data.items);
  }

  useEffect(() => {
    (async () => {
      const me = await getMe();
      if (me.ok) setUsername(me.data.user.username);
      await refresh();
    })();
  }, []);

  async function handleLeave(org: Org) {
    setConfirmTarget(null);
    setBusy(org.name);
    setActionError(null);
    const result = await removeMember(org.name, username);
    setBusy(null);
    if (!result.ok) {
      const key = orgErrorKey(result);
      setActionError(key ? t(key) : errorMessage(t, result));
      return;
    }
    await refresh();
  }

  if (orgs === null && !error) return <SkeletonLines lines={4} />;

  if (needsLogin) {
    return (
      <ErrorState
        title={t("settings.organizations.loginRequiredTitle")}
        message={t("settings.organizations.loginRequiredMessage")}
        action={
          <Link
            href="/login?next=/settings/organizations"
            className={buttonClass({ variant: "primary" })}
          >
            {t("settings.organizations.login")}
          </Link>
        }
      />
    );
  }

  if (orgs === null) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={error ?? t("settings.organizations.loadFailed")}
        hint={t("settings.organizations.loadFailedHint")}
      />
    );
  }

  if (orgs.length === 0) {
    return (
      <EmptyState
        icon={Building2}
        title={t("settings.organizations.emptyTitle")}
        description={t("settings.organizations.emptyDescription")}
        action={
          <Link href="/orgs/new" className={buttonClass({ variant: "primary", size: "sm" })}>
            <Plus size={14} />
            {t("settings.organizations.create")}
          </Link>
        }
      />
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {actionError && <Alert tone="negative">{actionError}</Alert>}

      <div className="flex flex-col gap-3">
        {orgs.map((org) => (
          <div
            key={org.name}
            className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-3"
          >
            <NamespaceAvatar name={org.name} avatarUrl={org.avatar_url} kind="org" size={32} />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <Link
                  href={namespaceHref(org.name)}
                  className="text-sm font-medium text-fg hover:underline"
                >
                  {org.display_name || org.name}
                </Link>
                <OrgRoleBadge role={org.viewer_role} label={t(orgRoleLabelKey(org.viewer_role))} />
              </div>
              <div className="mt-0.5 flex flex-wrap gap-3 text-xs font-medium text-fg-subtle">
                <span className="tabular-nums">
                  {t(
                    org.num_members === 1
                      ? "settings.organizations.membersOne"
                      : "settings.organizations.membersOther",
                    { count: formatNumber(org.num_members) },
                  )}
                </span>
                <span className="tabular-nums">
                  {t(
                    org.num_repos === 1
                      ? "settings.organizations.reposOne"
                      : "settings.organizations.reposOther",
                    { count: formatNumber(org.num_repos) },
                  )}
                </span>
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              {canAdminOrg(org) && (
                <Link
                  href={orgSettingsHref(org.name)}
                  className={buttonClass({ variant: "secondary", size: "sm" })}
                >
                  <Settings size={13} />
                  {t("settings.organizations.manage")}
                </Link>
              )}
              <Button
                variant="danger"
                size="sm"
                disabled={busy === org.name || !username}
                onClick={() => setConfirmTarget(org)}
              >
                {busy === org.name
                  ? t("settings.organizations.leaving")
                  : t("settings.organizations.leave")}
              </Button>
            </div>
          </div>
        ))}
      </div>

      <div className="flex flex-wrap gap-2">
        <Link href="/orgs/new" className={buttonClass({ variant: "primary", size: "sm" })}>
          <Plus size={14} />
          {t("settings.organizations.create")}
        </Link>
        <Link href="/orgs" className={buttonClass({ variant: "secondary", size: "sm" })}>
          {t("settings.organizations.browse")}
        </Link>
      </div>

      <ConfirmDialog
        open={confirmTarget !== null}
        onClose={() => setConfirmTarget(null)}
        onConfirm={() => {
          if (confirmTarget) void handleLeave(confirmTarget);
        }}
        title={t("settings.organizations.confirmLeaveTitle", { name: confirmTarget?.name ?? "" })}
        description={
          <p className="text-sm text-fg-muted">
            {t("settings.organizations.confirmLeave", { name: confirmTarget?.name ?? "" })}
          </p>
        }
        confirmLabel={t("settings.organizations.leave")}
        confirmingLabel={t("settings.organizations.leaving")}
        confirming={busy !== null}
      />
    </div>
  );
}
