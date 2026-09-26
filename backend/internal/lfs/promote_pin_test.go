package lfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// versionedStorage is keyStorage with generation addressing
// (storage.Versioned), which is what GCS gives promotion in production. Every
// write keeps its generation's bytes in history; keepHistory decides whether a
// superseded generation stays readable (a bucket with object versioning) or
// disappears the moment it is replaced (the default bucket).
type versionedStorage struct {
	*keyStorage
	history     map[string]map[int64][]byte
	keepHistory bool
	// onCopy runs as a pinned copy starts -- after every check promotion
	// makes -- so a test can land a write in the last instant before the
	// bytes are published.
	onCopy func(srcKey string)
}

var (
	_ storage.Storage   = (*versionedStorage)(nil)
	_ storage.Versioned = (*versionedStorage)(nil)
)

func newVersionedStorage(keepHistory bool) *versionedStorage {
	return &versionedStorage{
		keyStorage:  newKeyStorage(nil),
		history:     map[string]map[int64][]byte{},
		keepHistory: keepHistory,
	}
}

func (v *versionedStorage) put(key string, body []byte) {
	v.keyStorage.put(key, body)
	if !v.keepHistory || v.history[key] == nil {
		v.history[key] = map[int64][]byte{}
	}
	v.history[key][v.generations[key]] = body
}

func (v *versionedStorage) GetGeneration(_ context.Context, key string, generation int64) (io.ReadCloser, error) {
	v.log.add(fmt.Sprintf("read %s#%d", key, generation))
	body, ok := v.history[key][generation]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (v *versionedStorage) CopyGeneration(_ context.Context, srcKey string, generation int64, dstKey string) error {
	if v.onCopy != nil {
		hook := v.onCopy
		v.onCopy = nil
		hook(srcKey)
	}
	v.log.add(fmt.Sprintf("copy %s#%d -> %s", srcKey, generation, dstKey))
	body, ok := v.history[srcKey][generation]
	if !ok {
		return storage.ErrNotFound
	}
	v.put(dstKey, body)
	return nil
}

func versionedTestHandler(rec *fakeRecorder, st *versionedStorage) *Handler {
	rec.log = st.log
	return testHandler(rec, st)
}

// The finding: promotion re-statted the staged object to prove it unchanged
// and then copied "whatever is at the key" onto lfs/{oid}. On the signed-URL
// path the uploader still holds a live PUT URL for that staging key, so it
// could verify genuine bytes and swap in a same-length forgery between the
// re-stat and the copy. That is not one promotion's problem: lfs/{oid} is
// shared by every repository on the instance and Batch dedups onto it, so the
// forgery would be served for that oid everywhere, for good.
//
// Pinning the copy to the generation that was checked closes it. On a bucket
// that keeps superseded generations the verified bytes are what gets copied
// anyway; on one that does not, the checked generation is gone and the copy
// fails rather than publishing its replacement.
func TestVerifyPublishesOnlyTheGenerationItChecked(t *testing.T) {
	forged := bytes.Repeat([]byte("x"), int(goodSize))
	for _, tc := range []struct {
		name        string
		keepHistory bool
	}{
		{"versioned bucket", true},
		{"unversioned bucket", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staging := storage.LFSStagingKey(1, goodOID)
			published := storage.LFSKey(goodOID)
			rec := &fakeRecorder{}
			st := newVersionedStorage(tc.keepHistory)
			st.put(staging, goodBody)
			st.onCopy = func(key string) {
				if key == staging {
					st.put(staging, forged) // the uploader's second PUT
				}
			}
			h := versionedTestHandler(rec, st)

			err := h.Verify(context.Background(), 1, goodOID, goodSize)
			if tc.keepHistory {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				if got := st.bodies[published]; !bytes.Equal(got, goodBody) {
					t.Fatalf("%s holds %q, want the bytes that were hashed", published, got)
				}
				return
			}
			var changed *StagedObjectChangedError
			if !errors.As(err, &changed) {
				t.Fatalf("Verify error = %v, want a StagedObjectChangedError", err)
			}
			if got, ok := st.bodies[published]; ok {
				t.Fatalf("%s now holds %q: bytes nobody checked reached the shared key", published, got)
			}
			if rec.calls != 0 {
				t.Errorf("RecordLFSObject calls = %d, want 0", rec.calls)
			}
		})
	}
}

// The digest has to be a statement about the same version the size check and
// the copy are about. An unpinned read hashes whatever is live when it
// starts, so a forgery that passed the size check could be swapped for the
// genuine bytes just long enough to be hashed.
func TestVerifyHashesTheGenerationItStatted(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newVersionedStorage(true)
	forged := bytes.Repeat([]byte("x"), int(goodSize))
	st.put(staging, forged)
	st.onStat = func(key string) {
		if key == staging {
			st.onStat = nil
			st.put(staging, goodBody)
		}
	}
	h := versionedTestHandler(rec, st)

	err := h.Verify(context.Background(), 1, goodOID, goodSize)
	var mismatch *DigestMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Verify error = %v, want a DigestMismatchError for the statted generation", err)
	}
	if mismatch.Got != oidOf(forged) {
		t.Errorf("hashed %s, want the statted generation's digest %s", mismatch.Got, oidOf(forged))
	}
	if _, ok := st.objects[storage.LFSKey(goodOID)]; ok {
		t.Error("bytes were promoted after a digest mismatch")
	}
	for _, op := range st.log.ops {
		if op == "read "+staging {
			t.Errorf("operations = %v: the staged object was read without a generation", st.log.ops)
		}
	}
}

// With generation addressing there is nothing left for the re-stat to prove,
// so a promotion is stat, pinned read, pinned copy -- and the copy names the
// generation the stat reported.
func TestVerifyPinsReadAndCopyToTheStattedGeneration(t *testing.T) {
	staging := storage.LFSStagingKey(1, goodOID)
	rec := &fakeRecorder{}
	st := newVersionedStorage(false)
	st.put(staging, goodBody)
	gen := st.generations[staging]
	h := versionedTestHandler(rec, st)

	if err := h.Verify(context.Background(), 1, goodOID, goodSize); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	wantRead := fmt.Sprintf("read %s#%d", staging, gen)
	wantCopy := fmt.Sprintf("copy %s#%d -> %s", staging, gen, storage.LFSKey(goodOID))
	var sawRead, sawCopy bool
	for _, op := range st.log.ops {
		switch {
		case op == wantRead:
			sawRead = true
		case op == wantCopy:
			sawCopy = true
		case strings.HasPrefix(op, "copy ") || op == "read "+staging:
			t.Errorf("unpinned operation %q", op)
		}
	}
	if !sawRead || !sawCopy {
		t.Errorf("operations = %v, want %q and %q", st.log.ops, wantRead, wantCopy)
	}
	if rec.calls != 1 {
		t.Errorf("RecordLFSObject calls = %d, want 1", rec.calls)
	}
}
