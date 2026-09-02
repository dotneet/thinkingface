package store

import (
	"testing"
	"time"
)

// The link set is what UsageByRepo sums, what NamespaceQuotaForRepo divides
// by, and what gc treats as the reference count. Nothing ever removed a link,
// so all three grew monotonically: a repository that pushed a big file,
// deleted it and pushed again stayed charged for both copies for ever, and a
// namespace that hit its quota could only be rescued by deleting it whole.
func TestIntegrationPruneRepoLFSLinksReleasesUnreferencedObjects(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "model", "model", nil)

		const (
			kept = "1111111111111111111111111111111111111111111111111111111111111111"
			gone = "2222222222222222222222222222222222222222222222222222222222222222"
		)
		for _, oid := range []string{kept, gone} {
			if err := s.RecordLFSObject(ctx, repo.ID, oid, 1024, func(string) (bool, error) { return true, nil }); err != nil {
				t.Fatalf("record %s: %v", oid, err)
			}
		}

		// The revision now names only one of the two: `gone` is the file that
		// was deleted from the tree.
		keptOID := kept
		if err := s.ReplaceRepoFiles(ctx, repo.ID, "main", []RepoFile{
			{Path: "weights.bin", Size: 1024, BlobSHA: "aa", LFSOID: &keptOID},
		}); err != nil {
			t.Fatalf("replace repo files: %v", err)
		}

		usage := func() int64 {
			t.Helper()
			rows, err := s.UsageByRepo(ctx, []string{"alice"})
			if err != nil {
				t.Fatalf("usage: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("usage rows = %d, want 1", len(rows))
			}
			return rows[0].LFSSize
		}

		// A link younger than the grace window is untouchable: an object is
		// uploaded and linked long before the commit naming it is pushed, so
		// "not in repo_files yet" is not evidence of anything.
		n, err := s.PruneRepoLFSLinks(ctx, repo.ID, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatalf("prune inside the grace window: %v", err)
		}
		if n != 0 {
			t.Fatalf("prune removed %d fresh links, want 0", n)
		}
		if got := usage(); got != 2048 {
			t.Fatalf("usage = %d, want both objects still counted", got)
		}

		// Past the window, the unreferenced one goes and the referenced one
		// stays -- and the usage number, which is the same query the quota
		// and the dashboard read, halves with it.
		n, err = s.PruneRepoLFSLinks(ctx, repo.ID, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("prune: %v", err)
		}
		if n != 1 {
			t.Fatalf("prune removed %d links, want 1", n)
		}
		if got := usage(); got != 1024 {
			t.Errorf("usage after prune = %d, want 1024", got)
		}

		stillLinked, err := s.RepoHasLFSObject(ctx, repo.ID, kept)
		if err != nil {
			t.Fatalf("RepoHasLFSObject: %v", err)
		}
		if !stillLinked {
			t.Error("the referenced object lost its link")
		}
		unlinked, err := s.RepoHasLFSObject(ctx, repo.ID, gone)
		if err != nil {
			t.Fatalf("RepoHasLFSObject: %v", err)
		}
		if unlinked {
			t.Error("the unreferenced object kept its link")
		}

		// And gc can now see it: ListReferencedLFSOIDs is the reference set
		// the collector subtracts from, so an object nothing links is finally
		// collectable rather than charged for ever.
		referenced, err := s.ListReferencedLFSOIDs(ctx)
		if err != nil {
			t.Fatalf("ListReferencedLFSOIDs: %v", err)
		}
		if referenced[gone] {
			t.Error("gc still counts the unreferenced object as referenced")
		}
		if !referenced[kept] {
			t.Error("gc lost the reference to a live object")
		}
	})
}

// **The invariant.** These links are the entitlement RepoHasLFSObject answers
// with -- resolve at any revision, and the LFS batch's download branch, both
// read it. An object a commit named must keep its link even after no ref tip
// names it any more, or `git checkout <old sha>` and
// GET /resolve/<old sha>/model.bin start 404ing on bytes that are still in the
// bucket, and gc then deletes them for real. LinkLFSObjects is what records
// that a commit named it.
func TestIntegrationPruneRepoLFSLinksKeepsObjectsNamedOnlyByHistory(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "model", "model", nil)

		const oid = "5555555555555555555555555555555555555555555555555555555555555555"
		if err := s.RecordLFSObject(ctx, repo.ID, oid, 4096, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatalf("record: %v", err)
		}
		// A commit names it: this is what the syncer and the HF-compatible
		// commit handler both do for the revision they are indexing.
		if err := s.LinkLFSObjects(ctx, repo.ID, []LFSObjectRef{{OID: oid, Size: 4096}}); err != nil {
			t.Fatalf("link: %v", err)
		}
		// A later commit deletes the file, so no ref tip names it any more.
		if err := s.ReplaceRepoFiles(ctx, repo.ID, "main", nil); err != nil {
			t.Fatalf("replace repo files: %v", err)
		}

		n, err := s.PruneRepoLFSLinks(ctx, repo.ID, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("prune: %v", err)
		}
		if n != 0 {
			t.Fatalf("prune removed %d links, want 0 -- a commit still names the object", n)
		}
		linked, err := s.RepoHasLFSObject(ctx, repo.ID, oid)
		if err != nil {
			t.Fatalf("RepoHasLFSObject: %v", err)
		}
		if !linked {
			t.Error("an object a historic commit names lost its entitlement")
		}

		// And gc must still count it, or the bytes go too.
		referenced, err := s.ListReferencedLFSOIDs(ctx)
		if err != nil {
			t.Fatalf("ListReferencedLFSOIDs: %v", err)
		}
		if !referenced[oid] {
			t.Error("gc no longer counts an object history still names")
		}
	})
}

