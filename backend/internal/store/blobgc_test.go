package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// The blobs/ layer has no row of its own -- a blob is written to its
// content-addressed key and recorded nowhere -- so `thinkingface gc` used to
// decide from a listing and a snapshot of repo_files and then delete, with
// nothing in between that a concurrent push could block on. blob_deletions is
// the row that was missing, and these tests pin the two halves that make it
// work: the collector refusing a sha a revision has claimed since the scan,
// and a push putting back the bytes a collector took anyway.

const (
	shaOne = "1111111111111111111111111111111111111111"
	shaTwo = "2222222222222222222222222222222222222222"
)

func plainFile(path, sha string) RepoFile {
	return RepoFile{Path: path, Size: 3, BlobSHA: sha}
}

// The scan that produces a candidate is a snapshot. A push landing between it
// and the delete is the whole hazard -- and unlike the lfs/ layer, nothing
// about it touches the object, so no age threshold can see it coming: a
// second repository committing byte-identical content finds the blob already
// at its key and writes nothing at all.
func TestIntegrationDeleteOrphanedBlobRefusesAShaARevisionStillNames(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "claimed", "model", nil)
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "main", []RepoFile{plainFile("a.txt", shaOne)}); err != nil {
			t.Fatalf("index the revision: %v", err)
		}

		removed := false
		deleted, err := s.DeleteOrphanedBlob(f.ctx, shaOne, func() error {
			removed = true
			return nil
		})
		if err != nil {
			t.Fatalf("DeleteOrphanedBlob: %v", err)
		}
		if deleted || removed {
			t.Fatalf("deleted=%v removed=%v, want the collector to refuse a referenced sha", deleted, removed)
		}
	})
}

func TestIntegrationDeleteOrphanedBlobRemovesAShaNothingNames(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "orphan", "model", nil)
		// Another sha entirely: the file index is not empty, it just does not
		// name this one.
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "main", []RepoFile{plainFile("a.txt", shaTwo)}); err != nil {
			t.Fatalf("index the revision: %v", err)
		}

		removed := 0
		deleted, err := s.DeleteOrphanedBlob(f.ctx, shaOne, func() error {
			removed++
			return nil
		})
		if err != nil {
			t.Fatalf("DeleteOrphanedBlob: %v", err)
		}
		if !deleted || removed != 1 {
			t.Fatalf("deleted=%v removed=%d, want the orphan collected exactly once", deleted, removed)
		}
	})
}

// An LFS file's blob is the pointer text and never reaches blobs/, so those
// rows are not part of this layer's reference count -- the same rule
// ListReferencedBlobSHAs applies when it chooses candidates. The re-check has
// to agree with it, or a sha would be offered and then always refused.
func TestIntegrationDeleteOrphanedBlobIgnoresLFSPointerRows(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "pointers", "model", nil)
		oid := "aaaa"
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "main",
			[]RepoFile{{Path: "model.bin", Size: 9, BlobSHA: shaOne, LFSOID: &oid}}); err != nil {
			t.Fatalf("index the revision: %v", err)
		}

		deleted, err := s.DeleteOrphanedBlob(f.ctx, shaOne, func() error { return nil })
		if err != nil {
			t.Fatalf("DeleteOrphanedBlob: %v", err)
		}
		if !deleted {
			t.Fatal("a sha named only by an LFS pointer row must not count as referenced")
		}
	})
}

// The intent is recorded in its own transaction, before any byte is removed,
// so that a failure anywhere after it still leaves the record a repair needs.
// The safe direction is a ledger row for bytes that survived: the repair pass
// answers that with one idempotent PublishBlob.
func TestIntegrationDeleteOrphanedBlobKeepsItsRecordWhenStorageFails(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "halfway", "model", nil)

		boom := errors.New("bucket unreachable")
		if _, err := s.DeleteOrphanedBlob(f.ctx, shaOne, func() error { return boom }); !errors.Is(err, boom) {
			t.Fatalf("DeleteOrphanedBlob = %v, want the storage failure", err)
		}

		// A revision claims the sha afterwards -- the crash could equally have
		// been between the record and a delete that did happen, and from here
		// the two are indistinguishable. The repair has to run either way.
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "main", []RepoFile{plainFile("a.txt", shaOne)}); err != nil {
			t.Fatalf("index the revision: %v", err)
		}
		var republished []string
		n, err := s.RepairDeletedBlobs(f.ctx, repo.ID, "main", func(sha string) error {
			republished = append(republished, sha)
			return nil
		})
		if err != nil {
			t.Fatalf("RepairDeletedBlobs: %v", err)
		}
		if n != 1 || len(republished) != 1 || republished[0] != shaOne {
			t.Fatalf("repaired %d %v, want the recorded sha", n, republished)
		}
	})
}

