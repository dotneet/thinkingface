package webhooks

import (
	"testing"
	"time"
)

func TestBackoffDurationDoublesFromBase(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, backoffBase}, // clamped up to attempt 1
		{1, backoffBase},
		{2, 2 * backoffBase},
		{3, 4 * backoffBase},
		{4, 8 * backoffBase},
	}
	for _, c := range cases {
		if got := BackoffDuration(c.attempt); got != c.want {
			t.Errorf("BackoffDuration(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestBackoffDurationSaturatesAtCap(t *testing.T) {
	// The important property under a large attempt count is that this never
	// overflows or grows past the cap, however many retries a caller asks
	// backoff for.
	for _, attempt := range []int{20, 50, 100} {
		if got := BackoffDuration(attempt); got != backoffCap {
			t.Errorf("BackoffDuration(%d) = %v, want cap %v", attempt, got, backoffCap)
		}
	}
}

func TestBackoffDurationMonotonic(t *testing.T) {
	prev := BackoffDuration(1)
	for attempt := 2; attempt <= MaxAttempts; attempt++ {
		cur := BackoffDuration(attempt)
		if cur < prev {
			t.Fatalf("BackoffDuration(%d) = %v is less than BackoffDuration(%d) = %v", attempt, cur, attempt-1, prev)
		}
		prev = cur
	}
}
