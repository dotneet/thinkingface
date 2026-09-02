package gitrepo

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
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
	// scanning is true while an eviction pass is running on its own
	// goroutine, so a burst of requests schedules one pass, not one each.
	scanning bool
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
//
// Prefer EnsureLocalWithDefaultBranch wherever the caller already holds the
// store.Repo: without the default branch a rebuilt copy has to reconstruct
// HEAD by rule, and the rule is wrong for any repository whose default branch
// is neither "main" nor alphabetically first.
func (m *Manager) EnsureLocal(ctx context.Context, storagePath string) error {
	return m.EnsureLocalWithDefaultBranch(ctx, storagePath, "")
}

// EnsureLocalWithDefaultBranch is EnsureLocal for a caller that knows the
// repository's configured default branch (store.Repo.DefaultBranch, no
// refs/heads/ prefix). The WAL index carries refs but not the symbolic HEAD,
// so this is the only way a materialised copy can point HEAD at the branch the
// repository actually declares — which is what `git clone` checks out and what
// Repo.Resolve("") answers from. An empty defaultBranch means "unknown" and
// falls back to the guess.
func (m *Manager) EnsureLocalWithDefaultBranch(ctx context.Context, storagePath, defaultBranch string) error {
	if m.wal == nil {
		return nil
	}
	// Detach from the caller's cancellation but keep a hard upper bound.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), materializeTimeout)
	defer cancel()

	dir := m.Dir(storagePath)
	lock := m.lockFor(dir)
	lock.Lock()
	// Authoritative is not a parameter: EnableWAL is only ever called when
	// TF_WAL_MODE=authoritative (cmd/thinkingface/serve.go, resync.go), which
	// is what makes "the WAL is the truth" true for every path that reaches
	// here. Shadow and off never build a walBackend at all.
	err := wal.MaterializeWith(mctx, m.wal.store, dir, storagePath, wal.Options{
		DefaultBranch: defaultBranch,
		Authoritative: true,
	})
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
	m.triggerEvict()
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

// triggerEvict considers running the eviction pass, and never makes the
// caller wait for it.
//
// The decision — has evictScanInterval elapsed, is a pass already running —
// is in-memory and costs a mutex. The pass itself is a WalkDir of the whole
// git root plus a dirSize walk and a state-file read per repository, and then
// possibly a synchronous RemoveAll: hundreds of milliseconds of unrelated
// filesystem work that used to land on whichever request happened to arrive
// first after the interval lapsed. Nothing about it needs to be ordered with
// respect to that request — the budget it enforces is a steady-state property
// — so it belongs on its own goroutine.
//
// The `scanning` flag is what the goroutine costs: without it a pass slower
// than evictScanInterval would be joined by a second one walking the same
// tree and racing it to the same RemoveAll. The recover is the other thing it
// costs: on the request goroutine this work sat under middleware.Recoverer
// (internal/api/server.go), and a panic in it was one 500. Detached, nothing
// is above it, so the same panic would take the process down and every
// in-flight request with it. The work -- WalkDir, dirSize, RemoveAll -- is
// unlikely to panic; the point is that the blast radius of it doing so must
// not have grown just because the work moved off the request path.
func (m *Manager) triggerEvict() {
	w := m.wal
	if w == nil || w.cacheBytes <= 0 {
		return
	}
	w.mu.Lock()
	if w.scanning || time.Since(w.lastScan) < evictScanInterval {
		w.mu.Unlock()
		return
	}
	w.lastScan = time.Now()
	w.scanning = true
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			w.scanning = false
			// Measure the interval from the end of a pass, not its start: a
			// scan that took longer than the interval must not be
			// immediately eligible again.
			w.lastScan = time.Now()
			w.mu.Unlock()
		}()
		// Innermost, so the pass is already unwound when the flag above is
		// cleared and the next trigger finds a consistent walBackend.
		defer recoverEvictPanic()
		m.evictOnce()
	}()
}

// recoverEvictPanic keeps a panic in the detached eviction pass from killing
// the process. Dropping the pass is the right response: the byte budget it
// enforces is a steady-state property, so the next trigger picks it up, and
// nothing a caller is waiting on depends on it.
func recoverEvictPanic() {
	if r := recover(); r != nil {
		slog.Error("gitrepo: the cache eviction pass panicked and was dropped",
			"panic", r, "stack", string(debug.Stack()))
	}
}

// evictOnce keeps the materialised cache under its byte budget. It only ever
// removes directories that are (a) WAL-backed — a state file proves the WAL
// can rebuild them — (b) idle for at least evictMinIdle, and (c) not
// currently locked. Repositories that predate the WAL (no state file) are
// never touched: deleting one would destroy data the WAL cannot restore — and
// for the same reason they are excluded from the budget itself, or a large
// legacy population would make the target unreachable and every scan would
// pointlessly flush the entire evictable cache.
//
// Separate from triggerEvict so a test can run one pass deterministically
// instead of racing the goroutine that normally schedules it.
func (m *Manager) evictOnce() {
	w := m.wal
	if w == nil || w.cacheBytes <= 0 {
		return
	}
	repos, evictableTotal := m.scanEvictable()
	if evictableTotal <= w.cacheBytes {
		return
	}
	m.evictDown(repos, evictableTotal, time.Now())
}

// repoDir is one eviction candidate as the scan found it. used is a
// *snapshot*: by the time evictDown acts on it a request may have arrived, so
// it decides ordering, never deletion.
type repoDir struct {
	dir  string
	size int64
	used time.Time
}

// scanEvictable walks the git root for eviction candidates and returns them
// with the total they occupy. Split from evictDown so the gap between
// measuring a repository and deleting it — the gap a request can arrive in —
// is something a test can open on purpose.
func (m *Manager) scanEvictable() ([]repoDir, int64) {
	var repos []repoDir
	var evictableTotal int64
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
		repos = append(repos, repoDir{dir: dir, size: size, used: m.lastUseOf(dir)})
		return filepath.SkipDir
	})
	return repos, evictableTotal
}

// evictDown removes candidates, least recently used first, until the total is
// back inside the budget.
func (m *Manager) evictDown(repos []repoDir, evictableTotal int64, now time.Time) {
	w := m.wal
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
		// Re-read the idle time now that the lock is held, and drop the
		// candidate if it went fresh. r.used is a snapshot from the scan, and
		// the scan is not instantaneous: an EnsureLocal that started after
		// this directory was measured has by now materialised it, stamped it
		// and returned, and its caller is about to read the directory.
		// Deleting on the stale snapshot would take it out from under that
		// caller even though the stamp landed before the unlock this TryLock
		// just won — the mid-request eviction the stamp ordering exists to
		// prevent (see EnsureLocal and docs/dev/continuity-design.md §16).
		if now.Sub(m.lastUseOf(r.dir)) < evictMinIdle {
			lock.Unlock()
			continue
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

// lastUseOf is when a repository directory was last handed to a caller: the
// in-memory stamp EnsureLocal writes, or -- when there is none, which is the
// state right after a restart -- the state file's mtime, which survives one.
// A directory with neither reads as the zero time, i.e. maximally idle.
func (m *Manager) lastUseOf(dir string) time.Time {
	m.wal.mu.Lock()
	used, ok := m.wal.lastUse[dir]
	m.wal.mu.Unlock()
	if ok {
		return used
	}
	if st, err := os.Stat(filepath.Join(dir, wal.StateFileName)); err == nil {
		return st.ModTime()
	}
	return time.Time{}
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
