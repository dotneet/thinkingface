"use client";

import { Inbox, UserPlus, Users } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { orgRoleLabelKey } from "@/components/orgs/org-role-badge";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { Field, Input, Select } from "@/components/ui/field";
import { SkeletonLines } from "@/components/ui/skeleton";
import { TimeText } from "@/components/ui/time-text";
import { errorMessage } from "@/lib/api-error-message";
import { formatNumber } from "@/lib/format";
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
  const [members, setMembers] = useState<OrgMember[] | null>(null);
  const [total, setTotal] = useState<number | null>(null);
  const [offset, setOffset] = useState(0);
  const [loadError, setLoadError] = useState<string | null>(null);
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

  // Every fetch, whoever started it, is only allowed to write state if it is
  // still the newest one. The `cancelled` closure below covers the effect's
  // own supersession, but the mutation handlers reload by calling refresh
  // directly and have no closure to be cancelled by -- so a slow reload could
  // land on a page the admin has since moved away from.
  const latestRequest = useRef(0);

  // Memoised because every mutation below re-reads through it and the mount
  // effect depends on it; without this the effect would re-run on every render.
  const refresh = useCallback(
    async (isStale: () => boolean = () => false) => {
      const ticket = ++latestRequest.current;
      const result = await listMembers(org, { limit: PAGE_SIZE, offset });
      if (isStale() || ticket !== latestRequest.current) return;
      if (!result.ok) {
        setLoadError(errorMessage(t, result));
        setMembers(null);
        // Never carry a count over from a failed read: a count beside an
        // empty list states something the page does not know (DESIGN.md §9).
        setTotal(null);
        return;
      }
      setLoadError(null);
      setMembers(result.data.items);
      setTotal(result.data.total);
    },
    [org, offset],
  );

  // Guards against a stale response landing after a newer one -- a fast
  // Prev/Next, or `org` changing while a request from the previous one is
  // still in flight.
  useEffect(() => {
    let cancelled = false;
    refresh(() => cancelled);
    return () => {
      cancelled = true;
    };
  }, [refresh]);

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

  // "This page is empty" and "there is nothing here" are different answers,
  // and paging is what makes the difference reachable: deleting the last row
  // of the last page leaves the window past the end of a list that is not
  // empty at all (DESIGN.md §9). The dedicated empty state says which one it
  // is, and the range line below is skipped because `to` would be smaller
  // than `from`.
  const outOfRange = total !== null && total > 0 && offset >= total;

  const hasPrev = offset > 0;
  const hasNext = total !== null && offset + PAGE_SIZE < total;

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
          <EmptyState
            icon={Inbox}
            title={t("ui.pagination.outOfRangeTitle")}
            description={t("ui.pagination.outOfRangeDescription")}
            action={
              <Button size="sm" onClick={() => setOffset(0)}>
                {t("ui.pagination.backToFirstPage")}
              </Button>
            }
          />
        ) : (
          <EmptyState
            icon={Users}
            title={t("org.settings.members.emptyTitle")}
            description={t("org.settings.members.emptyDescription")}
          />
        )
      ) : (
        <div className="scroll-x rounded-lg border border-border">
          <table className="w-full min-w-[520px] border-collapse text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs font-medium text-fg-subtle">
                <th className="px-3 py-2 font-medium">{t("org.settings.members.colUsername")}</th>
                <th className="px-3 py-2 font-medium">{t("org.settings.members.colRole")}</th>
                <th className="px-3 py-2 font-medium">{t("org.settings.members.colJoined")}</th>
                <th className="px-3 py-2 text-right font-medium">
                  {t("org.settings.members.colActions")}
                </th>
              </tr>
            </thead>
            <tbody>
              {members.map((member) => (
                <tr key={member.username} className="border-b border-border last:border-0">
                  <td className="px-3 py-2">
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
                  </td>
                  <td className="px-3 py-2">
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
                  </td>
                  <td className="px-3 py-2 text-fg-subtle">
                    <TimeText iso={member.created_at} style="date" />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Button
                      variant="danger"
                      size="sm"
                      disabled={busy === member.username}
                      onClick={() => {
                        setDialogError(null);
                        setConfirmTarget(member);
                      }}
                    >
                      {busy === member.username
                        ? t("org.settings.members.removing")
                        : t("org.settings.members.remove")}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {!outOfRange && (hasPrev || hasNext) && (
        <div className="flex items-center justify-between text-sm text-fg-subtle">
          {/* A failed reload leaves total null, and a count rendered as 0
              under the error state would read as "this organisation has no
              members" rather than "we could not ask" (DESIGN.md §9). The
              buttons stay, because paging back is how you recover. */}
          <span className="tabular-nums">
            {total === null || members === null
              ? "—"
              : t("ui.pagination.range", {
                  from: formatNumber(offset + 1),
                  // From what actually arrived, not from the window's width:
                  // the count and the page are separate reads, so a short last
                  // page or a roster that changed between them would otherwise
                  // be described by a number no row backs up.
                  to: formatNumber(offset + members.length),
                  total: formatNumber(total),
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
