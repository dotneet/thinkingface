// A branch that is deleted has to take its cached index with it. Nothing did
// that before: store.ReplaceRepoFiles is only ever reached from a sync job,
// and a sync job is only ever enqueued for a ref that still exists, so the
// rows of a deleted branch survived for the life of the repository. They kept
// answering ListRepoFiles for a branch that was gone, and -- the expensive
// half -- kept their blobs inside ListReferencedBlobSHAs, so `thinkingface
// gc` could never reclaim content whose last living ref had been deleted.
//
// Both deletion paths are covered here, because they are separate code:
// handleHFDeleteBranch over HTTP, and schedulePostPush for `git push
// --delete`, which cannot be driven over httptest (it needs receive-pack) and
// is called directly instead.

package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

const indexedSHA = "1111111111111111111111111111111111111111"

// indexRef writes the rows a sync job would have written for ref.
func indexRef(t *testing.T, st *store.Store, repoID int64, ref, sha string) {
	t.Helper()
	ctx := context.Background()
	if err := st.ReplaceRepoFiles(ctx, repoID, ref,
		[]store.RepoFile{{Path: "data.parquet", Size: 4, BlobSHA: sha}}); err != nil {
		t.Fatalf("index %s: %v", ref, err)
	}
	if err := st.UpsertParquetFile(ctx, repoID, ref, "data.parquet", 1, 1,
		json.RawMessage(`[{"name":"a"}]`)); err != nil {
		t.Fatalf("index parquet on %s: %v", ref, err)
	}
}

func assertRefIndexEmpty(t *testing.T, st *store.Store, repoID int64, ref string) {
	t.Helper()
	ctx := context.Background()
	files, err := st.ListRepoFiles(ctx, repoID, ref)
	if err != nil {
		t.Fatalf("list %s files: %v", ref, err)
	}
	if len(files) != 0 {
		t.Errorf("%s still lists %d files after the branch was deleted", ref, len(files))
	}
	parquet, err := st.ListParquetFiles(ctx, repoID, ref)
	if err != nil {
		t.Fatalf("list %s parquet: %v", ref, err)
	}
	if len(parquet) != 0 {
		t.Errorf("%s still lists %d parquet files after the branch was deleted", ref, len(parquet))
	}
	referenced, err := st.ListReferencedBlobSHAs(ctx)
	if err != nil {
		t.Fatalf("list referenced blob shas: %v", err)
	}
	if referenced[indexedSHA] {
		t.Error("the deleted branch's blob still counts as referenced, so gc can never reclaim it")
	}
}

func TestHFDeleteBranch_DropsTheBranchesFileIndex(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	if resp := f.do("POST", "/api/models/alice/foo/branch/feature", tok, map[string]any{}); resp.status() != 201 {
		t.Fatalf("create branch status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	indexRef(t, f.st, repo.ID, "feature", indexedSHA)

	if resp := f.do("DELETE", "/api/models/alice/foo/branch/feature", tok, nil); resp.status() != 200 {
		t.Fatalf("delete branch status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	assertRefIndexEmpty(t, f.st, repo.ID, "feature")

	// main is untouched: the delete is scoped to the ref that went away.
	files, err := f.st.ListRepoFiles(context.Background(), repo.ID, "main")
	if err != nil {
		t.Fatalf("list main files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("fixture changed: main has %d indexed files", len(files))
	}
}

// `git push --delete feature`. schedulePostPush is the one place that sees a
// ref present in `before` and absent from `after` -- HeadsAfterPush is
// branches only, and no sync job is scheduled for a branch that is gone, so
// this loop was the last chance to notice.
func TestSchedulePostPush_DropsTheIndexOfABranchThePushDeleted(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	indexRef(t, f.st, repo.ID, "feature", indexedSHA)

	before := map[string]string{"main": "aaaa", "feature": "bbbb"}
	after := map[string]string{"main": "aaaa"}
	f.s.schedulePostPush(context.Background(), repo, before, after, "push")

	assertRefIndexEmpty(t, f.st, repo.ID, "feature")
}

// A branch that only *moved* keeps its index: the sync job the push schedules
// is what refreshes it, and dropping the rows here would leave the repository
// with no listing at all until that job ran.
func TestSchedulePostPush_KeepsTheIndexOfABranchThatOnlyMoved(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	indexRef(t, f.st, repo.ID, "feature", indexedSHA)

	f.s.schedulePostPush(context.Background(),
		repo,
		map[string]string{"feature": "bbbb"},
		map[string]string{"feature": "cccc"},
		"push")

	files, err := f.st.ListRepoFiles(context.Background(), repo.ID, "feature")
	if err != nil {
		t.Fatalf("list feature files: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("feature lists %d files after a plain push, want its index left alone", len(files))
	}
}

// Deleting a *tag* must not touch the file index, and that is not an
// oversight: repo_files.ref holds a branch short name, and one short name can
// be a branch and a tag at once, so a tag delete that removed rows by name
// would take the identically named branch's listing with it.
func TestHFDeleteTag_LeavesTheIdenticallyNamedBranchesIndexAlone(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	if resp := f.do("POST", "/api/models/alice/foo/branch/v1", tok, map[string]any{}); resp.status() != 201 {
		t.Fatalf("create branch status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if resp := f.do("POST", "/api/models/alice/foo/tag/main", tok, map[string]any{"tag": "v1"}); resp.status() != 201 {
		t.Fatalf("create tag status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	indexRef(t, f.st, repo.ID, "v1", indexedSHA)

	if resp := f.do("DELETE", "/api/models/alice/foo/tag/v1", tok, nil); resp.status() != 200 {
		t.Fatalf("delete tag status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	files, err := f.st.ListRepoFiles(context.Background(), repo.ID, "v1")
	if err != nil {
		t.Fatalf("list v1 files: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("branch v1 lists %d files after the tag v1 was deleted, want 1", len(files))
	}
}

// ------------------------------------------------------------ repo teardown

// deleteRepo removed the database row before the bare repository and the WAL
// prefix, which made a failure in either of those unrecoverable: the request
// answered 500, the retry answered 404, and the bytes stayed on disk and in
// the bucket for good -- invisible to `thinkingface gc` and to wal compaction
// alike, since both enumerate repositories through the database.
//
// The local copy goes first now, because it is the only one of the three that
// is safe to lose: it is a cache the WAL rebuilds. So a failure there leaves a
// repository that is still entirely deletable.
func TestDeleteRepo_KeepsTheRowWhenTheGitDirectoryCannotBeRemoved(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	ctx := context.Background()

	// Make the containing directory read-only, so unlinking the repository
	// out of it fails the way a permissions or I/O fault would.
	parent := filepath.Dir(f.git.Dir(repo.StoragePath))
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Skipf("cannot make %s read-only: %v", parent, err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	err := f.s.deleteRepo(ctx, repo)
	if err == nil {
		t.Skip("this filesystem allows removing entries from a read-only directory")
	}

	// The row is what makes the retry a delete rather than a 404 -- and what
	// keeps the storage path enumerable by gc and by wal compaction until it
	// really is gone.
	if _, err := f.st.GetRepoByID(ctx, repo.ID); err != nil {
		t.Fatalf("the repository row is gone after a failed delete (%v), so nothing can find its git directory or WAL prefix again", err)
	}
}
