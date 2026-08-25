import { Archive } from "lucide-react";
import { DefaultBranchForm } from "@/components/repo/default-branch-form";
import { RefsManager } from "@/components/repo/refs-manager";
import { RenameRepoForm } from "@/components/repo/rename-repo-form";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoDangerZone } from "@/components/repo/repo-danger-zone";
import { RepoDescriptionForm } from "@/components/repo/repo-description-form";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { TransferRepoForm } from "@/components/repo/transfer-repo-form";
import { Alert } from "@/components/ui/alert";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { repoBase } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getRefs, getRepo } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind } from "@/types/api";

export async function RepoSettings({
  kind,
  ns,
  name,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
}) {
  // Forward the tf_session cookie so the request is authenticated and
  // resolves instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [result, refsResult, t] = await Promise.all([
    getRepo(kind, ns, name, { headers }),
    getRefs(kind, ns, name, { headers }),
    getT(),
  ]);

  redirectIfRepoMoved(result, (toNs, toName) => `${repoBase(kind, toNs, toName)}/settings`);
  if (isNotFound(result)) {
    return <RepoNotFoundOrLogin currentPath={`${repoBase(kind, ns, name)}/settings`} />;
  }

  if (!result.ok) {
    return (
      <ErrorState
        title={t("ui.errorStateTitle")}
        message={errorMessage(t, result)}
        hint={t("repo.overview.backendHint")}
      />
    );
  }

  const repo = result.data.repo;

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb
        kind={kind}
        ns={ns}
        name={name}
        trail={[{ label: t("repo.tabs.settings") }]}
      />

      <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>

      <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="settings" />

      {repo.archived && (
        <Alert tone="warning" icon={Archive} title={t("repo.archived.bannerTitle")}>
          {t("repo.archived.bannerBody")}
        </Alert>
      )}

      {!repo.can_admin ? (
        <ErrorState
          title={t("repo.settings.noPermissionTitle")}
          message={t("repo.settings.noPermission")}
        />
      ) : (
        <>
          {/* Rename and description come first: they are the two things
              someone opens this page to change, and the rename in particular
              used to be reachable only through the transfer form below --
              a screen about giving the repository away. */}
          <Card>
            <CardHeader>
              <CardTitle>{t("repo.settings.rename.title")}</CardTitle>
            </CardHeader>
            <p className="mt-1 text-sm text-fg-subtle">{t("repo.settings.rename.description")}</p>
            <div className="mt-4">
              <RenameRepoForm kind={kind} ns={ns} name={name} archived={repo.archived} />
            </div>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("repo.settings.description.title")}</CardTitle>
            </CardHeader>
            <p className="mt-1 text-sm text-fg-subtle">
              {t("repo.settings.description.description")}
            </p>
            <div className="mt-4">
              <RepoDescriptionForm
                kind={kind}
                ns={ns}
                name={name}
                description={repo.description}
                archived={repo.archived}
              />
            </div>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("repo.settings.defaultBranch.title")}</CardTitle>
            </CardHeader>
            <p className="mt-1 text-sm text-fg-subtle">
              {t("repo.settings.defaultBranch.description")}
            </p>
            <div className="mt-4">
              <DefaultBranchForm
                kind={kind}
                ns={ns}
                name={name}
                branches={repo.branches}
                defaultBranch={repo.default_branch}
                archived={repo.archived}
              />
            </div>
          </Card>

          {/* Branches and tags. The four HF-compatible endpoints behind this
              have always existed; only the UI for them was missing. */}
          <Card>
            <CardHeader>
              <CardTitle>{t("repo.refs.manageTitle")}</CardTitle>
            </CardHeader>
            <p className="mt-1 text-sm text-fg-subtle">{t("repo.refs.manageDescription")}</p>
            <div className="mt-4">
              {repo.archived ? (
                <p className="text-sm text-fg-subtle">{t("repo.refs.blockedByArchive")}</p>
              ) : !repo.can_write ? (
                <p className="text-sm text-fg-subtle">{t("repo.refs.noPermission")}</p>
              ) : !refsResult.ok ? (
                // A failed refs fetch is not an empty repository: say which
                // one it is rather than rendering two empty lists
                // (DESIGN.md §9).
                <ErrorState
                  title={t("ui.errorStateTitle")}
                  message={errorMessage(t, refsResult)}
                  hint={t("repo.overview.backendHint")}
                />
              ) : (
                <RefsManager
                  kind={kind}
                  ns={ns}
                  name={name}
                  defaultBranch={repo.default_branch}
                  branches={refsResult.data.branches}
                  tags={refsResult.data.tags}
                />
              )}
            </div>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("repo.settings.transfer.title")}</CardTitle>
            </CardHeader>
            <p className="mt-1 text-sm text-fg-subtle">{t("repo.settings.transfer.description")}</p>
            <div className="mt-4">
              {/* An archived repository cannot be transferred; unarchive it
                  from the danger zone below first. */}
              {repo.archived ? (
                <p className="text-sm text-fg-subtle">
                  {t("repo.settings.transfer.blockedByArchive")}
                </p>
              ) : (
                <TransferRepoForm kind={kind} ns={ns} name={name} />
              )}
            </div>
          </Card>

          <Card className="border-negative/40">
            <CardHeader>
              <CardTitle>{t("repo.settings.dangerZone")}</CardTitle>
            </CardHeader>
            <div className="mt-4">
              <RepoDangerZone kind={kind} ns={ns} name={name} archived={repo.archived} />
            </div>
          </Card>
        </>
      )}
    </div>
  );
}
