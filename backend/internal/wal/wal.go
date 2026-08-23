// Package wal implements the write-ahead log that makes object storage — not
// the bare repository on disk — the source of truth for git data
// (docs/continuity-design.md).
//
// One object per repository, wal/{storage_path}/index.json, records the refs
// and the ordered list of packs that reconstruct them. storage_path is the
// repository's immutable physical location (store.Repo.StoragePath), not its
// current name, so renaming or transferring a repository never moves its WAL
// (docs/repo-transfer-design.md §3). Every update to the index goes through a
// conditional write, so that object's generation is the repository version
// and the single linearisation point across instances. Local bare
// repositories are caches: Materialize brings one up to a given generation,
// and losing one costs nothing but the work to rebuild it.
//
// This package is deliberately free of HTTP, database, and gitserver
// dependencies: it is the library the transport layers call, not a layer that
// calls them.
package wal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// IndexVersion is the schema version written into every index (§4). Readers
// refuse anything newer rather than guessing at unknown semantics.
const IndexVersion = 1

// maxCASAttempts caps the re-read/retry loop of §6.1. Losing five races in a
// row means the repository is hot enough that pushing the conflict back to the
// client is the right answer.
const maxCASAttempts = 5

var (
	// ErrStaleRef reports that another writer moved a ref this update depends
	// on. Callers turn it into git's "stale info, fetch and retry" rejection
	// (§6.1); it is never retried internally, because retrying would overwrite
	// somebody else's commit.
	ErrStaleRef = errors.New("wal: stale ref")

	// ErrRetryExhausted means the CAS loop kept losing to unrelated writers.
	// The update is still valid; the caller may try again later.
	ErrRetryExhausted = errors.New("wal: index CAS retries exhausted")

	// ErrIndexVersion guards against a newer writer's schema.
	ErrIndexVersion = errors.New("wal: unsupported index version")
)

// StaleRefError names the ref that moved, so the transport can tell the client
// which branch to fetch.
type StaleRefError struct {
	Ref      string // the ref whose precondition failed
	Expected string // what the pushing client believed the ref pointed at
	Actual   string // what the index says now ("" when the ref is gone)
}

func (e *StaleRefError) Error() string {
	expected, actual := e.Expected, e.Actual
	if isAbsent(expected) {
		expected = "<absent>"
	}
	if isAbsent(actual) {
		actual = "<absent>"
	}
	return fmt.Sprintf("wal: stale ref %s: expected %s, index has %s", e.Ref, expected, actual)
}

func (e *StaleRefError) Is(target error) bool { return target == ErrStaleRef }

