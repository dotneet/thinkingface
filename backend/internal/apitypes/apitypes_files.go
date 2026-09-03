package apitypes

import "time"

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
	// DiffNoPatchBudgetSpent is a file the response's overall patch budget
	// ran out before. The per-file ceilings alone do not bound a response --
	// enough large-but-allowed patches add up -- so the sum is capped too,
	// and a file past the cap is listed without one. Nothing is wrong with
	// the file: the commit changed more text than one response renders.
	DiffNoPatchBudgetSpent DiffNoPatchReason = "budget_spent"
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
	// clobbers someone else's concurrent edit. Omit it when creating a file
	// and set MustNotExist instead.
	BaseOID string `json:"base_oid,omitempty"`
	// MustNotExist is the claim a caller creating a file makes: the path was
	// absent when they opened the editor. The commit is refused if anything
	// occupies it by the time it lands, so two people creating the same path
	// no longer resolve to whoever saved last.
	//
	// It is the counterpart of BaseOID, not a variant of it -- a request
	// carrying both is contradictory and refused. Neither one means the
	// caller is not tracking staleness at all, which the endpoint still
	// accepts: the browser always tracks, but a script that just wants a
	// path to end up with certain bytes should not have to read it first.
	MustNotExist bool `json:"must_not_exist,omitempty"`
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
