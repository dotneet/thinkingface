package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// runServe used to launch the sync worker, the flusher and the webhook
// dispatcher with a bare `go` and return the moment the HTTP drain finished.
// run() closes the database on the next line, so a worker that was mid-job at
// that instant lost its pool: FinishSyncJob failed, the row kept its
// 'running' lease for two minutes before any replica could pick it up, and
// the attempt the cancellation consumed was gone. workerGroup exists so that
// return waits for them.

func TestWorkerGroupStopWaitsForItsWorkers(t *testing.T) {
	g := newWorkerGroup(context.Background())

	var finished atomic.Bool
	g.start(func(ctx context.Context) {
		<-ctx.Done()
		// Stands in for the detached FinishSyncJob a worker runs after its
		// context is cancelled -- the write that must land before the
		// database handle goes away.
		time.Sleep(50 * time.Millisecond)
		finished.Store(true)
	})

	g.stop()
	if !finished.Load() {
		t.Fatal("stop returned while a worker was still finishing; the database would close underneath it")
	}
}

// The listener-failure exit: nothing has cancelled the parent context, so the
// group has to cancel the workers itself or shutdown never completes.
func TestWorkerGroupStopCancelsWorkersWithALiveParent(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	g := newWorkerGroup(parent)
	g.start(func(ctx context.Context) { <-ctx.Done() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		g.stop()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop blocked with the parent context still live; the workers were never cancelled")
	}
	if parent.Err() != nil {
		t.Fatal("stop cancelled the caller's context, which it does not own")
	}
}

// The wait is bounded. A worker that ignores its cancellation must not turn
// SIGTERM into a process the platform has to kill.
func TestWorkerGroupStopGivesUpAfterTheGrace(t *testing.T) {
	g := newWorkerGroup(context.Background())
	g.grace = 50 * time.Millisecond

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	g.start(func(context.Context) { <-release })

	start := time.Now()
	g.stop()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stop waited %s for a worker that never returns; want it bounded by the grace", elapsed)
	}
}

// Every worker is waited for, not just the first.
func TestWorkerGroupStopWaitsForEveryWorker(t *testing.T) {
	g := newWorkerGroup(context.Background())

	var done atomic.Int32
	for i := range 3 {
		g.start(func(ctx context.Context) {
			<-ctx.Done()
			time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
			done.Add(1)
		})
	}

	g.stop()
	if n := done.Load(); n != 3 {
		t.Fatalf("%d of 3 workers had finished when stop returned", n)
	}
}
