import Link from "next/link";
import { notFound } from "next/navigation";
import { FileEditor } from "@/components/repo/file-editor";
import { RepoBreadcrumb } from "@/components/repo/repo-breadcrumb";
import { RepoNotFoundOrLogin } from "@/components/repo/repo-not-found";
import { RepoTabs } from "@/components/repo/repo-tabs";
import { buttonClass } from "@/components/ui/button";
import { ErrorState } from "@/components/ui/error-state";
import { isNotFound } from "@/lib/api";
import { errorMessage } from "@/lib/api-error-message";
import { getT } from "@/lib/i18n/server";
import { publicApiBase, repoBlobHref, repoEditHref, repoTreeHref } from "@/lib/paths";
import { routeForPath } from "@/lib/preupload";
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
  isNew = false,
}: {
  kind: RepoKind;
  ns: string;
  name: string;
  rev: string;
  path: string[];
  /**
   * Set by `?new=1`, which the tree's "Create a new file" prompt appends.
   * Without it a path with nothing at it is a 404 -- a typo in a URL should
   * not silently become a new file. With it, the same page opens an empty
   * editor at that path instead.
   */
  isNew?: boolean;
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

  // A file being created may name a directory that does not exist yet --
  // "docs/notes/new.md" in a repository with no docs/ -- and the tree
  // endpoint answers 404 for a path it cannot resolve. The commit creates
  // every missing level (gitrepo's tree builder walks with create=true), so
  // that 404 is the expected answer here rather than a failure. Every other
  // way the listing can fail (backend down, no access) is still an error:
  // narrowing on 404 alone keeps "the parent isn't there yet" from swallowing
  // "we could not find out".
  const parentNotCreatedYet = isNew && isNotFound(dirResult);

  if (dirResult.ok && !entry && !isNew) notFound();

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

  function editError(message: string, hint?: string, action?: React.ReactNode) {
    return (
      <div className="flex flex-col gap-4">
        <RepoBreadcrumb kind={kind} ns={ns} name={name} trail={trail} />
        <h1 className="text-2xl font-semibold tracking-tight">{repo.full_name}</h1>
        <RepoTabs kind={kind} ns={ns} name={name} repo={repo} active="files" />
        <ErrorState
          title={t("repo.edit.cantEditTitle")}
          message={message}
          hint={hint}
          action={action}
        />
      </div>
    );
  }

  if (!dirResult.ok && !parentNotCreatedYet) return editError(errorMessage(t, dirResult));
  if (!entry && !isNew) return editError(t("repo.blob.fileNotFound"));

  if (!repo.can_write) {
    return editError(t("repo.edit.noPermission"), t("repo.edit.noPermissionHint"));
  }

  if (
    entry &&
    (entry.type !== "file" ||
      (entry.preview !== "text" && entry.preview !== "markdown") ||
      entry.lfs)
  ) {
    return editError(t("repo.edit.badType"), t("repo.edit.badTypeHint"));
  }

  if (!repo.branches.includes(rev)) {
    return editError(t("repo.edit.notBranch", { rev }), t("repo.edit.notBranchHint"));
  }

  // A file that already exists carries its own verdict in the tree entry
  // (`entry.lfs`, checked above). One being created has no entry, so the
  // question -- would this path be LFS-managed? -- has to be asked of the
  // server: the answer comes from the repository's .gitattributes at this
  // revision, and a copy of that rule here would go stale the moment someone
  // edits the file. Without this the editor opens happily and the commit is
  // refused afterwards (handleEditFile's lfsEditRejection), which is a dead
  // end reached only after the user has typed a whole file.
  //
  // "unknown" (the check itself failed) opens the editor: a flaky preupload
  // must not block a legitimate create, and the server still refuses an LFS
  // path on save, so the worst case is today's behaviour rather than a new
  // way to be locked out.
  if (!entry && (await routeForPath(kind, ns, name, rev, filePath, { headers })) === "lfs") {
    return editError(
      t("repo.edit.badType"),
      t("repo.upload.newFileIsLFS", { file: filePath }),
      <Link
        href={repoTreeHref(kind, ns, name, rev, dirPath.join("/"))}
        className={buttonClass({ variant: "primary" })}
      >
        {t("repo.upload.newFileIsLFSAction")}
      </Link>,
    );
  }

  // A file being created has nothing to read and nothing to lock against:
  // empty content, and an empty base_oid, which the API reads as "not
  // tracking staleness".
  let initialContent = "";
  let baseOid = "";
  if (entry) {
    const rawResult = await getRawFile(kind, ns, name, rev, path, { headers });
    if (!rawResult.ok) return editError(errorMessage(t, rawResult));
    const raw = rawResult.data;

    if (raw.truncated) {
      return editError(t("repo.edit.tooLarge"), t("repo.edit.tooLargeHint"));
    }

    if (raw.encoding === "base64") {
      return editError(t("repo.edit.notText"));
    }
    initialContent = raw.content;
    baseOid = entry.oid;
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
        initialContent={initialContent}
        baseOid={baseOid}
        blobHref={blobHref}
        cancelHref={entry ? blobHref : repoTreeHref(kind, ns, name, rev, dirPath.join("/"))}
        isNew={!entry}
        assetBaseUrl={assetBaseUrl}
        repoRootUrl={repoRootUrl}
        linkContext={{ kind, ns, name, rev, dir: dirPath }}
      />
    </div>
  );
}
