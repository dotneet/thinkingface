import Link from "next/link";
import { NamespaceAvatar } from "@/components/namespace/namespace-avatar";
import { OrgRoleBadge, orgRoleLabelKey } from "@/components/orgs/org-role-badge";
import { formatNumber } from "@/lib/format";
import { getT } from "@/lib/i18n/server";
import { namespaceHref } from "@/lib/namespace";
import { isOrgMember } from "@/lib/orgs";
import type { Org } from "@/types/api";

/** One organisation in the public directory listing. */
export async function OrgCard({ org }: { org: Org }) {
  const t = await getT();
  return (
    <Link
      href={namespaceHref(org.name)}
      className="group flex items-start gap-3 rounded-lg border border-border bg-bg-raised p-4 transition-colors hover:border-border-strong hover:bg-bg-hover"
    >
      <NamespaceAvatar name={org.name} avatarUrl={org.avatar_url} kind="org" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="truncate text-sm font-medium text-fg group-hover:underline">
            {org.display_name || org.name}
          </span>
          {isOrgMember(org) && (
            <OrgRoleBadge role={org.viewer_role} label={t(orgRoleLabelKey(org.viewer_role))} />
          )}
        </div>
        {org.display_name && (
          <p className="truncate font-mono text-xs font-medium text-fg-subtle">{org.name}</p>
        )}
        {org.description && (
          <p className="line-clamp-2 text-sm text-fg-subtle">{org.description}</p>
        )}
        <div className="mt-1 flex flex-wrap items-center gap-3 text-xs font-medium text-fg-subtle">
          <span className="tabular-nums">
            {t(org.num_members === 1 ? "org.directory.membersOne" : "org.directory.membersOther", {
              count: formatNumber(org.num_members),
            })}
          </span>
          <span className="tabular-nums">
            {t(org.num_repos === 1 ? "org.directory.reposOne" : "org.directory.reposOther", {
              count: formatNumber(org.num_repos),
            })}
          </span>
        </div>
      </div>
    </Link>
  );
}
