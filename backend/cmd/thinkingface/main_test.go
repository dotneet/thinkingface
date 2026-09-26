package main

import (
	"context"
	"testing"
	"time"
)

// TestReleaseSignalsOnDone checks the two things that matter about it: stop
// is not called before ctx is done (a signal handler released early would
// let an unrelated second SIGINT/SIGTERM kill the process before the first
// one even started shutting anything down), and it is called promptly once
// ctx is done (the whole point -- see releaseSignalsOnDone's doc comment for
// why a deferred stop() alone leaves a ~30s window where a second Ctrl-C
// during `serve`'s shutdown drain does nothing).
func TestReleaseSignalsOnDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := make(chan struct{})
	releaseSignalsOnDone(ctx, func() { close(called) })

	select {
	case <-called:
		t.Fatal("stop was called before ctx.Done()")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("stop was not called promptly after ctx.Done()")
	}
}
