// Package apitypes holds the JSON shapes the HTTP API sends to and accepts
// from clients -- the wire contract, and nothing else.
//
// These declarations are the single source of truth for the API's shape:
// `make gen-types` runs tygo over this package to produce
// frontend/types/api.gen.ts, so a field added or renamed here shows up in the
// TypeScript types without anyone editing them by hand.
//
// Two rules keep that guarantee honest:
//
//   - Handlers must marshal these types rather than ad-hoc map[string]any
//     literals; a map has no declaration for tygo to read.
//   - Packages whose result type *is* the wire shape (viewer, modelmeta,
//     experiments) alias the type declared here instead of declaring their
//     own, so there is exactly one definition of every field.
//
// Persistence models in internal/store deliberately stay separate: they carry
// primary keys and other columns that never leave the server, and are mapped
// onto these types explicitly.
package apitypes

import "time"

//tygo:emit
var _ = `
/**
 * A single point on a metric chart, as [x, y].
 */
export type MetricPoint = [number, number];
`

// ------------------------------------------------------------ string unions

// RepoKind is the kind of artifact a repository holds.
type RepoKind string

const (
	RepoKindDataset RepoKind = "dataset"
	RepoKindModel   RepoKind = "model"
)

// NamespaceKind distinguishes a personal namespace from an organisation.
type NamespaceKind string

const (
	NamespaceKindUser NamespaceKind = "user"
	NamespaceKindOrg  NamespaceKind = "org"
)

// TokenScope is the access level granted to an API token.
type TokenScope string

const (
	TokenScopeRead  TokenScope = "read"
	TokenScopeWrite TokenScope = "write"
)

// EntryType tells a directory listing entry apart from a file.
type EntryType string

const (
	EntryTypeFile      EntryType = "file"
	EntryTypeDirectory EntryType = "directory"
)

// PreviewKind names the viewer the web UI should open a file with. It is the
// empty string for directories, which have no preview.
type PreviewKind string

const (
	PreviewKindNone     PreviewKind = ""
	PreviewKindParquet  PreviewKind = "parquet"
	PreviewKindModel    PreviewKind = "model"
	PreviewKindText     PreviewKind = "text"
	PreviewKindImage    PreviewKind = "image"
	PreviewKindMarkdown PreviewKind = "markdown"
	PreviewKindBinary   PreviewKind = "binary"
)

// FileEncoding says how the bytes in a raw file response were encoded: valid
// UTF-8 is passed through, anything else is base64.
type FileEncoding string

const (
	FileEncodingUTF8   FileEncoding = "utf-8"
	FileEncodingBase64 FileEncoding = "base64"
)

// ModelFormat names a supported checkpoint container.
type ModelFormat string

const (
	ModelFormatSafetensors ModelFormat = "safetensors"
	ModelFormatPyTorch     ModelFormat = "pytorch"
)

// RunStatus is the lifecycle state of an experiment run.
type RunStatus string

const (
	RunStatusRunning  RunStatus = "running"
	RunStatusFinished RunStatus = "finished"
	RunStatusFailed   RunStatus = "failed"
	// RunStatusStale is a *derived* status, never stored: a run still
	// recorded as running whose last update is older than the staleness
	// window reads as stale. A training job killed by OOM or a lost host
	// never gets to call finish(), and before this existed such a run sat in
	// the listing as "running" forever, indistinguishable from a live one.
	RunStatusStale RunStatus = "stale"
)

// ------------------------------------------------------- accounts and auth

// Namespace is somewhere the user may create repositories.
type Namespace struct {
	Name string        `json:"name"`
	Kind NamespaceKind `json:"kind"`
	// Role is the user's role in this namespace ("admin", "write", ...), or
	// "" when membership carries no explicit role.
	Role string `json:"role"`
}

// User is the signed-in account as the web UI sees it.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"is_admin"`
	// DisplayName and AvatarURL come from the user's own namespace row
	// (docs/dev/namespace-design.md §5.3); both may be "".
	DisplayName string      `json:"display_name"`
	AvatarURL   string      `json:"avatar_url"`
	Namespaces  []Namespace `json:"namespaces"`
}

// UserResponse wraps the account in the envelope /me, /login and /signup use.
type UserResponse struct {
	User User `json:"user"`
}

// ------------------------------------------------------------- namespaces

// NamespaceProfile is the public face of a namespace -- a user or an
// organisation -- as GET /api/v1/namespaces/{ns} returns it
// (docs/dev/namespace-design.md §7.1). Both kinds share the same profile
// columns on the namespaces row; the organisation-only fields are zero for
// a user namespace.
type NamespaceProfile struct {
	// Name is the canonical spelling. Namespace names are case-insensitive,
	// so a lookup for "Alice" answers with Name "alice" when that is how the
	// account was registered; the UI redirects to the canonical URL.
	Name        string        `json:"name"`
	Kind        NamespaceKind `json:"kind"`
	DisplayName string        `json:"display_name"`
	Description string        `json:"description"`
	Website     string        `json:"website"`
	AvatarURL   string        `json:"avatar_url"`
	CreatedAt   time.Time     `json:"created_at"`

	NumModels      int64 `json:"num_models"`
	NumDatasets    int64 `json:"num_datasets"`
	NumExperiments int64 `json:"num_experiments"`

	// NumMembers and MembersVisibility only mean something for an
	// organisation; a user namespace reports 0 and "".
	NumMembers        int64             `json:"num_members"`
	MembersVisibility MembersVisibility `json:"members_visibility"`

	// ViewerRole is the caller's effective role (docs/dev/organization-design.md
	// §3.1): "admin" for the owner of a user namespace and for a site admin,
	// the org_members role for an organisation, "" otherwise.
	ViewerRole OrgRole `json:"viewer_role"`
	// CanEdit is ViewerRole == "admin", spelled out so the UI can show the
	// "Edit profile" / "Settings" button without re-deriving it.
	CanEdit bool `json:"can_edit"`
}

// NamespaceResponse wraps one profile.
type NamespaceResponse struct {
	Namespace NamespaceProfile `json:"namespace"`
}

