import { Users } from "lucide-react";
import Link from "next/link";
import { OrgRoleBadge, orgRoleLabelKey } from "@/components/orgs/org-role-badge";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { namespaceHref } from "@/lib/namespace";
import { listMembers, orgSettingsHref } from "@/lib/orgs";
import { authHeaders } from "@/lib/server-auth";

/**
 * The member list on an organisation's namespace page (`/{ns}?tab=members`).
 *
 * `GET /orgs/{org}/members` answers 403 for a non-member unless the
 * organisation opted into `members_visibility = "public"` (§4 note 1), so a
 * failure here is a *state* — "private" — not an error to shout about. Only a
 * successful 200 renders a list.
 */
export async function OrgMembersPanel({ ns, canAdmin }: { ns: string; canAdmin: boolean }) {
  const [t, headers] = await Promise.all([getT(), authHeaders()]);
  const result = await listMembers(ns, { headers });

  if (!result.ok) {
    // 401/403 is the documented "you may not see this" answer; anything else
    // (the backend being down, a 500) is a genuine failure and says so.
    const hidden = result.status === 401 || result.status === 403;
    return (
      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-semibold text-fg">{t("org.page.membersTitle")}</h2>
        {hidden ? (
          <EmptyState
            icon={Users}
            title={t("org.page.membersHiddenTitle")}
            description={t("org.page.membersHiddenDescription")}
          />
        ) : (
          <ErrorState
            title={t("org.page.loadFailedTitle")}
            message={errorMessage(t, result)}
            hint={t("org.page.loadFailedHint")}
          />
        )}
      </section>
    );
  }

  const members = result.data.items;

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-sm font-semibold text-fg">{t("org.page.membersTitle")}</h2>
        {canAdmin && (
          <Link
            href={`${orgSettingsHref(ns)}/members`}
            className={buttonClass({ variant: "ghost", size: "sm" })}
          >
            {t("org.page.manageMembers")}
          </Link>
        )}
      </div>
      {members.length === 0 ? (
        <EmptyState
          icon={Users}
          title={t("org.page.membersEmptyTitle")}
          description={t("org.page.membersEmptyDescription")}
        />
      ) : (
        <ul className="flex flex-wrap gap-2">
          {members.map((member) => (
            <li
              key={member.username}
              className="flex items-center gap-2 rounded-md border border-border bg-bg-raised px-2.5 py-1.5 text-sm"
            >
              <Link
                href={namespaceHref(member.username)}
                className="font-medium text-fg hover:underline"
              >
                {member.username}
              </Link>
              <OrgRoleBadge role={member.role} label={t(orgRoleLabelKey(member.role))} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
