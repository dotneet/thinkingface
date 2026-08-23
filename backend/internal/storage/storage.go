// Package storage abstracts the object store behind the two deployment modes:
// real GCS with V4 signed URLs, and the local fake-gcs-server emulator where
// signing is unavailable and transfers are proxied through the API server.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrNotFound = errors.New("storage: object not found")

// ErrPreconditionFailed is a generation mismatch on a conditional write (GCS
// answers 412). It is the linearisation signal the WAL builds on: whoever gets
// it lost the race and must re-read before retrying (docs/continuity-design.md §11).
var ErrPreconditionFailed = errors.New("storage: precondition failed")

type ObjectInfo struct {
	Key         string
	Size        int64
	ContentType string
	Updated     time.Time
	// Generation is the object version the store assigns on every write. It is
	// the repository version for wal/…/index.json (continuity-design §4).
	Generation int64
}

// Storage is the contract every driver implements. Keys are bucket-relative
// paths without a leading slash; the driver applies any configured prefix.
type Storage interface {
	// SupportsSignedURL reports whether SignedGetURL/SignedPutURL produce URLs
	// a client can use directly. When false, callers must proxy the transfer.
	SupportsSignedURL() bool

	SignedGetURL(ctx context.Context, key string, ttl time.Duration, downloadName string) (string, error)
	SignedPutURL(ctx context.Context, key string, ttl time.Duration, size int64) (string, error)

	Put(ctx context.Context, key string, r io.Reader, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// GetWithGeneration is the read half of a compare-and-swap: the body plus
	// the generation to pass back to PutIfGeneration. ErrNotFound when absent.
	GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// PutIfGeneration writes only when the stored generation still matches.
	// generation == 0 means "only if the object does not exist yet".
	// A mismatch returns ErrPreconditionFailed and nothing is written.
	// On success it reports the generation the write produced, so a caller
	// that just linearised an update knows its own version without a
	// read-back — a read-back is racy, because a later writer may have
	// moved the object on again by the time it is read.
	PutIfGeneration(ctx context.Context, key string, generation int64, r io.Reader, contentType string) (int64, error)
	// GetRange reads length bytes from offset. length < 0 means "to the end".
	GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// Copy performs a server-side copy; object bytes never transit this process.
	Copy(ctx context.Context, srcKey, dstKey string) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// PublicURI returns the gs:// URI shown in the UI for gcloud commands.
	PublicURI(key string) string
}
