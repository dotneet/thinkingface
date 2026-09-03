import { FileQuestion } from "lucide-react";
import Link from "next/link";
import { buttonClass } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { getT } from "@/lib/i18n/server";
import { getCurrentUser } from "@/lib/session";

export default async function NotFound() {
  const [t, me] = await Promise.all([getT(), getCurrentUser()]);
  // Signed-in visitors already have a session; offering to log in again is
  // both pointless and, if they've reached this page mid-session, confusing.
  // `getCurrentUser` never throws (it degrades to "not logged in" on any
  // failure), so this is safe to call unconditionally.
  const loggedIn = me.ok;
  return (
    <div className="py-16">
      <EmptyState
        icon={FileQuestion}
        title={t("home.notFound.title")}
        description={t("home.notFound.description")}
        action={
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Link href="/" className={buttonClass({ variant: "primary" })}>
              {t("home.notFound.goHome")}
            </Link>
            {/* Next.js's not-found.tsx has no way to read the request path
                (see [S15] in todo/security-audit-findings.md), so this can't
                target a `?next=` back to the page that 404'd — the repo-scoped
                RepoNotFoundOrLogin (components/repo/repo-not-found.tsx) does
                that for a repository page specifically. "/" is still a sensible
                landing spot after logging in from a generic not-found. */}
            {!loggedIn && (
              <Link href="/login?next=%2F" className={buttonClass()}>
                {t("home.notFound.login")}
              </Link>
            )}
          </div>
        }
      />
    </div>
  );
}
