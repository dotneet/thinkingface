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
	// liveRows is the set of oids that hold an lfs_objects row at delete
	// time. The untracked pass only ever sees oids that had none when it
	// scanned, so this is how a row appearing in between -- an upload
	// deduplicating against the very bytes gc is about to remove -- is
	// modelled.
	liveRows         map[string]bool
	removed          []string
	removedUntracked []string
	// blobRefs is what ListReferencedBlobSHAs answers.
	blobRefs map[string]bool
	// liveBlobRefs is the blob reference set at delete time, which the scan
	// snapshot in blobRefs cannot see: nothing about a push rewrites a blob
	// that is already at its key, so a sha claimed after the listing looks
	// exactly as orphaned and exactly as old as it did before.
	liveBlobRefs map[string]bool
	removedBlobs []string
	// prunedBefore records the cutoff the ledger prune was asked for, so a
	// test can tell "it ran" from "it was skipped".
	prunedBefore  []time.Time
	pruneRows     int64
	pruneErr      error
	deleteBlobErr error
}

func (f *fakeGCDB) DeleteOrphanedBlob(_ context.Context, sha string, removeStorage func() error) (bool, error) {
	if f.deleteBlobErr != nil {
		return false, f.deleteBlobErr
	}
	if f.liveBlobRefs[sha] {
		return false, nil
	}
	if err := removeStorage(); err != nil {
		return false, err
	}
	f.removedBlobs = append(f.removedBlobs, sha)
	return true, nil
}

func (f *fakeGCDB) PruneBlobDeletions(_ context.Context, before time.Time) (int64, error) {
	f.prunedBefore = append(f.prunedBefore, before)
	return f.pruneRows, f.pruneErr
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

func (f *fakeGCDB) DeleteUntrackedLFSObject(_ context.Context, oid string, removeStorage func() error) (bool, error) {
	if f.liveRows[oid] {
		return false, nil
	}
	if err := removeStorage(); err != nil {
		return false, err
	}
	f.removedUntracked = append(f.removedUntracked, oid)
	return true, nil
}

type fakeGCStorage struct {
	deleted []string
	fail    map[string]error
	// objects is the whole bucket: every pass gets the subset of it that
	// matches the prefix it lists.
	objects []storage.ObjectInfo
}

func (f *fakeGCStorage) List(_ context.Context, prefix string) ([]storage.ObjectInfo, error) {
	var out []storage.ObjectInfo
	for _, o := range f.objects {
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
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{
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
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{blobObject("bbbb2222", 90*24*time.Hour)}}

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
		objects: []storage.ObjectInfo{blobObject("bbbb2222", 90*24*time.Hour)},
		fail:    map[string]error{key: errors.New("boom")},
	}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want a blob storage failure, got nil")
	}
}

// The blob pass's scan is a snapshot, and the window after it is not one an
// age threshold can cover: gitrepo.PublishBlob skips an object already at its
// key, so a push that starts referencing a year-old blob leaves its Updated
// timestamp exactly where it was. Deleting anyway cost the file for good --
// every later push skips a sha the ref's index already names. The decision
// therefore belongs to store.DeleteOrphanedBlob, which re-checks under the
// row it holds, and this pins that the pass asks it rather than deleting.
func TestRunGC_SkipsBlobThatGainedAReferenceAfterTheScan(t *testing.T) {
	claimed := "bbbb2222"
	db := &fakeGCDB{
		referenced: map[string]bool{},
		liveRefs:   map[string]bool{},
		// The scan found neither sha referenced...
		blobRefs: map[string]bool{},
		// ...but by delete time a push had claimed one of them.
		liveBlobRefs: map[string]bool{claimed: true},
	}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{
		blobObject(claimed, 90*24*time.Hour),
		blobObject("cccc3333", 90*24*time.Hour),
	}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	want := storage.BlobKey("cccc3333")
	if len(obj.deleted) != 1 || obj.deleted[0] != want {
		t.Fatalf("deleted = %v, want only [%s]: the other sha was referenced by delete time", obj.deleted, want)
	}
	if len(db.removedBlobs) != 1 || db.removedBlobs[0] != "cccc3333" {
		t.Fatalf("collected = %v, want only the still-orphaned sha", db.removedBlobs)
	}
}

