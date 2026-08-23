package syncer

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// pushFixture is one repository plus the plumbing to drive pushes against it.
type pushFixture struct {
	*harness
	repo *store.Repo
	git  *gitrepo.Repo
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	h := newHarness(t)
	h.user("alice")
	ns := h.namespace("alice")
	repo, err := h.st.CreateRepo(h.ctx, ns.ID, "foo", "dataset", "", "main", store.NewStoragePath())
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	if err := h.git.Init(repo.StoragePath, "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	gitRepo, err := h.git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	return &pushFixture{harness: h, repo: repo, git: gitRepo}
}

// push commits ops on branch and runs the sync job the way a real push would.
func (f *pushFixture) push(branch string, ops ...gitrepo.Op) {
	f.t.Helper()
	newHash, oldHash, err := f.git.Commit(gitrepo.CommitRequest{
		Branch: branch, Message: "sync test",
		Author: gitrepo.Signature{Name: "alice", Email: "alice@example.com"},
		Ops:    ops,
	})
	if err != nil {
		f.t.Fatalf("commit on %s: %v", branch, err)
	}
	old := ""
	if !oldHash.IsZero() {
		old = oldHash.String()
	}
	if err := f.st.EnqueueSync(f.ctx, f.repo.ID, branch, old, newHash.String()); err != nil {
		f.t.Fatalf("enqueue push: %v", err)
	}
	f.step()
}

func (f *pushFixture) blobSHA(ref, path string) string {
	f.t.Helper()
	entry, _, err := f.git.Stat(ref, path)
	if err != nil {
		f.t.Fatalf("stat %s@%s: %v", path, ref, err)
	}
	return entry.Hash.String()
}

func (f *pushFixture) mustGet(key string) []byte {
	f.t.Helper()
	rc, err := f.obj.Get(f.ctx, key)
	if err != nil {
		f.t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		f.t.Fatalf("read %s: %v", key, err)
	}
	return data
}

// TestPublishBlobs_PublishesEveryRefUnderBlobsPrefix is the core of the
// content-addressed layout: a push writes each plain file's bytes to
// blobs/{sha[0:2]}/{sha[2:4]}/{sha}, whatever ref it landed on, and writes
// nothing under any name-derived prefix.
func TestPublishBlobs_PublishesEveryRefUnderBlobsPrefix(t *testing.T) {
	f := newPushFixture(t)

	f.push("main", addOp("README.md", "# foo\n"))
	f.push("experiment", addOp("notes.txt", "scratch\n"))

	for _, tc := range []struct{ ref, path, content string }{
		{"main", "README.md", "# foo\n"},
		{"experiment", "notes.txt", "scratch\n"},
	} {
		key := storage.BlobKey(f.blobSHA(tc.ref, tc.path))
		if got := string(f.mustGet(key)); got != tc.content {
			t.Errorf("%s holds %q, want %q", key, got, tc.content)
		}
	}

	// Nothing may be keyed by namespace, repository name or ref.
	for key := range f.obj.keys() {
		if !strings.HasPrefix(key, "blobs/") && !strings.HasPrefix(key, "lfs/") {
			t.Errorf("push wrote %s, want only blobs/ and lfs/ keys", key)
		}
		if strings.Contains(key, "alice") || strings.Contains(key, "/foo/") {
			t.Errorf("storage key %s carries the repository's identity", key)
		}
	}
}

// TestPublishBlobs_LeavesLFSObjectsWhereTheyAre: an LFS pointer's bytes are
// already at their content-addressed key, put there by whichever upload path
// produced them. The push must not copy them anywhere, and must not publish
// the pointer text as if it were the file.
func TestPublishBlobs_LeavesLFSObjectsWhereTheyAre(t *testing.T) {
	f := newPushFixture(t)

	const oid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	content := []byte("large file bytes")
	if err := f.obj.Put(f.ctx, storage.LFSKey(oid), bytes.NewReader(content), "application/octet-stream"); err != nil {
		t.Fatalf("seed lfs object: %v", err)
	}
	if err := f.st.RecordLFSObject(f.ctx, f.repo.ID, oid, int64(len(content)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	f.push("main",
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: ".gitattributes", Data: []byte(gitrepo.DefaultGitAttributes)},
		gitrepo.Op{Kind: gitrepo.OpAdd, Path: "model.bin", Data: gitrepo.FormatLFSPointer(oid, int64(len(content)))},
	)

	if got := string(f.mustGet(storage.LFSKey(oid))); got != string(content) {
		t.Errorf("lfs object = %q, want %q", got, content)
	}
	// The pointer blob itself must not be published: it is not the file.
	pointerKey := storage.BlobKey(f.blobSHA("main", "model.bin"))
	if _, err := f.obj.Stat(f.ctx, pointerKey); err == nil {
		t.Errorf("the LFS pointer text was published at %s", pointerKey)
	}
	// The plain file on the same commit still is.
	if _, err := f.obj.Stat(f.ctx, storage.BlobKey(f.blobSHA("main", ".gitattributes"))); err != nil {
		t.Errorf(".gitattributes was not published: %v", err)
	}
}

// TestPublishBlobs_DeletingAFileLeavesTheBlob pins the layer's other half:
// publishing only ever adds. A blob may be shared with any number of other
// repositories, so a push that drops a path must not remove its bytes --
// `thinkingface gc` is the only thing that ever deletes one.
func TestPublishBlobs_DeletingAFileLeavesTheBlob(t *testing.T) {
	f := newPushFixture(t)
	f.push("main", addOp("README.md", "# foo\n"), addOp("doomed.txt", "bye\n"))
	doomed := storage.BlobKey(f.blobSHA("main", "doomed.txt"))

	f.push("main", gitrepo.Op{Kind: gitrepo.OpDelete, Path: "doomed.txt"})

	if _, _, err := f.git.Stat("main", "doomed.txt"); err == nil {
		t.Fatal("doomed.txt still in the tree; the test setup is wrong")
	}
	if _, err := f.obj.Stat(f.ctx, doomed); err != nil {
		t.Errorf("deleting a path removed the shared blob %s: %v", doomed, err)
	}
}

// TestPublishBlobs_RepeatedPushIsIdempotent: re-publishing content that is
// already there must be a no-op rather than an overwrite, which is what makes
// a retried job free.
func TestPublishBlobs_RepeatedPushIsIdempotent(t *testing.T) {
	f := newPushFixture(t)
	f.push("main", addOp("README.md", "# foo\n"))
	key := storage.BlobKey(f.blobSHA("main", "README.md"))

	// Overwrite the object with a marker the push would clobber if it wrote
	// again, then force a full pass (no old SHA, so changedPaths falls back
	// to the whole tree).
	if err := f.obj.Put(f.ctx, key, strings.NewReader("sentinel"), ""); err != nil {
		t.Fatalf("overwrite blob: %v", err)
	}
	if err := f.st.EnqueueSync(f.ctx, f.repo.ID, "main", "", ""); err != nil {
		t.Fatalf("enqueue full pass: %v", err)
	}
	f.step()

	if got := string(f.mustGet(key)); got != "sentinel" {
		t.Errorf("existing blob was rewritten (%q), want the Stat shortcut to skip it", got)
	}
}

// TestProcess_UnknownJobKindFails guards the process() dispatch: a kind other
// than "push"/"" must fail loudly rather than silently running the wrong
// pipeline.
func TestProcess_UnknownJobKindFails(t *testing.T) {
	h := newHarness(t)
	h.user("alice")
	ns := h.namespace("alice")
	repo, err := h.st.CreateRepo(h.ctx, ns.ID, "foo", "dataset", "desc", "main", store.NewStoragePath())
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}

	if err := h.syn.process(h.ctx, &store.SyncJob{RepoID: repo.ID, Kind: "bogus"}); err == nil {
		t.Fatal("process with unknown kind: want error, got nil")
	}
}

// TestPublishBlobs_CoversFilesALostJobSkipped is the hole a per-push diff
// would leave: the job for one push dies (or two jobs for the ref race) and a
// later push's job, whose own old..new range does not include the earlier
// files, is the one that writes the index. Publishing is decided against the
// previous index rather than the job's diff, so that later job still
// publishes everything the index did not already cover.
func TestPublishBlobs_CoversFilesALostJobSkipped(t *testing.T) {
	f := newPushFixture(t)
	f.push("main", addOp("README.md", "# foo\n"))

	// Two more commits, of which only the second ever gets a sync job -- the
	// first one's job is the one that was lost.
	commit := func(ops ...gitrepo.Op) string {
		f.t.Helper()
		h, _, err := f.git.Commit(gitrepo.CommitRequest{
			Branch: "main", Message: "sync test",
			Author: gitrepo.Signature{Name: "alice", Email: "alice@example.com"},
			Ops:    ops,
		})
		if err != nil {
			f.t.Fatalf("commit: %v", err)
		}
		return h.String()
	}
	lost := commit(addOp("skipped.txt", "never in a diff the worker saw\n"))
	head := commit(addOp("seen.txt", "in the job's diff\n"))
	if err := f.st.EnqueueSync(f.ctx, f.repo.ID, "main", lost, head); err != nil {
		f.t.Fatalf("enqueue push: %v", err)
	}
	f.step()

	for _, path := range []string{"README.md", "skipped.txt", "seen.txt"} {
		key := storage.BlobKey(f.blobSHA("main", path))
		if _, err := f.obj.Stat(f.ctx, key); err != nil {
			t.Errorf("%s (%s) is not published after the later job: %v", path, key, err)
		}
	}
}