// The repair is scoped to the ref being indexed and forgets what it has
// answered, so the ledger stays proportional to the damage rather than to
// everything ever collected.
func TestIntegrationRepairDeletedBlobsIsScopedToTheRefAndForgetsWhatItFixed(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "scoped", "model", nil)
		// The collector's order, which is the whole race: it re-checks, finds
		// nothing, records and deletes -- and only then do the pushes commit
		// the rows that name those shas.
		recordDeletion(t, f.ctx, s, shaOne)
		recordDeletion(t, f.ctx, s, shaTwo)
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "main", []RepoFile{plainFile("a.txt", shaOne)}); err != nil {
			t.Fatalf("index main: %v", err)
		}
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "side", []RepoFile{plainFile("b.txt", shaTwo)}); err != nil {
			t.Fatalf("index side: %v", err)
		}

		var republished []string
		n, err := s.RepairDeletedBlobs(f.ctx, repo.ID, "main", func(sha string) error {
			republished = append(republished, sha)
			return nil
		})
		if err != nil {
			t.Fatalf("RepairDeletedBlobs(main): %v", err)
		}
		if n != 1 || len(republished) != 1 || republished[0] != shaOne {
			t.Fatalf("main repaired %d %v, want only main's sha", n, republished)
		}

		// Answered once, then forgotten: a second pass over the same ref has
		// nothing left to do.
		again, err := s.RepairDeletedBlobs(f.ctx, repo.ID, "main", func(string) error {
			t.Error("republished a sha the previous pass had already put back")
			return nil
		})
		if err != nil {
			t.Fatalf("second RepairDeletedBlobs(main): %v", err)
		}
		if again != 0 {
			t.Errorf("second pass repaired %d, want 0", again)
		}

		// side's record is untouched by main's repair.
		if n, err := s.RepairDeletedBlobs(f.ctx, repo.ID, "side", func(string) error { return nil }); err != nil || n != 1 {
			t.Errorf("RepairDeletedBlobs(side) = %d, %v; want 1, nil", n, err)
		}
	})
}

// A republish that fails must not forget the record: the next push has to
// find it and try again.
func TestIntegrationRepairDeletedBlobsKeepsTheRecordWhenRepublishFails(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "retry", "model", nil)
		recordDeletion(t, f.ctx, s, shaOne)
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "main", []RepoFile{plainFile("a.txt", shaOne)}); err != nil {
			t.Fatalf("index main: %v", err)
		}

		boom := errors.New("object store down")
		if _, err := s.RepairDeletedBlobs(f.ctx, repo.ID, "main", func(string) error { return boom }); !errors.Is(err, boom) {
			t.Fatalf("RepairDeletedBlobs = %v, want the republish failure", err)
		}
		if n, err := s.RepairDeletedBlobs(f.ctx, repo.ID, "main", func(string) error { return nil }); err != nil || n != 1 {
			t.Errorf("retry repaired %d, %v; want 1, nil", n, err)
		}
	})
}

// Pruning is what keeps one row per collected blob from becoming permanent.
// It may only take rows nothing references -- a referenced sha is one a push
// is still owed a repair for -- and only after the floor, because the intent
// is recorded before the bytes go and the push that claims the sha may still
// be committing.
func TestIntegrationPruneBlobDeletionsKeepsWhatARevisionStillNames(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "pruned", "model", nil)
		recordDeletion(t, f.ctx, s, shaOne)
		recordDeletion(t, f.ctx, s, shaTwo)
		if err := s.ReplaceRepoFiles(f.ctx, repo.ID, "main", []RepoFile{plainFile("a.txt", shaOne)}); err != nil {
			t.Fatalf("index main: %v", err)
		}

		// Nothing is old enough yet.
		if n, err := s.PruneBlobDeletions(f.ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
			t.Fatalf("early prune removed %d, %v; want 0, nil", n, err)
		}

		n, err := s.PruneBlobDeletions(f.ctx, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("PruneBlobDeletions: %v", err)
		}
		if n != 1 {
			t.Fatalf("pruned %d rows, want only the unreferenced one", n)
		}
		if got, err := s.RepairDeletedBlobs(f.ctx, repo.ID, "main", func(string) error { return nil }); err != nil || got != 1 {
			t.Errorf("main's record survived as %d, %v; want 1, nil", got, err)
		}
	})
}

