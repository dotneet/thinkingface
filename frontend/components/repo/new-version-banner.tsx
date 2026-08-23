import { ArrowUpRight } from "lucide-react";
import Link from "next/link";
import { Alert } from "@/components/ui/alert";
import { getT } from "@/lib/i18n/server";
import { getRepoLineage, lineageRefHref, lineageRefLabel } from "@/lib/lineage";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind } from "@/types/api";

/**
 * "A newer version is available", at the top of a repository whose card
 * declares `new_version:` (docs/dev/api-contract.md §12).
 *
 * The link goes to the *end* of the successor chain, not to the immediate
 * successor: someone landing on v1 wants v4, not v2. The server resolves that
 * walk; when it could not -- the chain loops, or runs past the depth limit --
 * it hands back the direct successor and says so, and this banner then names
 * that one and adds the warning rather than pretending it is the newest.
 *
 * A successor that does not resolve (a typo, or not pushed yet) is stated in
 * plain text instead of linked, the same way every other dangling reference
 * is.
 *
 * It fetches on its own rather than taking the lineage as a prop so that
 * wiring it into a page is one line; the request is the same GET the lineage
 * section further down the page makes, which Next.js dedupes within a render.
 */
export async function NewVersionBanner({
  kind,
  ns,
  name,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
}) {
  const [result, t] = await Promise.all([
    getRepoLineage(kind, ns, name, { headers: await authHeaders() }),
    getT(),
  ]);
  // A banner is not worth an error state: if lineage is unavailable the
  // section further down the page already says so.
  if (!result.ok) return null;

  const successor = result.data.new_version;
  if (!successor) return null;

  const target = successor.latest;
  const href = lineageRefHref(target);
  const label = lineageRefLabel(target);

  if (href === null) {
    const [before = "", after = ""] = t("repo.lineage.newVersionDangling").split("{ref}");
    return (
      <Alert tone="info" role="presentation" icon={ArrowUpRight}>
        {before}
        <span className="font-mono">{label}</span>
        {after}
      </Alert>
    );
  }

  const [before = "", after = ""] = t("repo.lineage.newVersionBody").split("{link}");
  return (
    <Alert
      tone="info"
      role="presentation"
      icon={ArrowUpRight}
      title={t("repo.lineage.newVersionTitle")}
    >
      <span>
        {before}
        <Link href={href} className="font-mono text-accent hover:underline">
          {label}
        </Link>
        {after}
      </span>
      {successor.truncated ? (
        <span className="text-xs font-medium text-fg-subtle">
          {t("repo.lineage.newVersionTruncated")}
        </span>
      ) : (
        successor.hops > 1 && (
          <span className="text-xs font-medium text-fg-subtle">
            {t("repo.lineage.newVersionChain", { count: successor.hops })}
          </span>
        )
      )}
    </Alert>
  );
}
