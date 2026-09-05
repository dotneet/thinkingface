package gitserver

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A request that moves no bytes for the whole idle budget has its process
// killed: this is what bounds the git-receive-pack a client opens and then
// never writes to.
func TestWatchIdleForKillsAnIdleProcess(t *testing.T) {
	progress := &atomic.Int64{}
	progress.Store(time.Now().UnixNano())
	killed := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)

	go watchIdleFor(stop, progress, 100*time.Millisecond, func() error {
		killed <- struct{}{}
		return nil
	})
	select {
	case <-killed:
	case <-time.After(10 * time.Second):
		t.Fatal("an idle process was not killed within the budget")
	}
}

// A transfer that keeps moving is left alone however long it runs: the budget
// is on idleness, not on duration.
func TestWatchIdleForSparesABusyProcess(t *testing.T) {
	progress := &atomic.Int64{}
	progress.Store(time.Now().UnixNano())
	killed := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)

	go watchIdleFor(stop, progress, 200*time.Millisecond, func() error {
		killed <- struct{}{}
		return nil
	})
	// Stay busy across several idle budgets.
	for i := 0; i < 8; i++ {
		time.Sleep(50 * time.Millisecond)
		progress.Store(time.Now().UnixNano())
	}
	select {
	case <-killed:
		t.Fatal("a process that kept moving bytes was killed")
	default:
	}
}

// Stopping the watchdog ends it without killing anything: a finished request
// must not leave a ticker behind, and must not kill a process it no longer
// owns.
func TestWatchIdleForStopsWhenTold(t *testing.T) {
	progress := &atomic.Int64{}
	progress.Store(time.Now().Add(-time.Hour).UnixNano()) // idle already
	killed := make(chan struct{}, 1)
	stop := make(chan struct{})
	close(stop)

	watchIdleFor(stop, progress, 100*time.Millisecond, func() error {
		close(killed)
		return nil
	})
	select {
	case <-killed:
		t.Fatal("a stopped watchdog killed the process")
	default:
	}
}

// Body reads move the deadline: a push whose bytes are still arriving is
// progress, even when git has produced no output yet.
func TestProgressReaderMarksReads(t *testing.T) {
	progress := &atomic.Int64{}
	progress.Store(time.Now().Add(-time.Hour).UnixNano()) // idle already
	before := time.Now().UnixNano()

	pr := &progressReader{r: strings.NewReader("hello"), progress: progress}
	if _, err := io.ReadAll(pr); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := progress.Load(); got < before {
		t.Fatalf("last progress = %d, want at least %d (the read)", got, before)
	}
}
