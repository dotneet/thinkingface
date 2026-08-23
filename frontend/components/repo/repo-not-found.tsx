import Link from "next/link";
import { notFound } from "next/navigation";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { getT } from "@/lib/i18n/server";
import { getCurrentUser } from "@/lib/session";

/**
 * Renders when a repository fetch 404s (see [S15] in
 * todo/security-audit-findings.md).
 *
 * There is no visibility concept here, so a 404 means the repository really
 * is absent rather than hidden. What this still buys a signed-out visitor is
 * a way back: a stale link or a bookmark from before the session expired
 * lands on a "log in" prompt `?next=`-linked to the page that 404'd, instead
 * of a dead end. A *signed-in* 404 falls through to the ordinary
 * `notFound()`.
 */
export async function RepoNotFoundOrLogin({ currentPath }: { currentPath: string }) {
  const [t, currentUser] = await Promise.all([getT(), getCurrentUser()]);

  if (currentUser.ok) {
    notFound();
  }

  return (
    <ErrorState
      title={t("repo.notFoundOrNoAccess.title")}
      message={t("repo.notFoundOrNoAccess.message")}
      action={
        <Link
          href={`/login?next=${encodeURIComponent(currentPath)}`}
          className={buttonClass({ variant: "primary" })}
        >
          {t("repo.notFoundOrNoAccess.login")}
        </Link>
      }
    />
  );
}
