package syncer

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// blockingStorage parks the very first Put and lets every later one through,
// which is enough to hold one pipeline open in the middle of publishBlobs
// while the test drives a second one.
type blockingStorage struct {
	*memStorage
	parked  atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func newBlockingStorage() *blockingStorage {
	return &blockingStorage{
		memStorage: newMemStorage(),
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (b *blockingStorage) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	// CompareAndSwap rather than sync.Once: Once makes later callers wait for
	// the first call to return, which would park the second pipeline here
	// instead of where the test wants to observe it.
	if b.parked.CompareAndSwap(false, true) {
		close(b.entered)
		<-b.release
	}
	return b.memStorage.Put(ctx, key, r, contentType)
}

// TestRunPushPipelineDoesNotIndexAStaleTree pins the hazard refLocks exists
// for: the pipeline reads the tree the ref points at now and finishes by
// replacing the whole index for that ref, so a pipeline that read an older
// tree must never be allowed to write after one that read a newer tree.
//
// The metrics flush (flush.go) runs this same pipeline without holding a
// sync_jobs row, so ClaimSyncJob's NOT EXISTS clause -- which is what keeps
// two workers off one ref -- does not apply to it. Before the ref lock, a
// flush landing inside a worker's pipeline was overwritten by it and the
// freshly committed parquet dropped straight back out of repo_files.
func TestRunPushPipelineDoesNotIndexAStaleTree(t *testing.T) {
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

	obj := newBlockingStorage()
	parquet := viewer.New(obj, 8<<20)
	syn := New(h.st, h.git, obj, parquet, experiments.NewIndexer(h.st, h.git, obj, parquet), nil, 1)

	commit := func(ops ...gitrepo.Op) string {
		t.Helper()
		newHash, _, err := gitRepo.Commit(gitrepo.CommitRequest{
			Branch: "main", Message: "reflock test",
			Author: gitrepo.Signature{Name: "alice", Email: "alice@example.com"},
			Ops:    ops,
		})
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		return newHash.String()
	}
	run := func(sha string) chan error {
		done := make(chan error, 1)
		go func() {
			_, err := syn.runPushPipeline(h.ctx, repo, &store.SyncJob{
				RepoID: repo.ID, Ref: "main", NewSHA: sha,
			})
			done <- err
		}()
		return done
	}

	// The older revision, indexed by a pipeline that we park mid-publish.
	older := commit(addOp("a.txt", "a"))
	first := run(older)
	<-obj.entered

	// The newer revision lands while that pipeline is parked, and a second
	// pipeline picks it up -- the flush's shape exactly.
	newer := commit(addOp("b.txt", "b"))
	second := run(newer)

	// Without the lock the second pipeline runs to completion here, so the
	// parked one is guaranteed to write last and lose b.txt. With the lock it
	// cannot start until the first releases, so the wait has to fall back to
	// a timeout instead of deadlocking on it.
	secondErr := make(chan error, 1)
	select {
	case err := <-second:
		secondErr <- err
	case <-time.After(500 * time.Millisecond):
		go func() { secondErr <- <-second }()
	}

	close(obj.release)
	if err := <-first; err != nil {
		t.Fatalf("first pipeline: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second pipeline: %v", err)
	}

	files, err := h.st.ListRepoFiles(h.ctx, repo.ID, "main")
	if err != nil {
		t.Fatalf("list repo files: %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Path] = true
	}
	if !got["a.txt"] || !got["b.txt"] {
		t.Errorf("index = %v, want both a.txt and b.txt; the pipeline that read the older tree overwrote the one that read %s", got, newer)
	}
}

// TestRefLocksReleaseDropsTheEntry keeps the lock table from becoming the
// unbounded map an instance that has seen a million refs would otherwise
// carry for the rest of its life.
func TestRefLocksReleaseDropsTheEntry(t *testing.T) {
	var l refLocks
	release, err := l.lock(context.Background(), 7, "refs/heads/main")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	l.mu.Lock()
	held := len(l.locks)
	l.mu.Unlock()
	if held != 1 {
		t.Fatalf("locks held while locked = %d, want 1", held)
	}

	release()
	release() // idempotent: a double release must not corrupt the table

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.locks) != 0 {
		t.Errorf("locks after release = %d, want 0", len(l.locks))
	}
}

// TestRefLocksLockRespectsContext keeps a shutdown from having to sit behind
// a large repository's publish.
func TestRefLocksLockRespectsContext(t *testing.T) {
	var l refLocks
	release, err := l.lock(context.Background(), 1, "main")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.lock(ctx, 1, "main"); err != context.Canceled {
		t.Errorf("lock on a cancelled context = %v, want context.Canceled", err)
	}

	// The abandoned waiter must not leave its entry behind either.
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.locks) != 1 {
		t.Errorf("locks after a cancelled wait = %d, want 1 (only the holder's)", len(l.locks))
	}
}
