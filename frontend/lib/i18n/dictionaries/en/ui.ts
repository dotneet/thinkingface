// UI primitives with built-in copy. Used from both Server and Client
// Components (e.g. ErrorState's required `title` default), so keys here
// cannot assume a client-only translator is available.
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const ui = {
  skipToContent: "Skip to content",
  copy: "Copy",
  copied: "Copied",
  close: "Close",
  cellValue: "Cell value",
  viewFullValue: "Click to view full value",
  // Table cell renderers: image thumbnails and the JSON tree (ValueCell /
  // CellModal / JsonTree). `*One` / `*Other` pairs are picked by the caller —
  // the translator has no plural machinery, so the count goes in as a param.
  cell: {
    image: "Image",
    imageUnavailable: "Image unavailable",
    viewImage: "Click to view the image",
    viewMode: "Cell view mode",
    tree: "Tree",
    raw: "Raw",
    expand: "Expand",
    collapse: "Collapse",
    keysOne: "{count} key",
    keysOther: "{count} keys",
    itemsOne: "{count} item",
    itemsOther: "{count} items",
    moreItems: "… {count} more",
    showFullString: "Show the full string",
  },
  errorStateTitle: "Couldn't load this",
  pagination: {
    range: "{from}–{to} of {total}",
    prev: "Prev",
    next: "Next",
    outOfRangeTitle: "This page has no results",
    outOfRangeDescription: "You've paged past the end of the list.",
    backToFirstPage: "Back to the first page",
  },
  confirmDialog: {
    defaultCancel: "Cancel",
    defaultConfirm: "Confirm",
    typeToConfirm: "Type {value} to confirm",
  },
  // Rendered Markdown (components/ui/markdown.tsx and its leaf components).
  markdown: {
    headingAnchor: "Permalink: {heading}",
    table: "Table",
    copyCode: "Copy code",
  },
  markdownEditor: {
    modeLabel: "View mode",
    modeEdit: "Edit",
    modePreview: "Preview",
    modeSplit: "Split",
    nothingToPreview: "Nothing to preview yet.",
    status: "{lines} lines · {chars} characters",
  },
  // [S14]: app/error.tsx, app/global-error.tsx, and the per-repo
  // error.tsx boundaries. global-error.tsx has no I18nProvider (it replaces
  // the root layout that provides it), so it resolves a translator directly
  // via createTranslator() instead of useT() — keep this domain's keys
  // usable that way (no dependence on request-only context).
  unexpectedError: {
    title: "Something went wrong",
    description:
      "An unexpected error occurred while rendering this page. You can try again, or go back.",
    retry: "Try again",
    goHome: "Go to homepage",
    backToRepo: "Back to repository",
  },
};
