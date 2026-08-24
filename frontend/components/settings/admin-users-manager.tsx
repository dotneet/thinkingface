"use client";

import { ShieldCheck, ShieldOff, UserPlus, Users } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Dialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Checkbox, Field, Input } from "@/components/ui/field";
import { SearchInput } from "@/components/ui/search-input";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
import { adminUserErrorKey, createAdminUser, listAdminUsers, updateAdminUser } from "@/lib/admin";
import type { FailedApiResult } from "@/lib/api-error-message";
import { errorMessage } from "@/lib/api-error-message";
import { formatNumber } from "@/lib/format";
import { useT } from "@/lib/i18n/client";
import type { AdminUser } from "@/types/api";

/** Mirrors the backend's validatePassword (backend/internal/api/auth.go). */
const MIN_PASSWORD_LENGTH = 8;
/** The store's own default page size, restated so the two agree. */
const PAGE_SIZE = 50;

/**
 * The site administrator's account directory
 * (docs/dev/api-contract.md §1.3).
 *
 * Everything here is one endpoint, `PATCH /api/v1/admin/users/{username}`:
 * resetting a password and flipping the administrator flag are the same
 * partial update with different fields set.
 *
 * The viewer's own row never offers "Revoke admin". The backend refuses
 * self-demotion with a 400, and an affordance whose only outcome is an error
 * is worse than no affordance at all.
 */