// recordDeletion drives one whole collection of sha through the public
// method, which is the only way to write a ledger row. removeStorage is a
// no-op: these tests care about the bookkeeping, not the bucket.
func recordDeletion(t *testing.T, ctx context.Context, s *Store, sha string) {
	t.Helper()
	deleted, err := s.DeleteOrphanedBlob(ctx, sha, func() error { return nil })
	if err != nil {
		t.Fatalf("collect %s: %v", sha, err)
	}
	if !deleted {
		t.Fatalf("collect %s: refused, so no record was written", sha)
	}
}

// ------------------------------------------------------------ ref teardown

// A branch that is deleted takes its cached index with it. Until
// DeleteRefIndex existed nothing removed those rows: ListRepoFiles kept
// answering for a branch that was gone, and -- the expensive half --
// ListReferencedBlobSHAs kept its blobs out of the collector's reach for the
// life of the repository.
func TestIntegrationDeleteRefIndexDropsOnlyThatRefsRows(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "branches", "dataset", nil)
		for _, ref := range []string{"main", "feature"} {
			if err := s.ReplaceRepoFiles(f.ctx, repo.ID, ref,
				[]RepoFile{plainFile("data.parquet", shaOne+ref[:1])}); err != nil {
				t.Fatalf("index %s: %v", ref, err)
			}
			if err := s.UpsertParquetFile(f.ctx, repo.ID, ref, "data.parquet", 1, 1,
				json.RawMessage(`[{"name":"a"}]`)); err != nil {
				t.Fatalf("index parquet on %s: %v", ref, err)
			}
		}

		if err := s.DeleteRefIndex(f.ctx, repo.ID, "feature"); err != nil {
			t.Fatalf("DeleteRefIndex: %v", err)
		}

		gone, err := s.ListRepoFiles(f.ctx, repo.ID, "feature")
		if err != nil {
			t.Fatalf("list feature files: %v", err)
		}
		if len(gone) != 0 {
			t.Errorf("feature still lists %d files after its branch was deleted", len(gone))
		}
		goneParquet, err := s.ListParquetFiles(f.ctx, repo.ID, "feature")
		if err != nil {
			t.Fatalf("list feature parquet: %v", err)
		}
		if len(goneParquet) != 0 {
			t.Errorf("feature still lists %d parquet files", len(goneParquet))
		}

		kept, err := s.ListRepoFiles(f.ctx, repo.ID, "main")
		if err != nil {
			t.Fatalf("list main files: %v", err)
		}
		if len(kept) != 1 {
			t.Errorf("main lists %d files, want its own 1 untouched", len(kept))
		}
		keptParquet, err := s.ListParquetFiles(f.ctx, repo.ID, "main")
		if err != nil {
			t.Fatalf("list main parquet: %v", err)
		}
		if len(keptParquet) != 1 {
			t.Errorf("main lists %d parquet files, want 1", len(keptParquet))
		}

		// The blobs of the deleted branch stop counting as referenced, which
		// is the leak this closes: gc could never reclaim them before.
		refs, err := s.ListReferencedBlobSHAs(f.ctx)
		if err != nil {
			t.Fatalf("list referenced blob shas: %v", err)
		}
		if refs[shaOne+"f"] {
			t.Error("a deleted branch's blob is still counted as referenced")
		}
		if !refs[shaOne+"m"] {
			t.Error("main's blob stopped counting as referenced")
		}
	})
}

// A repository deleted in the meantime is the outcome asked for, not a
// failure: its rows went with it through ON DELETE CASCADE.
func TestIntegrationDeleteRefIndexToleratesADeletedRepository(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "vanishing", "model", nil)
		if err := s.DeleteRepo(f.ctx, repo.ID); err != nil {
			t.Fatalf("delete repo: %v", err)
		}
		if err := s.DeleteRefIndex(f.ctx, repo.ID, "main"); err != nil {
			t.Errorf("DeleteRefIndex on a deleted repository = %v, want nil", err)
		}
	})
}
