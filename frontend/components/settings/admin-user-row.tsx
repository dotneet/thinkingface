"use client";

import { Clock, KeyRound, ShieldCheck, ShieldOff, UserCheck, UserX } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SpinnerSlot } from "@/components/ui/spinner";
import { Td, Tr } from "@/components/ui/table";
import { TimeText } from "@/components/ui/time-text";
import { useT } from "@/lib/i18n/client";
import type { AdminUser } from "@/types/api";
import { UserApprovalPending } from "@/types/api";

/**
 * Everything a row can ask the directory to do, in one object.
 *
 * One prop rather than six callbacks spelled out at the call site: the set
 * grows every time an account operation is added, and threading each new one
 * through the row's signature by hand is how the previous version of this
 * screen ended up with five parallel "target user" states.
 */
export type AdminUserActions = {
  resetPassword: (user: AdminUser) => void;
  approve: (user: AdminUser) => void;
  hold: (user: AdminUser) => void;
  toggleDisabled: (user: AdminUser) => void;
  revokeCredentials: (user: AdminUser) => void;
  toggleAdmin: (user: AdminUser) => void;
};

/**
 * One account in the directory.
 *
 * The viewer's own row never offers "Revoke admin", "Suspend", "Put back" or
 * "Revoke credentials": the backend refuses all four on your own account with
 * a 400, and an affordance whose only outcome is an error is worse than no
 * affordance at all.
 */
export function AdminUserRow({
  user,
  isSelf,
  busy,
  actions,
}: {
  user: AdminUser;
  isSelf: boolean;
  /** True while any write against this account is in flight. */
  busy: boolean;
  actions: AdminUserActions;
}) {
  const t = useT();
  return (
    <Tr>
      <Td>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-fg">{user.username}</span>
          {user.is_admin && <Badge tone="accent">{t("settings.adminUsers.adminBadge")}</Badge>}
          {/* A suspended account looks exactly like an active one everywhere
              else on this row, so the badge is the only thing that tells them
              apart. */}
          {user.disabled && <Badge tone="negative">{t("settings.adminUsers.disabledBadge")}</Badge>}
          {/* The waiting room is invisible everywhere else on this row: a
              pending account looks exactly like an active one, and answers
              even the correct password with a refusal. */}
          {user.approval === UserApprovalPending && (
            <Badge tone="warning">{t("settings.adminUsers.pendingBadge")}</Badge>
          )}
          {isSelf && (
            <span className="text-xs font-medium text-fg-subtle">
              ({t("settings.adminUsers.you")})
            </span>
          )}
        </div>
      </Td>
      <Td className="text-fg-muted">{user.email}</Td>
      <Td className="text-fg-subtle">
        <TimeText iso={user.created_at} style="date" />
      </Td>
      {/* "Never" is spelled out rather than left blank: an empty cell reads as
          missing data, and the whole point of this column is telling a dormant
          account from a live one. */}
      <Td className="text-fg-subtle">
        {user.last_login_at ? (
          <TimeText iso={user.last_login_at} style="date" />
        ) : (
          t("settings.adminUsers.neverLoggedIn")
        )}
      </Td>
      <Td>
        <div className="flex items-center justify-end gap-2">
          {/* A reserved slot, not `{busy && <Spinner/>}`: it is the only sign
              that an action with no dialog (Approve) is in flight, and it must
              not shift the buttons beside it while it appears
              (DESIGN.md §8.3). */}
          <SpinnerSlot active={busy} size={14} label={t("settings.adminUsers.working")} />
          <Button size="sm" disabled={busy} onClick={() => actions.resetPassword(user)}>
            {t("settings.adminUsers.resetPassword")}
          </Button>
          {/* Approving is the one action that lets somebody in at all, so it
              leads. Putting an account *back* is offered only for an approved
              one, and never on your own row: the write revokes the session
              this page is running on, which the backend refuses with a 400. */}
          {user.approval === UserApprovalPending ? (
            <Button
              size="sm"
              variant="primary"
              disabled={busy}
              onClick={() => actions.approve(user)}
            >
              <UserCheck size={13} />
              {t("settings.adminUsers.approve")}
            </Button>
          ) : (
            !isSelf && (
              <Button size="sm" disabled={busy} onClick={() => actions.hold(user)}>
                <Clock size={13} />
                {t("settings.adminUsers.hold")}
              </Button>
            )
          )}
          {/* Suspending or revoking your own account is a 400 by design, so
              neither control appears on your own row (the same rule as the
              admin toggle below). */}
          {!isSelf && (
            <Button
              size="sm"
              variant={user.disabled ? "secondary" : "danger"}
              disabled={busy}
              onClick={() => actions.toggleDisabled(user)}
            >
              {user.disabled ? <UserCheck size={13} /> : <UserX size={13} />}
              {user.disabled ? t("settings.adminUsers.restore") : t("settings.adminUsers.suspend")}
            </Button>
          )}
          {!isSelf && (
            <Button size="sm" disabled={busy} onClick={() => actions.revokeCredentials(user)}>
              <KeyRound size={13} />
              {t("settings.adminUsers.revokeCredentials")}
            </Button>
          )}
          {/* Self-demotion is a 400 by design, so the control is simply absent
              on your own row. */}
          {!(isSelf && user.is_admin) && (
            <Button
              size="sm"
              variant={user.is_admin ? "danger" : "secondary"}
              disabled={busy}
              onClick={() => actions.toggleAdmin(user)}
            >
              {user.is_admin ? <ShieldOff size={13} /> : <ShieldCheck size={13} />}
              {busy
                ? t("settings.adminUsers.working")
                : user.is_admin
                  ? t("settings.adminUsers.demote")
                  : t("settings.adminUsers.promote")}
            </Button>
          )}
        </div>
      </Td>
    </Tr>
  );
}
