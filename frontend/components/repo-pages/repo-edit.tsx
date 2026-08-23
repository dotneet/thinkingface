import { notFound } from "next/navigation";
import { FileEditor } from "@/components/repo/file-editor";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { publicApiBase, repoBlobHref, repoEditHref, repoTreeHref } from "@/lib/paths";
import { redirectIfRepoMoved } from "@/lib/repo-redirect";
import { getRawFile, getRepo, getTree, resolveFileUrl } from "@/lib/repos";
import { authHeaders } from "@/lib/server-auth";
import type { RepoKind, TreeEntryUI } from "@/types/api";

export async function RepoEdit({
  kind,
  ns,
  name,
  rev,
  path,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
}) {
  const fileName = path[path.length - 1] ?? "";
  const dirPath = path.slice(0, -1);
  const filePath = path.join("/");

  // Forward the tf_session cookie so the request is authenticated and
  // resolve instead of 404ing (see lib/server-auth.ts).
  const headers = await authHeaders();
  const [repoResult, dirResult, t] = await Promise.all([
    getRepo(kind, ns, name, { headers }),
    getTree(kind, ns, name, rev, dirPath, { headers }),
    getT(),
  ]);

  redirectIfRepoMoved(repoResult, (toNs, toName) =>
    repoEditHref(kind, toNs, toName, rev, filePath),
  );
  if (isNotFound(repoResult)) {
    return <RepoNotFoundOrLogin currentPath={repoEditHref(kind, ns, name, rev, filePath)} />;
  }
  if (!repoResult.ok) {
    return <ErrorState title={t("ui.errorStateTitle")} message={errorMessage(t, repoResult)} />;
  }
  const repo = repoResult.data.repo;

  const entry: TreeEntryUI | undefined = dirResult.ok
    ? dirResult.data.entries.find((e) => e.path === filePath)
    : undefined;

  if (dirResult.ok && !entry) notFound();

  const trail = [
    { label: `blob/${rev}`, href: repoTreeHref(kind, ns, name, rev, dirPath.join("/")) },
    ...dirPath.map((seg, i) => ({
      label: seg,
      href: repoTreeHref(kind, ns, name, rev, dirPath.slice(0, i + 1).join("/")),
    })),
    { label: fileName },
    // Reuses repo.blob.edit ("Edit" / "編集") — the same word repo-blob.tsx
    // uses for the button that lands here — instead of a hardcoded English
    // literal, so the trail is localized like every other segment.
    { label: t("repo.blob.edit") },
  ];

  const blobHref = repoBlobHref(kind, ns, name, rev, filePath);

  function editError(message: string, hint?: string) {
    return (
      <div className="flex flex-col gap-4">
        <RepoBreadcrumb kind={kind} ns={ns} name={name} trail={trail} />
        <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>
        <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="files" />
        <ErrorState title={t("repo.edit.cantEditTitle")} message={message} hint={hint} />
      </div>
    );
  }

  if (!dirResult.ok) return editError(errorMessage(t, dirResult));
  if (!entry) return editError(t("repo.blob.fileNotFound"));

  if (!repo.can_write) {
    return editError(t("repo.edit.noPermission"), t("repo.edit.noPermissionHint"));
  }

  if (
    entry.type !== "file" ||
    (entry.preview !== "text" && entry.preview !== "markdown") ||
    entry.lfs
  ) {
    return editError(t("repo.edit.badType"), t("repo.edit.badTypeHint"));
  }

  if (!repo.branches.includes(rev)) {
    return editError(t("repo.edit.notBranch", { rev }), t("repo.edit.notBranchHint"));
  }

  const rawResult = await getRawFile(kind, ns, name, rev, path, { headers });
  if (!rawResult.ok) return editError(errorMessage(t, rawResult));
  const raw = rawResult.data;

  if (raw.truncated) {
    return editError(t("repo.edit.tooLarge"), t("repo.edit.tooLargeHint"));
  }

  if (raw.encoding === "base64") {
    return editError(t("repo.edit.notText"));
  }

  const assetBaseUrl = resolveFileUrl(kind, ns, name, rev, dirPath, publicApiBase());
  const repoRootUrl = resolveFileUrl(kind, ns, name, rev, [], publicApiBase());

  return (
    <div className="flex flex-col gap-4">
      <RepoBreadcrumb kind={kind} ns={ns} name={name} trail={trail} />
      <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>
      <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="files" />

      <FileEditor
        kind={kind}
        ns={ns}
        name={name}
        rev={rev}
        path={path}
        fileName={fileName}
        initialContent={raw.content}
        baseOid={entry.oid}
        blobHref={blobHref}
        assetBaseUrl={assetBaseUrl}
        repoRootUrl={repoRootUrl}
        linkContext={{ kind, ns, name, rev, dir: dirPath }}
      />
    </div>
  );
}