// NamespaceProfileUpdate is the body of PATCH /api/v1/me/profile. Every
// field is optional; a present field replaces the stored value (an empty
// string clears it). The namespace name itself is not editable
// (docs/dev/namespace-design.md §5.4).
type NamespaceProfileUpdate struct {
	DisplayName *string `json:"display_name,omitempty"`
	Description *string `json:"description,omitempty"`
	Website     *string `json:"website,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

// TokenItem is one API token, without its secret value.
type TokenItem struct {
	ID    int64      `json:"id"`
	Name  string     `json:"name"`
	Scope TokenScope `json:"scope"`
	// CreatedAt is an RFC 3339 timestamp.
	CreatedAt time.Time `json:"created_at"`
	// LastUsedAt is null until the token authenticates a request.
	LastUsedAt *time.Time `json:"last_used_at" tstype:"string | null,required"`
	// ExpiresAt is null for a token that never expires.
	ExpiresAt *time.Time `json:"expires_at" tstype:"string | null,required"`
}

// TokenListResponse is the body of GET /api/v1/tokens.
type TokenListResponse struct {
	Items []TokenItem `json:"items"`
}

// CreateTokenResponse returns the freshly minted token. The plaintext value
// appears here and nowhere else, so a client that loses it must issue another.
type CreateTokenResponse struct {
	TokenItem `tstype:",extends"`
	Token     string `json:"token"`
}

// SSHKeyItem is one registered SSH public key. Unlike a token there is no
// secret to withhold: the key material is public by construction, so it is
// returned in full and the UI can show it.
type SSHKeyItem struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// KeyType is the algorithm name, e.g. "ssh-ed25519".
	KeyType string `json:"key_type"`
	// PublicKey is the canonical "<type> <base64>" authorized_keys line,
	// with the comment stripped.
	PublicKey string `json:"public_key"`
	// Fingerprint is the OpenSSH "SHA256:<base64>" form, which is what
	// `ssh-keygen -lf` prints.
	Fingerprint string `json:"fingerprint"`
	// CreatedAt is an RFC 3339 timestamp.
	CreatedAt time.Time `json:"created_at"`
	// LastUsedAt is null until the key authenticates an SSH session.
	LastUsedAt *time.Time `json:"last_used_at" tstype:"string | null,required"`
}

// SSHKeyListResponse is the body of GET /api/v1/me/ssh-keys.
type SSHKeyListResponse struct {
	Items []SSHKeyItem `json:"items"`
}

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
	// QuotaBytes is the storage limit actually enforced for this namespace
	// (its own override, or the instance default). Null means unlimited.
	// Only a site administrator can change it -- an organisation admin
	// raising their own cap would not be a cap.
	QuotaBytes *int64 `json:"quota_bytes" tstype:"number | null,required"`
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

// -------------------------------------------------------------- file browser

// CommitInfoUI is one commit as the file browser and history views show it.
type CommitInfoUI struct {
	OID string `json:"oid"`
	// Message is the commit's subject line only.
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// TreeEntryUI is one row of the repository file browser.
type TreeEntryUI struct {
	Type EntryType `json:"type"`
	Name string    `json:"name"`
	Path string    `json:"path"`
	Size int64     `json:"size"`
	// LFS reports that the content lives in object storage, not in git.
	LFS       bool   `json:"lfs"`
	OID       string `json:"oid"`
	IsParquet bool   `json:"is_parquet"`
	// IsModel is set for checkpoint files the model inspector can read.
	IsModel bool        `json:"is_model"`
	Preview PreviewKind `json:"preview"`
	// LastCommit is the most recent commit that changed this entry, or null
	// when the history walk hit its cap before finding it.
	LastCommit *CommitInfoUI `json:"last_commit" tstype:"CommitInfoUI | null,required"`
	// GcloudCommand is a ready-made, shell-quoted `gcloud storage cp` that
	// fetches this entry's bytes from the bucket into ./{name}: the
	// content-addressed lfs/ object for an LFS file, the blobs/ object for any
	// other file. Empty for directories, and for a plain (non-LFS) file whose
	// blob the sync worker has not indexed for rev yet -- the worker publishes
	// to blobs/ before it writes the index, so an indexed blob is promised to
	// be there and an unindexed one (a fresh push, a bare commit) is not.
	GcloudCommand string `json:"gcloud_command"`
}

// RepoGCSFile is one file of a revision together with where its bytes live
// in the bucket.
type RepoGCSFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  bool   `json:"lfs"`
	// URI is the gs:// URI of the object holding the file's bytes.
	URI string `json:"uri"`
}

// RepoGCSResponse is what GET /api/v1/repos/{kind}/{ns}/{name}/gcs/{rev}
// returns: the bucket location of every file on an indexed ref, plus
// ready-made snippets to fetch them. Objects are content-addressed
// (lfs/{oid}, blobs/{sha}), so the human-readable layout lives in the
// destination side of these snippets, never in the bucket.
type RepoGCSResponse struct {
	Ref   string        `json:"ref"`
	Files []RepoGCSFile `json:"files"`
	// GcloudScript is a POSIX shell script that copies every file to
	// $DEST/{path} with `gcloud storage cp`. DEST defaults to ./{name} and
	// may be a local directory or a gs:// prefix.
	GcloudScript string `json:"gcloud_script"`
	// DuckDBSnippet is a read_parquet() call over the revision's parquet
	// files, or "" when it has none.
	DuckDBSnippet string `json:"duckdb_snippet"`
}

// TreeResponseUI is a directory listing, with the directory's README if it
// has one.
type TreeResponseUI struct {
	Path    string        `json:"path"`
	Entries []TreeEntryUI `json:"entries"`
	// Readme is the rendered source of README.md in this directory, or null.
	Readme *string `json:"readme" tstype:"string | null,required"`
	// ReadmeTooLarge is true when this directory's README.md exists but
	// exceeds the server's size limit for rendering, in which case Readme is
	// left null instead of silently looking like "no README".
	ReadmeTooLarge bool `json:"readme_too_large"`
	// LatestCommit is the commit the listing was taken at (null for an empty
	// repository).
	LatestCommit *CommitInfoUI `json:"latest_commit" tstype:"CommitInfoUI | null,required"`
}

// RefUI is one branch or tag of a repository.
type RefUI struct {
	Name      string `json:"name"`
	TargetOID string `json:"target_oid"`
}

// RefsResponseUI feeds the revision picker in the file browser.
type RefsResponseUI struct {
	Branches      []RefUI `json:"branches"`
	Tags          []RefUI `json:"tags"`
	DefaultBranch string  `json:"default_branch"`
}

// CommitListResponse is one page of a revision's history, newest first.
type CommitListResponse struct {
	Commits []CommitInfoUI `json:"commits"`
	// NextCursor is the value to pass as ?after= for the next page, or null
	// when this page reached the root commit.
	NextCursor *string `json:"next_cursor" tstype:"string | null,required"`
}

// ------------------------------------------------------------- commit diff

// DiffStatus is what happened to one path between two commits.
type DiffStatus string

const (
	DiffStatusAdded    DiffStatus = "added"
	DiffStatusModified DiffStatus = "modified"
	DiffStatusDeleted  DiffStatus = "deleted"
)

// DiffNoPatchReason says why a file carries no unified diff. It is stated
// rather than inferred: a reader that works it out from Binary/LFS being
// false has to assume the only remaining reason is size, and there are two
// others -- which is how an empty file came to be reported as "too large to
// diff".
type DiffNoPatchReason string

const (
	// DiffNoPatchNone is the value on a file that does carry a patch.
	DiffNoPatchNone DiffNoPatchReason = ""
	DiffNoPatchLFS  DiffNoPatchReason = "lfs"
	// DiffNoPatchBinary is a file that is not text on either side.
	DiffNoPatchBinary DiffNoPatchReason = "binary"
	// DiffNoPatchTooLarge is a text file whose blob exceeded the size budget.
	DiffNoPatchTooLarge DiffNoPatchReason = "too_large"
	// DiffNoPatchNoTextChange is a change with nothing to render as lines:
	// an added or deleted empty file, or a file whose mode moved while its
	// bytes did not. The change is real; it just has no lines in it.
	DiffNoPatchNoTextChange DiffNoPatchReason = "no_text_change"
	// DiffNoPatchUnsupported is a path that is not a regular file on either
	// side, such as a submodule.
	DiffNoPatchUnsupported DiffNoPatchReason = "unsupported"
)

// DiffFile is one path's change in a commit. Additions/Deletions are line
// counts and are 0 for a file with no textual diff (binary, LFS, or one whose
// patch was skipped for size) -- read HasPatch to tell "no lines changed"
// apart from "lines were not counted", rather than showing a 0 that looks
// like a fact, and NoPatchReason for why there is no patch.
type DiffFile struct {
	Path   string     `json:"path"`
	Status DiffStatus `json:"status"`
	// Additions and Deletions are meaningless unless HasPatch is true.
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	// Binary reports a path whose contents are not text on either side.
	Binary bool `json:"binary"`
	// LFS reports a path stored as a Git LFS pointer. The pointer itself is
	// text, but diffing it shows an oid changing rather than the content, so
	// it is called out instead of rendered.
	LFS bool `json:"lfs"`
	// HasPatch reports whether Patch carries a unified diff.
	HasPatch bool `json:"has_patch"`
	// NoPatchReason says why, whenever HasPatch is false; it is "" exactly
	// when HasPatch is true.
	NoPatchReason DiffNoPatchReason `json:"no_patch_reason"`
	// Patch is a unified diff body without the `diff --git` header, empty
	// unless HasPatch.
	Patch string `json:"patch"`
	// PatchTruncated reports that Patch was cut off mid-diff.
	PatchTruncated bool `json:"patch_truncated"`
	// OldSize and Size are the blob sizes on each side; 0 where the path did
	// not exist on that side.
	OldSize int64 `json:"old_size"`
	Size    int64 `json:"size"`
}

// CommitDiffResponse is the body of
// GET /api/v1/repos/{kind}/{ns}/{name}/diff/{rev}: what one commit changed,
// against its first parent. A merge commit is diffed against its first parent
// only, which is what makes the file list readable.
type CommitDiffResponse struct {
	Commit CommitInfoUI `json:"commit"`
	// ParentOID is null for the root commit, where every file reads as added.
	ParentOID *string `json:"parent_oid" tstype:"string | null,required"`
	// Files is capped; FilesTruncated says the commit touched more paths
	// than are listed. NumFiles is always the true total.
	Files          []DiffFile `json:"files"`
	NumFiles       int        `json:"num_files"`
	FilesTruncated bool       `json:"files_truncated"`
	// Additions and Deletions total the per-file counts that were computed.
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
}

// RawFileResponse is a file's contents, cut off at the preview limit.
type RawFileResponse struct {
	Path string `json:"path"`
	// Size is the file's full size, even when Content was truncated.
	Size      int64        `json:"size"`
	Truncated bool         `json:"truncated"`
	Content   string       `json:"content"`
	Encoding  FileEncoding `json:"encoding"`
}

// EditFileRequest saves an in-browser edit as a commit.
type EditFileRequest struct {
	Content     string `json:"content"`
	Message     string `json:"message"`
	Description string `json:"description,omitempty"`
	// BaseOID is the blob SHA the edit started from. When set, the commit is
	// refused if the path has moved on since, so a save never silently
	// clobbers someone else's concurrent edit. Omit it when creating a file.
	BaseOID string `json:"base_oid,omitempty"`
}

// EditFileResponse reports where the edit landed.
type EditFileResponse struct {
	Path      string `json:"path"`
	CommitOID string `json:"commit_oid"`
	OID       string `json:"oid"`
	Size      int64  `json:"size"`
}

// DeleteFileRequest removes one file in a commit of its own. Every field is
// optional: an empty body deletes the path named in the URL with a generated
// commit message and no staleness check.
type DeleteFileRequest struct {
	Message     string `json:"message,omitempty"`
	Description string `json:"description,omitempty"`
	// BaseOID is the blob SHA the caller last saw at this path. When set, the
	// delete is refused if the path has moved on since, so it never removes a
	// version nobody looked at. For an LFS file this is the SHA of the
	// *pointer* blob, which is what the tree listing reports.
	BaseOID string `json:"base_oid,omitempty"`
}

// RenameFileRequest moves one file to a new path in a single commit. Doing
// it as one commit is the point: the browser had to create-then-delete before
// this existed, which put two commits in the history for one rename and left
// the repository momentarily holding both copies.
type RenameFileRequest struct {
	// NewPath is the destination, relative to the repository root. Its
	// parent directories do not need to exist -- git has no empty
	// directories, so a path is all it takes to "create" one.
	NewPath     string `json:"new_path"`
	Message     string `json:"message,omitempty"`
	Description string `json:"description,omitempty"`
	// BaseOID is the blob SHA the caller last saw at the source path. When
	// set, the rename is refused if the path has moved on since. For an LFS
	// file this is the SHA of the *pointer* blob, as the tree listing
	// reports it -- the object itself is untouched by a rename, which is why
	// renaming an LFS file costs no transfer.
	BaseOID string `json:"base_oid,omitempty"`
}

// RenameFileResponse reports where the rename landed.
type RenameFileResponse struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path"`
	CommitOID string `json:"commit_oid"`
	OID       string `json:"oid"`
	Size      int64  `json:"size"`
}

