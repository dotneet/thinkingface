package main

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/lfs"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// testSignedURLMaxTTL stands in for config.SignedURLMaxTTL, which is what
// the staging grace is derived from. It is the shipped default.
const testSignedURLMaxTTL = 12 * time.Hour

type fakeGCDB struct {
	all        []store.LFSObjectRef
	referenced map[string]bool
	// liveRefs is the reference set at delete time, which can differ from
	// the scan-time snapshot in referenced.
	liveRefs map[string]bool
	removed  []string
	// blobRefs is what ListReferencedBlobSHAs answers.
	blobRefs map[string]bool
}

func (f *fakeGCDB) ListReferencedBlobSHAs(context.Context) (map[string]bool, error) {
	return f.blobRefs, nil
}

func (f *fakeGCDB) ListLFSObjects(context.Context) ([]store.LFSObjectRef, error) {
	return f.all, nil
}

func (f *fakeGCDB) ListReferencedLFSOIDs(context.Context) (map[string]bool, error) {
	return f.referenced, nil
}

func (f *fakeGCDB) DeleteOrphanedLFSObject(_ context.Context, oid string, removeStorage func() error) (bool, error) {
	if f.liveRefs[oid] {
		return false, nil
	}
	if err := removeStorage(); err != nil {
		return false, err
	}
	f.removed = append(f.removed, oid)
	return true, nil
}

type fakeGCStorage struct {
	deleted []string
	fail    map[string]error
	blobs   []storage.ObjectInfo
}

func (f *fakeGCStorage) List(_ context.Context, prefix string) ([]storage.ObjectInfo, error) {
	var out []storage.ObjectInfo
	for _, o := range f.blobs {
		if strings.HasPrefix(o.Key, prefix) {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeGCStorage) Delete(_ context.Context, key string) error {
	if err := f.fail[key]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, key)
	return nil
}

func TestRunGC_SkipsObjectThatGainedAReferenceAfterTheScan(t *testing.T) {
	db := &fakeGCDB{
		all: []store.LFSObjectRef{
			{OID: "aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999", Size: 10},
			{OID: "bbbbccccddddeeeeffff0000111122223333444455556666777788889999aaaa", Size: 20},
		},
		referenced: map[string]bool{},
		liveRefs: map[string]bool{
			"aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999": true,
		},
	}
	obj := &fakeGCStorage{}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}

	if len(db.removed) != 1 || db.removed[0] != db.all[1].OID {
		t.Fatalf("removed = %v, want only the still-orphaned oid", db.removed)
	}
	wantKey := storage.LFSKey(db.all[1].OID)
	if len(obj.deleted) != 1 || obj.deleted[0] != wantKey {
		t.Fatalf("storage deleted = %v, want [%s]", obj.deleted, wantKey)
	}
}

func TestRunGC_DryRunDeletesNothing(t *testing.T) {
	db := &fakeGCDB{
		all:        []store.LFSObjectRef{{OID: "a", Size: 1}},
		referenced: map[string]bool{},
		liveRefs:   map[string]bool{},
	}
	obj := &fakeGCStorage{}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, nil)
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(db.removed) != 0 || len(obj.deleted) != 0 {
		t.Fatalf("dry run deleted db=%v storage=%v", db.removed, obj.deleted)
	}
}

func TestRunGC_StorageFailureDoesNotCountAsASkip(t *testing.T) {
	oid := "aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999"
	db := &fakeGCDB{
		all:        []store.LFSObjectRef{{OID: oid, Size: 10}},
		referenced: map[string]bool{},
		liveRefs:   map[string]bool{},
	}
	obj := &fakeGCStorage{fail: map[string]error{storage.LFSKey(oid): errors.New("boom")}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want storage failure, got nil")
	}
	if len(db.removed) != 0 {
		t.Fatalf("removed = %v, want none after storage failure", db.removed)
	}
}

func withDiscardedStdout(fn func() error) error {
	stdout := os.Stdout
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	os.Stdout = devNull
	defer func() {
		os.Stdout = stdout
		_ = devNull.Close()
	}()
	return fn()
}

// ------------------------------------------------------------------- blobs

func blobObject(sha string, age time.Duration) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:     storage.BlobKey(sha),
		Size:    int64(len(sha)),
		Updated: time.Now().Add(-age),
	}
}

// The blob pass has no row to lock, so its whole safety story is the two
// rules store.OrphanedBlobs applies: still referenced, or written too recently.
func TestOrphanedBlobs_KeepsReferencedAndYoungObjects(t *testing.T) {
	referenced := blobObject("aaaa1111", 90*24*time.Hour)
	orphanOld := blobObject("bbbb2222", 90*24*time.Hour)
	orphanYoung := blobObject("cccc3333", time.Minute)

	got := store.OrphanedBlobs(
		[]storage.ObjectInfo{referenced, orphanOld, orphanYoung},
		map[string]bool{"aaaa1111": true},
		time.Now().Add(-blobGrace),
	)

	if len(got) != 1 || got[0].Key != orphanOld.Key {
		t.Fatalf("OrphanedBlobs = %v, want only %s", got, orphanOld.Key)
	}
}

