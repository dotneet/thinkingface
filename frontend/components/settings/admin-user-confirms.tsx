"use client";

import { Alert } from "@/components/ui/alert";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { useT } from "@/lib/i18n/client";
import type { AdminUser } from "@/types/api";

/**
 * The four account operations that ask before they act.
 *
 * - `admin` — promote or demote a site administrator.
 * - `disable` — suspend or restore. Reversible: nothing is destroyed and
 *   restoring gives it all back, so a plain yes/no.
 * - `hold` — send an approved account back to the sign-up waiting room.
 *   Reversible like suspension, but it does revoke the account's sessions, so
 *   it is not silent either. (Approving asks nothing: it only grants, and a
 *   dialog in front of the one action an administrator came here to perform is
 *   friction with nothing to protect.)
 * - `revoke` — delete every token and SSH key the account holds. The one
 *   irreversible operation on this screen, so it asks for the username to be
 *   typed, the same bar repository and run deletion use.
 */
export type AdminUserConfirmKind = "admin" | "disable" | "hold" | "revoke";

/**
 * Which operation is pending, and on whom. **One** piece of state rather than
 * one per kind: the four dialogs are mutually exclusive by construction, and
 * the previous shape — a separate `AdminUser | null` for each — made "two open
 * at once" representable, and grew by one variable per new operation.
 */
export type AdminUserConfirmTarget = { kind: AdminUserConfirmKind; user: AdminUser };

export function AdminUserConfirms({
  target,
  onClose,
  onConfirm,
}: {
  target: AdminUserConfirmTarget | null;
  onClose: () => void;
  onConfirm: (target: AdminUserConfirmTarget) => void;
}) {
  const t = useT();
  const user = target?.user;
  const username = user?.username ?? "";
  const kind = target?.kind;

  // Every dialog is rendered unconditionally with its own `open`, so each one
  // keeps its own mount (and, for `revoke`, its typed confirmation) instead of
  // one component swapping copy underneath the user mid-animation.
  //
  // None of them takes `confirming`: confirming closes the dialog and *then*
  // starts the write, so by the time anything is in flight `target` is already
  // null and no dialog is open to show a "Working…" button. The prop used to
  // be passed and was dead by construction. Showing progress here would mean
  // holding the dialog open across the write instead, which is a different
  // design, not a missing prop.
  const isOpen = (k: AdminUserConfirmKind) => kind === k;
  const fire = () => {
    if (target) onConfirm(target);
  };

  return (
    <>
      <ConfirmDialog
        open={isOpen("admin")}
        onClose={onClose}
        onConfirm={fire}
        tone={user?.is_admin ? "danger" : "primary"}
        title={t(
          user?.is_admin ? "settings.adminUsers.demoteTitle" : "settings.adminUsers.promoteTitle",
          { username },
        )}
        description={
          <p className="text-sm text-fg-muted">
            {t(
              user?.is_admin
                ? "settings.adminUsers.demoteDescription"
                : "settings.adminUsers.promoteDescription",
              { username },
            )}
          </p>
        }
        confirmLabel={t(
          user?.is_admin
            ? "settings.adminUsers.demoteConfirm"
            : "settings.adminUsers.promoteConfirm",
        )}
      />

      {/* Reversible, so a plain yes/no dialog: no typed confirmation. */}
      <ConfirmDialog
        open={isOpen("disable")}
        onClose={onClose}
        onConfirm={fire}
        tone={user?.disabled ? "primary" : "danger"}
        title={t(
          user?.disabled ? "settings.adminUsers.restoreTitle" : "settings.adminUsers.suspendTitle",
          { username },
        )}
        description={
          <p className="text-sm text-fg-muted">
            {t(
              user?.disabled
                ? "settings.adminUsers.restoreDescription"
                : "settings.adminUsers.suspendDescription",
              { username },
            )}
          </p>
        }
        confirmLabel={t(
          user?.disabled
            ? "settings.adminUsers.restoreConfirm"
            : "settings.adminUsers.suspendConfirm",
        )}
      />

      {/* Reversible like suspension, so a plain yes/no dialog — but it does
          revoke the account's sessions, so it is not silent either. */}
      <ConfirmDialog
        open={isOpen("hold")}
        onClose={onClose}
        onConfirm={fire}
        tone="danger"
        title={t("settings.adminUsers.holdTitle", { username })}
        description={
          <p className="text-sm text-fg-muted">
            {t("settings.adminUsers.holdDescription", { username })}
          </p>
        }
        confirmLabel={t("settings.adminUsers.holdConfirm")}
      />

      {/* Irreversible, so it asks for the username to be typed — the same bar
          the repository and run deletions use. */}
      <ConfirmDialog
        open={isOpen("revoke")}
        onClose={onClose}
        onConfirm={fire}
        requireText={isOpen("revoke") ? username : undefined}
        title={t("settings.adminUsers.revokeTitle", { username })}
        description={
          <Alert tone="negative">{t("settings.adminUsers.revokeDescription", { username })}</Alert>
        }
        confirmLabel={t("settings.adminUsers.revokeConfirm")}
      />
    </>
  );
}