// UploadFilesResponse reports the single commit one browser upload produced.
// Paths lists what landed, in the order the parts arrived.
type UploadFilesResponse struct {
	CommitOID string   `json:"commit_oid"`
	Paths     []string `json:"paths"`
}

// ---------------------------------------------------------- parquet viewer

// ParquetColumn describes one column of a parquet schema.
type ParquetColumn struct {
	// Name is the column's name.
	Name string `json:"name"`
	// Type is the physical parquet type, e.g. "INT64", "BYTE_ARRAY". For
	// non-leaf (nested group) columns this is "GROUP".
	Type string `json:"type"`
	// LogicalType is the logical type annotation, e.g. "STRING",
	// "TIMESTAMP(MICROS)", "LIST", "MAP", or "" when none is set.
	LogicalType string `json:"logical_type"`
	// Optional reports whether the column may be null.
	Optional bool `json:"optional"`
	// Repeated reports whether the column may hold multiple values per row.
	Repeated bool `json:"repeated"`
	// Feature is the Hugging Face `datasets` feature type of the column,
	// lower-cased (e.g. "image", "audio", "classlabel"), when the file or the
	// repository's README declares one; "" otherwise. The viewer reads it
	// from the parquet key-value metadata written by `datasets` (the
	// "huggingface" key) and falls back to the README's
	// `dataset_info.features`. It is a rendering hint only: an "image"
	// column's values are the usual `{bytes, path}` struct or raw bytes.
	Feature string `json:"feature"`
}

