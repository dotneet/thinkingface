package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

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
		return runGC(context.Background(), db, obj, []string{"--yes"})
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
		return runGC(context.Background(), db, obj, nil)
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
		return runGC(context.Background(), db, obj, []string{"--yes"})
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
		return runGC(context.Background(), db, obj, []string{"--yes"})
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
		return runGC(context.Background(), db, obj, nil)
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
		return runGC(context.Background(), db, obj, []string{"--yes"})
	})
	if err == nil {
		t.Fatal("runGC: want a blob storage failure, got nil")
	}
}
