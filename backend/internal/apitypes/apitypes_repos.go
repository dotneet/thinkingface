package apitypes

import "time"

// ------------------------------------------------------------ repositories

// RepoSummary is the repository shape used in listings and search results.
type RepoSummary struct {
	ID        int64    `json:"id"`
	Kind      RepoKind `json:"kind"`
	Namespace string   `json:"namespace"`
	// NamespaceKind says whether Namespace is a user or an organisation, so
	// the UI can link to the right profile page.
	NamespaceKind NamespaceKind `json:"namespace_kind"`
	Name          string        `json:"name"`
	// FullName is "namespace/name".
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	// License is "" when the repository card does not name one.
	License   string `json:"license"`
	Downloads int64  `json:"downloads"`
	// TotalSize is the sum of all file sizes, in bytes.
	TotalSize int64 `json:"total_size"`
	NumFiles  int   `json:"num_files"`
	// IsExperiment marks a dataset repository holding tracked training runs.
	IsExperiment  bool      `json:"is_experiment"`
	DefaultBranch string    `json:"default_branch"`
	HeadSHA       string    `json:"head_sha"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Archived marks a read-only repository: it still resolves, clones and
	// downloads, but every write is refused with 403 until it is unarchived.
	Archived bool `json:"archived"`
	// ArchivedAt is when it was archived, or null while it is active.
	ArchivedAt *time.Time `json:"archived_at" tstype:"string | null,required"`
}

// ParquetSummary describes an indexed parquet file inside a repository.
type ParquetSummary struct {
	Path         string `json:"path"`
	NumRows      int64  `json:"num_rows"`
	NumRowGroups int    `json:"num_row_groups"`
	NumColumns   int    `json:"num_columns"`
	Size         int64  `json:"size"`
}

// RepoDetail is the summary plus everything a repository page needs.
type RepoDetail struct {
	RepoSummary `tstype:",extends"`
	// Card is the parsed YAML front matter of README.md.
	Card   map[string]any `json:"card"`
	Readme string         `json:"readme"`
	// ReadmeTooLarge is true when README.md exists but exceeds the server's
	// size limit for rendering, in which case Readme is left empty instead of
	// silently looking like "no README". Card is unaffected: it comes from the
	// index built at push time, not from this read.
	ReadmeTooLarge bool   `json:"readme_too_large"`
	CloneURL       string `json:"clone_url"`
	// SSHCloneURL is the git-over-SSH remote, empty when TF_SSH_ENABLED is
	// off. It is served because the port is deployment-specific and cannot be
	// guessed: the UI happily let people register an SSH key at
	// /settings/ssh-keys while showing no URL that key could be used against.
	SSHCloneURL string   `json:"ssh_clone_url"`
	Branches    []string `json:"branches"`
	TagsRefs    []string `json:"tags_refs"`
	// ParquetFiles lists the indexed parquet files on the default branch.
	ParquetFiles []ParquetSummary `json:"parquet_files"`
	// Indexing reports that a background index of this repository is running,
	// so ParquetFiles may still be incomplete.
	Indexing bool `json:"indexing"`
	// CanWrite tells the web UI whether to offer in-browser editing. It is
	// false for an archived repository even when the viewer would otherwise
	// have write access, so every editing affordance disappears at once.
	CanWrite bool `json:"can_write"`
	// CanAdmin tells the web UI whether to offer the owner-only operations:
	// transfer, archive/unarchive and delete. Unlike CanWrite it stays true
	// while the repository is archived -- unarchiving it is exactly what an
	// owner needs to be able to do.
	CanAdmin bool `json:"can_admin"`
	// DownloadsLast30Days is the resolve-endpoint hit count for the trailing
	// 30 days. RepoSummary.Downloads (embedded above) stays the all-time
	// cumulative counter.
	DownloadsLast30Days int64 `json:"downloads_last_30_days"`
}

// RepoDetailResponse wraps the repository page's data in its envelope.
type RepoDetailResponse struct {
	Repo RepoDetail `json:"repo"`
}

// RepoUpdateRequest is the body of PATCH /api/v1/repos/{kind}/{ns}/{name}.
// Every field is optional and absent ones are left unchanged, so new
// configuration fields can be added here without breaking existing callers;
// today there is only one, and the request must set it (there is nothing
// else to update).
type RepoUpdateRequest struct {
	// DefaultBranch switches which branch clone, tree listings, the
	// repository card, lineage and the parquet index read by default. The
	// branch must already exist in the repository.
	DefaultBranch *string `json:"default_branch,omitempty"`
	// Name renames the repository inside its current namespace, leaving a
	// redirect behind exactly as a transfer does. Renaming is a rename, not
	// a change of owner: it deliberately does not go through the transfer
	// approval flow, which exists because the *destination namespace* has to
	// consent -- here the destination is the namespace it already lives in.
	Name *string `json:"name,omitempty"`
	// Description replaces the repository's one-line description. A README
	// card that carries its own `description` still wins on the next push
	// (the card is the source of truth when it says anything); this field is
	// what a repository with no card description has instead.
	Description *string `json:"description,omitempty"`
}

// RepoFacetItem is one value of a listing facet (a tag, a license, a task)
// together with how many repositories in the current result set carry it.
type RepoFacetItem struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// RepoFacets aggregates the filterable repository-card dimensions the
// listing sidebar offers. Each facet is computed under every filter
// currently applied except its own dimension, so picking a value shows how
// many *more* results narrowing further would leave.
type RepoFacets struct {
	Tags     []RepoFacetItem `json:"tags"`
	Licenses []RepoFacetItem `json:"licenses"`
	Tasks    []RepoFacetItem `json:"tasks"`
	// Relations counts the base model relations present in the result set
	// (LineageRelation, or whatever else a card declared), so the sidebar can
	// offer "quantized (12)". A repository that declares no base model is in
	// no bucket: "base only" is the `base_only=true` filter, not a relation.
	Relations []RepoFacetItem `json:"relations"`
}

// RepoListResponse is one page of a repository listing.
type RepoListResponse struct {
	Items []RepoSummary `json:"items"`
	// Total is how many repositories match, ignoring the page window.
	Total int64 `json:"total"`
	// Facets is only populated for GET /api/v1/repos; the HF-compatible
	// list endpoints leave it as its zero value.
	Facets RepoFacets `json:"facets"`
}

// StatsResponse holds the dashboard counters.
type StatsResponse struct {
	Datasets    int64 `json:"datasets"`
	Models      int64 `json:"models"`
	Experiments int64 `json:"experiments"`
	// TotalSize is the sum of every visible repository's size, in bytes.
	TotalSize int64 `json:"total_size"`
}

// -------------------------------------------------------------- storage usage

// UsageNamespace aggregates one namespace's storage footprint: the actual
// bytes kept in GCS (the LFS objects its repositories reference -- plain git
// blobs never leave the repository, so they cost nothing in GCS and are not
// counted here), how many files are indexed across those repositories, and
// how many repositories it holds.
type UsageNamespace struct {
	Namespace string `json:"namespace"`
	LFSSize   int64  `json:"lfs_size"`
	NumFiles  int64  `json:"num_files"`
	NumRepos  int64  `json:"num_repos"`
	// EffectiveQuotaBytes is the storage limit actually enforced for this
	// namespace (its own override, or the instance default). Null means
	// unlimited. Only a site administrator can change it -- an organisation
	// admin raising their own cap would not be a cap.
	//
	// It is spelled the same as AdminNamespaceUsage.EffectiveQuotaBytes on
	// purpose: `quota_bytes` there means the *override*, and one name that
	// means the resolved limit in one response and the raw override in
	// another is a field whose null is read backwards half the time.
	EffectiveQuotaBytes *int64 `json:"effective_quota_bytes" tstype:"number | null,required"`
}

// UsageRepo is one repository's contribution to storage usage.
type UsageRepo struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Kind      RepoKind `json:"kind"`
	FullName  string   `json:"full_name"`
	LFSSize   int64    `json:"lfs_size"`
	NumFiles  int64    `json:"num_files"`
}

// UsageResponse is the body of GET /api/v1/usage.
type UsageResponse struct {
	Namespaces []UsageNamespace `json:"namespaces"`
	// Repos is sorted by LFSSize descending.
	Repos []UsageRepo `json:"repos"`
}