// The ledger is one row per collected blob, so something has to forget them.
// A sha nothing references any more is one no push will ever ask to have put
// back, and the prune runs on every pass -- including one that deleted
// nothing, since the rows it clears are a previous run's.
func TestRunGC_PrunesTheBlobDeletionLedger(t *testing.T) {
	db := &fakeGCDB{
		referenced: map[string]bool{}, liveRefs: map[string]bool{},
		blobRefs: map[string]bool{}, pruneRows: 3,
	}
	obj := &fakeGCStorage{}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(db.prunedBefore) != 1 {
		t.Fatalf("prune ran %d times, want once", len(db.prunedBefore))
	}
	// Never "now": the record is written before the bytes go, and the push
	// that claims the sha may still be committing its repo_files rows.
	if age := time.Since(db.prunedBefore[0]); age < deletionLedgerGrace {
		t.Errorf("pruned rows younger than %s (cutoff was %s ago)", deletionLedgerGrace, age)
	}
}

// A dry run reports and touches nothing -- the ledger included, since pruning
// it is a write like any other.
func TestRunGC_DryRunLeavesTheDeletionLedgerAlone(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{blobObject("bbbb2222", 90*24*time.Hour)}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, nil)
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(db.prunedBefore) != 0 {
		t.Errorf("dry run pruned the ledger %d times, want 0", len(db.prunedBefore))
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
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{
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
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{
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
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{
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
		objects: []storage.ObjectInfo{stagingObject(key, stagingGrace(testSignedURLMaxTTL)+time.Hour)},
		fail:    map[string]error{key: errors.New("boom")},
	}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want a staging storage failure, got nil")
	}
}

// ----------------------------------------------------- untracked lfs objects

// Two full-length oids, so the keys these produce are the real three-level
// shape (lfs/aa/aa/aaaa…) rather than the short-oid fallback.
const (
	untrackedOID = "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	deduppedOID  = "2222333344445555666677778888999900001111bbbbccccddddeeeeffffaaaa"
)

func lfsObject(oid string, age time.Duration) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:     storage.LFSKey(oid),
		Size:    int64(len(oid)),
		Updated: time.Now().Add(-age),
	}
}

// The leak this pass exists for: bytes at the content-addressed key that no
// lfs_objects row mentions. Enumerating rows cannot find them by
// construction, so before this pass they were charged for forever.
func TestRunGC_DeletesStoredLFSObjectWithNoRow(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{lfsObject(untrackedOID, 90*24*time.Hour)}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	want := storage.LFSKey(untrackedOID)
	if len(obj.deleted) != 1 || obj.deleted[0] != want {
		t.Fatalf("deleted = %v, want [%s]", obj.deleted, want)
	}
	if len(db.removedUntracked) != 1 || db.removedUntracked[0] != untrackedOID {
		t.Fatalf("removedUntracked = %v, want [%s]", db.removedUntracked, untrackedOID)
	}
}

// A row is written after the bytes on every upload path, so an object young
// enough to be one still being uploaded looks exactly like a leak. gc must
// leave it alone rather than fail somebody's push.
func TestRunGC_KeepsUntrackedLFSObjectWithinGrace(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{lfsObject(untrackedOID, time.Minute)}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 0 {
		t.Fatalf("deleted = %v, want none (object is within untrackedLFSGrace)", obj.deleted)
	}
}

// The race age cannot cover: an upload batch that finds these bytes already
// present deduplicates against them and writes the row without touching
// storage, so the object gains a row while its timestamp stays ancient.
// DeleteUntrackedLFSObject re-checks under the row lock, and gc must honour
// its answer instead of deleting bytes a repository now links to.
func TestRunGC_KeepsUntrackedLFSObjectThatGainedARowAfterTheScan(t *testing.T) {
	db := &fakeGCDB{
		referenced: map[string]bool{},
		liveRefs:   map[string]bool{},
		blobRefs:   map[string]bool{},
		liveRows:   map[string]bool{deduppedOID: true},
	}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{
		lfsObject(untrackedOID, 90*24*time.Hour),
		lfsObject(deduppedOID, 90*24*time.Hour),
	}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	want := storage.LFSKey(untrackedOID)
	if len(obj.deleted) != 1 || obj.deleted[0] != want {
		t.Fatalf("deleted = %v, want only [%s]", obj.deleted, want)
	}
}

