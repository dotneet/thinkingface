"use client";

import { UserPlus, Users } from "lucide-react";
import { useCallback, useState } from "react";
import {
  AdminUserConfirms,
  type AdminUserConfirmTarget,
} from "@/components/settings/admin-user-confirms";
import { AdminUserCreateDialog } from "@/components/settings/admin-user-create-dialog";
import { AdminUserResetDialog } from "@/components/settings/admin-user-reset-dialog";
import { type AdminUserActions, AdminUserRow } from "@/components/settings/admin-user-row";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { OutOfRangeEmptyState, PaginationControls } from "@/components/ui/pagination-controls";
import { SearchInput } from "@/components/ui/search-input";
import { SkeletonLines } from "@/components/ui/skeleton";
import { Table, TBody, THead, Th } from "@/components/ui/table";
import { usePagedList } from "@/hooks/use-paged-list";
import {
  adminUserErrorKey,
  listAdminUsers,
  revokeAdminUserCredentials,
  setAdminUserApproval,
  setAdminUserDisabled,
  updateAdminUser,
} from "@/lib/admin";
import type { ApiResult } from "@/lib/api";
import type { FailedApiResult } from "@/lib/api-error-message";
import { errorMessage } from "@/lib/api-error-message";
import { formatNumber } from "@/lib/format";
import type { MessageKey } from "@/lib/i18n";
import { useT } from "@/lib/i18n/client";
import type { AdminUser } from "@/types/api";
import { UserApprovalApproved, UserApprovalPending } from "@/types/api";

/** The store's own default page size, restated so the two agree. */
const PAGE_SIZE = 50;

/**
 * The site administrator's account directory
 * (docs/dev/api-contract.md §1.3).
 *
 * Most of it is one endpoint, `PATCH /api/v1/admin/users/{username}`:
 * resetting a password, flipping the administrator flag and suspending an
 * account are the same partial update with different fields set. Revoking
 * credentials is its own POST, because it is the one irreversible action here
 * and must not ride along as a field on a partial update.
 *
 * Suspension and revocation are offered as separate controls for the same
 * reason they are separate endpoints: suspending is a switch (nothing is
 * destroyed, restoring gives it all back), revoking permanently deletes the
 * account's tokens and SSH keys. Presenting them as one "offboard" button
 * would make the reversible one look irreversible and the irreversible one
 * look undoable.
 *
 * What lives here is the listing and the writes. The row, the two forms and
 * the four confirmations are siblings of this file — see `admin-user-row.tsx`,
 * `admin-user-create-dialog.tsx`, `admin-user-reset-dialog.tsx` and
 * `admin-user-confirms.tsx` — so adding an operation costs a case in
 * `handleConfirm` rather than another dialog and another `…Target` state.
 */