// Index is the JSON document at wal/…/index.json (§4).
type Index struct {
	Version int `json:"version"`
	// Seq is the last entry number handed out; the next push uses Seq+1.
	Seq int `json:"seq"`
	// Base is the compacted snapshot to apply first, or "" when there is none.
	Base string `json:"base"`
	// Entries are applied on top of Base *in this order*. Order is meaning.
	Entries []string `json:"entries"`
	// Refs is the authority on refs. On-disk refs are a projection of this.
	Refs      map[string]string `json:"refs"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// NewIndex returns the index of a repository that has never been written to.
func NewIndex() *Index {
	return &Index{Version: IndexVersion, Refs: map[string]string{}}
}

func (ix *Index) clone() *Index {
	out := &Index{
		Version:   ix.Version,
		Seq:       ix.Seq,
		Base:      ix.Base,
		Entries:   append([]string(nil), ix.Entries...),
		Refs:      make(map[string]string, len(ix.Refs)),
		UpdatedAt: ix.UpdatedAt,
	}
	for k, v := range ix.Refs {
		out.Refs[k] = v
	}
	return out
}

// RefUpdate is one line of receive-pack's pre-receive stdin: the ref, the value
// the client believed it had, and the value it wants. An empty or all-zero New
// deletes the ref; an empty or all-zero Old asserts the ref does not exist.
type RefUpdate struct {
	Ref string
	Old string
	New string
}

// ReadIndex fetches the index and its generation. A repository with no index
// yet reads as an empty index at generation 0, which PutIfGeneration turns into
// a "create only if absent" precondition — so the first push races safely.
func ReadIndex(ctx context.Context, st storage.Storage, storagePath string) (*Index, int64, error) {
	rc, gen, err := st.GetWithGeneration(ctx, storage.WALIndexKey(storagePath))
	if errors.Is(err, storage.ErrNotFound) {
		return NewIndex(), 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read wal index %s: %w", storagePath, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, 0, fmt.Errorf("read wal index body %s: %w", storagePath, err)
	}
	var ix Index
	if err := json.Unmarshal(body, &ix); err != nil {
		return nil, 0, fmt.Errorf("parse wal index %s: %w", storagePath, err)
	}
	if ix.Version > IndexVersion {
		return nil, 0, fmt.Errorf("%w: %d", ErrIndexVersion, ix.Version)
	}
	if ix.Refs == nil {
		ix.Refs = map[string]string{}
	}
	return &ix, gen, nil
}

// PutIndex writes the index only if it is still at generation. Pass 0 to
// create it. On success it returns the generation this write produced, which
// is the only trustworthy way to learn it: a read-back can already observe a
// later writer's version. Invariant 1 of §5: no unconditional PUT of this
// object exists anywhere.
func PutIndex(ctx context.Context, st storage.Storage, storagePath string, generation int64, ix *Index) (int64, error) {
	ix.Version = IndexVersion
	// Second precision keeps the JSON readable; nothing depends on this value.
	ix.UpdatedAt = time.Now().UTC().Truncate(time.Second)

	body, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encode wal index: %w", err)
	}
	body = append(body, '\n')
	return st.PutIfGeneration(ctx, storage.WALIndexKey(storagePath), generation,
		strings.NewReader(string(body)), "application/json")
}

// UpdateIndex applies refUpdates (and, when entryKey is non-empty, appends that
// pack) to the index under compare-and-swap, following §6.1 exactly:
//
//	precondition check → CAS → on 412, re-read and re-check → retry or reject
//
// entryKey is the index-relative name returned by UploadEntry. The pack must be
// completely written to storage before this is called (invariant 2 of §5),
// otherwise the index would point at bytes that do not exist yet.
//
// Success means the update is durable and linearised: this is the only moment
// at which a push may be acknowledged to the client (invariant 4).
func UpdateIndex(ctx context.Context, st storage.Storage, storagePath string, refUpdates []RefUpdate, entryKey string) error {
	var lastErr error
	for attempt := 0; attempt < maxCASAttempts; attempt++ {
		ix, gen, err := ReadIndex(ctx, st, storagePath)
		if err != nil {
			return err
		}
		// The client's <old> values were validated against a local copy that
		// may be older than what we just read. Re-validating here — on every
		// attempt, not just after a 412 — is what stops a non-fast-forward
		// overwrite. Omitting it is the single most dangerous change possible
		// in this package.
		if err := checkPreconditions(ix, refUpdates); err != nil {
			return err
		}

		next := ix.clone()
		next.apply(refUpdates, entryKey)

		_, err = PutIndex(ctx, st, storagePath, gen, next)
		if err == nil {
			return nil
		}
		if errors.Is(err, storage.ErrPreconditionFailed) {
			// Somebody else won this round. Loop: re-read, re-check, retry.
			lastErr = err
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts: %v", ErrRetryExhausted, maxCASAttempts, lastErr)
}

// checkPreconditions compares every RefUpdate's Old against the index. This is
// the R2[ref] == <old> test of §6.1.
func checkPreconditions(ix *Index, refUpdates []RefUpdate) error {
	for _, u := range refUpdates {
		actual := ix.Refs[u.Ref]
		if !sameHash(actual, u.Old) {
			return &StaleRefError{Ref: u.Ref, Expected: u.Old, Actual: actual}
		}
	}
	return nil
}

func (ix *Index) apply(refUpdates []RefUpdate, entryKey string) {
	if ix.Refs == nil {
		ix.Refs = map[string]string{}
	}
	for _, u := range refUpdates {
		if isAbsent(u.New) {
			delete(ix.Refs, u.Ref)
			continue
		}
		ix.Refs[u.Ref] = u.New
	}
	if entryKey != "" {
		// Seq only advances when an entry is appended: it numbers entries, not
		// index revisions. A ref deletion carries no pack and leaves it alone.
		ix.Entries = append(ix.Entries, entryKey)
		ix.Seq++
	}
}

// isAbsent treats git's all-zero hash and the empty string alike: both mean
// "this ref does not exist". receive-pack sends the zero hash; the HF commit
// API path builds RefUpdates by hand and finds "" more natural.
func isAbsent(hash string) bool {
	if hash == "" {
		return true
	}
	for i := 0; i < len(hash); i++ {
		if hash[i] != '0' {
			return false
		}
	}
	return true
}

func sameHash(a, b string) bool {
	if isAbsent(a) && isAbsent(b) {
		return true
	}
	return strings.EqualFold(a, b)
}
