package syncer

import (
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// noLFSLinkGrace makes the prune consider every link settled, so a test does
// not have to wait out a day to see the reconciliation it is about.
func noLFSLinkGrace(t *testing.T) {
	t.Helper()
	restore := lfsLinkGrace
	lfsLinkGrace = -time.Second
	t.Cleanup(func() { lfsLinkGrace = restore })
}

// **The invariant.** repo_lfs_objects is the entitlement `resolve` and the LFS
// batch's download branch check (store.RepoHasLFSObject), not a cache of the
// tip. An object named by a commit that is no longer the tip -- a file deleted
// on main, a file that only ever existed on an older commit -- must keep its
// link, or `git checkout <that sha>`, `git lfs fetch --all` and
// GET /resolve/<that sha>/model.bin all start answering 404 while the bytes sit
// untouched in the bucket. Worse, `thinkingface gc` reads the same links as its
// reference set, so it then deletes those bytes for real.
//
// A prune that reconciles against repo_files -- one row per file of each *ref
// tip* -- does exactly that, silently, one grace period after the deletion.
// blobs/ can be collected on the tip rule because it is a publishing copy and
// the bare repository still holds the git object; for LFS the bucket is the
// only copy there is.
func TestPush_KeepsLFSLinksNamedOnlyByHistory(t *testing.T) {
	noLFSLinkGrace(t)
	f := newPushFixture(t)
	content := []byte("large file bytes")
	seedLFSObject(t, f.harness, f.repo.ID, pointerOID, content)

	f.push("main",
		addOp("README.md", "# foo\n"),
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))},
	)
	historic, err := f.git.Resolve("main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}

	linked, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !linked {
		t.Fatal("the pushed pointer did not earn a link")
	}

	// The file is deleted from the tip and the change pushed. The commit that
	// added it is still in the repository's history, so it is still something
	// a client can check out.
	f.push("main", gitrepo.Op{Kind: gitrepo.OpDelete, Path: "model.bin"})

	// The pointer is where it always was...
	entry, _, err := f.git.Stat(historic.String(), "model.bin")
	if err != nil {
		t.Fatalf("stat model.bin at the historic revision: %v", err)
	}
	if entry.LFS == nil || entry.LFS.OID != pointerOID {
		t.Fatalf("historic revision no longer names the object: %+v", entry.LFS)
	}
	// ...and so is the entitlement that lets resolve hand the bytes over.
	linked, err = f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !linked {
		t.Fatal("an object a historic commit still names lost its link; " +
			"resolve and git checkout at that revision now 404")
	}
}

// The same thing said in terms of the number the operator sees: deleting a
// file does not give the bytes back while a commit still names them. That is
// not a leak, it is what a versioned store costs -- `git clone` of that
// revision still has to work. Only deleting the repository (or rewriting the
// history and running gc) frees them.
func TestPush_UsageKeepsCountingObjectsHistoryStillNames(t *testing.T) {
	noLFSLinkGrace(t)
	f := newPushFixture(t)
	content := []byte("large file bytes")
	seedLFSObject(t, f.harness, f.repo.ID, pointerOID, content)

	usage := func() int64 {
		t.Helper()
		rows, err := f.st.UsageByRepo(f.ctx, []string{"alice"})
		if err != nil {
			t.Fatalf("usage: %v", err)
		}
		var total int64
		for _, r := range rows {
			total += r.LFSSize
		}
		return total
	}

	f.push("main",
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))},
	)
	if got := usage(); got != int64(len(content)) {
		t.Fatalf("usage after the push = %d, want %d", got, len(content))
	}

	f.push("main", gitrepo.Op{Kind: gitrepo.OpDelete, Path: "model.bin"})
	if got := usage(); got != int64(len(content)) {
		t.Errorf("usage after deleting the file = %d, want %d -- history still names the object",
			got, len(content))
	}
}

// What the prune *is* for: a transfer that completed and whose commit never
// arrived. `tf up` interrupted between the upload and the commit used to leave
// tens of gigabytes charged to a repository holding no files at all.
func TestPush_ReleasesLFSLinksNoCommitEverNamed(t *testing.T) {
	noLFSLinkGrace(t)
	f := newPushFixture(t)
	content := []byte("uploaded, never committed")
	seedLFSObject(t, f.harness, f.repo.ID, pointerOID, content)

	// A push that names nothing -- the object stays exactly as the abandoned
	// upload left it.
	f.push("main", addOp("README.md", "# foo\n"))

	linked, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if linked {
		t.Error("an object no commit has ever named kept its link")
	}
	rows, err := f.st.UsageByRepo(f.ctx, []string{"alice"})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	for _, r := range rows {
		if r.LFSSize != 0 {
			t.Errorf("usage = %d, want 0 -- the abandoned upload is still charged", r.LFSSize)
		}
	}
}

// The other direction, and the reason the grace window exists at all: an
// object is uploaded, promoted and linked long before the commit naming it is
// pushed. A `tf up` that transfers a dataset and then commits must not have
// its freshly linked objects released by a sync of some other ref in between.
func TestPush_KeepsLFSLinksYoungerThanTheGraceWindow(t *testing.T) {
	f := newPushFixture(t)
	content := []byte("uploaded but not yet committed")
	seedLFSObject(t, f.harness, f.repo.ID, pointerOID, content)

	// A push that names nothing: exactly the state between the transfer and
	// the commit.
	f.push("main", addOp("README.md", "# foo\n"))

	linked, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !linked {
		t.Error("an object uploaded moments ago was unlinked before its commit could arrive")
	}
}

// Another ref still counts. A push to main must not strip the objects a
// branch or a tag names.
func TestPush_KeepsLFSLinksNamedByAnotherRef(t *testing.T) {
	noLFSLinkGrace(t)
	f := newPushFixture(t)
	content := []byte("branch only bytes")
	seedLFSObject(t, f.harness, f.repo.ID, pointerOID, content)

	f.push("experiment",
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))},
	)
	f.push("main", addOp("README.md", "# foo\n"))

	linked, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !linked {
		t.Error("a push to main released an object the experiment branch names")
	}
}

// The syncer is what stamps an object as committed, so a ref whose job has not
// run has pointers nothing has vouched for. Pruning around one would look at a
// live pointer and see an abandoned upload, so the prune waits for the queue
// to settle.
func TestPush_DoesNotReleaseLinksWhileAnotherRefIsUnindexed(t *testing.T) {
	noLFSLinkGrace(t)
	f := newPushFixture(t)
	content := []byte("bytes of an unindexed ref")
	seedLFSObject(t, f.harness, f.repo.ID, pointerOID, content)

	// The experiment branch's pointer is committed and its job queued, but
	// never processed -- a job still pending, or one parked after five
	// failures, looks the same to the prune.
	newHash, _, err := f.git.Commit(gitrepo.CommitRequest{
		Branch: "experiment", Message: "unindexed",
		Author: gitrepo.Signature{Name: "alice", Email: "alice@example.com"},
		Ops: []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "model.bin",
			Data: gitrepo.FormatLFSPointer(pointerOID, int64(len(content)))}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := f.st.EnqueueSync(f.ctx, f.repo.ID, "experiment", "", newHash.String()); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	f.push("main", addOp("README.md", "# foo\n"))

	linked, err := f.st.RepoHasLFSObject(f.ctx, f.repo.ID, pointerOID)
	if err != nil {
		t.Fatalf("RepoHasLFSObject: %v", err)
	}
	if !linked {
		t.Error("an object was released while another ref of the same repository was still waiting to be indexed")
	}
}