// An object that is both stored and tracked-but-unreferenced belongs to the
// first pass. The second must not consider it again -- deleting a key twice
// would turn a completed reclaim into a storage error on the next call.
func TestRunGC_UnreferencedLFSObjectIsNotAlsoTreatedAsUntracked(t *testing.T) {
	db := &fakeGCDB{
		all:        []store.LFSObjectRef{{OID: untrackedOID, Size: 10}},
		referenced: map[string]bool{},
		liveRefs:   map[string]bool{},
		blobRefs:   map[string]bool{},
	}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{lfsObject(untrackedOID, 90*24*time.Hour)}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 1 {
		t.Fatalf("deleted = %v, want exactly one delete for the key", obj.deleted)
	}
	if len(db.removed) != 1 || len(db.removedUntracked) != 0 {
		t.Fatalf("removed = %v, removedUntracked = %v, want the row-driven pass to own it",
			db.removed, db.removedUntracked)
	}
}

func TestRunGC_DryRunDeletesNoUntrackedLFSObjects(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{lfsObject(untrackedOID, 90*24*time.Hour)}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, nil)
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 0 || len(db.removedUntracked) != 0 {
		t.Fatalf("dry run deleted storage=%v db=%v", obj.deleted, db.removedUntracked)
	}
}

func TestRunGC_UntrackedLFSStorageFailureIsReported(t *testing.T) {
	key := storage.LFSKey(untrackedOID)
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{
		objects: []storage.ObjectInfo{lfsObject(untrackedOID, 90*24*time.Hour)},
		fail:    map[string]error{key: errors.New("boom")},
	}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want an untracked lfs storage failure, got nil")
	}
	if len(db.removedUntracked) != 0 {
		t.Fatalf("removedUntracked = %v, want none after storage failure", db.removedUntracked)
	}
}

// The staging area lives under tmp/uploads/lfs/, not lfs/, so listing the
// content-addressed prefix must not reach it: an in-flight upload would
// otherwise be deleted by the pass that has no idea what staging is.
func TestRunGC_UntrackedPassIgnoresStagingObjects(t *testing.T) {
	db := &fakeGCDB{referenced: map[string]bool{}, liveRefs: map[string]bool{}, blobRefs: map[string]bool{}}
	obj := &fakeGCStorage{objects: []storage.ObjectInfo{
		stagingObject(storage.LFSStagingKey(7, untrackedOID), time.Minute),
	}}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err != nil {
		t.Fatalf("runGC: %v", err)
	}
	if len(obj.deleted) != 0 || len(db.removedUntracked) != 0 {
		t.Fatalf("deleted storage=%v db=%v, want none", obj.deleted, db.removedUntracked)
	}
}

// An object gc cannot remove must not cost every *other* object a full
// scheduling interval. The passes share no state, so a failure in one is
// reported and the rest still run.
func TestRunGC_OnePassFailingDoesNotStopTheOthers(t *testing.T) {
	failing := storage.LFSKey(deduppedOID)
	db := &fakeGCDB{
		all:        []store.LFSObjectRef{{OID: deduppedOID, Size: 10}},
		referenced: map[string]bool{},
		liveRefs:   map[string]bool{},
		blobRefs:   map[string]bool{},
	}
	obj := &fakeGCStorage{
		objects: []storage.ObjectInfo{
			// Reclaimed by the unreferenced pass, and its delete fails.
			lfsObject(deduppedOID, 90*24*time.Hour),
			// Each of these belongs to one of the passes that follow.
			lfsObject(untrackedOID, 90*24*time.Hour),
			blobObject("bbbb2222", 90*24*time.Hour),
			stagingObject("tmp/uploads/lfs/1/cccc", stagingGrace(testSignedURLMaxTTL)+time.Hour),
		},
		fail: map[string]error{failing: errors.New("boom")},
	}

	err := withDiscardedStdout(func() error {
		return runGC(context.Background(), db, obj, testSignedURLMaxTTL, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want the failed delete reported, got nil")
	}
	want := []string{
		storage.LFSKey(untrackedOID),
		storage.BlobKey("bbbb2222"),
		"tmp/uploads/lfs/1/cccc",
	}
	if len(obj.deleted) != len(want) {
		t.Fatalf("deleted = %v, want %v", obj.deleted, want)
	}
	for i, key := range want {
		if obj.deleted[i] != key {
			t.Fatalf("deleted = %v, want %v", obj.deleted, want)
		}
	}
}