func TestRunGC_DeletesOrphanedBlobsAndKeepsReferencedOnes(t *testing.T) {
	db := &fakeGCDB{
		referenced: map[string]bool{},
		liveRefs:   map[string]bool{},
		blobRefs:   map[string]bool{"aaaa1111": true},
	}
	obj := &fakeGCStorage{blobs: []storage.ObjectInfo{
		blobObject("aaaa1111", 90*24*time.Hour),
		blobObject("bbbb2222", 90*24*time.Hour),
		blobObject("cccc3333", time.Minute),
	}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	want := storage.BlobKey("bbbb2222")
	if len(obj.deleted) != 1 || obj.deleted[0] != want {
		t.Fatalf("deleted = %v, want [%s]", obj.deleted, want)
	}
}

func TestRunGC_DryRunDeletesNoBlobs(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{blobs: []storage.ObjectInfo{blobObject("bbbb2222", 90*24*time.Hour)}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, nil)
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 0 {
		t.Fatalf("dry run deleted %v", obj.deleted)
	}
}

func TestRunGC_BlobStorageFailureIsReported(t *testing.T) {
	key := storage.BlobKey("bbbb2222")
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{
		blobs: []storage.ObjectInfo{blobObject("bbbb2222", 90*24*time.Hour)},
		fail:  map[string]error{key: errors.New("boom")},
	}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want a blob storage failure, got nil")
	}
}

// ----------------------------------------------------------------- staging

func stagingObject(key string, age time.Duration) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:     key,
		Size:    int64(len(key)),
		Updated: time.Now().Add(-age),
	}
}

// A zero TF_SIGNED_URL_MAX_TTL means "no ceiling", which makes signed URLs
// live *longer* -- up to GCS's 7-day signing limit -- not shorter. Reading the
// config value literally would hand back the 24h floor and have gc deleting
// staging objects whose upload URL is still valid for days.
func TestStagingGraceOutlastsEveryURLItCanFace(t *testing.T) {
	for _, tt := range []struct {
		name   string
		maxTTL time.Duration
	}{
		{"no ceiling configured", 0},
		{"negative ceiling", -time.Hour},
		{"ceiling above what GCS will sign", 30 * 24 * time.Hour},
		{"shipped default", 12 * time.Hour},
		{"tiny ceiling", time.Minute},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Measured against TTLFor itself, not against the helper
			// stagingGrace uses: asking the same function the implementation
			// asks would move both sides together and assert nothing. An
			// unbounded transfer is what pins the longest lifetime the
			// signing path can ever hand out for this ceiling.
			longestURL := lfs.TTLFor(time.Hour, tt.maxTTL, math.MaxInt64)
			if got := stagingGrace(tt.maxTTL); got <= longestURL {
				t.Fatalf("stagingGrace(%v) = %v, which does not outlast the %v a signed URL can live",
					tt.maxTTL, got, longestURL)
			}
		})
	}
}

func TestRunGC_KeepsStagingObjectsWithinGrace(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{blobs: []storage.ObjectInfo{
		// Well within stagingGrace: this looks like an upload still in
		// flight and must survive even a --yes run.
		stagingObject("tmp/uploads/lfs/1/aaaa", time.Minute),
	}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 0 {
		t.Fatalf("deleted = %v, want none (object is within stagingGrace)", obj.deleted)
	}
}

func TestRunGC_DryRunLeavesOldStagingObjects(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{blobs: []storage.ObjectInfo{
		stagingObject("tmp/uploads/lfs/1/bbbb", stagingGrace(testSignedURLMaxTTL)+time.Hour),
	}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, nil)
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 0 {
		t.Fatalf("dry run deleted %v", obj.deleted)
	}
}

func TestRunGC_DeletesOldStagingObjectsWithYes(t *testing.T) {
	key := "tmp/uploads/lfs/1/cccc"
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{blobs: []storage.ObjectInfo{
		stagingObject(key, stagingGrace(testSignedURLMaxTTL)+time.Hour),
	}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 1 || obj.deleted[0] != key {
		t.Fatalf("deleted = %v, want [%s]", obj.deleted, key)
	}
}

func TestRunGC_StagingStorageFailureIsReported(t *testing.T) {
	key := "tmp/uploads/lfs/1/dddd"
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{
		blobs: []storage.ObjectInfo{stagingObject(key, stagingGrace(testSignedURLMaxTTL)+time.Hour)},
		fail:  map[string]error{key: errors.New("boom")},
	}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want a staging storage failure, got nil")
	}
}
