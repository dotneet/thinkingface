package webhooks

import "time"

// MaxAttempts bounds how many times a delivery is tried in total (the initial
// attempt plus retries) before it is parked as "failed".
const MaxAttempts = 5

// backoffBase is the wait before the second attempt; each later attempt
// doubles it.
const backoffBase = 30 * time.Second

// backoffCap keeps a long-parked queue from scheduling attempts hours apart.
const backoffCap = 15 * time.Minute

// BackoffDuration returns how long to wait before retrying a delivery after
// its attempt'th failure (attempt is 1 for the first failed attempt). It
// doubles from BackoffBase and saturates at backoffCap rather than growing
// unbounded, since attempt can be as large as MaxAttempts.
func BackoffDuration(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := backoffBase
	for i := 1; i < attempt; i++ {
		if d >= backoffCap {
			return backoffCap
		}
		d *= 2
	}
	if d > backoffCap {
		d = backoffCap
	}
	return d
}
