package store

import (
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

func TestOrphanedLFSObjects_ExcludesReferenced(t *testing.T) {
	all := []LFSObjectRef{
		{OID: "a", Size: 10},
		{OID: "b", Size: 20},
		{OID: "c", Size: 30},
	}
	referenced := map[string]bool{"b": true}

	got := OrphanedLFSObjects(all, referenced)

	want := []LFSObjectRef{{OID: "a", Size: 10}, {OID: "c", Size: 30}}
	if len(got) != len(want) {
		t.Fatalf("OrphanedLFSObjects() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("OrphanedLFSObjects()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestOrphanedLFSObjects_NoneOrphaned(t *testing.T) {
	all := []LFSObjectRef{{OID: "a", Size: 10}, {OID: "b", Size: 20}}
	referenced := map[string]bool{"a": true, "b": true}

	got := OrphanedLFSObjects(all, referenced)
	if len(got) != 0 {
		t.Errorf("OrphanedLFSObjects() = %v, want empty", got)
	}
}

func TestOrphanedLFSObjects_AllOrphaned(t *testing.T) {
	all := []LFSObjectRef{{OID: "a", Size: 10}, {OID: "b", Size: 20}}
	referenced := map[string]bool{}

	got := OrphanedLFSObjects(all, referenced)
	if len(got) != 2 {
		t.Errorf("OrphanedLFSObjects() = %v, want both objects", got)
	}
}

func TestOrphanedLFSObjects_EmptyInputsDoNotPanic(t *testing.T) {
	if got := OrphanedLFSObjects(nil, nil); len(got) != 0 {
		t.Errorf("OrphanedLFSObjects(nil, nil) = %v, want empty", got)
	}
	if got := OrphanedLFSObjects([]LFSObjectRef{}, map[string]bool{}); len(got) != 0 {
		t.Errorf("OrphanedLFSObjects(empty, empty) = %v, want empty", got)
	}
}

// A nil referenced map must behave exactly like an empty one: every object is
// orphaned, since Go allows reading (but not writing) a nil map.
func TestOrphanedLFSObjects_NilReferencedMapTreatsEverythingAsOrphaned(t *testing.T) {
	all := []LFSObjectRef{{OID: "a", Size: 10}}
	got := OrphanedLFSObjects(all, nil)
	if len(got) != 1 {
		t.Errorf("OrphanedLFSObjects(all, nil) = %v, want [a]", got)
	}
}

// A second snapshot that includes a newly linked oid must drop that oid from
// the delete set. This is the decision DeleteOrphanedLFSObject re-applies
// under a row lock; the lock itself needs a database, but the filter is the
// same pure function the scan already uses.
func TestOrphanedLFSObjects_ReCheckDropsNewlyReferencedOID(t *testing.T) {
	all := []LFSObjectRef{{OID: "a", Size: 10}, {OID: "b", Size: 20}}
	atScan := map[string]bool{}
	candidates := OrphanedLFSObjects(all, atScan)
	if len(candidates) != 2 {
		t.Fatalf("scan orphaned = %v, want both", candidates)
	}

	atDelete := map[string]bool{"a": true}
	got := OrphanedLFSObjects(candidates, atDelete)
	if len(got) != 1 || got[0].OID != "b" {
		t.Fatalf("re-check = %v, want only b", got)
	}
}

// ListReferencedBlobSHAs is the reference count for the blobs/ layer, so it
// must answer for every ref -- not only the default branch -- and must ignore
// LFS files, whose bytes live in the other layer entirely.
func TestListReferencedBlobSHAs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		r := f.repo(t, "alice", "foo", "dataset", nil)
		other := f.repo(t, "bob", "bar", "dataset", nil)

		oid := "oid-1"
		if err := s.ReplaceRepoFiles(f.ctx, r.ID, "main", []RepoFile{
			{Path: "README.md", Size: 1, BlobSHA: "sha-readme"},
			{Path: "big.bin", Size: 9, BlobSHA: "sha-pointer", LFSOID: &oid},
		}); err != nil {
			t.Fatalf("replace files on main: %v", err)
		}
		if err := s.ReplaceRepoFiles(f.ctx, r.ID, "dev", []RepoFile{
			{Path: "scratch.txt", Size: 1, BlobSHA: "sha-scratch"},
		}); err != nil {
			t.Fatalf("replace files on dev: %v", err)
		}
		// The same content in a second repository must not double-count or
		// disappear when only one of them is rewritten.
		if err := s.ReplaceRepoFiles(f.ctx, other.ID, "main", []RepoFile{
			{Path: "copy.md", Size: 1, BlobSHA: "sha-readme"},
		}); err != nil {
			t.Fatalf("replace files for other repo: %v", err)
		}

		got, err := s.ListReferencedBlobSHAs(f.ctx)
		if err != nil {
			t.Fatalf("ListReferencedBlobSHAs: %v", err)
		}
		want := map[string]bool{"sha-readme": true, "sha-scratch": true}
		if len(got) != len(want) {
			t.Fatalf("ListReferencedBlobSHAs = %v, want %v", got, want)
		}
		for sha := range want {
			if !got[sha] {
				t.Errorf("%s missing from the referenced set", sha)
			}
		}
		if got["sha-pointer"] {
			t.Error("an LFS file's pointer blob was counted as a referenced blob")
		}

		// Dropping the only ref that carried a sha releases it.
		if err := s.ReplaceRepoFiles(f.ctx, r.ID, "dev", nil); err != nil {
			t.Fatalf("clear dev: %v", err)
		}
		got, err = s.ListReferencedBlobSHAs(f.ctx)
		if err != nil {
			t.Fatalf("ListReferencedBlobSHAs after clear: %v", err)
		}
		if got["sha-scratch"] {
			t.Error("sha-scratch still referenced after its only ref was cleared")
		}
		if !got["sha-readme"] {
			t.Error("sha-readme lost its reference; the other repository still carries it")
		}
	})
}

func lfsStored(oid string, age time.Duration) storage.ObjectInfo {
	return storage.ObjectInfo{
		Key:     storage.LFSKey(oid),
		Size:    int64(len(oid)),
		Updated: time.Now().Add(-age),
	}
}

func TestUntrackedLFSObjects_KeepsTrackedAndYoungObjects(t *testing.T) {
	tracked := lfsStored("aaaa1111", 90*24*time.Hour)
	leaked := lfsStored("bbbb2222", 90*24*time.Hour)
	// Bytes written moments ago, whose row is probably being committed right
	// now: every write path stores before it records.
	inFlight := lfsStored("cccc3333", time.Minute)

	got := UntrackedLFSObjects(
		[]storage.ObjectInfo{tracked, leaked, inFlight},
		[]LFSObjectRef{{OID: "aaaa1111", Size: 8}},
		time.Now().Add(-24*time.Hour),
	)

	if len(got) != 1 || got[0].Key != leaked.Key {
		t.Fatalf("UntrackedLFSObjects = %v, want only %s", got, leaked.Key)
	}
}

// Only keys that are the canonical home of the oid in their basename are
// touched. Nothing this system writes produces any other shape under lfs/, so
// one that turns up is not something to guess the meaning of and delete.
func TestUntrackedLFSObjects_SkipsKeysThatAreNotACanonicalLFSKey(t *testing.T) {
	old := time.Now().Add(-90 * 24 * time.Hour)
	objects := []storage.ObjectInfo{
		{Key: "lfs/aaaa1111", Updated: old},          // missing the fanout directories
		{Key: "lfs/zz/zz/aaaa1111", Updated: old},    // fanout that does not match the oid
		{Key: "lfs/aa/aa/1111/nested", Updated: old}, // a directory where the object should be
	}

	if got := UntrackedLFSObjects(objects, nil, time.Now()); len(got) != 0 {
		t.Fatalf("UntrackedLFSObjects = %v, want none", got)
	}
}

func TestUntrackedLFSObjects_EmptyInputsDoNotPanic(t *testing.T) {
	if got := UntrackedLFSObjects(nil, nil, time.Now()); len(got) != 0 {
		t.Errorf("UntrackedLFSObjects(nil, nil) = %v, want empty", got)
	}
}
