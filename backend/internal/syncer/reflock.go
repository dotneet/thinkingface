package syncer

import (
	"context"
	"strconv"
	"sync"
)

// refLocks serialises the post-push pipeline per repository+ref inside one
// process.
//
// The pipeline reads the tree the ref points at *now* (runPushPipeline calls
// Tree(job.Ref) rather than Tree(job.NewSHA)) and finishes by replacing the
// whole index for that ref. Two of them interleaved therefore resolve to
// "whoever writes last wins", and if that is the one that read the older tree
// the repository is left indexed at a state it has already moved past --
// exactly the hazard ClaimSyncJob's NOT EXISTS clause is there to prevent
// between two sync workers.
//
// That clause only covers claims, though, so it only covers work that goes
// through the queue. The metrics flush (flush.go) does not: it commits a
// parquet of its own and re-indexes inline, holding no sync_jobs row, and it
// runs in a goroutine right next to the worker pool. Nothing stopped a flush
// from landing in the middle of a worker's pipeline and being overwritten by
// it, which drops the freshly committed metrics.parquet out of repo_files and
// takes the run's series off the dashboard until somebody pushes again.
//
// This is what closes that gap. It is deliberately an in-process lock and not
// a second lease: the flush is already designed to tolerate two replicas
// racing on one project (RunFlush's comment -- the commit's path precondition
// settles that), and the residual cross-replica window here is the same shape
// and the same size as the one that design already accepts. What was new, and
// is now gone, is the two of them racing *within* a single process, where they
// are started as sibling goroutines by every deployment.
type refLocks struct {
	mu    sync.Mutex
	locks map[string]*refLock
}

// refLock is one ref's lock plus the number of callers holding or waiting for
// it. The count is what lets the entry be dropped again: a server that has
// seen a million refs must not keep a million mutexes alive to show for it.
type refLock struct {
	ch      chan struct{}
	waiters int
}

func refLockKey(repoID int64, ref string) string {
	// A ref name cannot contain a NUL (gitrepo.ValidateRefName rejects far
	// more than that), so this cannot collide across repositories.
	return strconv.FormatInt(repoID, 10) + "\x00" + ref
}

// refHold is proof that the caller holds a ref's lock. runPushPipeline takes
// one, so the compiler -- not a comment -- is what stops a future entry point
// from indexing a ref without serialising against the others.
type refHold struct{ release func() }

func (h refHold) unlock() {
	if h.release != nil {
		h.release()
	}
}

// enter registers a waiter on the ref's entry, creating it if this is the
// first, and returns it with the function that unregisters again. Both lock
// and tryLock go through here so the bookkeeping that keeps the table from
// growing forever exists in one place.
func (l *refLocks) enter(repoID int64, ref string) (*refLock, func()) {
	key := refLockKey(repoID, ref)

	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*refLock{}
	}
	e := l.locks[key]
	if e == nil {
		// Buffered by one and empty: a send is the acquire, a receive is the
		// release. A plain sync.Mutex would do everything but the cancel.
		e = &refLock{ch: make(chan struct{}, 1)}
		l.locks[key] = e
	}
	e.waiters++
	l.mu.Unlock()

	return e, func() {
		l.mu.Lock()
		e.waiters--
		if e.waiters == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

// hold wraps an acquired entry in the idempotent release.
func hold(e *refLock, leave func()) refHold {
	var once sync.Once
	return refHold{release: func() {
		once.Do(func() {
			<-e.ch
			leave()
		})
	}}
}

// lock waits for the ref's lock. It gives up and returns ctx.Err() if the
// context is cancelled while waiting, so a shutdown does not have to sit
// behind a large repository's publish.
func (l *refLocks) lock(ctx context.Context, repoID int64, ref string) (refHold, error) {
	e, leave := l.enter(repoID, ref)
	select {
	case e.ch <- struct{}{}:
		return hold(e, leave), nil
	case <-ctx.Done():
		leave()
		return refHold{}, ctx.Err()
	}
}

// tryLock takes the ref's lock only if it is free. The metrics flush uses it:
// flushDue walks its candidates one at a time, so a flush that waited here
// would hold up every *other* project behind whichever repository happens to
// be mid-publish. There is nothing to wait for anyway -- the parquet commit is
// already durable, the points stay buffered, and the next poll ten seconds
// later re-runs a flush that appends nothing twice (FlushProject's comment).
func (l *refLocks) tryLock(repoID int64, ref string) (refHold, bool) {
	e, leave := l.enter(repoID, ref)
	select {
	case e.ch <- struct{}{}:
		return hold(e, leave), true
	default:
		leave()
		return refHold{}, false
	}
}
