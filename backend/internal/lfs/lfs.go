// Package lfs implements the Git LFS batch API. Object bytes never pass
// through this process in a real GCS deployment: clients get V4 signed URLs
// and transfer directly with the bucket.
package lfs

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ContentType is the media type the LFS spec mandates on batch requests and
// responses.
const ContentType = "application/vnd.git-lfs+json"

// ErrUnsupportedOperation reports a batch naming something other than upload
// or download, which is a malformed request rather than a server fault.
var ErrUnsupportedOperation = errors.New("lfs: unsupported operation")

// ErrTooManyObjects reports a batch naming more objects than the server will
// decide in one request. Like ErrUnsupportedOperation it is the caller's
// mistake, and the API answers it with 400 rather than a server fault.
var ErrTooManyObjects = errors.New("lfs: too many objects in batch request")

// MaxBatchObjects bounds the objects one batch request may name. The body is
// already capped (the API's maxBatchBody, 8 MiB), but that is not a bound on
// the work: 8 MiB of minimal records is hundreds of thousands of them, and
// each costs at least a storage Stat on the upload path. A legitimate push
// names far fewer -- git-lfs sends one batch per push, and even a push
// touching thousands of files stays an order of magnitude below this -- so
// anything past it is a client that should have split the push, not one that
// needs a larger ceiling.
const MaxBatchObjects = 1000

// oidRe matches the only object id this server accepts. Every oid reaches
// storage.LFSKey, which splices it straight into an object key, so an
// unchecked value from a batch request would let a client name a key outside
// the content-addressed lfs/ prefix.
var oidRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidOID reports whether oid is a lowercase sha256 hex digest.
func ValidOID(oid string) bool { return oidRe.MatchString(oid) }

type BatchRequest struct {
	Operation string      `json:"operation"`
	Transfers []string    `json:"transfers"`
	Ref       *Ref        `json:"ref"`
	Objects   []ObjectRef `json:"objects"`
	HashAlgo  string      `json:"hash_algo"`
}

type Ref struct {
	Name string `json:"name"`
}

type ObjectRef struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type Action struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
}

type ObjectResponse struct {
	OID           string            `json:"oid"`
	Size          int64             `json:"size"`
	Authenticated bool              `json:"authenticated"`
	Actions       map[string]Action `json:"actions,omitempty"`
	Error         *ObjectError      `json:"error,omitempty"`
}

type ObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type BatchResponse struct {
	Transfer string           `json:"transfer"`
	Objects  []ObjectResponse `json:"objects"`
	HashAlgo string           `json:"hash_algo"`
}

// lfsRecorder is the store surface the batch/verify paths need. *store.Store
// implements it; tests substitute a fake so they can drive ErrLFSObjectGone
// without Postgres.
type lfsRecorder interface {
	RecordLFSObject(ctx context.Context, repoID int64, oid string, size int64, confirmPresent func(key string) (bool, error)) error
	// RepoHasLFSObject answers whether the repository is entitled to an
	// object at all. Objects are stored by content hash with no repository
	// in the key, so this is the only thing separating "this repository has
	// this file" from "somebody on this instance uploaded these bytes once".
	RepoHasLFSObject(ctx context.Context, repoID int64, oid string) (bool, error)
}

type Handler struct {
	store   lfsRecorder
	storage storage.Storage
	// ttl is the floor for a signed URL's lifetime; maxTTL is the ceiling a
	// transfer-sized lifetime is clamped to. See TTLFor.
	ttl       time.Duration
	maxTTL    time.Duration
	publicURL string
	secret    []byte
	// quota is the namespace storage allowance the upload path checks
	// against, and defaultQuota the instance-wide fallback for a namespace
	// with no override. A nil quota means enforcement is off: nothing on the
	// upload path asks the database anything (see quota.go).
	quota        QuotaSource
	defaultQuota int64
}

// New builds the batch handler. ttl is the base signed-URL lifetime
// (TF_SIGNED_URL_TTL) and maxTTL the ceiling a large transfer may stretch it
// to (TF_SIGNED_URL_MAX_TTL).
func New(st *store.Store, obj storage.Storage, ttl, maxTTL time.Duration, publicURL, secret string) *Handler {
	return &Handler{store: st, storage: obj, ttl: ttl, maxTTL: maxTTL, publicURL: publicURL, secret: []byte(secret)}
}

var _ lfsRecorder = (*store.Store)(nil)
