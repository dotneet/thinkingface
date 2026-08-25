package api

import (
	"context"
	"testing"
	"time"
)

// TestDetachedWriteOutlivesTheRequestButNotForever pins both halves of what a
// detached database write needs.
//
// The first half is why these writes are detached at all: recordDownload
// (resolve.go) and the token touch (auth.go) run after the response has been
// written, so a context that dies with the request would cancel them.
//
// The second half is the one that was missing. context.WithoutCancel carries
// no deadline of its own, so a resolve -- which needs no authentication --
// started one goroutine and two writes that would wait indefinitely on a
// database that had stopped answering, with the response already sent and
// therefore nothing anywhere applying back pressure.
func TestDetachedWriteOutlivesTheRequestButNotForever(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	writeCtx, done := detachedWrite(parent)
	defer done()

	// The request finishes -- or the client hangs up mid-download.
	cancel()
	if err := writeCtx.Err(); err != nil {
		t.Fatalf("detached context died with its request: %v", err)
	}

	deadline, ok := writeCtx.Deadline()
	if !ok {
		t.Fatal("detached context has no deadline; a stalled database would park this goroutine forever")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > detachedWriteTimeout {
		t.Fatalf("deadline is %v away, want (0, %v]", remaining, detachedWriteTimeout)
	}
}

// TestDetachedWriteKeepsRequestValues guards the reason this is
// context.WithoutCancel rather than context.Background(): the store's logging
// reads the request id off the context, and a background context would drop
// it, leaving these writes unattributable in the log.
func TestDetachedWriteKeepsRequestValues(t *testing.T) {
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "request-42")
	writeCtx, done := detachedWrite(parent)
	defer done()

	if got := writeCtx.Value(key{}); got != "request-42" {
		t.Fatalf("detached context value = %#v, want the request's", got)
	}
}
