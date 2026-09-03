package syncer

import (
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// TestRunPushPipeline_RepublishesBlobsTheCollectorRemoved is the push side of
// the blobs/ deletion ledger.
//
// The state it starts from is the one the collector's race produces: a
// repo_files row naming a sha, a blob_deletions row for that sha, and no
// object in the bucket. `thinkingface gc` gets there by re-checking
// repo_files, finding nothing, recording the removal and deleting -- all
// before the push that claims the sha commits its index rows. (It is also
// what a collector that crashed between its two transactions leaves, and what
// the previous binary left behind unconditionally.)
//
// What used to happen next was nothing, forever: publishBlobs skips a sha the
// ref's index already names, since the worker that wrote that row is supposed
// to have published it, so resolve answered 404 for that file until somebody
// ran `thinkingface resync`.
func TestRunPushPipeline_RepublishesBlobsTheCollectorRemoved(t *testing.T) {
	f := newPushFixture(t)

	f.push("main", addOp("README.md", "# foo\n"), addOp("keep.txt", "kept\n"))
	sha := f.blobSHA("main", "README.md")
	key := storage.BlobKey(sha)
	if string(f.mustGet(key)) != "# foo\n" {
		t.Fatalf("fixture: %s was not published", key)
	}

	// The collector's own sequence, run at the moment its snapshot said the
	// sha was unreferenced. DeleteRefIndex is what makes that true here; a
	// second repository dropping its last reference is what makes it true in
	// production.
	files, err := f.st.ListRepoFiles(f.ctx, f.repo.ID, "main")
	if err != nil {
		t.Fatalf("read the index: %v", err)
	}
	if err := f.st.DeleteRefIndex(f.ctx, f.repo.ID, "main"); err != nil {
		t.Fatalf("clear the index: %v", err)
	}
	collected, err := f.st.DeleteOrphanedBlob(f.ctx, sha, func() error {
		return f.obj.Delete(f.ctx, key)
	})
	if err != nil || !collected {
		t.Fatalf("collect %s = %v, %v; want it taken", sha, collected, err)
	}
	// ...and the push it raced commits its rows just after.
	if err := f.st.ReplaceRepoFiles(f.ctx, f.repo.ID, "main", files); err != nil {
		t.Fatalf("restore the index: %v", err)
	}
	if _, err := f.obj.Get(f.ctx, key); err == nil {
		t.Fatal("fixture: the blob is still in the bucket")
	}

	// The next push touches a different file entirely: README.md's sha is in
	// the ref's index, so publishBlobs skips it and only the repair pass can
	// put it back.
	f.push("main", addOp("other.txt", "unrelated\n"))

	if got := string(f.mustGet(key)); got != "# foo\n" {
		t.Errorf("%s = %q after the next push, want the collected blob republished", key, got)
	}
	// And the record is forgotten, so the following push does no work at all.
	n, err := f.st.RepairDeletedBlobs(f.ctx, f.repo.ID, "main", func(string) error {
		t.Error("the ledger row outlived the repair that answered it")
		return nil
	})
	if err != nil {
		t.Fatalf("RepairDeletedBlobs: %v", err)
	}
	if n != 0 {
		t.Errorf("%d records left after the repair, want 0", n)
	}
}

// TestRunPushPipeline_LeavesTheRepositoryIndexAloneWhenTheRefIsGone pins the
// other half of a deleted ref.
//
// gitrepo.Tree folds "this revision does not resolve" into an empty listing
// with a nil error and a zero commit -- so the `errors.Is(err, ErrEmptyRepo)`
// guard the pipeline used to open with was unreachable, and a job whose ref
// had been deleted between the enqueue and the worker ran the whole pipeline
// on an empty tree. When that ref was the default branch, the repository's
// metadata index was rewritten from it: head_sha = "0000...", the card
// emptied, the lineage dropped.
func TestRunPushPipeline_LeavesTheRepositoryIndexAloneWhenTheRefIsGone(t *testing.T) {
	f := newPushFixture(t)

	f.push("main", addOp("README.md", "---\ntags:\n  - vision\n---\n\n# foo\n"))
	indexed, err := f.st.GetRepoByID(f.ctx, f.repo.ID)
	if err != nil {
		t.Fatalf("load repo: %v", err)
	}
	if indexed.HeadSHA == "" || len(indexed.Card) == 0 {
		t.Fatalf("fixture: head_sha=%q card=%v, want both indexed", indexed.HeadSHA, indexed.Card)
	}

	// The default branch goes away, and a job for it is still in the queue --
	// `git push --delete main` cannot do this, but the WAL index catching up
	// with another instance's delete can, and so can a job left over from a
	// push that the branch API's delete then overtook.
	if _, err := f.git.DeleteRef(gitrepo.BranchRef("main")); err != nil {
		t.Fatalf("delete refs/heads/main: %v", err)
	}
	if err := f.st.EnqueueSync(f.ctx, f.repo.ID, "main", indexed.HeadSHA, indexed.HeadSHA); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	f.step()

	after, err := f.st.GetRepoByID(f.ctx, f.repo.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if after.HeadSHA != indexed.HeadSHA {
		t.Errorf("head_sha = %q, want %q unchanged by a job for a ref that is gone",
			after.HeadSHA, indexed.HeadSHA)
	}
	if len(after.Card) != len(indexed.Card) {
		t.Errorf("card = %v, want %v", after.Card, indexed.Card)
	}
}

// A ref that never existed is the same case, and the one the unreachable
// guard was written for: a repository with no commits at all has nothing to
// index and must not have its index rewritten either.
func TestRunPushPipeline_IndexesNothingForARepositoryWithNoCommits(t *testing.T) {
	f := newPushFixture(t)

	if err := f.st.EnqueueSync(f.ctx, f.repo.ID, "main", "", ""); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	f.step()

	files, err := f.st.ListRepoFiles(f.ctx, f.repo.ID, "main")
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("indexed %d files for an empty repository", len(files))
	}
	repo, err := f.st.GetRepoByID(f.ctx, f.repo.ID)
	if err != nil {
		t.Fatalf("load repo: %v", err)
	}
	if repo.HeadSHA != "" {
		t.Errorf("head_sha = %q, want it left empty", repo.HeadSHA)
	}
}
