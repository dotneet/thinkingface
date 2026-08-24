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
    downloads: "Downloads",
    downloads30d: "Downloads (30d)",
    size: "Size",
    files: "Files",
    license: "License",
    updated: "Updated",
    tags: "Tags",
    branches: "Branches",
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
  },
  blob: {
    fileNotFound: "File not found in tree listing.",
    edit: "Edit",
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
