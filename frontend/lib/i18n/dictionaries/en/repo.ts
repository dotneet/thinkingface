// repo: repository detail (overview, file browser, commit history, editing, viewer).
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const repo = {
  overview: {
    backendHint:
      "The backend API may be unreachable. Check API_URL / NEXT_PUBLIC_API_URL and try again.",
    browseFilesOne: "Browse {count} file",
    browseFilesOther: "Browse {count} files",
  },
  // [S15]: shown instead of a bare 404 when a signed-out visitor hits a
  // repository that doesn't resolve, so a stale link still offers a way back
  // rather than a dead end.
  notFoundOrNoAccess: {
    title: "Not found",
    message: "This repository doesn't exist. Log in if you followed a link from an old session.",
    login: "Log in",
  },
  tabs: {
    card: "Card",
    files: "Files",
    viewer: "Viewer",
    experiments: "Experiments",
    settings: "Settings",
  },
  breadcrumb: {
    datasets: "Datasets",
    models: "Models",
  },
  indexing: {
    message:
      "This repository is still indexing after a recent push. File counts, the Parquet viewer, and experiment charts may be incomplete for a moment.",
  },
  archived: {
    badge: "archived",
    bannerTitle: "This repository is archived",
    bannerBody:
      "It is read-only: pushes, commits, in-browser edits and transfers are refused. Everything stays readable and downloadable. An owner can unarchive it at any time.",
  },
  sidebar: {
    organization: "Organization",
    userNamespace: "User",
    // "File downloads", not "Downloads": the counter only sees the resolve
    // endpoint, so naming it after the whole repository would overstate what
    // it measures. `downloadsHint` says the rest out loud.
    downloads: "File downloads",
    downloads30d: "File downloads (30d)",
    downloadsHint:
      "Counts single-file downloads served by this server (resolve URLs, hf_hub_download, snapshot_download). git clone, git lfs pull and transfers straight out of the bucket are not counted.",
    size: "Size",
    files: "Files",
    license: "License",
    updated: "Updated",
    // Card topics, not git tags — the two used to share the word "Tags" in
    // this sidebar and only one of them was ever shown.
    tags: "Topics",
    branches: "Branches",
    gitTags: "Git tags",
    gcsAccess: {
      label: "GCS access",
      emptyTitle: "No indexed files",
      emptyDescription: "This revision doesn't have any indexed files yet.",
      summaryOne: "{count} file, {size}",
      summaryOther: "{count} files, {size}",
      scriptLabel: "gcloud storage script",
      copyScript: "Copy script",
      destHint:
        "Set DEST to a gs:// prefix instead of a local path to copy between buckets rather than downloading.",
      duckdbLabel: "DuckDB",
      copyDuckdb: "Copy query",
    },
  },
  // The clone URL block. Two protocols rather than one, because a user who
  // registered an SSH key at /settings/ssh-keys had no way to find out which
  // URL that key works against — the port is deployment-specific.
  clone: {
    title: "Clone",
    protocolLabel: "Clone protocol",
    http: "HTTP",
    ssh: "SSH",
    sshHint: "SSH authenticates by key only. Register your public key in Settings → SSH keys.",
    // Git LFS always speaks HTTP, even when the remote is SSH.
    sshLfsHint: "Git LFS transfers over HTTP; clone over HTTP for repositories with LFS files.",
  },
  // "Use this model / dataset": the snippets that point huggingface_hub,
  // datasets and transformers at this server. This is the whole point of the
  // product and it used to appear nowhere in the UI.
  usage: {
    labelModel: "Use this model",
    labelDataset: "Use this dataset",
    intro:
      "huggingface_hub, datasets and transformers work against thinkingface unchanged — point them at this server with HF_ENDPOINT.",
    envLabel: "Environment",
    envHint:
      "Export these before Python starts: huggingface_hub reads its endpoint once, at import time.",
    copyEnv: "Copy environment",
    tokenHint: "Add HF_TOKEN=… as well for a token-authenticated client.",
    datasetsLabel: "datasets",
    downloadLabel: "huggingface_hub",
    transformersLabel: "transformers",
    transformersHint:
      "AutoModel / AutoTokenizer stand in for whichever task-specific class this model needs.",
    copySnippet: "Copy snippet",
    revisionHint: "Pass revision=… to pin a branch, tag or commit. Showing {rev}.",
  },
  // Creating and deleting branches and tags from the web UI. The HF-compatible
  // API has had all four operations from the start; only the UI was missing.
  refs: {
    newBranch: "New branch",
    newBranchTitle: "Create a branch",
    newBranchBody: "The new branch starts at {rev}.",
    branchNameLabel: "Branch name",
    branchNamePlaceholder: "feature/my-change",
    createBranch: "Create branch",
    creating: "Creating…",
    cancel: "Cancel",
    manageTitle: "Branches and tags",
    manageDescription:
      "Tags mark a revision so it can be downloaded by name. Deleting a ref removes the name, not the commits it pointed at.",
    branchesTitle: "Branches",
    tagsTitle: "Tags",
    noBranches: "This repository has no branches yet.",
    noTags: "This repository has no tags yet.",
    defaultBadge: "default",
    // Why the default branch has no delete control (the server refuses it too).
    defaultUndeletable: "The default branch can't be deleted.",
    newTagTitle: "Create a tag",
    tagNameLabel: "Tag name",
    tagNamePlaceholder: "v1.0",
    tagRevLabel: "Revision to tag",
    tagMessageLabel: "Message (optional)",
    tagMessageHint: "A message makes it an annotated tag, the way git tag -m does.",
    createTag: "Create tag",
    deleteBranchAction: "Delete branch {name}",
    deleteTagAction: "Delete tag {name}",
    deleteBranchTitle: "Delete this branch?",
    deleteBranchBody:
      "The branch {name} will be removed from {repo}. Its commits stay in the repository until git garbage-collects the ones nothing else references.",
    deleteTagTitle: "Delete this tag?",
    deleteTagBody:
      "The tag {name} will be removed from {repo}. Anything pinned to it by name stops resolving.",
    confirmDelete: "Delete",
    deleting: "Deleting…",
    blockedByArchive: "Unarchive this repository before changing its branches or tags.",
    noPermission: "You need write access to this repository to change its branches and tags.",
  },
  readme: {
    emptyTitle: "No README",
    emptyDescription: "This repository doesn't have a README.md yet.",
    toc: "Contents",
    tooLargeTitle: "README is too large to render",
    tooLargeDescription: "README.md is over {limit}, so it isn't shown here.",
    tooLargeOpenFile: "Open README.md",
  },
  lineage: {
    title: "Lineage",
    unavailable: "Lineage unavailable",
    // {code} is replaced with a `lineage:` code snippet.
    empty:
      "No lineage declared. Add a {code} block to this repository's README front matter to record the datasets, base model and training run it came from.",
    trainedOn: "Trained on",
    baseModel: "Base model",
    evaluatedOn: "Evaluated on",
    trainingRun: "Training run",
    // The other direction: a run that called log_model() and named this
    // repository. Stored with the run, not in this repository's card.
    producedByRun: "Produced by run",
    // {rev} is the revision (abbreviated) the run recorded.
    producedRevision: "at {rev}",
    derivedFromThis: "Derived from this",
    evaluatedBy: "Evaluated by",
    supersededVersions: "Supersedes",
    notFound: "not found on this server",
    fromRun: "from run {run}",
    // Successor banner, shown at the top of a repository that declares
    // `new_version:` in its card.
    newVersionTitle: "A newer version is available",
    // {link} is replaced with a link to the successor repository.
    newVersionBody: "Use {link} instead.",
    // {count} is the number of new_version edges followed (only shown when it's 2 or more).
    newVersionChain: "Reached through {count} successor links.",
    newVersionTruncated:
      "The chain of successors doesn't end — it loops, or is longer than this page follows. Showing the version this one names directly.",
    // {ref} is the reference string exactly as written in the card.
    newVersionDangling:
      "This repository declares {ref} as its successor, but it isn't on this server.",
    // Model tree: the derived repositories, grouped by how each one relates to
    // this model (`base_model_relation`).
    relationFinetune: "Fine-tunes",
    relationAdapter: "Adapters",
    relationQuantized: "Quantizations",
    relationMerge: "Merges",
    relationOther: "Other",
    showAllDerived: "Show all {count}",
    showFewerDerived: "Show fewer",
  },
  tree: {
    errorHint: "The backend API may be unreachable, or this path doesn't exist at this revision.",
    emptyDir: "This directory is empty",
    unknownRevTitle: "Revision not found",
    unknownRev: "This repository has no branch, tag or commit called {rev}.",
    unknownRevHint: "Check the revision in the URL, or browse the default branch ({branch}).",
    unknownRevAction: "Go to the default branch",
  },
  treeTable: {
    name: "Name",
    lastCommit: "Last commit",
    size: "Size",
    updated: "Updated",
    openInViewer: "Open in viewer",
    actionsSr: "Actions",
    upDir: "Parent directory",
  },
  fileNav: {
    copyPath: "Copy path",
  },
  refSwitcher: {
    viewingCommit: "Viewing commit",
    branches: "Branches",
    noBranches: "No branches",
    tags: "Tags",
    filterLabel: "Filter branches and tags",
    noMatches: "No matching branches or tags",
    // The commit each ref points at, which the API has always returned
    // (`RefUI.target_oid`) and this menu never showed. Two branches with the
    // same tip are otherwise indistinguishable here.
    targetTitle: "Points at commit {oid}",
  },
  commitBar: {
    history: "History",
  },
  commits: {
    // {path} is replaced with the file path (shown in monospace).
    historyFor: "History for {path}",
    clearFilter: "Clear filter",
    errorHint: "The backend API may be unreachable, or this revision doesn't exist.",
    emptyPage: "No commits touching this path in this page of history.",
    older: "Older",
    emptyForPath: "No commits for this path",
    empty: "No commits yet",
    browseFiles: "Browse files",
    viewDiff: "View diff",
  },
  // Commit diff: what one commit changed against its first parent.
  //
  // Most of this group exists because the response's numbers are only
  // sometimes numbers. `additions` / `deletions` mean nothing unless the file
  // has a patch, and the three reasons it may not have one (binary, LFS,
  // skipped for size) each need saying — printing "+0 −0" for any of them
  // would state that nothing changed (DESIGN.md §9,
  // docs/dev/api-contract.md §2).
  diff: {
    metaTitle: "Commit",
    backToHistory: "Back to commit history",
    browseFiles: "Browse files at this commit",
    copySha: "Copy the full commit SHA",
    // {oid} is the abbreviated parent SHA (shown in monospace).
    parent: "Parent {oid}",
    rootCommit: "Root commit — it has no parent, so every file reads as added.",
    filesChangedOne: "{count} file changed",
    filesChangedOther: "{count} files changed",
    additions: "+{count}",
    additionsTitle: "{count} lines added",
    deletions: "−{count}",
    deletionsTitle: "{count} lines removed",
    countsPartial:
      "These totals only cover the files with a text diff below — binary, LFS and skipped files were never counted.",
    filesTruncatedTitle: "Showing {shown} of {total} changed files",
    filesTruncated:
      "This commit touched more paths than one response lists. The files left out are not shown here at all; clone the repository to see the whole commit.",
    empty: "This commit changed no files",
    emptyDescription: "Nothing differs between it and its first parent.",
    revisionNotFound: "No commit here",
    revisionNotFoundMessage:
      "This revision doesn't resolve to a commit — it may have been deleted, or the repository may have no commits yet.",
    errorHint: "The backend API may be unreachable, or this commit doesn't exist.",
    status: {
      added: "added",
      modified: "modified",
      deleted: "deleted",
    },
    // Shown instead of a line count, because there is no line count.
    noPatch: {
      binary: "Binary file — there is no text diff to show.",
      lfs: "Stored with Git LFS — the pointer's oid changed; the contents are not diffed here.",
      tooLarge: "Too large to diff — the patch was skipped, so its lines were not counted.",
      noTextChange: "No lines to show — the file is empty on both sides, or only its mode changed.",
      unsupported: "Not a regular file — a submodule or a special entry has no text diff.",
      budgetSpent:
        "Not shown — this commit changed more text than one page renders. Open the file to see its contents.",
      linesNotCounted: "lines not counted",
    },
    sizeAdded: "{size} added",
    sizeDeleted: "{size} removed",
    // {from} and {to} are byte sizes on either side of the commit.
    sizeChanged: "{from} → {to}",
    patchEmpty: "No line changes to show for this file.",
    patchTruncated:
      "This patch was cut off mid-diff — the rest of this file's changes are not shown.",
  },
  blob: {
    fileNotFound: "File not found in tree listing.",
    edit: "Edit",
    download: "Download",
  },
  // Source-file preview: the line gutter and the reasons a file is shown flat.
  codePreview: {
    lineLink: "Line {line}",
    tooManyLines:
      "This file has {lines} lines — more than the {limit} this preview highlights, so it is shown as plain text without line numbers.",
    tooLarge:
      "This file is too large to highlight in the browser, so it is shown as plain text without line numbers.",
  },
  markdownPreview: {
    previewMode: "Preview mode",
    modeRendered: "Rendered",
    modeRaw: "Raw",
  },
  preview: {
    emptyFile: "This file is empty",
    downloadOriginal: "Download original",
    download: "Download",
    parquetTitle: "Parquet file",
    parquetDescription: "{size} — open this in the table viewer to browse rows and schema.",
    openInViewer: "Open in viewer",
    loadErrorTitle: "Couldn't load this file",
    loadErrorHint:
      "The backend API may be unreachable. Reload the page, or download the file instead.",
    noPreviewTitle: "No preview available",
    noPreviewDescription: "{size} — this file type can't be previewed inline.",
    decodeErrorTitle: "Couldn't decode this file",
    decodeErrorMessage: "The preview the server returned isn't readable as text.",
    // {link} is replaced with a "download the full file" link.
    truncatedNotice: "This preview was truncated at 512KB. {link}.",
    truncatedNoticeLink: "Download the full file",
    gcsCommandLabel: "GCS access",
    gcsCopyCommand: "Copy gcloud command",
  },
  tabular: {
    modeTable: "Table",
    modeRaw: "Raw",
    previewMode: "Preview mode",
    stats: "{rows} rows · {columns} columns · {size}",
    rawFallbackTitle: "Showing raw text",
    rawFallbackBody: "This file couldn't be read as a table: {message}",
    // Reasons parseTabular can give up, translated here because lib/tabular.ts
    // is framework-free and returns a reason code instead of a sentence.
    parseNoRows: "the file has no rows to show.",
    parseTooManyColumns: "too many columns to display ({columns}).",
    parseRaggedRows: "the rows don't line up with the header row.",
    parseNoJsonObjects: "no JSON objects were found — one per line is expected.",
    parseTooManyInvalidLines: "too many lines are not valid JSON objects.",
    fetchFailedTitle: "Showing the truncated preview",
    fetchFailedBody:
      "The full file couldn't be downloaded ({error}), so only the first 512KB is parsed here.",
    networkError: "Network error",
    // Short, translated stand-ins for the HTTP status line, which is
    // server-authored English (see fetchFailureReason in
    // components/repo/tabular-preview.tsx). Noun phrases, because they are
    // interpolated into fetchFailedBody's parenthesis.
    fetchReasonUnauthorized: "Not signed in",
    fetchReasonForbidden: "No permission",
    fetchReasonNotFound: "File not found",
    fetchReasonServer: "Server error",
    // Anything unmapped keeps only the number: "HTTP 418" is language-neutral
    // in a way the status text that goes with it is not.
    fetchReasonStatus: "HTTP {status}",
    // {link} is replaced with a "download the full file" link.
    rowLimit: "Stopped after the first {rows} rows. {link} to see the rest.",
    rowLimitLink: "Download the full file",
    malformedJsonlOne: "{count} line was not a valid JSON object and was skipped.",
    malformedJsonlOther: "{count} lines were not valid JSON objects and were skipped.",
    malformedCsvOne: "{count} row didn't match the header and was padded or clipped.",
    malformedCsvOther: "{count} rows didn't match the header and were padded or clipped.",
    switchToRaw: "Switch to Raw to see the file as-is.",
    emptyTitle: "No rows",
    emptyDescription: "This file has a header but no data rows.",
  },
  edit: {
    cantEditTitle: "Can't edit this file",
    noPermission: "You don't have permission to edit this repository.",
    noPermissionHint:
      "Log in with an account that has write access, or ask the repository owner for access.",
    badType: "This file type can't be edited from the web UI.",
    badTypeHint:
      "Only plain text and Markdown files (that aren't stored in LFS) can be edited here.",
    notBranch: '"{rev}" isn\'t a branch.',
    notBranchHint:
      "Open this file on a branch (e.g. the default branch) to edit it — commits and tags are read-only.",
    tooLarge: "This file is larger than 512KB.",
    tooLargeHint:
      "Files over 512KB can't be loaded into the web editor. Edit it locally and push instead.",
    notText: "This file isn't plain text and can't be edited from the web UI.",
  },
  // Adding files from the browser: the "Add file" menu on the tree, the
  // new-file path prompt, and the upload dialog.
  upload: {
    menuLabel: "Add file",
    menuNewFile: "Create a new file",
    menuUpload: "Upload files",
    newFileTitle: "Create a new file",
    // Two bodies rather than one with a "/" in it: at the repository root
    // there is no directory worth naming, and printing one there would be
    // noise on the most common case.
    newFileBody: "The file is created at the repository root. Use / to put it in a subdirectory.",
    newFileBodyIn: "The file is created in {dir}. Use / to put it in a subdirectory of that.",
    // Shown under the input as the user types, so the path that will actually
    // be created is never something they have to work out themselves.
    newFileResolved: "Creates {path}",
    // Shown in place of "Creates …" when the typed path can't be used, so the
    // disabled Create button is never a mystery (DESIGN.md §9). Each names
    // what git itself refuses, since that is the reason underneath.
    newFileRelativeSegment:
      'A path can\'t contain "." or ".." segments — git refuses them, and the file would not be created where this says.',
    newFileGitDirectory: 'A path can\'t contain a ".git" segment — git reserves that name.',
    newFilePathLabel: "File path",
    newFilePathPlaceholder: "notes.md",
    newFileConfirm: "Create file",
    newFileIsLFS:
      "{file} is tracked by Git LFS in this repository, so its contents can't be written in the browser editor. Add it with Add file → Upload files instead — uploads handle LFS for you.",
    newFileIsLFSAction: "Upload it instead",
    title: "Upload files",
    dropLabel: "Drop files here, or click to choose",
    dropHint: "They will be committed to {dir} on {rev}.",
    browseHint: "Choose files to upload",
    selectedOne: "{count} file",
    selectedOther: "{count} files",
    totalSize: "{size} in total",
    remove: "Remove {file}",
    emptyTitle: "No files chosen yet",
    emptyDescription: "Nothing is uploaded until you choose at least one file.",
    commitMessageLabel: "Commit message",
    commitMessagePlaceholder: "Upload files",
    submit: "Upload",
    uploading: "Uploading…",
    progressLabel: "Upload progress",
    progressCount: "{done} of {total} sent",
    tooMany: "You can upload at most {count} files at once.",
    lfsNote: "Large files and known binary formats are stored with Git LFS automatically.",
  },
  // Deleting a file from the file view. Destructive, so it always goes
  // through ConfirmDialog.
  deleteFile: {
    action: "Delete",
    title: "Delete this file?",
    body: "{file} will be removed from {rev} in a new commit. Earlier commits still contain it.",
    lfsNote:
      "The stored LFS object itself is kept until nothing references it any more and garbage collection reclaims it.",
    confirm: "Delete file",
    deleting: "Deleting…",
    cancel: "Cancel",
  },
  renameFile: {
    action: "Rename",
    title: "Rename or move this file",
    body: "{file} will move to its new path in a single commit on {rev}. Earlier commits still show it where it was.",
    pathLabel: "New path",
    pathHint:
      "The full path from the repository root. Change the last part to rename the file, the rest to move it into another directory.",
    confirm: "Rename file",
    renaming: "Renaming…",
    cancel: "Cancel",
  },
  editor: {
    conflict:
      "This file changed while you were editing. Reload the page and reapply your changes — your edit is still here in the meantime.",
    editAria: "Edit {file}",
    commitMessageLabel: "Commit message",
    commitMessagePlaceholder: "Update {file}",
    descriptionLabel: "Description (optional)",
    descriptionPlaceholder: "Add an optional extended description…",
    committing: "Committing…",
    commit: "Commit changes",
    cancel: "Cancel",
    discardTitle: "Discard unsaved changes?",
    discardBody: "Your edit to {file} has not been committed yet. Leaving this page will lose it.",
    discardConfirm: "Discard changes",
    keepEditing: "Keep editing",
  },
  viewer: {
    errorHint: "Make sure this path points to a Parquet file that exists at this revision.",
  },
  settings: {
    noPermissionTitle: "No access",
    noPermission:
      "You need owner or organisation-admin access to this repository to change its settings.",
    dangerZone: "Danger zone",
    transfer: {
      title: "Transfer ownership",
      description:
        "Move this repository to another user or organisation, or rename it within the same namespace. Git history, LFS objects, and downloads carry over unchanged.",
      destinationLabel: "Destination namespace",
      destinationModeLabel: "Destination namespace kind",
      destinationModeMine: "One of mine",
      destinationModeOther: "Another user or org",
      otherNamespacePlaceholder: "e.g. some-org",
      noOwnNamespaces: "You don't belong to any namespace yet.",
      newNameLabel: "New name (optional)",
      newNameHint: "Leave blank to keep the current name.",
      blockedByArchive: "Unarchive this repository before transferring it.",
      submit: "Start transfer",
      confirmTitle: "Confirm transfer",
      confirmBody: "{from} will move to {to}. This can be undone only by transferring it back.",
      confirmInputLabel: 'Type "{value}" to confirm',
      confirmCancel: "Cancel",
      confirmSubmit: "Transfer repository",
      confirming: "Transferring…",
      pendingTitle: "Transfer pending",
      pendingDestination: "Waiting for {destination} to accept this transfer.",
      pendingExpires: "Expires {date}.",
      cancel: "Cancel transfer",
      cancelling: "Cancelling…",
      loginRequiredTitle: "Login required",
      loginRequiredMessage: "You need to be logged in to transfer this repository.",
      errors: {
        namespaceRequired: "Choose or enter a destination namespace.",
        nameInvalid: "That name isn't valid.",
        nameGitSuffix: 'A repository name can\'t end in ".git".',
      },
    },
    rename: {
      title: "Rename repository",
      description:
        "Change this repository's name without changing who owns it. The old name keeps working: a redirect is left behind, exactly as a transfer leaves one.",
      label: "Repository name",
      hint: "Letters, digits, dot, dash and underscore, starting with a letter or digit.",
      save: "Rename",
      saving: "Renaming…",
      blockedByArchive: "Unarchive this repository before renaming it.",
      errors: {
        nameInvalid: "That name isn't valid.",
        nameGitSuffix: 'A repository name can\'t end in ".git".',
      },
    },
    description: {
      title: "Description",
      description: "The one line shown in listings and at the top of this repository.",
      label: "Description",
      placeholder: "What is in this repository?",
      cardNote:
        "If the README's card carries a description, that one wins and replaces this on every push. This field is what a repository with no card description has instead.",
      save: "Save",
      saving: "Saving…",
      saved: "Description updated.",
      blockedByArchive: "Unarchive this repository before changing its description.",
    },
    defaultBranch: {
      hint: "Saving the branch that is already the default re-runs indexing for it.",
      title: "Default branch",
      description:
        "The branch clone checks out, and that the file list, README, and lineage are read from.",
      label: "Branch",
      save: "Save",
      saving: "Saving…",
      saved: "Default branch updated.",
      blockedByArchive: "Unarchive this repository before changing its default branch.",
      noBranches: "This repository has no commits yet, so there is no branch to switch to.",
    },
    archive: {
      title: "Archive this repository",
      description:
        "Make the repository read-only. Git history, LFS objects and downloads are untouched, and everyone can still read and clone it — but pushes, commits, in-browser edits and transfers are refused until it is unarchived.",
      descriptionArchived:
        "This repository is archived and read-only. Unarchive it to allow pushes, commits and edits again.",
      archive: "Archive repository",
      unarchive: "Unarchive repository",
      working: "Working…",
      confirmTitle: "Archive this repository?",
      confirmBody:
        "Pushes, commits, in-browser edits and transfers will be refused until it is unarchived. Reading, cloning and downloading are unaffected.",
      confirmCancel: "Cancel",
      confirmSubmit: "Archive repository",
    },
    delete: {
      title: "Delete this repository",
      description:
        "Permanently removes the repository and its git history; files it alone referenced are reclaimed by the next storage GC. This cannot be undone. Consider archiving it instead.",
      button: "Delete repository",
      confirmTitle: "Delete repository",
      confirmWarningTitle: "This cannot be undone",
      confirmWarning: "{repo}, its git history and its indexed files will be deleted permanently.",
      confirmCancel: "Cancel",
      confirmSubmit: "Delete this repository",
      deleting: "Deleting…",
    },
  },
};
