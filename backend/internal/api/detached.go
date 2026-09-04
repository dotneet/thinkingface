package api

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// detachedQueueSize bounds how many fire-and-forget database writes may wait
// for a worker, and detachedQueueWorkers bounds how many run at once. Both
// are about the same failure: a database that has stopped answering while
// requests keep arriving. detachedWrite (auth.go) already gives each parked
// write its own deadline, but a deadline only ends a write -- it does nothing
// about how many are parked. One resolve was one goroutine and two writes,
// and resolve needs no authentication, so without this an unauthenticated
// caller could pile them up without limit just by asking for files.
//
// The pool is shared process-wide rather than per Server so tests that build
// many Servers do not each mint their own workers. The work itself stays
// request-scoped: each function carries its own detached context (values
// without cancellation, plus a deadline), so a slow write still cannot delay
// the response it belongs to.
//
// A full queue drops the work instead of blocking the request. That is a
// product decision, not an oversight: everything submitted here is a counter
// (a token's last-used stamp, a repository's download counts), and a missing
// increment is strictly better than a slow download. Drops are counted and
// logged so a sustained one shows up before anyone reads the dashboard as
// the truth.
const (
	detachedQueueSize    = 1024
	detachedQueueWorkers = 4
)

// detachedPool is the bounded queue described above. The zero value is not
// usable; build one with newDetachedPool, which also starts its workers.
type detachedPool struct {
	ch      chan func()
	dropped atomic.Int64
}

func newDetachedPool(workers, size int) *detachedPool {
	p := &detachedPool{ch: make(chan func(), size)}
	for i := 0; i < workers; i++ {
		go func() {
			for fn := range p.ch {
				fn()
			}
		}()
	}
	return p
}

// submit queues fn, dropping it when the queue is full. It never blocks, so
// it is safe to call on the request path.
func (p *detachedPool) submit(fn func()) {
	select {
	case p.ch <- fn:
	default:
		n := p.dropped.Add(1)
		slog.Warn("detached write dropped: queue is full; counters will undercount",
			"dropped_total", n)
	}
}

var (
	sharedDetached     *detachedPool
	sharedDetachedOnce sync.Once
)

// submitDetached queues fn on the shared pool, dropping it when the pool is
// full. Every fire-and-forget database write goes through here so there is
// one place that bounds them.
func submitDetached(fn func()) {
	sharedDetachedOnce.Do(func() {
		sharedDetached = newDetachedPool(detachedQueueWorkers, detachedQueueSize)
	})
	sharedDetached.submit(fn)
}
