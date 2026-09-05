package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A transient failure of the post-commit bookkeeping is retried rather than
// reported: both steps are idempotent, while a 500 after a durable commit
// leaves the index stale behind work the client was told failed.
func TestRetryPostCommit_RetriesATransientFailure(t *testing.T) {
	ctx := context.Background()
	var calls int
	err := retryPostCommit(ctx, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("jobs table is locked")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryPostCommit: %v, want nil after the transient failure clears", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

// A persistent failure still surfaces after the budget runs out, instead of
// holding the request forever.
func TestRetryPostCommit_GivesUpAfterTheBudget(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("database is down")
	var calls int
	start := time.Now()
	if err := retryPostCommit(ctx, func(context.Context) error {
		calls++
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("retryPostCommit = %v, want the operation's own error", err)
	}
	if calls != postCommitAttempts {
		t.Fatalf("calls = %d, want %d", calls, postCommitAttempts)
	}
	// Two short backoffs, not a hang: the whole budget is well under a second.
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("retries took %v, want prompt failure", took)
	}
}

// A request that went away stops the retries: sleeping to redo bookkeeping
// for a client that already hung up is pure latency.
func TestRetryPostCommit_StopsWhenTheRequestIsGone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	if err := retryPostCommit(ctx, func(context.Context) error {
		calls++
		return errors.New("jobs table is locked")
	}); err == nil {
		t.Fatal("retryPostCommit on a cancelled request succeeded, want the error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 -- no retry after the request died", calls)
	}
}
