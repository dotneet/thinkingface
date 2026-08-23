package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// An index rebuild for a repository that was deleted in the meantime must
// surface as ErrNotFound (the syncer treats it as "nothing left to do"),
// not as a foreign-key violation or a success that leaves orphan rows.
func TestReplaceRepoFiles_DeletedRepositoryIsNotFound(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		r := f.repo(t, "alice", "gone", "dataset", nil)
		if err := s.DeleteRepo(f.ctx, r.ID); err != nil {
			t.Fatalf("DeleteRepo: %v", err)
		}
		err := s.ReplaceRepoFiles(f.ctx, r.ID, "main", []RepoFile{{Path: "a", Size: 1, BlobSHA: "sha-a"}})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ReplaceRepoFiles after delete = %v, want ErrNotFound", err)
		}
		err = s.ReplaceRepoLineage(f.ctx, r.ID, []LineageEdge{{Kind: "dataset", Raw: "x/y", Namespace: "x", Name: "y"}})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ReplaceRepoLineage after delete = %v, want ErrNotFound", err)
		}
		err = s.UpsertParquetFile(f.ctx, r.ID, "main", "data/x.parquet", 1, 1, nil)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpsertParquetFile after delete = %v, want ErrNotFound", err)
		}
	})
}

// Regression for the push-vs-delete deadlock (SQLSTATE 40P01 on Postgres):
// ReplaceRepoFiles used to lock repo_files rows before the repositories row
// while DeleteRepo's cascade did the opposite. Both writers now take the
// repositories row first, so racing them must only ever end in success or
// ErrNotFound -- never in a deadlock error. On SQLite the single writer
// serialises them anyway; the Postgres backend is the one this is for.
func TestReplaceRepoFiles_RacesDeleteRepoWithoutDeadlock(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		const rounds = 25
		for i := 0; i < rounds; i++ {
			r := f.repo(t, "alice", fmt.Sprintf("race-%d", i), "dataset", nil)
			seed := make([]RepoFile, 0, 40)
			for j := 0; j < 40; j++ {
				seed = append(seed, RepoFile{Path: fmt.Sprintf("f%02d", j), Size: 1, BlobSHA: fmt.Sprintf("sha-%d-%d", i, j)})
			}
			if err := s.ReplaceRepoFiles(f.ctx, r.ID, "main", seed); err != nil {
				t.Fatalf("seed ReplaceRepoFiles: %v", err)
			}
			next := append([]RepoFile(nil), seed[:20]...)
			next = append(next, RepoFile{Path: "new", Size: 2, BlobSHA: fmt.Sprintf("sha-new-%d", i)})

			var wg sync.WaitGroup
			var replaceErr, deleteErr error
			wg.Add(2)
			go func() { defer wg.Done(); replaceErr = s.ReplaceRepoFiles(f.ctx, r.ID, "main", next) }()
			go func() { defer wg.Done(); deleteErr = s.DeleteRepo(f.ctx, r.ID) }()
			wg.Wait()

			if replaceErr != nil && !errors.Is(replaceErr, ErrNotFound) {
				t.Fatalf("round %d: ReplaceRepoFiles = %v, want nil or ErrNotFound", i, replaceErr)
			}
			if deleteErr != nil {
				t.Fatalf("round %d: DeleteRepo = %v, want nil", i, deleteErr)
			}
			// Whatever the interleaving, nothing may be left behind.
			files, err := s.ListRepoFiles(f.ctx, r.ID, "main")
			if err != nil {
				t.Fatalf("ListRepoFiles: %v", err)
			}
			if len(files) != 0 {
				t.Fatalf("round %d: %d repo_files rows survived the delete", i, len(files))
			}
		}
	})
}
