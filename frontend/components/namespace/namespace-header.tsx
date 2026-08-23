import { Boxes, Database, Globe, Settings, UserPen, Users } from "lucide-react";
import Link from "next/link";
import { NamespaceAvatar } from "@/components/namespace/namespace-avatar";
import { OrgRoleBadge, orgRoleLabelKey } from "@/components/orgs/org-role-badge";
import { Badge } from "@/components/ui/badge";
import { buttonClass } from "@/components/ui/button";
import { formatDate, formatNumber } from "@/lib/format";
import { getLocale, getT } from "@/lib/i18n/server";
import { safeExternalHref } from "@/lib/namespace";
import { orgSettingsHref } from "@/lib/orgs";
import { getCurrentUser } from "@/lib/session";
import type { NamespaceProfile } from "@/types/api";

/**
 * Identity block at the top of `/{ns}` — the same one for a user and for an
 * organisation, with the kind carried by a badge rather than by a different
 * layout (docs/dev/namespace-design.md §4.3).
 *
 * The namespace name is always visible even when a display name is set: it is
 * the identifier that appears in the URL and in every `repo_id`.
 */
export async function NamespaceHeader({ profile }: { profile: NamespaceProfile }) {
  const [t, locale, me] = await Promise.all([getT(), getLocale(), getCurrentUser()]);
  // Only an http(s) website becomes a link (see safeExternalHref).
  const website = safeExternalHref(profile.website);
  const isOrg = profile.kind === "org";
  // `viewer_role` is "" for someone with no relationship to the namespace,
  // which tygo types as OrgRole (its three named constants), so the empty
  // case has to be compared through `string` — same as lib/orgs.ts.
  const role = profile.viewer_role as string;
  // `can_edit` is also true for a site admin looking at somebody else's user
  // namespace, but `PATCH /api/v1/me/profile` only ever edits your own
  // profile — so the "Edit profile" button needs the stronger test that this
  // namespace *is* the viewer. Organisations have a real admin screen, so
  // `can_edit` is the right gate there.
  const isOwnProfile = me.ok && me.data.user.username.toLowerCase() === profile.name.toLowerCase();

  return (
    <div className="flex flex-wrap items-start gap-4">
      <NamespaceAvatar
        name={profile.name}
        avatarUrl={profile.avatar_url}
        kind={profile.kind}
        size={64}
      />

      <div className="flex min-w-0 flex-1 flex-col gap-1.5">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">
            {profile.display_name || profile.name}
          </h1>
          <Badge tone="muted">{t(isOrg ? "namespace.kind.org" : "namespace.kind.user")}</Badge>
          {isOrg && role !== "" && (
            <OrgRoleBadge
              role={profile.viewer_role}
              label={t(orgRoleLabelKey(profile.viewer_role))}
            />
          )}
        </div>

        {profile.display_name && (
          <p className="font-mono text-xs font-medium text-fg-subtle">{profile.name}</p>
        )}
        {profile.description && <p className="text-sm text-fg-muted">{profile.description}</p>}

        <div className="mt-1 flex flex-wrap items-center gap-4 text-xs font-medium text-fg-subtle">
          {isOrg && (
            <span className="flex items-center gap-1.5 tabular-nums">
              <Users size={13} />
              {t(
                profile.num_members === 1
                  ? "namespace.counts.membersOne"
                  : "namespace.counts.membersOther",
                { count: formatNumber(profile.num_members) },
              )}
            </span>
          )}
          <span className="flex items-center gap-1.5 tabular-nums">
            <Boxes size={13} />
            {t(
              profile.num_models === 1
                ? "namespace.counts.modelsOne"
                : "namespace.counts.modelsOther",
              { count: formatNumber(profile.num_models) },
            )}
          </span>
          <span className="flex items-center gap-1.5 tabular-nums">
            <Database size={13} />
            {t(
              profile.num_datasets === 1
                ? "namespace.counts.datasetsOne"
                : "namespace.counts.datasetsOther",
              { count: formatNumber(profile.num_datasets) },
            )}
          </span>
          {website && (
            <a
              href={website}
              rel="nofollow noopener noreferrer"
              target="_blank"
              className="flex items-center gap-1.5 text-accent hover:underline"
            >
              <Globe size={13} />
              {website}
            </a>
          )}
          <span>
            {t("namespace.joinedOn", { date: formatDate(profile.created_at, locale, "UTC") })}
          </span>
        </div>
      </div>

      {(isOrg ? profile.can_edit : profile.can_edit && isOwnProfile) &&
        (isOrg ? (
          <Link
            href={orgSettingsHref(profile.name)}
            className={buttonClass({ variant: "secondary" })}
          >
            <Settings size={15} />
            {t("namespace.settings")}
          </Link>
        ) : (
          <Link href="/settings/profile" className={buttonClass({ variant: "secondary" })}>
            <UserPen size={15} />
            {t("namespace.editProfile")}
          </Link>
        ))}
    </div>
  );
}
