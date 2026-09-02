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
  const [actionError, setActionError] = useState<string | null>(null);
  // A failed removal is reported inside the confirmation dialog, which stays
  // open until the request finishes; the page-level Alert below the table
  // carries the failures that have no dialog (a role change).
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  // Member whose removal is pending confirmation in the ConfirmDialog.
  const [confirmTarget, setConfirmTarget] = useState<OrgMember | null>(null);

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

  async function handleRoleChange(member: OrgMember, next: string) {
    if (!isOrgRole(next) || next === member.role) return;
    setBusy(member.username);
    setActionError(null);
    const result = await updateMemberRole(org, member.username, next);
    setBusy(null);
    if (!result.ok) {
      const key = orgErrorKey(result);
      setActionError(key ? t(key) : errorMessage(t, result));
      // Re-read so the <select> snaps back to the role the server still holds.
      await refresh();
      return;
    }
    await refresh();
  }

  // The dialog stays open — with its confirm button in the "Removing…" state —
  // until the request has actually finished. Clearing the target first closed
  // it the instant the request left, which also made `confirming` permanently
  // false: the removal ran with nothing on screen to say so.
  async function handleRemove(member: OrgMember) {
    setBusy(member.username);
    setDialogError(null);
    setActionError(null);
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
                    disabled={busy === member.username}
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
                    disabled={busy === member.username}
                    onClick={() => {
                      // Clear the previous failure: the dialog is reused for
                      // every row, and a stale error under a different name
                      // reads as this removal having already failed.
                      setDialogError(null);
                      setConfirmTarget(member);
                    }}
                  >
                    {busy === member.username
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

      {/* Below the table, not above it: a failed role change or removal
          reported here used to push every remaining row down by the Alert's
          height, right where the next Role select or Remove button was
          (DESIGN.md §8.1). */}
      {actionError && <Alert tone="negative">{actionError}</Alert>}

      <ConfirmDialog
        open={confirmTarget !== null}
        onClose={() => {
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
        confirming={busy !== null && confirmTarget !== null}
        error={dialogError}
      />
    </div>
  );
}