// ParquetSchemaResponse describes a parquet file without reading its rows.
type ParquetSchemaResponse struct {
	Path         string          `json:"path"`
	Size         int64           `json:"size"`
	NumRows      int64           `json:"num_rows"`
	NumRowGroups int             `json:"num_row_groups"`
	Compression  string          `json:"compression"`
	Columns      []ParquetColumn `json:"columns"`
}

// ParquetRowsResponse is one page of decoded parquet rows.
type ParquetRowsResponse struct {
	Path string `json:"path"`
	// Offset is the row offset this page starts at.
	Offset int64 `json:"offset"`
	// Limit is the page size that was applied, after clamping.
	Limit int `json:"limit"`
	// NumRows is the total number of rows in the file, not just this page.
	NumRows int64 `json:"num_rows"`
	// Columns describes the columns present in Rows, in the requested order.
	Columns []ParquetColumn `json:"columns"`
	// Rows holds one JSON-safe object per row, keyed by column name.
	Rows []map[string]any `json:"rows"`
}

// ---------------------------------------------------------- model inspector

// ModelTensor is one named tensor in a checkpoint.
type ModelTensor struct {
	Name string `json:"name"`
	// DType is the framework-neutral dtype name, e.g. "float32", "bfloat16".
	DType string  `json:"dtype"`
	Shape []int64 `json:"shape"`
	// NumParameters is the product of Shape (1 for a scalar tensor).
	NumParameters int64 `json:"num_parameters"`
	// SizeBytes is NumParameters * the dtype's width, 0 when the width is
	// unknown.
	SizeBytes int64 `json:"size_bytes"`
}

// ModelDTypeStat aggregates the tensors sharing one dtype.
type ModelDTypeStat struct {
	DType         string `json:"dtype"`
	NumTensors    int    `json:"num_tensors"`
	NumParameters int64  `json:"num_parameters"`
	SizeBytes     int64  `json:"size_bytes"`
}

// ModelInfo is everything the inspector learns from a checkpoint's header.
type ModelInfo struct {
	Format ModelFormat `json:"format"`
	// NumTensors, NumParameters and TensorBytes cover the whole file even
	// when Tensors below is truncated.
	NumTensors    int              `json:"num_tensors"`
	NumParameters int64            `json:"num_parameters"`
	TensorBytes   int64            `json:"tensor_bytes"`
	DTypes        []ModelDTypeStat `json:"dtypes"`
	// Metadata is the file's own metadata: the safetensors `__metadata__`
	// map, or the scalar entries sitting next to the weights in a PyTorch
	// checkpoint (epoch, global_step, ...).
	Metadata map[string]string `json:"metadata"`
	// HeaderBytes is the size of the parsed header (the safetensors JSON
	// header or the pickled `data.pkl`).
	HeaderBytes int64         `json:"header_bytes"`
	Tensors     []ModelTensor `json:"tensors"`
	// Truncated reports that Tensors lists only the first few thousand
	// entries; the totals above still cover every tensor.
	Truncated bool `json:"truncated"`
	// Warnings carries recoverable problems, e.g. a structure the reader
	// only understood in part.
	Warnings []string `json:"warnings"`
}

// ModelMetaResponse flattens an inspection into the file's own identity, so
// the UI gets `path` and `size` alongside the header fields.
type ModelMetaResponse struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModelInfo `tstype:",extends"`
}

// ------------------------------------------------------------- experiments

// ExpProjectListItem is one experiment repository in the global listing.
type ExpProjectListItem struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	FullName    string    `json:"full_name"`
	NumProjects int       `json:"num_projects"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ExpProjectListResponse is the body of GET /api/v1/experiments.
type ExpProjectListResponse struct {
	Items []ExpProjectListItem `json:"items"`
	// Total is the number of matching repositories regardless of limit /
	// offset (docs/dev/namespace-design.md §5.6).
	Total int64 `json:"total"`
}

// ExpProject is one project (a group of runs) inside an experiment repository.
type ExpProject struct {
	Name      string    `json:"name"`
	NumRuns   int       `json:"num_runs"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExpRepoResponse is an experiment repository together with its projects.
type ExpRepoResponse struct {
	Repo     RepoSummary  `json:"repo"`
	Projects []ExpProject `json:"projects"`
}