// A ref is a reference. An object named only by a branch other than the one
// being indexed must survive a prune, or a push to main would strip the
// objects of every other branch.
func TestIntegrationPruneRepoLFSLinksKeepsObjectsOfOtherRefs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "model", "model", nil)

		const oid = "3333333333333333333333333333333333333333333333333333333333333333"
		if err := s.RecordLFSObject(ctx, repo.ID, oid, 7, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatalf("record: %v", err)
		}
		branchOID := oid
		if err := s.ReplaceRepoFiles(ctx, repo.ID, "experiment", []RepoFile{
			{Path: "w.bin", Size: 7, BlobSHA: "bb", LFSOID: &branchOID},
		}); err != nil {
			t.Fatalf("replace repo files (experiment): %v", err)
		}
		if err := s.ReplaceRepoFiles(ctx, repo.ID, "main", nil); err != nil {
			t.Fatalf("replace repo files (main): %v", err)
		}

		n, err := s.PruneRepoLFSLinks(ctx, repo.ID, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("prune: %v", err)
		}
		if n != 0 {
			t.Fatalf("prune removed %d links, want 0 -- the object is named by another ref", n)
		}
	})
}

// The prune is per repository. Another repository's link to the same content
// is not this repository's to release.
func TestIntegrationPruneRepoLFSLinksLeavesOtherRepositoriesAlone(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		mine := f.repo(t, "alice", "mine", "model", nil)
		theirs := f.repo(t, "bob", "theirs", "model", nil)

		const oid = "4444444444444444444444444444444444444444444444444444444444444444"
		for _, r := range []*Repo{mine, theirs} {
			if err := s.RecordLFSObject(ctx, r.ID, oid, 5, func(string) (bool, error) { return true, nil }); err != nil {
				t.Fatalf("record: %v", err)
			}
		}
		linked := oid
		if err := s.ReplaceRepoFiles(ctx, theirs.ID, "main", []RepoFile{
			{Path: "w.bin", Size: 5, BlobSHA: "cc", LFSOID: &linked},
		}); err != nil {
			t.Fatalf("replace repo files: %v", err)
		}

		if _, err := s.PruneRepoLFSLinks(ctx, mine.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("prune: %v", err)
		}
		still, err := s.RepoHasLFSObject(ctx, theirs.ID, oid)
		if err != nil {
			t.Fatalf("RepoHasLFSObject: %v", err)
		}
		if !still {
			t.Error("pruning one repository released another repository's link")
		}
	})
}

// HasUnsettledSyncJobs is what keeps the prune away from a repository whose
// index is incomplete: a queued or parked job means some ref's files have
// never been written, so its objects would look unreferenced.
func TestIntegrationHasUnsettledSyncJobs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		repo := f.repo(t, "alice", "model", "model", nil)

		busy, err := s.HasUnsettledSyncJobs(ctx, repo.ID, 0)
		if err != nil {
			t.Fatalf("HasUnsettledSyncJobs: %v", err)
		}
		if busy {
			t.Fatal("a repository with no jobs at all reported unsettled work")
		}

		if err := s.EnqueueSync(ctx, repo.ID, "main", "", "abc"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		job, err := s.ClaimSyncJob(ctx, time.Minute)
		if err != nil || job == nil {
			t.Fatalf("claim: job=%v err=%v", job, err)
		}

		// The job doing the asking does not count as work against itself.
		busy, err = s.HasUnsettledSyncJobs(ctx, repo.ID, job.ID)
		if err != nil {
			t.Fatalf("HasUnsettledSyncJobs: %v", err)
		}
		if busy {
			t.Error("a repository whose only job is the caller's own reported unsettled work")
		}

		// A second ref waiting in the queue does.
		if err := s.EnqueueSync(ctx, repo.ID, "other", "", "def"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		busy, err = s.HasUnsettledSyncJobs(ctx, repo.ID, job.ID)
		if err != nil {
			t.Fatalf("HasUnsettledSyncJobs: %v", err)
		}
		if !busy {
			t.Error("a queued job for another ref was not reported")
		}
	})
}
