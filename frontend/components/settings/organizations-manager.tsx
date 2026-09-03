"use client";

import { Building2, Plus, Settings } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { NamespaceAvatar } from "@/components/namespace/namespace-avatar";
import { OrgRoleBadge, orgRoleLabelKey } from "@/components/orgs/org-role-badge";
import { LoginRequiredState } from "@/components/settings/login-required-state";
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
  // getMe() failing is a distinct outcome from "not signed in" (that's
  // `needsLogin`, from the *list* request below): the list can load fine
  // while this fails, leaving `username` at its initial "" — which every
  // Leave button's `disabled={... || !username}` reads no differently from
  // "confirmed you have no name". Tracked separately so that silent case can
  // say what actually happened instead (DESIGN.md §9).
  const [usernameError, setUsernameError] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needsLogin, setNeedsLogin] = useState(false);
  // Two separate errors on purpose, the same split admin-users-manager makes.
  // `actionError` is the page-level record of the last failed leave and
  // outlives the dialog; `dialogError` belongs to the dialog that is open
  // right now and is cleared both when it opens and when it closes. Sharing
  // one state meant organisation A's `last_admin` failure was still in it when
  // the dialog reopened for B, so B's confirmation appeared pre-failed —
  // before anything had been confirmed at all.
  const [actionError, setActionError] = useState<string | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  // Organisation the user is about to leave, pending the ConfirmDialog.
  const [confirmTarget, setConfirmTarget] = useState<Org | null>(null);

  /** Opens the leave confirmation with no failure carried over from the last one. */
  function openConfirm(org: Org) {
    setActionError(null);
    setDialogError(null);
    setConfirmTarget(org);
  }

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
      if (me.ok) {
        setUsername(me.data.user.username);
      } else {
        setUsernameError(true);
      }
      await refresh();
    })();
  }, []);

  // The dialog stays open — with its confirm button reading "Leaving…" —
  // until the request has finished, and reports its own failure. Clearing the
  // target first closed it the instant the request left, which also made
  // `confirming` permanently false.
  async function handleLeave(org: Org) {
    setBusy(org.name);
    setActionError(null);
    setDialogError(null);
    const result = await removeMember(org.name, username);
    setBusy(null);
    if (!result.ok) {
      const key = orgErrorKey(result);
      const message = key ? t(key) : errorMessage(t, result);
      // Into both: the dialog shows it while it is still up, and the page
      // keeps it after the user dismisses the dialog, so a refused leave is
      // never silently lost the way a dialog-only error would be.
      setDialogError(message);
      setActionError(message);
      return;
    }
    setActionError(null);
    setDialogError(null);
    setConfirmTarget(null);
    await refresh();
  }

  if (orgs === null && !error) return <SkeletonLines lines={4} />;

  if (needsLogin) {
    return <LoginRequiredState next="/settings/organizations" />;
  }

  if (orgs === null) {
    return (
      <ErrorState
        title={t("settings.errorTitle")}
        message={error ?? t("settings.organizations.loadFailed")}
        hint={t("settings.organizations.loadFailedHint")}
        action={
          <Button size="sm" onClick={() => refresh()}>
            {t("ui.unexpectedError.retry")}
          </Button>
        }
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
      {usernameError && <Alert tone="warning">{t("settings.organizations.identityUnknown")}</Alert>}

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
                title={!username ? t("settings.organizations.leaveDisabledHint") : undefined}
                onClick={() => openConfirm(org)}
              >
                {busy === org.name
                  ? t("settings.organizations.leaving")
                  : t("settings.organizations.leave")}
              </Button>
            </div>
          </div>
        ))}
      </div>

      {/* Below the list, not above it: a failed leave reported here used to
          push every remaining organisation's Leave and Manage controls down by
          the Alert's height (DESIGN.md §8.1). While the confirmation dialog is
          still up the failure is shown there instead, where the user is
          actually looking — not in both places at once. */}
      {actionError && confirmTarget === null && <Alert tone="negative">{actionError}</Alert>}

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
        // Clear the dialog's failure along with the selection: this one dialog
        // serves every organisation in the list, so a leftover message greeted
        // the next one before it had been confirmed. Matches transfers-manager
        // / admin-users-manager. The page-level Alert above still carries it.
        onClose={() => {
          setConfirmTarget(null);
          setDialogError(null);
        }}
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
        confirming={busy !== null && confirmTarget !== null}
        error={dialogError}
      />
    </div>
  );
}
