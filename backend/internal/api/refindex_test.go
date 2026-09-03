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

// The two storage removals cannot fail the delete, and neither can run
// before the row is gone.
//
// Both halves matter and they pull in opposite directions. Failing the call
// on a `git.Remove` error answered 500 and then 404 on the retry, stranding
// the directory and the WAL prefix where nothing enumerating through the
// database would ever find them. But removing the directory *first* to avoid
// that was worse: outside TF_WAL_MODE=authoritative the WAL rebuilds nothing
// (Manager.wal is nil, so Open just opens what is on disk), and a transient
// database error would then have destroyed the repository's history while
// leaving a row that claims it exists.
func TestDeleteRepo_ReportsSuccessWhenTheGitDirectoryCannotBeRemoved(t *testing.T) {
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

	if err := f.s.deleteRepo(ctx, repo); err != nil {
		t.Fatalf("deleteRepo = %v, want nil: leftover bytes on disk are not a failed delete", err)
	}
	if _, err := f.st.GetRepoByID(ctx, repo.ID); err == nil {
		t.Fatal("the repository row survived a successful delete")
	}
}

// The mirror of the case above: when the row cannot be deleted, the git data
// has to still be there. This is the one that stops a database blip from
// taking the repository's history with it in the default (non-authoritative)
// WAL mode, where nothing can rebuild it.
func TestDeleteRepo_LeavesTheGitDirectoryWhenTheRowCannotBeDeleted(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	ctx := context.Background()
	dir := f.git.Dir(repo.StoragePath)

	// Closing the store is the cheapest way to make every statement fail the
	// way an unreachable database would.
	f.st.Close()

	if err := f.s.deleteRepo(ctx, repo); err == nil {
		t.Fatal("deleteRepo succeeded against a closed store")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("stat %s: %v -- the git data was removed even though the row delete failed, and outside authoritative WAL mode nothing rebuilds it", dir, err)
	}
}
