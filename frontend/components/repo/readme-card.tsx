import { FileText } from "lucide-react";
import Link from "next/link";
import { buttonClass } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Markdown, type MarkdownLinkContext } from "@/components/ui/markdown";
import { getT } from "@/lib/i18n/server";

/**
 * Mirrors `maxReadmeBytes` in backend/internal/api/repos.go: above this the
 * server leaves `readme` empty and sets `readme_too_large` instead.
 */
const README_LIMIT_LABEL = "256KB";

export async function ReadmeCard({
  readme,
  tooLarge,
  fileHref,
  assetBaseUrl,
  repoRootUrl,
  linkContext,
}: {
  readme: string;
  /**
   * README.md exists but is over the server's render limit, so `readme` is
   * empty. Shown as its own state — "no README" would be a lie.
   */
  tooLarge?: boolean;
  /** Blob page of README.md, offered as the way out when it can't be rendered here. */
  fileHref?: string;
  assetBaseUrl?: string;
  repoRootUrl?: string;
  /** Lets relative links in the card resolve to blob / tree pages. */
  linkContext?: MarkdownLinkContext;
}) {
  if (tooLarge) {
    const t = await getT();
    return (
      <EmptyState
        icon={FileText}
        title={t("repo.readme.tooLargeTitle")}
        description={t("repo.readme.tooLargeDescription", { limit: README_LIMIT_LABEL })}
        action={
          fileHref ? (
            <Link href={fileHref} className={buttonClass({ variant: "secondary" })}>
              <FileText size={14} />
              {t("repo.readme.tooLargeOpenFile")}
            </Link>
          ) : undefined
        }
      />
    );
  }

  if (!readme.trim()) {
    const t = await getT();
    return (
      <EmptyState
        icon={FileText}
        title={t("repo.readme.emptyTitle")}
        description={t("repo.readme.emptyDescription")}
      />
    );
  }

  return (
    <Card className="p-6">
      <Markdown
        source={readme}
        assetBaseUrl={assetBaseUrl}
        repoRootUrl={repoRootUrl}
        linkContext={linkContext}
        // A repository card is stored with its YAML metadata at the top; the
        // sidebar already presents that, so the card itself shows only prose.
        stripFrontmatter
      />
    </Card>
  );
}
