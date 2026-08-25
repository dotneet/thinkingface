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
// two workers off one ref -- does not apply to it.
//
// Both halves are run: the unlocked one is what the code did before the lock
// existed, and asserting that it still loses the newer tree is what keeps this
// test honest about what it is proving.
func TestRunPushPipelineDoesNotIndexAStaleTree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		serialise bool
		wantB     bool
	}{
		{"without the ref lock the older tree wins", false, false},
		{"with the ref lock the newer tree survives", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runStaleTreeRace(t, tc.serialise)
			if !got["a.txt"] {
				t.Errorf("index = %v, want a.txt", got)
			}
			if got["b.txt"] != tc.wantB {
				t.Errorf("index = %v, want b.txt present = %v", got, tc.wantB)
			}
		})
	}
}

// runStaleTreeRace parks one pipeline mid-publish, commits a second revision
// underneath it, runs a second pipeline, and returns the index both left
// behind. With serialise the two take the ref lock the way processPush and
// FlushProject do; without it they interleave the way they used to.
func runStaleTreeRace(t *testing.T, serialise bool) map[string]bool {
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
			// The zero refHold is "no lock held", which is only reachable
			// from a test -- every production caller acquires one first.
			held := refHold{}
			if serialise {
				var err error
				if held, err = syn.refLocks.lock(h.ctx, repo.ID, "main"); err != nil {
					done <- err
					return
				}
				defer held.unlock()
			}
			_, err := syn.runPushPipeline(h.ctx, repo, &store.SyncJob{
				RepoID: repo.ID, Ref: "main", NewSHA: sha,
			}, held)
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

	// Unserialised the second pipeline runs to completion here, so the parked
	// one is guaranteed to write last. Serialised it cannot start until the
	// first releases, so the wait falls back to a timeout instead of
	// deadlocking on it.
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
	return got
}

// TestRefLocksTryLockDoesNotWait keeps the metrics flush from stalling every
// other project behind whichever repository a sync job happens to be
// publishing: flushDue walks its candidates one at a time.
func TestRefLocksTryLockDoesNotWait(t *testing.T) {
	var l refLocks
	held, err := l.lock(context.Background(), 3, "main")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	if _, ok := l.tryLock(3, "main"); ok {
		t.Error("tryLock succeeded on a ref that is already held")
	}
	// A different ref of the same repository is not contended.
	if other, ok := l.tryLock(3, "dev"); !ok {
		t.Error("tryLock failed on a free ref")
	} else {
		other.unlock()
	}

	held.unlock()
	regained, ok := l.tryLock(3, "main")
	if !ok {
		t.Fatal("tryLock failed after the holder released")
	}
	regained.unlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.locks) != 0 {
		t.Errorf("locks after every release = %d, want 0", len(l.locks))
	}
}

// TestRefLocksReleaseDropsTheEntry keeps the lock table from becoming the
// unbounded map an instance that has seen a million refs would otherwise
// carry for the rest of its life.
func TestRefLocksReleaseDropsTheEntry(t *testing.T) {
	var l refLocks
	held, err := l.lock(context.Background(), 7, "refs/heads/main")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	l.mu.Lock()
	entries := len(l.locks)
	l.mu.Unlock()
	if entries != 1 {
		t.Fatalf("locks held while locked = %d, want 1", entries)
	}

	held.unlock()
	held.unlock() // idempotent: a double release must not corrupt the table

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
	held, err := l.lock(context.Background(), 1, "main")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer held.unlock()

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
