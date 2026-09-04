package api

import (
	"testing"
	"time"
)

// The pool runs what it is given: a queued function executes exactly once.
func TestDetachedPool_RunsQueuedWork(t *testing.T) {
	p := newDetachedPool(1, 4)
	done := make(chan struct{}, 1)
	p.submit(func() { done <- struct{}{} })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued detached write never ran")
	}
	if got := p.dropped.Load(); got != 0 {
		t.Fatalf("dropped = %d, want 0", got)
	}
}

// A full queue drops rather than blocking the request path, and counts what
// it dropped. One worker holds the blocking function, one slot holds the
// queued one, and the third submit has nowhere to go.
func TestDetachedPool_DropsInsteadOfBlocking(t *testing.T) {
	p := newDetachedPool(1, 1)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	ran := make(chan string, 2)

	p.submit(func() { close(entered); <-release; ran <- "first" })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking write never started")
	}
	p.submit(func() { ran <- "second" })

	// Must return promptly: blocking here would mean the request path stalls
	// whenever the database cannot keep up.
	dropped := make(chan struct{}, 1)
	go func() { p.submit(func() { ran <- "third" }); close(dropped) }()
	select {
	case <-dropped:
	case <-time.After(5 * time.Second):
		t.Fatal("submit blocked on a full queue; detached writes must never stall a request")
	}
	if got := p.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}

	close(release)
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-ran:
			if got != want {
				t.Fatalf("ran %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("queued write %q never ran", want)
		}
	}
}
