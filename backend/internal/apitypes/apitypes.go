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
