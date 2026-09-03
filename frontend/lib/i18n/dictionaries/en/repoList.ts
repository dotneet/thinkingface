// repoList: the /datasets and /models listing pages (filters, facets, cards included).
// NOTE: the en dictionary is the source of truth for shape, so it must not be `as const`.
export const repoList = {
  datasets: {
    title: "Datasets",
    blurb: "Browse published datasets, filter by tag, and open the Parquet viewer.",
  },
  models: {
    title: "Models",
    blurb: "Browse published model checkpoints and their files.",
  },
  searchPlaceholder: "Search by name, description, tag...",
  sort: {
    updated: "Recently updated",
    created: "Recently created",
    downloads: "Most downloads",
    name: "Name",
  },
  facets: {
    tags: "Tags",
    license: "License",
    task: "Task",
    relation: "Relation",
    // Read by the count column when the listing request itself failed: the
    // row still has to be there (a selected value stays removable even when
    // its facet dropped out), but a count drawn from a failed response would
    // be a fabricated zero, not a real one (DESIGN.md §9-1).
    countUnavailable: "Count unavailable",
  },
  lineage: {
    title: "Lineage",
    baseOnly: "Base models only",
    baseOnlyHint: "Hide fine-tunes, adapters, quantizations and merges.",
    derivedFrom: "Derived from {ref}",
    trainedOn: "Trained on {ref}",
    remove: "Remove this filter",
    /** Link from a repository's model tree into this listing, pre-filtered. */
    seeAll: "See all",
  },
  archive: {
    title: "Archive",
    all: "All",
    active: "Active",
    archived: "Archived",
  },
  clearFilters: "Clear filters",
  clearSearch: "Clear search",
  /** The chip row's own reset, worded apart from the sidebar's "Clear filters". */
  clearAll: "Clear all",
  /** The sidebar's own heading, so its "clear" control has a fixed row to sit in. */
  filtersTitle: "Filters",
  /** Collapsed filter panel on narrow screens: "Filters (3)". */
  filtersToggle: "Filters",
  filtersToggleWithCount: "Filters ({count})",
  /** The always-present row above the results: count on the left, chips on the right. */
  results: {
    countOne: "{count} repository",
    countOther: "{count} repositories",
    /** Prefixes each chip so the value is not just a bare word. */
    search: "Search",
    tag: "Tag",
    license: "License",
    task: "Task",
    relation: "Relation",
    baseModel: "Derived from",
    dataset: "Trained on",
    baseOnly: "Base models only",
    archivedOnly: "Archived only",
    activeOnly: "Active only",
    remove: "Remove filter: {value}",
  },
  noMatches: "No matches",
  tryRemovingFilter: "Try removing a filter or search term.",
  noDatasetsFound: "No datasets found",
  noModelsFound: "No models found",
  noDatasetsPublished: "No datasets have been published yet.",
  noModelsPublished: "No models have been published yet.",
  backendHint: "The backend API may not be running. Check API_URL / NEXT_PUBLIC_API_URL.",
  card: {
    experiment: "experiment",
  },
};
