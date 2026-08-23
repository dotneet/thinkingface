package gitrepo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// walBackend is set once at startup when the WAL is authoritative
// (docs/dev/continuity-design.md §15 Phase 4+). While it is nil — WAL off or
// shadow — the manager behaves exactly as before: the on-disk repositories
// are the truth and nothing here runs.
type walBackend struct {
	store      storage.Storage
	cacheBytes int64

	mu       sync.Mutex
	lastUse  map[string]time.Time // dir → last EnsureLocal
	lastScan time.Time
}

// materializeTimeout bounds one catch-up. Independent of the request context
// on purpose: a half-materialised copy is recoverable but wasteful, so once
// started, a catch-up should finish even if the triggering client goes away.
const materializeTimeout = 5 * time.Minute

// evictScanInterval limits how often the cache walks the disk to measure
// itself; between scans, eviction bookkeeping is purely in-memory.
const evictScanInterval = 30 * time.Second

// evictMinIdle keeps recently used repositories out of eviction's reach. It
// is the practical guard against deleting a directory an in-flight
// upload-pack or streaming read is still walking: every request bumps
// lastUse via EnsureLocal at its start, and with LFS keeping repositories
// tiny (§16-3: ≤ hundreds of KiB) no transfer runs anywhere near this long.
// A full reader/writer lock shared with the smart-HTTP exec paths is the
// complete fix; until then this window plus the per-repo lock TryLock is
// the mitigation (reviewed and accepted as such — see continuity-design §16).
const evictMinIdle = 10 * time.Minute

// EnableWAL makes the manager materialise repositories from the WAL before
// use and bound the on-disk cache. Call once, before the manager is shared.
func (m *Manager) EnableWAL(st storage.Storage, cacheBytes int64) {
	m.wal = &walBackend{store: st, cacheBytes: cacheBytes, lastUse: map[string]time.Time{}}
}

// EnsureLocal brings the local copy of one repository up to the current WAL
// index (§9), under the same per-repository lock every write path uses. A
// no-op unless EnableWAL was called. storagePath is the repository's
// immutable physical location (store.Repo.StoragePath), not its name.
func (m *Manager) EnsureLocal(ctx context.Context, storagePath string) error {
	if m.wal == nil {
		return nil
	}
	// Detach from the caller's cancellation but keep a hard upper bound.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), materializeTimeout)
	defer cancel()

	dir := m.Dir(storagePath)
	lock := m.lockFor(dir)
	lock.Lock()
	err := wal.Materialize(mctx, m.wal.store, dir, storagePath)
	if err == nil {
		// Stamp before releasing the lock. Eviction can only look at this
		// directory once its TryLock succeeds — i.e. after this unlock — and
		// by then it must already see a fresh lastUse. Stamping after the
		// unlock leaves a window in which a stale state-file mtime (or an
		// empty lastUse map right after a restart) makes the directory look
		// idle, and a concurrent maybeEvict deletes what this caller is
		// about to open.
		m.wal.mu.Lock()
		m.wal.lastUse[dir] = time.Now()
		m.wal.mu.Unlock()
	}
	lock.Unlock()
	if err != nil {
		return fmt.Errorf("materialize %s: %w", storagePath, err)
	}
	m.maybeEvict()
	return nil
}

// AdoptLocal runs wal.AdoptIfConverged under the same per-repository lock
// Materialize uses: both write the local state file, and the documented
// contract of that file is single-writer (materialize.go).
func (m *Manager) AdoptLocal(ctx context.Context, storagePath string) error {
	if m.wal == nil {
		return nil
	}
	dir := m.Dir(storagePath)
	lock := m.lockFor(dir)
	lock.Lock()
	defer lock.Unlock()
	return wal.AdoptIfConverged(ctx, m.wal.store, dir, storagePath)
}

// maybeEvict keeps the materialised cache under its byte budget. It walks the
// git root at most once per evictScanInterval, and only ever removes
// directories that are (a) WAL-backed — a state file proves the WAL can
// rebuild them — (b) idle for at least evictMinIdle, and (c) not currently
// locked. Repositories that predate the WAL (no state file) are never
// touched: deleting one would destroy data the WAL cannot restore — and for
// the same reason they are excluded from the budget itself, or a large
// legacy population would make the target unreachable and every scan would
// pointlessly flush the entire evictable cache.
func (m *Manager) maybeEvict() {
	w := m.wal
	if w == nil || w.cacheBytes <= 0 {
		return
	}
	w.mu.Lock()
	if time.Since(w.lastScan) < evictScanInterval {
		w.mu.Unlock()
		return
	}
	w.lastScan = time.Now()
	w.mu.Unlock()

	type repoDir struct {
		dir  string
		size int64
		used time.Time
	}
	var repos []repoDir
	var evictableTotal int64
	now := time.Now()
	// A repository directory is any directory whose name ends in ".git",
	// wherever it sits under root — {root}/repos/{ulid}.git (new) and
	// {root}/{models|datasets}/{ns}/{name}.git (legacy) alike
	// (docs/dev/repo-transfer-design.md §8). Never descend into one: nothing
	// inside a bare repository's own directory tree ends in ".git" in a way
	// that should be treated as a nested repository.
	_ = filepath.WalkDir(m.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort scan; skip unreadable entries
		}
		if path == m.root || !d.IsDir() || !strings.HasSuffix(d.Name(), ".git") {
			return nil
		}
		dir := path
		if wal.LocalGeneration(dir) == 0 {
			// not WAL-backed: never evictable, never budgeted
			return filepath.SkipDir
		}
		size := dirSize(dir)
		evictableTotal += size
		w.mu.Lock()
		used, ok := w.lastUse[dir]
		w.mu.Unlock()
		if !ok {
			// Fall back to the state file's mtime: survives restarts.
			if st, err := os.Stat(filepath.Join(dir, wal.StateFileName)); err == nil {
				used = st.ModTime()
			}
		}
		repos = append(repos, repoDir{dir: dir, size: size, used: used})
		return filepath.SkipDir
	})
	if evictableTotal <= w.cacheBytes {
		return
	}

	sort.Slice(repos, func(i, j int) bool { return repos[i].used.Before(repos[j].used) })
	for _, r := range repos {
		if evictableTotal <= w.cacheBytes {
			return
		}
		if now.Sub(r.used) < evictMinIdle {
			// Candidates are oldest-first: once one is inside the idle
			// window, so is everything after it. Also what stops a repository
			// larger than the whole budget from evicting itself right after
			// its own materialisation.
			return
		}
		lock := m.lockFor(r.dir)
		if !lock.TryLock() {
			continue // in use right now; skip rather than wait
		}
		err := os.RemoveAll(r.dir)
		lock.Unlock()
		if err == nil {
			evictableTotal -= r.size
			w.mu.Lock()
			delete(w.lastUse, r.dir)
			w.mu.Unlock()
		}
	}
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort accounting
		}
		if info, err := d.Info(); err == nil {
			n += info.Size()
		}
		return nil
	})
	return n
}