export function AdminUsersManager({ viewer }: { viewer: string }) {
  const t = useT();
  const [users, setUsers] = useState<AdminUser[] | null>(null);
  const [total, setTotal] = useState<number | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);

  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  // The account whose password is being reset, and the account whose admin
  // flag is pending confirmation. Separate dialogs, separate state.
  const [resetTarget, setResetTarget] = useState<AdminUser | null>(null);
  const [resetPassword, setResetPassword] = useState("");
  const [resetConfirm, setResetConfirm] = useState("");
  const [resetError, setResetError] = useState<string | null>(null);
  const [adminTarget, setAdminTarget] = useState<AdminUser | null>(null);

  // The "add user" dialog. Its own fields rather than a shared form object:
  // the create and reset dialogs can never be open at once, but sharing state
  // between them would let one leave a value behind in the other.
  const [addOpen, setAddOpen] = useState(false);
  const [addUsername, setAddUsername] = useState("");
  const [addEmail, setAddEmail] = useState("");
  const [addPassword, setAddPassword] = useState("");
  const [addIsAdmin, setAddIsAdmin] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  // Localizes a failure through the endpoint-specific keys first, falling
  // back to the shared `error.type` mapping.
  const describe = useCallback(
    (result: FailedApiResult) => {
      const key = adminUserErrorKey(result);
      return key ? t(key) : errorMessage(t, result);
    },
    [t],
  );

  const refresh = useCallback(
    async (isStale: () => boolean = () => false) => {
      const result = await listAdminUsers({ search, limit: PAGE_SIZE, offset });
      if (isStale()) return;
      if (!result.ok) {
        setLoadError(describe(result));
        setUsers(null);
        // Never carry a count over from a failed read: an empty list next to a
        // stale total states something the page does not know (DESIGN.md §9).
        setTotal(null);
        return;
      }
      setLoadError(null);
      setUsers(result.data.items);
      setTotal(result.data.total);
    },
    [search, offset, describe],
  );

  // Guards against a fast search/page change letting an older, slower
  // response land after the newer one and overwrite it (e.g. typing "alice"
  // then quickly clearing the box could show alice's single result after the
  // full list already rendered).
  useEffect(() => {
    let cancelled = false;
    refresh(() => cancelled);
    return () => {
      cancelled = true;
    };
  }, [refresh]);

  function runSearch(query: string) {
    setSearch(query);
    setOffset(0);
  }

  async function handleReset(e: React.FormEvent) {
    e.preventDefault();
    if (!resetTarget) return;
    if (resetPassword !== resetConfirm) {
      setResetError(t("settings.account.mismatch"));
      return;
    }
    if (resetPassword.length < MIN_PASSWORD_LENGTH) {
      setResetError(t("settings.account.tooShort"));
      return;
    }
    const target = resetTarget;
    setBusy(target.username);
    setResetError(null);
    const result = await updateAdminUser(target.username, { password: resetPassword });
    setBusy(null);
    if (!result.ok) {
      setResetError(describe(result));
      return;
    }
    closeReset();
    setActionError(null);
    setNotice(t("settings.adminUsers.resetDone", { username: target.username }));
    await refresh();
  }

  function openAdd() {
    setAddUsername("");
    setAddEmail("");
    setAddPassword("");
    setAddIsAdmin(false);
    setAddError(null);
    setAddOpen(true);
  }

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    if (addPassword.length < MIN_PASSWORD_LENGTH) {
      setAddError(t("settings.account.tooShort"));
      return;
    }
    const username = addUsername.trim();
    setAdding(true);
    setAddError(null);
    const result = await createAdminUser({
      username,
      email: addEmail.trim(),
      password: addPassword,
      is_admin: addIsAdmin,
    });
    setAdding(false);
    if (!result.ok) {
      setAddError(describe(result));
      return;
    }
    setAddOpen(false);
    setActionError(null);
    setNotice(t("settings.adminUsers.addDone", { username: result.data.user.username }));
    await refresh();
  }

  function closeReset() {
    setResetTarget(null);
    setResetPassword("");
    setResetConfirm("");
    setResetError(null);
  }

  async function handleAdminToggle(target: AdminUser) {
    setAdminTarget(null);
    setBusy(target.username);
    setActionError(null);
    setNotice(null);
    const result = await updateAdminUser(target.username, { is_admin: !target.is_admin });
    setBusy(null);
    if (!result.ok) {
      setActionError(describe(result));
      return;
    }
    setNotice(t("settings.adminUsers.adminChanged", { username: target.username }));
    await refresh();
  }

  const hasPrev = offset > 0;
  const hasNext = total !== null && offset + PAGE_SIZE < total;

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
        <Button variant="primary" onClick={openAdd} className="px-3 py-1.5">
          <UserPlus size={15} />
          {t("settings.adminUsers.addUser")}
        </Button>
      </div>

      {actionError && <Alert tone="negative">{actionError}</Alert>}
      {notice && <Alert tone="positive">{notice}</Alert>}

      {users === null && !loadError ? (
        <SkeletonLines lines={5} />
      ) : users === null ? (
        <ErrorState
          title={t("settings.adminUsers.loadFailed")}
          message={loadError ?? t("settings.adminUsers.loadFailed")}
          hint={t("settings.adminUsers.loadFailedHint")}
        />
      ) : users.length === 0 ? (
        <EmptyState
          icon={Users}
          title={t("settings.adminUsers.emptyTitle")}
          description={t("settings.adminUsers.emptyDescription")}
        />
      ) : (
        <div className="scroll-x rounded-lg border border-border">
          <table className="w-full min-w-[640px] border-collapse text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
                <th className="px-3 py-2 font-medium">{t("settings.adminUsers.colUsername")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.adminUsers.colEmail")}</th>
                <th className="px-3 py-2 font-medium">{t("settings.adminUsers.colCreated")}</th>
                <th className="px-3 py-2 text-right font-medium">
                  {t("settings.adminUsers.colActions")}
                </th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => {
                const isSelf = user.username === viewer;
                return (
                  <tr key={user.id} className="border-b border-border last:border-0">
                    <td className="px-3 py-2">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-medium text-fg">{user.username}</span>
                        {user.is_admin && (
                          <Badge tone="accent">{t("settings.adminUsers.adminBadge")}</Badge>
                        )}
                        {isSelf && (
                          <span className="text-xs font-medium text-fg-subtle">
                            ({t("settings.adminUsers.you")})
                          </span>
                        )}
                      </div>
                    </td>
                    <td className="px-3 py-2 text-fg-muted">{user.email}</td>
                    <td className="px-3 py-2 text-fg-subtle">
                      <TimeText iso={user.created_at} style="date" />
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex justify-end gap-2">
                        <Button
                          size="sm"
                          disabled={busy === user.username}
                          onClick={() => {
                            setResetTarget(user);
                            setResetPassword("");
                            setResetConfirm("");
                            setResetError(null);
                          }}
                        >
                          {t("settings.adminUsers.resetPassword")}
                        </Button>
                        {/* Self-demotion is a 400 by design, so the control
                            is simply absent on your own row. */}
                        {!(isSelf && user.is_admin) && (
                          <Button
                            size="sm"
                            variant={user.is_admin ? "danger" : "secondary"}
                            disabled={busy === user.username}
                            onClick={() => setAdminTarget(user)}
                          >
                            {user.is_admin ? <ShieldOff size={13} /> : <ShieldCheck size={13} />}
                            {busy === user.username
                              ? t("settings.adminUsers.working")
                              : user.is_admin
                                ? t("settings.adminUsers.demote")
                                : t("settings.adminUsers.promote")}
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {(hasPrev || hasNext) && (
        <div className="flex items-center justify-between text-sm text-fg-subtle">
          <span className="tabular-nums">
            {t("ui.pagination.range", {
              from: offset + 1,
              to: Math.min(offset + PAGE_SIZE, total ?? 0),
              total: formatNumber(total ?? 0),
            })}
          </span>
          <div className="flex gap-2">
            <Button
              size="sm"
              disabled={!hasPrev}
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            >
              {t("ui.pagination.prev")}
            </Button>
            <Button size="sm" disabled={!hasNext} onClick={() => setOffset(offset + PAGE_SIZE)}>
              {t("ui.pagination.next")}
            </Button>
          </div>
        </div>
      )}

      <Dialog
        open={addOpen}
        onClose={() => setAddOpen(false)}
        title={t("settings.adminUsers.addTitle")}
        footer={
          <>
            <Button type="button" onClick={() => setAddOpen(false)} disabled={adding}>
              {t("ui.confirmDialog.defaultCancel")}
            </Button>
            <Button
              type="submit"
              form="admin-create-user"
              variant="primary"
              disabled={adding || !addUsername.trim() || !addEmail.trim() || !addPassword}
            >
              {adding ? t("settings.adminUsers.addSubmitting") : t("settings.adminUsers.addSubmit")}
            </Button>
          </>
        }
        footerNote={addError ? <Alert tone="negative">{addError}</Alert> : undefined}
      >
        <form id="admin-create-user" onSubmit={handleAdd} className="flex flex-col gap-4 px-4 py-4">
          <p className="text-sm text-fg-muted">{t("settings.adminUsers.addDescription")}</p>
          <Field
            label={t("settings.adminUsers.addUsernameLabel")}
            hint={t("settings.adminUsers.addUsernameHint")}
          >
            <Input
              value={addUsername}
              onChange={(e) => {
                setAddUsername(e.target.value);
                setAddError(null);
              }}
              placeholder={t("settings.adminUsers.addUsernamePlaceholder")}
              autoComplete="off"
              required
            />
          </Field>
          <Field label={t("settings.adminUsers.addEmailLabel")}>
            <Input
              type="email"
              value={addEmail}
              onChange={(e) => {
                setAddEmail(e.target.value);
                setAddError(null);
              }}
              autoComplete="off"
              required
            />
          </Field>
          <Field
            label={t("settings.adminUsers.addPasswordLabel")}
            hint={t("settings.account.newPasswordHint")}
          >
            <Input
              type="password"
              value={addPassword}
              onChange={(e) => {
                setAddPassword(e.target.value);
                setAddError(null);
              }}
              autoComplete="new-password"
              required
            />
          </Field>
          <label className="flex items-start gap-2 text-sm">
            <Checkbox
              checked={addIsAdmin}
              onChange={(e) => setAddIsAdmin(e.target.checked)}
              className="mt-1"
            />
            <span className="flex flex-col gap-0.5">
              <span className="font-medium text-fg-muted">
                {t("settings.adminUsers.addIsAdminLabel")}
              </span>
              <span className="text-xs font-medium text-fg-subtle">
                {t("settings.adminUsers.addIsAdminHint")}
              </span>
            </span>
          </label>
        </form>
      </Dialog>

      <Dialog
        open={resetTarget !== null}
        onClose={closeReset}
        title={t("settings.adminUsers.resetTitle", { username: resetTarget?.username ?? "" })}
        footer={
          <>
            <Button type="button" onClick={closeReset} disabled={busy !== null}>
              {t("ui.confirmDialog.defaultCancel")}
            </Button>
            <Button
              type="submit"
              form="admin-reset-password"
              variant="primary"
              disabled={busy !== null || !resetPassword || !resetConfirm}
            >
              {busy !== null
                ? t("settings.adminUsers.resetSubmitting")
                : t("settings.adminUsers.resetSubmit")}
            </Button>
          </>
        }
        footerNote={resetError ? <Alert tone="negative">{resetError}</Alert> : undefined}
      >
        <form
          id="admin-reset-password"
          onSubmit={handleReset}
          className="flex flex-col gap-4 px-4 py-4"
        >
          <p className="text-sm text-fg-muted">
            {t("settings.adminUsers.resetDescription", {
              username: resetTarget?.username ?? "",
            })}
          </p>
          <Field
            label={t("settings.adminUsers.resetNewPasswordLabel")}
            hint={t("settings.account.newPasswordHint")}
          >
            <Input
              type="password"
              value={resetPassword}
              onChange={(e) => {
                setResetPassword(e.target.value);
                setResetError(null);
              }}
              autoComplete="new-password"
              required
            />
          </Field>
          <Field label={t("settings.adminUsers.resetConfirmLabel")}>
            <Input
              type="password"
              value={resetConfirm}
              onChange={(e) => {
                setResetConfirm(e.target.value);
                setResetError(null);
              }}
              autoComplete="new-password"
              required
            />
          </Field>
        </form>
      </Dialog>

      <ConfirmDialog
        open={adminTarget !== null}
        onClose={() => setAdminTarget(null)}
        onConfirm={() => {
          if (adminTarget) void handleAdminToggle(adminTarget);
        }}
        tone={adminTarget?.is_admin ? "danger" : "primary"}
        title={t(
          adminTarget?.is_admin
            ? "settings.adminUsers.demoteTitle"
            : "settings.adminUsers.promoteTitle",
          { username: adminTarget?.username ?? "" },
        )}
        description={
          <p className="text-sm text-fg-muted">
            {t(
              adminTarget?.is_admin
                ? "settings.adminUsers.demoteDescription"
                : "settings.adminUsers.promoteDescription",
              { username: adminTarget?.username ?? "" },
            )}
          </p>
        }
        confirmLabel={t(
          adminTarget?.is_admin
            ? "settings.adminUsers.demoteConfirm"
            : "settings.adminUsers.promoteConfirm",
        )}
        confirmingLabel={t("settings.adminUsers.working")}
        confirming={busy !== null && adminTarget !== null}
      />
    </div>
  );
}
