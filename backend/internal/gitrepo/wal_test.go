package gitrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// mkRepoDir plants a fake repository directory with size bytes of payload,
// optionally WAL-managed (a plausible state file with generation > 0).
func mkRepoDir(t *testing.T, root, kindDir, ns, name string, size int, managed bool) string {
	t.Helper()
	dir := filepath.Join(root, kindDir, ns, name+".git")
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if managed {
		state, _ := json.Marshal(map[string]any{"generation": 42, "base": "", "applied": []string{}, "refs": map[string]string{}, "seq": 0})
		if err := os.WriteFile(filepath.Join(dir, wal.StateFileName), state, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func evictionManager(t *testing.T, cacheBytes int64) *Manager {
	t.Helper()
	m := NewManager(t.TempDir())
	m.EnableWAL(nil, cacheBytes) // maybeEvict never touches the store
	return m
}

func TestMaybeEvict_LegacyDirsAreNeitherBudgetedNorRemoved(t *testing.T) {
	m := evictionManager(t, 1024)
	// A legacy (pre-WAL) directory far over budget on its own: without the
	// evictable-only budget it would doom every managed repository (H-3-1).
	legacy := mkRepoDir(t, m.root, "datasets", "acme", "legacy", 8192, false)
	managed := mkRepoDir(t, m.root, "models", "acme", "small", 128, true)
	m.wal.lastUse[managed] = time.Now().Add(-24 * time.Hour)

	m.maybeEvict()

	for _, dir := range []string{legacy, managed} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s was evicted; budget must count evictable dirs only", dir)
		}
	}
}

func TestMaybeEvict_RecentlyUsedSurvivesEvenOverBudget(t *testing.T) {
	m := evictionManager(t, 1024)
	// A single repository larger than the whole budget, used just now: the
	// idle window is what stops it evicting itself right after its own
	// materialisation (H-3-2).
	big := mkRepoDir(t, m.root, "models", "acme", "big", 4096, true)
	m.wal.lastUse[big] = time.Now()

	m.maybeEvict()

	if _, err := os.Stat(big); err != nil {
		t.Fatal("a just-used repository must never be evicted")
	}
}

func TestMaybeEvict_IdleOldestGoFirstUntilUnderBudget(t *testing.T) {
	m := evictionManager(t, 3000)
	oldest := mkRepoDir(t, m.root, "models", "acme", "oldest", 2048, true)
	middle := mkRepoDir(t, m.root, "models", "acme", "middle", 2048, true)
	newest := mkRepoDir(t, m.root, "models", "acme", "newest", 2048, true)
	m.wal.lastUse[oldest] = time.Now().Add(-3 * time.Hour)
	m.wal.lastUse[middle] = time.Now().Add(-2 * time.Hour)
	m.wal.lastUse[newest] = time.Now().Add(-1 * time.Hour)

	m.maybeEvict()

	if _, err := os.Stat(oldest); err == nil {
		t.Fatal("oldest idle repository should have been evicted")
	}
	if _, err := os.Stat(middle); err == nil {
		t.Fatal("6KiB over a 3000B budget needs two evictions")
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatal("newest must survive once the budget is met")
	}
}

func TestMaybeEvict_LockedRepositoryIsSkipped(t *testing.T) {
	m := evictionManager(t, 1024)
	busy := mkRepoDir(t, m.root, "models", "acme", "busy", 4096, true)
	m.wal.lastUse[busy] = time.Now().Add(-24 * time.Hour)
	lock := m.lockFor(busy)
	lock.Lock()
	defer lock.Unlock()

	m.maybeEvict()

	if _, err := os.Stat(busy); err != nil {
		t.Fatal("a locked repository must never be evicted")
	}
}

// TestMaybeEvict_FindsBothLegacyAndNewStoragePathShapes exercises the walker
// added for the storage_path migration (docs/dev/repo-transfer-design.md §8):
// eviction must treat {root}/repos/{ulid}.git (two levels) exactly like
// {root}/{models|datasets}/{ns}/{name}.git (three levels) — any directory
// ending in ".git", wherever it sits under root.
func TestMaybeEvict_FindsBothLegacyAndNewStoragePathShapes(t *testing.T) {
	m := evictionManager(t, 1024)
	legacyManaged := mkRepoDir(t, m.root, "models", "acme", "small", 128, true)
	m.wal.lastUse[legacyManaged] = time.Now().Add(-24 * time.Hour)

	newDir := filepath.Join(m.root, "repos", "01JAV0NEWSTORAGEPATH.git")
	if err := os.MkdirAll(filepath.Join(newDir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "payload"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]any{"generation": 42, "base": "", "applied": []string{}, "refs": map[string]string{}, "seq": 0})
	if err := os.WriteFile(filepath.Join(newDir, wal.StateFileName), state, 0o644); err != nil {
		t.Fatal(err)
	}
	m.wal.lastUse[newDir] = time.Now().Add(-24 * time.Hour)

	m.maybeEvict()

	if _, err := os.Stat(newDir); err == nil {
		t.Fatal("repos/{ulid}.git (new storage_path shape) was not found by the walker and so was never evicted")
	}
}

func TestMaybeEvict_ScanIntervalThrottles(t *testing.T) {
	m := evictionManager(t, 1024)
	victim := mkRepoDir(t, m.root, "models", "acme", "victim", 4096, true)
	m.wal.lastUse[victim] = time.Now().Add(-24 * time.Hour)
	m.wal.lastScan = time.Now() // a scan just happened

	m.maybeEvict()

	if _, err := os.Stat(victim); err != nil {
		t.Fatal("within the scan interval maybeEvict must be a no-op")
	}
}

// indexOnlyStore serves exactly one WAL index object — the minimum Storage
// for exercising EnsureLocal's cache-hit path without an emulator.
type indexOnlyStore struct {
	key  string
	body []byte
	gen  int64
	// gate, when non-nil, blocks GetWithGeneration until closed — the test's
	// handle on "EnsureLocal is holding the repo lock right now".
	gate chan struct{}
}

func (s *indexOnlyStore) SupportsSignedURL() bool { return false }
func (s *indexOnlyStore) SignedGetURL(context.Context, string, time.Duration, string) (string, error) {
	return "", errors.New("unused")
}
func (s *indexOnlyStore) SignedPutURL(context.Context, string, time.Duration, int64) (string, error) {
	return "", errors.New("unused")
}
func (s *indexOnlyStore) Put(context.Context, string, io.Reader, string) error {
	return errors.New("unused")
}
func (s *indexOnlyStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, _, err := s.GetWithGeneration(ctx, key)
	return rc, err
}
func (s *indexOnlyStore) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if gate := s.gate; gate != nil {
		<-gate // hold the caller (and the repo lock it took) until the test says go
	}
	if key != s.key {
		return nil, 0, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(s.body)), s.gen, nil
}
func (s *indexOnlyStore) PutIfGeneration(context.Context, string, int64, io.Reader, string) (int64, error) {
	return 0, errors.New("unused")
}
func (s *indexOnlyStore) GetRange(context.Context, string, int64, int64) (io.ReadCloser, error) {
	return nil, errors.New("unused")
}
func (s *indexOnlyStore) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, storage.ErrNotFound
}
func (s *indexOnlyStore) Copy(context.Context, string, string) error { return errors.New("unused") }
func (s *indexOnlyStore) Delete(context.Context, string) error       { return errors.New("unused") }
func (s *indexOnlyStore) List(context.Context, string) ([]storage.ObjectInfo, error) {
	return nil, nil
}
func (s *indexOnlyStore) PublicURI(key string) string { return "mem://" + key }