export function AdminUsersManager({ viewer }: { viewer: string }) {
  const t = useT();
  const [search, setSearch] = useState("");

  const [actionError, setActionError] = useState<string | null>(null);
  // A failure from a confirmation dialog is shown *in* that dialog rather than
  // in the page-level Alert below the table: the dialog is where the user is
  // looking, and it stays open until the request it fired has finished (see
  // `handleConfirm`).
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const [addOpen, setAddOpen] = useState(false);
  const [resetTarget, setResetTarget] = useState<AdminUser | null>(null);
  const [confirmTarget, setConfirmTarget] = useState<AdminUserConfirmTarget | null>(null);

  // Localizes a failure through the endpoint-specific keys first, falling
  // back to the shared `error.type` mapping.
  const describe = useCallback(
    (result: FailedApiResult) => {
      const key = adminUserErrorKey(result);
      return key ? t(key) : errorMessage(t, result);
    },
    [t],
  );

  const {
    items: users,
    total,
    setOffset,
    loadError,
    reload: refresh,
    outOfRange,
    pager,
  } = usePagedList({
    pageSize: PAGE_SIZE,
    deps: [search],
    fetchPage: ({ limit, offset }) => listAdminUsers({ search, limit, offset }),
    describe,
  });

  function runSearch(query: string) {
    setSearch(query);
    setOffset(0);
  }

  /**
   * Every write on this screen has the same shape: mark the row busy, run it,
   * either surface the failure or announce the success and re-read. The
   * listing is always re-read rather than patched in place — the row's state
   * afterwards belongs to the server, not to the copy this page took before
   * somebody else touched the account.
   *
   * `firedFrom` says who is waiting for it. A write started from a
   * confirmation dialog keeps that dialog open across the request — its
   * confirm button reads "Working…" — and a failure lands in it, where the
   * administrator is already looking; the dialog closes only once the write
   * has actually succeeded. A write started straight from a row (Approve asks
   * nothing) has only the row's spinner and the page-level Alert.
   */
  async function run(
    username: string,
    write: () => Promise<ApiResult<unknown>>,
    successKey: MessageKey,
    firedFrom: "row" | "dialog" = "row",
  ) {
    setBusy(username);
    setActionError(null);
    setDialogError(null);
    setNotice(null);
    const result = await write();
    setBusy(null);
    if (!result.ok) {
      (firedFrom === "dialog" ? setDialogError : setActionError)(describe(result));
      return;
    }
    if (firedFrom === "dialog") setConfirmTarget(null);
    setNotice(t(successKey, { username }));
    await refresh();
  }

  /** The write behind each confirmed operation, and how to announce it. */
  function confirmedWrite(target: AdminUserConfirmTarget): {
    write: () => Promise<ApiResult<unknown>>;
    successKey: MessageKey;
  } {
    const { kind, user } = target;
    switch (kind) {
      case "admin":
        return {
          write: () => updateAdminUser(user.username, { is_admin: !user.is_admin }),
          successKey: "settings.adminUsers.adminChanged",
        };
      case "disable":
        return {
          write: () => setAdminUserDisabled(user.username, !user.disabled),
          successKey: user.disabled
            ? "settings.adminUsers.restoreDone"
            : "settings.adminUsers.suspendDone",
        };
      case "hold":
        return {
          write: () => setAdminUserApproval(user.username, UserApprovalPending),
          successKey: "settings.adminUsers.holdDone",
        };
      case "revoke":
        // The listing does not change — nothing on the wire type counts tokens
        // or keys — but `run` reloads anyway so the row cannot be acted on
        // again from a copy taken before somebody else touched the account.
        return {
          write: () => revokeAdminUserCredentials(user.username),
          successKey: "settings.adminUsers.revokeDone",
        };
    }
  }

  // Confirming does not close the dialog: `run` holds it open across the write
  // and closes it on success. Clearing the target here closed it the instant
  // the request left, which also made `confirming` permanently false —
  // revoking somebody's credentials looked like nothing had happened at all,
  // and on a slow link the natural response is to do it again.
  function handleConfirm(target: AdminUserConfirmTarget) {
    const { write, successKey } = confirmedWrite(target);
    void run(target.user.username, write, successKey, "dialog");
  }

  // One dialog serves all four operations, so a failure left over from the
  // last one would otherwise greet the next account it opens for.
  function openConfirm(target: AdminUserConfirmTarget) {
    setDialogError(null);
    setConfirmTarget(target);
  }

  const actions: AdminUserActions = {
    resetPassword: setResetTarget,
    approve: (user) =>
      void run(
        user.username,
        () => setAdminUserApproval(user.username, UserApprovalApproved),
        "settings.adminUsers.approveDone",
      ),
    hold: (user) => openConfirm({ kind: "hold", user }),
    toggleDisabled: (user) => openConfirm({ kind: "disable", user }),
    revokeCredentials: (user) => openConfirm({ kind: "revoke", user }),
    toggleAdmin: (user) => openConfirm({ kind: "admin", user }),
  };

  // A failed read leaves `users` null, and the banner below is simply not
  // rendered then: "nobody is waiting" is a different statement from "we
  // could not ask", and the error state already says the second (DESIGN.md §9).
  const pendingHere =
    users === null ? 0 : users.filter((u) => u.approval === UserApprovalPending).length;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <SearchInput
          activeValue={search}
          onSearch={runSearch}
          placeholder={t("settings.adminUsers.searchPlaceholder")}
          formClassName="min-w-[240px] flex-1"
        />
        {/* Only ever rendered from a successful read (DESIGN.md §9). */}
        {total !== null && (
          <span className="text-xs font-medium tabular-nums text-fg-subtle">
            {t(total === 1 ? "settings.adminUsers.countOne" : "settings.adminUsers.countOther", {
              count: formatNumber(total),
            })}
          </span>
        )}
        <Button variant="primary" onClick={() => setAddOpen(true)} className="px-3 py-1.5">
          <UserPlus size={15} />
          {t("settings.adminUsers.addUser")}
        </Button>
      </div>

      {users === null && !loadError ? (
        <SkeletonLines lines={5} />
      ) : users === null ? (
        <ErrorState
          title={t("settings.adminUsers.loadFailed")}
          message={loadError ?? t("settings.adminUsers.loadFailed")}
          hint={t("settings.adminUsers.loadFailedHint")}
          action={
            <Button size="sm" onClick={() => refresh()}>
              {t("ui.unexpectedError.retry")}
            </Button>
          }
        />
      ) : users.length === 0 ? (
        outOfRange ? (
          <OutOfRangeEmptyState onBackToFirstPage={() => setOffset(0)} />
        ) : (
          <EmptyState
            icon={Users}
            title={t("settings.adminUsers.emptyTitle")}
            description={t("settings.adminUsers.emptyDescription")}
          />
        )
      ) : (
        <Table minWidth={640}>
          <THead>
            <Th>{t("settings.adminUsers.colUsername")}</Th>
            <Th>{t("settings.adminUsers.colEmail")}</Th>
            <Th>{t("settings.adminUsers.colCreated")}</Th>
            <Th>{t("settings.adminUsers.colLastLogin")}</Th>
            <Th align="right">{t("settings.adminUsers.colActions")}</Th>
          </THead>
          <TBody>
            {users.map((user) => (
              <AdminUserRow
                key={user.id}
                user={user}
                isSelf={user.username === viewer}
                busy={busy === user.username}
                actions={actions}
              />
            ))}
          </TBody>
        </Table>
      )}

      <PaginationControls pager={pager} />

      {/* Every banner on this screen lives *below* the table, and none of them
          sits between the toolbar and the rows. Approving an account inserts a
          success Alert and — once the last pending account is gone — removes
          the waiting-room banner; from above the table each of those moved
          every row by its own height, so the second click of a run of
          approvals landed on a different account's Suspend or Revoke
          credentials (DESIGN.md §8.1). */}
      {actionError && <Alert tone="negative">{actionError}</Alert>}
      {notice && <Alert tone="positive">{notice}</Alert>}

      {/* A pending account authenticates on nothing at all, so somebody is
          sitting locked out until an administrator acts. The listing already
          sorts them to the top; this is what makes the reason visible without
          reading the badges. Only ever rendered from a successful read, and it
          counts what is on this page rather than the instance — the endpoint
          reports no instance-wide pending total, and inventing one from a page
          would state something the screen does not know (DESIGN.md §9). */}
      {pendingHere > 0 && (
        <Alert tone="warning">
          {t(
            pendingHere === 1
              ? "settings.adminUsers.pendingNoticeOne"
              : "settings.adminUsers.pendingNoticeOther",
            { count: formatNumber(pendingHere) },
          )}
        </Alert>
      )}

      <AdminUserCreateDialog
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onCreated={(username) => {
          setAddOpen(false);
          setActionError(null);
          setNotice(t("settings.adminUsers.addDone", { username }));
          void refresh();
        }}
        describe={describe}
      />

      <AdminUserResetDialog
        target={resetTarget}
        onClose={() => setResetTarget(null)}
        onDone={(username) => {
          setResetTarget(null);
          setActionError(null);
          setNotice(t("settings.adminUsers.resetDone", { username }));
          void refresh();
        }}
        describe={describe}
      />

      <AdminUserConfirms
        target={confirmTarget}
        onClose={() => {
          setConfirmTarget(null);
          setDialogError(null);
        }}
        onConfirm={handleConfirm}
        confirming={busy !== null && confirmTarget !== null}
        error={dialogError}
      />
    </div>
  );
}