// ExpRun is one training run's summary row.
type ExpRun struct {
	Name   string    `json:"name"`
	Status RunStatus `json:"status"`
	// LastStep is the highest step seen for this run.
	LastStep int64 `json:"last_step"`
	// NumPoints is how many metric points have been recorded.
	NumPoints int64 `json:"num_points"`
	// StartedAt is null for a run recovered from an export that carried no
	// start time.
	StartedAt *time.Time `json:"started_at" tstype:"string | null,required"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Config is the run's hyperparameters, as logged.
	Config     map[string]any `json:"config"`
	MetricKeys []string       `json:"metric_keys"`
	// Summary holds the last value seen for each metric.
	Summary map[string]float64 `json:"summary"`
	// Group is the sweep this run belongs to, as `trackio.init(group=...)`
	// declared it, and JobType the role it played in that sweep
	// (`job_type=...`). Both are "" for a run that declared neither, which is
	// how the run table tells a sweep member from a standalone run.
	Group   string `json:"group"`
	JobType string `json:"job_type"`
	// Tags are free-form labels a user attached to the run.
	Tags []string `json:"tags"`
	// Archived hides the run from the default listing without deleting it.
	Archived bool `json:"archived"`
	// IsBaseline marks the run every other run is compared against. At most one
	// run per project carries it.
	IsBaseline bool `json:"is_baseline"`
	// Note is free-form Markdown a user wrote about the run. Like the other
	// annotations it is never written by ingest or by the parquet indexer, so
	// re-indexing the project leaves it in place.
	Note string `json:"note"`
	// Models are the model repositories this run declared it produced
	// (`trackio.log_model`). Another annotation: ingest and the indexer leave
	// it alone.
	Models []ExpRunModelRef `json:"models"`
}

// ExpRunModelRef is one model a run recorded as its output.
type ExpRunModelRef struct {
	// RepoID is the model repository as "ns/name".
	RepoID string `json:"repo_id"`
	// Revision is the commit, branch or tag the run pinned, "" when the shim
	// could not resolve one. It is recorded verbatim and never verified: only
	// the repository's existence is checked (see Exists), so a link to a
	// revision that has since been rewritten fails in the file browser rather
	// than being hidden here.
	Revision string `json:"revision"`
	// Exists reports that RepoID resolves to a model repository this viewer
	// may read. A false value means the UI shows text and a warning instead of
	// a link -- the same treatment a dangling lineage reference gets.
	Exists bool `json:"exists"`
}

// ExpRunModelInput is one entry of a produced-model list being written. Unlike
// ExpRunModelRef it carries no Exists: that is the server's answer, not the
// client's claim.
type ExpRunModelInput struct {
	RepoID   string `json:"repo_id"`
	Revision string `json:"revision,omitempty" tstype:"string"`
}

// ExpRunListResponse is the body of the run listing endpoint.
type ExpRunListResponse struct {
	Runs []ExpRun `json:"runs"`
}

// ExpArtifact is one file a run stored under its artifact directory.
type ExpArtifact struct {
	// Name is the path relative to the run's artifact directory -- the name
	// `log_artifact` was given, possibly with subdirectories.
	Name string `json:"name"`
	// Path is the full path inside the repository, which is what the file
	// browser and `resolve` need.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// LFS reports that the file is stored as a Git LFS pointer.
	LFS bool `json:"lfs"`
	// Preview is how the file browser would render this file, so the run page
	// can pick a matching icon and link.
	Preview PreviewKind `json:"preview"`
}

// ExpArtifactListResponse lists one run's artifacts.
type ExpArtifactListResponse struct {
	// Path is the directory the listing came from,
	// "{project}/artifacts/{run}" (docs/dev/api-contract.md §7).
	Path string `json:"path"`
	// Rev is the revision listed, always the repository's default branch.
	Rev       string        `json:"rev"`
	Artifacts []ExpArtifact `json:"artifacts"`
}

// ExpRunAnnotationRequest is a partial update of a run's annotations: an
// omitted field is left as it is, so a client can toggle one flag without
// having to send the rest.
//
// For the two list fields, Tags and Models, "omitted" and "empty" are
// different requests and JSON spells them differently: a missing key or an
// explicit null leaves the list unchanged, while [] replaces it with nothing
// -- which is the only way to clear one. Sending null to clear a list is the
// mistake this note exists to prevent.
type ExpRunAnnotationRequest struct {
	// Tags replaces the run's tag list wholesale; an empty array clears it.
	Tags       *[]string `json:"tags,omitempty" tstype:"string[]"`
	Archived   *bool     `json:"archived,omitempty" tstype:"boolean"`
	IsBaseline *bool     `json:"is_baseline,omitempty" tstype:"boolean"`
	Note       *string   `json:"note,omitempty" tstype:"string"`
	// Models replaces the run's produced-model list wholesale; an empty array
	// clears it. This is the write path behind `trackio.log_model`.
	Models *[]ExpRunModelInput `json:"models,omitempty" tstype:"ExpRunModelInput[]"`
}

// ExpRunAnnotationResponse returns the run as it stands after the update.
type ExpRunAnnotationResponse struct {
	Run ExpRun `json:"run"`
}

// ExpMetricSeries is one metric's trace for one run, as [x, y] pairs.
type ExpMetricSeries struct {
	Run    string       `json:"run"`
	Key    string       `json:"key"`
	Points [][2]float64 `json:"points" tstype:"MetricPoint[]"`
}

// ExpMetricsResponse carries the traces a chart asked for.
type ExpMetricsResponse struct {
	Series []ExpMetricSeries `json:"series"`
}

// ---------------------------------------------------------------- lineage

// LineageEdgeKind names the sort of provenance one lineage edge records.
type LineageEdgeKind string

const (
	// LineageEdgeKindDataset points at a dataset the repository was built from.
	LineageEdgeKindDataset LineageEdgeKind = "dataset"
	// LineageEdgeKindBaseModel points at the checkpoint it started from.
	LineageEdgeKindBaseModel LineageEdgeKind = "base_model"
	// LineageEdgeKindRun points at the experiment run that produced it.
	LineageEdgeKindRun LineageEdgeKind = "run"
	// LineageEdgeKindEvalDataset points at a dataset the repository was
	// evaluated on, which is a different claim from having trained on it.
	LineageEdgeKindEvalDataset LineageEdgeKind = "eval_dataset"
	// LineageEdgeKindNewVersion points at the repository that supersedes this
	// one. It is the only kind that targets a repository of its own kind, and
	// it does not appear in RepoLineageResponse.Upstream: the resolved chain
	// in NewVersion says the same thing more usefully.
	LineageEdgeKindNewVersion LineageEdgeKind = "new_version"
)

// LineageRelation names how a repository relates to the base model it points
// at -- HuggingFace Hub's `base_model_relation`. A card may declare it
// outright; when it does not, the sync worker infers it from the repository's
// contents (docs/dev/api-contract.md §12).
//
// The wire fields carrying it are plain strings, not this type: a card is free
// to write something outside the four known values, and such a value is passed
// through verbatim rather than being rewritten into a lie. These constants are
// the set the UI groups by; everything else belongs under "other".
type LineageRelation string

const (
	// LineageRelationFinetune is further training from the base model's own
	// weights. It is the default when nothing more specific applies.
	LineageRelationFinetune LineageRelation = "finetune"
	// LineageRelationAdapter is a LoRA/PEFT adapter over the base model.
	LineageRelationAdapter LineageRelation = "adapter"
	// LineageRelationQuantized is the base model at a lower precision.
	LineageRelationQuantized LineageRelation = "quantized"
	// LineageRelationMerge is a blend of two or more base models.
	LineageRelationMerge LineageRelation = "merge"
)

// LineageRef is one upstream reference a repository card declares.
type LineageRef struct {
	Kind LineageEdgeKind `json:"kind"`
	// Raw is the reference exactly as the card spelled it, e.g.
	// "team/imdb-ja@v1". It is the only field worth showing when Exists is
	// false.
	Raw string `json:"raw"`
	// TargetKind is the repository kind this edge points at. Dataset and run
	// edges both target dataset repositories: experiment logs live in one.
	TargetKind RepoKind `json:"target_kind"`
	// Namespace, Name and FullName are the normalised target, all "" when the
	// raw reference does not parse as one.
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	FullName  string `json:"full_name"`
	// Rev is the branch, tag or commit the reference pinned, "" for none.
	Rev string `json:"rev"`
	// Project and Run are set on run edges only.
	Project string `json:"project"`
	Run     string `json:"run"`
	// Relation is how this repository relates to the base model it names --
	// one of LineageRelation, or whatever else the card declared. Set on
	// base_model edges only; "" on dataset and run edges.
	Relation string `json:"relation"`
	// Exists reports that the target resolves to a repository. A false value
	// means the UI must render plain text, not a link: the reference may be a
	// typo, or may not have been pushed yet.
	Exists bool `json:"exists"`
}

// LineageDependent is one repository that names this repository -- or one of
// its runs -- as part of its own origin.
type LineageDependent struct {
	Repo RepoSummary     `json:"repo"`
	Kind LineageEdgeKind `json:"kind"`
	// Raw is the dependent's own reference string, which shows how it pinned
	// this repository.
	Raw string `json:"raw"`
	Rev string `json:"rev"`
	// Project and Run name the run the dependent came from, on run edges only.
	Project string `json:"project"`
	Run     string `json:"run"`
	// Relation is how the dependent describes itself relative to this
	// repository -- one of LineageRelation, or whatever else its card
	// declared. Set on base_model edges only; "" on dataset and run edges.
	// It is what the model tree groups the derived repositories by.
	Relation string `json:"relation"`
}

// LineageSuccessor is where a repository's `new_version:` declaration leads:
// the successor its own card names, and the end of the chain that successor
// starts (docs/dev/api-contract.md §12).
type LineageSuccessor struct {
	// Direct is the successor this repository's card names outright. It is
	// the only field with anything in it when the reference is dangling, in
	// which case Direct.Exists is false and Hops is 0.
	Direct LineageRef `json:"direct"`
	// Latest is the newest version reachable by following `new_version:` from
	// one repository to the next -- what a reader should be sent to. It
	// equals Direct for a one-hop chain, and also whenever Truncated is set.
	Latest LineageRef `json:"latest"`
	// Hops is how many edges were followed to reach Latest: 1 for a direct
	// successor, 0 when the declared successor does not resolve.
	Hops int `json:"hops" tstype:"number"`
	// Truncated reports that the chain never ended -- it formed a cycle, or
	// ran past the walk's depth limit. Latest is then the direct successor
	// only, and the UI must not claim it is the newest version.
	Truncated bool `json:"truncated" tstype:"boolean"`
}

// RepoLineageResponse is a repository's provenance in both directions.
type RepoLineageResponse struct {
	// Upstream is what this repository's card declares it came from.
	Upstream []LineageRef `json:"upstream"`
	// Downstream is the reverse lookup: repositories whose cards point here.
	Downstream []LineageDependent `json:"downstream"`
	// NewVersion is the successor the card declares, resolved through the
	// chain of successors behind it, or null when the card declares none.
	// Successor edges are reported here instead of in Upstream: they point
	// forward in time, not back at an origin.
	NewVersion *LineageSuccessor `json:"new_version" tstype:"LineageSuccessor | null,required"`
	// ProducedBy lists the experiment runs that declared this repository as
	// their output (`trackio.log_model`). It is separate from the `run` edges
	// in Upstream because the claim comes from the other end: those are what
	// this repository's own card says, these are what a training script said.
	// Empty for anything but a model repository.
	ProducedBy []ExpRunProducer `json:"produced_by"`
}

// ExpRunProducer is one experiment run that declared it produced a model.
type ExpRunProducer struct {
	// Repo is the experiment *dataset* repository the run lives in (§7).
	Repo    RepoSummary `json:"repo"`
	Project string      `json:"project"`
	Run     string      `json:"run"`
	// Revision is the revision of the produced model the run recorded, "" if
	// it could not resolve one.
	Revision string `json:"revision"`
}

// ExpRunLineage lists the repositories one experiment run produced.
type ExpRunLineage struct {
	Run    string             `json:"run"`
	Models []LineageDependent `json:"models"`
}

// ExpLineageResponse carries the run-to-model links of an experiment project.
type ExpLineageResponse struct {
	Items []ExpRunLineage `json:"items"`
}

// ------------------------------------------------------------- transfers

// RepoTransferStatus is the lifecycle state of a transfer request
// (docs/dev/repo-transfer-design.md §7).
type RepoTransferStatus string

const (
	RepoTransferPending   RepoTransferStatus = "pending"
	RepoTransferAccepted  RepoTransferStatus = "accepted"
	RepoTransferRejected  RepoTransferStatus = "rejected"
	RepoTransferCancelled RepoTransferStatus = "cancelled"
	RepoTransferExpired   RepoTransferStatus = "expired"
)

// RepoTransferRequest asks to move a repository to another namespace (and
// optionally rename it at the same time).
type RepoTransferRequest struct {
	// Namespace is the destination user or organisation.
	Namespace string `json:"namespace"`
	// Name is the new repository name; empty keeps the current one.
	Name string `json:"name,omitempty"`
}

// RepoTransfer is one transfer request as the web UI sees it.
type RepoTransfer struct {
	ID            int64              `json:"id"`
	Kind          RepoKind           `json:"kind"`
	FromNamespace string             `json:"from_namespace"`
	FromName      string             `json:"from_name"`
	ToNamespace   string             `json:"to_namespace"`
	ToName        string             `json:"to_name"`
	RequestedBy   string             `json:"requested_by"`
	Status        RepoTransferStatus `json:"status"`
	ExpiresAt     time.Time          `json:"expires_at"`
	CreatedAt     time.Time          `json:"created_at"`
}

// RepoTransferResponse answers a transfer call. Repo is present only when the
// move completed (immediately, or on accept) and describes the repository at
// its new location.
type RepoTransferResponse struct {
	Transfer RepoTransfer `json:"transfer"`
	Repo     *RepoDetail  `json:"repo,omitempty"`
}

// MyTransfersResponse lists the pending transfers the signed-in user can act
// on: Incoming ones they may accept or reject, Outgoing ones they may cancel.
type MyTransfersResponse struct {
	Incoming []RepoTransfer `json:"incoming"`
	Outgoing []RepoTransfer `json:"outgoing"`
}

// --------------------------------------------------------- organisations
// (docs/dev/organization-design.md)

// OrgRole is a member's role in an organisation. "" means "not a member".
type OrgRole string

const (
	OrgRoleAdmin OrgRole = "admin"
	OrgRoleWrite OrgRole = "write"
	OrgRoleRead  OrgRole = "read"
)

// MembersVisibility says who may list an organisation's members.
type MembersVisibility string

const (
	MembersVisibilityMembers MembersVisibility = "members"
	MembersVisibilityPublic  MembersVisibility = "public"
)

// Org is one organisation as the web UI sees it.
type Org struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Website     string `json:"website"`
	AvatarURL   string `json:"avatar_url"`

	// MembersVisibility is about the member list, not about repositories:
	// there is no repository visibility concept here
	// (docs/dev/content-addressed-storage-design.md §1).
	MembersVisibility MembersVisibility `json:"members_visibility"`

	NumMembers int64     `json:"num_members"`
	NumRepos   int64     `json:"num_repos"`
	CreatedAt  time.Time `json:"created_at"`
	// ViewerRole is the caller's effective role ("admin" for a site admin,
	// "" when signed out or not a member).
	ViewerRole OrgRole `json:"viewer_role"`
}

// OrgMember is one membership row.
type OrgMember struct {
	Username string `json:"username"`
	// Email is "" when the member list is being viewed by a non-member
	// (members_visibility = "public").
	Email     string    `json:"email"`
	Role      OrgRole   `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// OrgAuditEntry is one line of an organisation's audit log.
type OrgAuditEntry struct {
	ID int64 `json:"id"`
	// Actor is the username that performed the action ("" when the account
	// has since been deleted and nothing was recorded).
	Actor  string `json:"actor"`
	Action string `json:"action"`
	// Target is the affected username, repository full name, or webhook URL,
	// depending on Action.
	Target    string         `json:"target"`
	Details   map[string]any `json:"details"`
	CreatedAt time.Time      `json:"created_at"`
}

// OrgCreateRequest is the body of POST /api/v1/orgs.
type OrgCreateRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// OrgUpdateRequest is the body of PATCH /api/v1/orgs/{org}. Every field is
// optional; absent ones are left unchanged.
type OrgUpdateRequest struct {
	DisplayName       *string            `json:"display_name,omitempty"`
	Description       *string            `json:"description,omitempty"`
	Website           *string            `json:"website,omitempty"`
	AvatarURL         *string            `json:"avatar_url,omitempty"`
	MembersVisibility *MembersVisibility `json:"members_visibility,omitempty"`
}

// OrgMemberAddRequest is the body of POST /api/v1/orgs/{org}/members.
type OrgMemberAddRequest struct {
	Username string `json:"username"`
	// Role defaults to "read" when empty.
	Role OrgRole `json:"role,omitempty"`
}

// OrgMemberUpdateRequest is the body of PATCH /api/v1/orgs/{org}/members/{username}.
type OrgMemberUpdateRequest struct {
	Role OrgRole `json:"role"`
}

// OrgResponse wraps one organisation.
type OrgResponse struct {
	Org Org `json:"org"`
}

// OrgListResponse is the body of GET /api/v1/orgs and GET /api/v1/me/orgs.
type OrgListResponse struct {
	Items []Org `json:"items"`
	Total int64 `json:"total"`
}

// OrgMembersResponse is one page of GET /api/v1/orgs/{org}/members. Total is
// the organisation's whole membership, ignoring the page window, so a client
// can tell a full page with more behind it from the end of the roster.
type OrgMembersResponse struct {
	Items []OrgMember `json:"items"`
	Total int64       `json:"total"`
}

// OrgMemberResponse wraps one membership row.
type OrgMemberResponse struct {
	Member OrgMember `json:"member"`
}

// OrgAuditLogResponse is one page of GET /api/v1/orgs/{org}/audit-log.
// NextBefore is the cursor for the following page, 0 when this was the last.
type OrgAuditLogResponse struct {
	Items      []OrgAuditEntry `json:"items"`
	NextBefore int64           `json:"next_before"`
}

// -------------------------------------------------------------- webhooks

// WebhookEvent names one kind of event a webhook may subscribe to.
type WebhookEvent string

const (
	WebhookEventRepoPush    WebhookEvent = "repo.push"
	WebhookEventRepoCreated WebhookEvent = "repo.created"
	WebhookEventRepoDeleted WebhookEvent = "repo.deleted"
	// WebhookEventRepoMoved fires after a repository was transferred or
	// renamed; delivered to the *new* namespace's subscriptions.
	WebhookEventRepoMoved WebhookEvent = "repo.moved"
	// WebhookEventRepoTransferRequested fires when a transfer needs the
	// destination namespace's approval; delivered to that namespace.
	WebhookEventRepoTransferRequested WebhookEvent = "repo.transfer_requested"
	// WebhookEventRepoArchived / RepoUnarchived fire when a repository is
	// frozen read-only and when it is thawed again. Mirroring systems care:
	// an archived repository will not change until it is unarchived.
	WebhookEventRepoArchived   WebhookEvent = "repo.archived"
	WebhookEventRepoUnarchived WebhookEvent = "repo.unarchived"
	// WebhookEventRepoRefDeleted fires when a branch or tag is removed,
	// whether by `git push --delete` or through the API. Creation and
	// update already arrive as repo.push; without this one a mirroring
	// subscriber saw refs appear and never saw them go away.
	WebhookEventRepoRefDeleted WebhookEvent = "repo.ref_deleted"
	WebhookEventRunFinished    WebhookEvent = "run.finished"
	WebhookEventRunFailed      WebhookEvent = "run.failed"
)

// WebhookDeliveryStatus is the lifecycle state of one delivery attempt.
type WebhookDeliveryStatus string

const (
	WebhookDeliveryPending WebhookDeliveryStatus = "pending"
	WebhookDeliverySuccess WebhookDeliveryStatus = "success"
	WebhookDeliveryFailed  WebhookDeliveryStatus = "failed"
)

// Webhook is one subscription as the web UI sees it. Secret is included only
// in the response to a create call (see CreateWebhookResponse); every other
// response omits it.
type Webhook struct {
	ID        int64  `json:"id"`
	Namespace string `json:"namespace"`
	// RepoFullName is "" for a namespace-wide subscription, "ns/name"
	// when scoped to one repository.
	RepoFullName string         `json:"repo_full_name"`
	URL          string         `json:"url"`
	Events       []WebhookEvent `json:"events"`
	Active       bool           `json:"active"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// WebhookListResponse is the body of GET .../webhooks.
type WebhookListResponse struct {
	Items []Webhook `json:"items"`
}

// WebhookResponse wraps a single webhook.
type WebhookResponse struct {
	Webhook Webhook `json:"webhook"`
}

// CreateWebhookRequest is the body of POST .../webhooks.
type CreateWebhookRequest struct {
	// Repo is "" for a namespace-wide webhook, or "kind/name" (e.g.
	// "dataset/my-metrics") to scope it to one repository in the namespace.
	Repo   string         `json:"repo,omitempty"`
	URL    string         `json:"url"`
	Events []WebhookEvent `json:"events"`
	// Active defaults to true when omitted; the field exists so a webhook can
	// be created disabled.
	Active *bool `json:"active,omitempty"`
}

// CreateWebhookResponse returns the freshly minted webhook including its
// secret, which is never shown again — a client that loses it must rotate it
// via UpdateWebhookRequest.
type CreateWebhookResponse struct {
	Webhook `tstype:",extends"`
	Secret  string `json:"secret"`
}

// UpdateWebhookRequest patches a webhook. Nil/omitted fields are left
// unchanged; Events, when present, replaces the whole set. Setting
// RotateSecret regenerates the secret, returned once in the response.
type UpdateWebhookRequest struct {
	URL          *string        `json:"url,omitempty"`
	Events       []WebhookEvent `json:"events,omitempty"`
	Active       *bool          `json:"active,omitempty"`
	RotateSecret bool           `json:"rotate_secret,omitempty"`
}

// UpdateWebhookResponse carries the new secret only when the update rotated
// it.
type UpdateWebhookResponse struct {
	Webhook `tstype:",extends"`
	// Secret is set only when the request rotated it.
	Secret string `json:"secret,omitempty"`
}

// WebhookDelivery is one delivery attempt's history row.
type WebhookDelivery struct {
	ID             int64                 `json:"id"`
	Event          WebhookEvent          `json:"event"`
	Payload        map[string]any        `json:"payload"`
	Status         WebhookDeliveryStatus `json:"status"`
	Attempts       int                   `json:"attempts"`
	LastAttemptAt  *time.Time            `json:"last_attempt_at" tstype:"string | null,required"`
	ResponseStatus *int                  `json:"response_status" tstype:"number | null,required"`
	ResponseBody   string                `json:"response_body"`
	CreatedAt      time.Time             `json:"created_at"`
}

// WebhookDeliveryListResponse is one page of a webhook's delivery history.
type WebhookDeliveryListResponse struct {
	Items []WebhookDelivery `json:"items"`
	Total int64             `json:"total"`
}

// ------------------------------------------------------- site administration

// PasswordChangeRequest is the body of PATCH /api/v1/me/password. The current
// password is always required: holding a session is not on its own permission
// to replace the credential that session was minted from.
type PasswordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// AdminUser is one account as GET /api/v1/admin/users lists it. The stored
// password hash has no field here and never will: this type *is* the wire
// contract, so a field that does not exist cannot be serialised by accident.
type AdminUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	// IsAdmin is the instance-wide administrator flag (users.is_admin), not
	// a role in any organisation.
	IsAdmin bool `json:"is_admin"`
	// Disabled reports whether the account is suspended. A disabled account
	// authenticates on no path at all -- not password, not access token, not
	// SSH key -- which is what makes it the offboarding switch. Resetting a
	// password deliberately does not revoke tokens, so before this existed
	// there was no way to actually cut somebody off.
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	// LastLoginAt is when this account last authenticated with its password
	// (a session being minted), null for one that never has. Access tokens
	// and SSH keys carry their own last-used timestamps and deliberately do
	// not move this one: the question it answers is "is anybody still using
	// this account", for which an automation's token is the wrong signal.
	LastLoginAt *time.Time `json:"last_login_at" tstype:"string | null,required"`
	// Approval is "pending" for an account that signed up while
	// TF_SIGNUP_REQUIRE_APPROVAL was on and has not been approved yet. A
	// pending account cannot authenticate on any path.
	Approval UserApproval `json:"approval"`
}

// UserApproval is whether a self-registered account has been let in yet.
type UserApproval string

const (
	UserApprovalApproved UserApproval = "approved"
	UserApprovalPending  UserApproval = "pending"
)

// AdminUserListResponse is one page of the account directory. Total counts
// every account matching `search`, ignoring the page window.
type AdminUserListResponse struct {
	Items []AdminUser `json:"items"`
	Total int64       `json:"total"`
}

// AdminUserResponse wraps the account after an administrative change.
type AdminUserResponse struct {
	User AdminUser `json:"user"`
}

// AdminUserCreateRequest is the body of POST /api/v1/admin/users: a site
// administrator adds an account directly. It is the only way to create one on
// an instance with TF_ALLOW_SIGNUP=false, so it deliberately does not consult
// that setting.
type AdminUserCreateRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// IsAdmin makes the new account a site administrator. Optional; the
	// account is an ordinary user when it is absent or false.
	IsAdmin bool `json:"is_admin,omitempty"`
}

// AdminUserUpdateRequest is the body of PATCH
// /api/v1/admin/users/{username}. Both fields are optional and an absent one
// is left unchanged, but a body setting neither is refused (400) rather than
// treated as a no-op.
type AdminUserUpdateRequest struct {
	// Password replaces the account's password and revokes its sessions.
	// The account's access tokens are deliberately not revoked.
	Password *string `json:"password,omitempty"`
	// IsAdmin grants or revokes site administrator rights. Revoking your
	// own is 400; revoking the last one on the instance is 409.
	IsAdmin *bool `json:"is_admin,omitempty"`
	// Disabled suspends or restores the account. Suspending it stops every
	// identity path at once (session, password, access token, SSH key) and
	// revokes its sessions; disabling your own account is 400
	// (self_disable) and disabling the last usable site administrator is
	// 409 (last_admin). Restoring does not bring back credentials revoked
	// separately.
	Disabled *bool `json:"disabled,omitempty"`
	// Approval admits a pending self-registration ("approved") or puts an
	// account back in the waiting room ("pending"). Sending "pending" for
	// your own account is 400 (self_pending); doing it to the last usable
	// site administrator is 409 (last_admin), the same pair of codes the
	// Disabled field uses.
	Approval *UserApproval `json:"approval,omitempty"`
}

// AdminNamespaceUsage is one namespace as GET /api/v1/admin/namespaces lists
// it: what it is storing and what it is allowed to store.
type AdminNamespaceUsage struct {
	Namespace string        `json:"namespace"`
	Kind      NamespaceKind `json:"kind"`
	LFSSize   int64         `json:"lfs_size"`
	NumRepos  int64         `json:"num_repos"`
	// QuotaBytes is this namespace's own override; null means it has none
	// and the instance default applies.
	QuotaBytes *int64 `json:"quota_bytes" tstype:"number | null,required"`
	// EffectiveQuotaBytes is what is actually enforced on an upload: the
	// override when set, otherwise the instance default. Null is unlimited.
	EffectiveQuotaBytes *int64 `json:"effective_quota_bytes" tstype:"number | null,required"`
}

// AdminNamespaceListResponse is one page of the namespace directory.
type AdminNamespaceListResponse struct {
	Items []AdminNamespaceUsage `json:"items"`
	Total int64                 `json:"total"`
	// DefaultQuotaBytes is the instance-wide default every namespace without
	// an override gets (TF_DEFAULT_STORAGE_QUOTA_BYTES). Null is unlimited.
	// It is configuration, not data: changing it needs a redeploy.
	DefaultQuotaBytes *int64 `json:"default_quota_bytes" tstype:"number | null,required"`
}

// AdminNamespaceQuotaRequest is the body of PATCH
// /api/v1/admin/namespaces/{ns}. The field is required and nullable: null
// clears the override so the instance default applies again, which is a
// different thing from setting a quota of zero.
type AdminNamespaceQuotaRequest struct {
	QuotaBytes *int64 `json:"quota_bytes" tstype:"number | null,required"`
}

// SyncJob is one row of the post-push queue as GET /api/v1/admin/sync-jobs
// lists it. Only jobs that exhausted their attempts are listed: a job still
// retrying is not an operator's problem yet, and the queue is otherwise high
// churn.
//
// A failed job means the repository's file index, search entry and blobs/
// export are frozen at the previous push. Nothing republishes it on its own,
// which is why this listing exists at all -- before it, the only trace was a
// single log line.
type SyncJob struct {
	ID int64 `json:"id"`
	// Repo is the full name including the kind segment, e.g.
	// "datasets/acme/imdb-ja", so an operator can open it directly.
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	// Attempts is how many times the job was claimed before it parked.
	Attempts int `json:"attempts"`
	// LastError is the error from the final attempt, verbatim.
	LastError string    `json:"last_error"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SyncJobListResponse is one page of failed sync jobs. Total counts every
// failed job, ignoring the page window.
type SyncJobListResponse struct {
	Items []SyncJob `json:"items"`
	Total int64     `json:"total"`
}

// ------------------------------------------------------------------ errors

// ApiError describes what went wrong.
type ApiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	// MovedTo is set (with Type "repo_moved", status 404) when the requested
	// repository name is a former name of a repository that has since been
	// transferred or renamed; the client should retry at the new location.
	MovedTo *RepoLocation `json:"moved_to,omitempty"`
}

// RepoLocation names a repository by namespace and name.
type RepoLocation struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ApiErrorBody is the body of every non-2xx API response.
type ApiErrorBody struct {
	Error ApiError `json:"error"`
}