var _ storage.Storage = (*indexOnlyStore)(nil)

// walManagedRepo plants a bare repository whose state file matches the
// store's index at generation gen — EnsureLocal takes the cache-hit path.
func walManagedRepo(t *testing.T, m *Manager, st *indexOnlyStore, storagePath string) string {
	t.Helper()
	dir := m.Dir(storagePath)
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	st.key = storage.WALIndexKey(storagePath)
	st.body = []byte(`{"version":1,"seq":0,"base":"","entries":[],"refs":{}}`)
	st.gen = 42
	state := []byte(`{"generation":42,"base":"","applied":[],"refs":{},"seq":0}`)
	if err := os.WriteFile(filepath.Join(dir, wal.StateFileName), state, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The Bugbot regression: right after a restart lastUse is empty and the
// state-file mtime may be arbitrarily old, so eviction must only ever see a
// directory (its TryLock only succeeds after EnsureLocal's unlock) with the
// fresh stamp already in place. A cache-hit EnsureLocal therefore has to
// stamp lastUse — before releasing the lock — even though it did no work.
func TestEnsureLocal_CacheHitStampsLastUseForEviction(t *testing.T) {
	m := NewManager(t.TempDir())
	st := &indexOnlyStore{}
	m.EnableWAL(st, 1024)
	dir := walManagedRepo(t, m, st, "datasets/acme/widgets")

	// Simulate the post-restart state: no in-memory stamp, ancient mtime.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, wal.StateFileName), old, old); err != nil {
		t.Fatal(err)
	}

	if err := m.EnsureLocal(context.Background(), "datasets/acme/widgets"); err != nil {
		t.Fatalf("EnsureLocal: %v", err)
	}

	m.wal.mu.Lock()
	used, ok := m.wal.lastUse[dir]
	m.wal.mu.Unlock()
	if !ok || time.Since(used) > time.Minute {
		t.Fatalf("lastUse not stamped on cache hit (ok=%v used=%v): eviction would treat the directory as idle", ok, used)
	}
}

// Stress the exact interleaving: one goroutine ensures repo A, another keeps
// evicting under pressure from an idle repo B. A successful EnsureLocal must
// always leave A's directory in place for the caller that just got it.
func TestEnsureLocal_SurvivesConcurrentEviction(t *testing.T) {
	m := NewManager(t.TempDir())
	st := &indexOnlyStore{}
	m.EnableWAL(st, 512) // tiny budget: B alone exceeds it
	dirA := walManagedRepo(t, m, st, "datasets/acme/widgets")

	// B: idle, evictable, big enough to keep eviction hungry. Its index key
	// differs from A's, but eviction never reads the store, so that is fine.
	dirB := filepath.Join(m.root, "models", "acme", "filler.git")
	if err := os.MkdirAll(filepath.Join(dirB, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "payload"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, wal.StateFileName),
		[]byte(`{"generation":7,"base":"","applied":[],"refs":{},"seq":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	oldT := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(dirB, wal.StateFileName), oldT, oldT)

	// The evictor runs until the loop below is finished rather than for a
	// fixed number of rounds: maybeEvict is microseconds, so a counted loop
	// drains before the first EnsureLocal even starts and the two never
	// overlap at all (this test used to report the race happening in 0 of 300
	// rounds).
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			m.wal.mu.Lock()
			m.wal.lastScan = time.Time{} // defeat the scan throttle
			m.wal.mu.Unlock()
			m.maybeEvict()
		}
	}()

	for i := 0; i < 300; i++ {
		// Strip both of A's protections -- the aged state file and the
		// in-memory stamp -- *before* the call, so the only thing that can
		// keep the directory alive is the stamp EnsureLocal itself writes.
		//
		// Dropping the stamp from the evicting goroutine instead would race
		// the assertion below rather than this call: a delete landing between
		// EnsureLocal's return and the Stat leaves A looking 48 hours idle
		// again, and evicting it there is the documented, accepted gap (see
		// evictMinIdle and continuity-design §16), not the invariant under
		// test. That is what made this test fail on CI while passing locally.
		_ = os.Chtimes(filepath.Join(dirA, wal.StateFileName), oldT, oldT)
		m.wal.mu.Lock()
		delete(m.wal.lastUse, dirA)
		m.wal.mu.Unlock()

		if err := m.EnsureLocal(context.Background(), "datasets/acme/widgets"); err != nil {
			t.Fatalf("EnsureLocal round %d: %v", i, err)
		}
		if _, err := os.Stat(dirA); err != nil {
			t.Fatalf("round %d: repository evicted right after a successful EnsureLocal", i)
		}
	}
	close(stop)
	<-done
	// Note for anyone reading a green run: whether the evictor ever lands
	// inside the unstamped window is a scheduling accident, so this test
	// passing is not proof the interleaving occurred. The deterministic half
	// of this invariant is TestEnsureLocal_StampVisibleBeforeLockRelease
	// below, which forces the ordering with a gate instead of racing for it.
}

// The sharp version of the Bugbot regression: the stamp must be visible the
// instant the repo lock is released, because that instant is the earliest a
// concurrent maybeEvict's TryLock can succeed. The gate parks EnsureLocal
// inside Materialize while holding the lock; a spinning watcher then races
// the unlock itself. With the stamp inside the lock the watcher can never
// observe "lock free, stamp missing"; with the pre-fix ordering it can.
func TestEnsureLocal_StampVisibleBeforeLockRelease(t *testing.T) {
	m := NewManager(t.TempDir())
	st := &indexOnlyStore{}
	m.EnableWAL(st, 1024)
	dir := walManagedRepo(t, m, st, "datasets/acme/widgets")
	lock := m.lockFor(dir)

	const rounds = 400
	for i := 0; i < rounds; i++ {
		m.wal.mu.Lock()
		delete(m.wal.lastUse, dir) // restart amnesia
		m.wal.mu.Unlock()

		gate := make(chan struct{})
		st.gate = gate
		done := make(chan error, 1)
		go func() {
			done <- m.EnsureLocal(context.Background(), "datasets/acme/widgets")
		}()

		// Wait until EnsureLocal provably holds the lock (parked at the gate).
		for lock.TryLock() {
			lock.Unlock()
		}

		violation := make(chan bool, 1)
		go func() {
			for {
				if lock.TryLock() {
					m.wal.mu.Lock()
					_, stamped := m.wal.lastUse[dir]
					m.wal.mu.Unlock()
					lock.Unlock()
					violation <- !stamped
					return
				}
			}
		}()

		close(gate)
		if err := <-done; err != nil {
			t.Fatalf("EnsureLocal: %v", err)
		}
		if <-violation {
			t.Fatalf("round %d: lock observed free before lastUse was stamped — eviction could delete the directory", i)
		}
		st.gate = nil
	}
}
