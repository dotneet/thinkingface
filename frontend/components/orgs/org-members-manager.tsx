"use client";

import { UserPlus, Users } from "lucide-react";
import { useState } from "react";
import { orgRoleLabelKey } from "@/components/orgs/org-role-badge";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input, Select } from "@/components/ui/field";
import { OutOfRangeEmptyState, PaginationControls } from "@/components/ui/pagination-controls";
import { SkeletonLines } from "@/components/ui/skeleton";
import { Table, TBody, Td, THead, Th, Tr } from "@/components/ui/table";
import { TimeText } from "@/components/ui/time-text";
import { usePagedList } from "@/hooks/use-paged-list";
import { errorMessage } from "@/lib/api-error-message";
import { useT } from "@/lib/i18n/client";
import {
  addMember,
  isOrgRole,
  listMembers,
  ORG_ROLES,
  orgErrorKey,
  removeMember,
  updateMemberRole,
} from "@/lib/orgs";
import type { OrgMember, OrgRole } from "@/types/api";

// One screen of members. The server clamps to 200 whatever is asked for
// (store.MaxOrgPageSize), so this only has to be a comfortable page.
const PAGE_SIZE = 50;

export function OrgMembersManager({
  org,
  /** Username of the signed-in admin, so their own row can be marked. */
  viewer,
}: {
  org: string;
  viewer: string;
}) {
  const t = useT();
  // Every write on this screen (remove, role change) is started from a
  // confirmation dialog and reported back inside it. That is the only place
  // the message renders, so both dialogs ignore a close while their request is
  // in flight — dismissing one mid-request would leave the failure with
  // nowhere to appear, and the <select> silently snapping back after refresh()
  // is not a report. There is deliberately no page-level fallback Alert: with
  // the dialog held open, the error is always on screen.
  const [dialogError, setDialogError] = useState<string | null>(null);
  // Which member has a write in flight, and which one — the Remove button used
  // to say "Removing…" while a role change was running, because both wrote the
  // same bare username here.
  const [busy, setBusy] = useState<{ username: string; kind: "remove" | "role" } | null>(null);
  // Member whose removal is pending confirmation in the ConfirmDialog.
  const [confirmTarget, setConfirmTarget] = useState<OrgMember | null>(null);
  // Role change pending confirmation: the member and the role they would move
  // to. A privilege change is confirmed here for the same reason the admin
  // screens confirm one (components/settings/admin-user-confirms.tsx) — the
  // trigger is a native <select>, so a scroll wheel over a focused control or
  // a mis-release in the open dropdown used to promote a read-only member to
  // organisation admin with no prompt and nothing to undo it.
  const [confirmRole, setConfirmRole] = useState<{ member: OrgMember; next: OrgRole } | null>(null);

  const [username, setUsername] = useState("");
  const [role, setRole] = useState<OrgRole>("read");
  const [adding, setAdding] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);

  const {
    items: members,
    total,
    offset,
    setOffset,
    loadError,
    reload: refresh,
    outOfRange,
    pager,
  } = usePagedList({
    pageSize: PAGE_SIZE,
    deps: [org],
    fetchPage: ({ limit, offset }) => listMembers(org, { limit, offset }),
    describe: (result) => errorMessage(t, result),
  });

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    const name = username.trim();
    if (!name) return;
    setAdding(true);
    setAddError(null);
    const result = await addMember(org, { username: name, role });
    setAdding(false);
    if (!result.ok) {
      // A 404 here is the *user*, not the organisation — the org resolved a
      // moment ago to render this screen.
      const key = orgErrorKey(result, { 404: "org.errors.userNotFound" });
      setAddError(key ? t(key) : errorMessage(t, result));
      return;
    }
    setUsername("");
    setRole("read");
    await refresh();
  }

  // The <select> is controlled on `member.role`, so declining the dialog (or
  // simply opening it) re-renders the row back to the role the server holds:
  // nothing is written until the confirmation fires.
  function handleRoleChange(member: OrgMember, next: string) {
    if (!isOrgRole(next) || next === member.role) return;
    // Clear the previous failure: both dialogs share the slot, and a stale
    // error under a different name reads as this change having already failed.
    setDialogError(null);
    setConfirmRole({ member, next });
  }

  async function applyRoleChange(member: OrgMember, next: OrgRole) {
    setBusy({ username: member.username, kind: "role" });
    setDialogError(null);
    const result = await updateMemberRole(org, member.username, next);
    setBusy(null);
    if (!result.ok) {
      const key = orgErrorKey(result);
      setDialogError(key ? t(key) : errorMessage(t, result));
      // Re-read so the <select> snaps back to the role the server still holds.
      await refresh();
      return;
    }
    setConfirmRole(null);
    await refresh();
  }

  // The dialog stays open — with its confirm button in the "Removing…" state —
  // until the request has actually finished. Clearing the target first closed
  // it the instant the request left, which also made `confirming` permanently
  // false: the removal ran with nothing on screen to say so.
  async function handleRemove(member: OrgMember) {
    setBusy({ username: member.username, kind: "remove" });
    setDialogError(null);
    const result = await removeMember(org, member.username);
    setBusy(null);
    if (!result.ok) {
      const key = orgErrorKey(result);
      setDialogError(key ? t(key) : errorMessage(t, result));
      return;
    }
    setConfirmTarget(null);
    await refresh();
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h2 className="text-sm font-semibold text-fg">{t("org.settings.members.title")}</h2>
        <p className="mt-1 text-sm text-fg-subtle">{t("org.settings.members.description")}</p>
      </div>

      <form
        onSubmit={handleAdd}
        className="flex flex-col gap-3 rounded-lg border border-border bg-bg-sunken p-4"
      >
        <span className="text-sm font-semibold text-fg">{t("org.settings.members.addTitle")}</span>
        <div className="flex flex-wrap items-start gap-3">
          <Field label={t("org.settings.members.usernameLabel")} className="min-w-[200px] flex-1">
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder={t("org.settings.members.usernamePlaceholder")}
              autoComplete="off"
              required
            />
          </Field>
          <Field
            label={t("org.settings.members.roleLabel")}
            className="w-44"
            hint={t("org.settings.members.roleHint")}
          >
            <Select
              value={role}
              onChange={(e) => {
                if (isOrgRole(e.target.value)) setRole(e.target.value);
              }}
            >
              {ORG_ROLES.map((r) => (
                <option key={r} value={r}>
                  {t(orgRoleLabelKey(r))}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <Button
          type="submit"
          variant="primary"
          disabled={adding || !username.trim()}
          className="self-start px-4 py-2"
        >
          <UserPlus size={15} />
          {adding ? t("org.settings.members.adding") : t("org.settings.members.add")}
        </Button>
        {/* Below the submit button so a failed add never pushes it down
            right before the retry click (DESIGN.md §8). */}
        {addError && <Alert tone="negative">{addError}</Alert>}
      </form>

      {members === null && !loadError ? (
        <SkeletonLines lines={4} />
      ) : members === null ? (
        <ErrorState
          title={t("org.settings.members.loadFailed")}
          message={loadError ?? t("org.settings.members.loadFailed")}
          hint={t("org.settings.members.loadFailedHint")}
        />
      ) : members.length === 0 ? (
        outOfRange ? (
          <OutOfRangeEmptyState onBackToFirstPage={() => setOffset(0)} />
        ) : (
          <EmptyState
            icon={Users}
            title={t("org.settings.members.emptyTitle")}
            description={t("org.settings.members.emptyDescription")}
          />
        )
      ) : (
        <Table minWidth={520}>
          <THead>
            <Th>{t("org.settings.members.colUsername")}</Th>
            <Th>{t("org.settings.members.colRole")}</Th>
            <Th>{t("org.settings.members.colJoined")}</Th>
            <Th align="right">{t("org.settings.members.colActions")}</Th>
          </THead>
          <TBody>
            {members.map((member) => (
              <Tr key={member.username}>
                <Td>
                  <span className="font-medium text-fg">{member.username}</span>
                  {member.username === viewer && (
                    <span className="ml-2 text-xs font-medium text-fg-subtle">
                      ({t("org.settings.members.you")})
                    </span>
                  )}
                  {member.email && (
                    <div className="truncate text-xs font-medium text-fg-subtle">
                      {member.email}
                    </div>
                  )}
                </Td>
                <Td>
                  <Select
                    value={member.role}
                    disabled={busy?.username === member.username}
                    aria-label={t("org.settings.members.colRole")}
                    onChange={(e) => handleRoleChange(member, e.target.value)}
                    className="w-36 py-1 text-xs"
                  >
                    {ORG_ROLES.map((r) => (
                      <option key={r} value={r}>
                        {t(orgRoleLabelKey(r))}
                      </option>
                    ))}
                  </Select>
                </Td>
                <Td className="text-fg-subtle">
                  <TimeText iso={member.created_at} style="date" />
                </Td>
                <Td align="right">
                  <Button
                    variant="danger"
                    size="sm"
                    disabled={busy?.username === member.username}
                    onClick={() => {
                      // Clear the previous failure: the dialog is reused for
                      // every row, and a stale error under a different name
                      // reads as this removal having already failed.
                      setDialogError(null);
                      setConfirmTarget(member);
                    }}
                  >
                    {busy?.username === member.username && busy.kind === "remove"
                      ? t("org.settings.members.removing")
                      : t("org.settings.members.remove")}
                  </Button>
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}

      <PaginationControls pager={pager} />

      <ConfirmDialog
        open={confirmTarget !== null}
        // Ignored while the DELETE is in flight: Escape, a backdrop click or
        // the header × would read as a cancel for a request that is still on
        // its way, and would take the only place its failure renders with it.
        onClose={() => {
          if (busy) return;
          setConfirmTarget(null);
          setDialogError(null);
        }}
        onConfirm={() => {
          if (confirmTarget) void handleRemove(confirmTarget);
        }}
        title={t("org.settings.members.confirmRemoveTitle", {
          username: confirmTarget?.username ?? "",
        })}
        description={
          <p className="text-sm text-fg-muted">
            {t("org.settings.members.confirmRemove", {
              username: confirmTarget?.username ?? "",
              org,
            })}
          </p>
        }
        confirmLabel={t("org.settings.members.remove")}
        confirmingLabel={t("org.settings.members.removing")}
        confirming={busy?.kind === "remove"}
        error={dialogError}
      />

      <ConfirmDialog
        open={confirmRole !== null}
        // Same guard as the removal dialog above: a PATCH dismissed mid-flight
        // would 403 into a dialog that is no longer mounted, leaving the user
        // with a <select> that quietly snaps back and no explanation.
        onClose={() => {
          if (busy) return;
          setConfirmRole(null);
          setDialogError(null);
        }}
        onConfirm={() => {
          if (confirmRole) void applyRoleChange(confirmRole.member, confirmRole.next);
        }}
        // Admin hands over the member list and the organisation's settings —
        // the one direction that grants rather than narrows, so it gets the
        // destructive styling the admin screens' promote dialog uses.
        tone={confirmRole?.next === "admin" ? "danger" : "primary"}
        title={t("org.settings.members.confirmRoleTitle", {
          username: confirmRole?.member.username ?? "",
        })}
        description={
          <>
            <p className="text-sm text-fg-muted">
              {t("org.settings.members.confirmRole", {
                username: confirmRole?.member.username ?? "",
                org,
                from: confirmRole ? t(orgRoleLabelKey(confirmRole.member.role)) : "",
                to: confirmRole ? t(orgRoleLabelKey(confirmRole.next)) : "",
              })}
            </p>
            {confirmRole?.next === "admin" && (
              <p className="mt-2 text-sm text-fg-muted">
                {t("org.settings.members.confirmRoleAdmin")}
              </p>
            )}
          </>
        }
        confirmLabel={t("org.settings.members.confirmRoleConfirm")}
        confirmingLabel={t("org.settings.members.changingRole")}
        confirming={busy?.kind === "role"}
        error={dialogError}
      />
    </div>
  );
}
