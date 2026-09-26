package store

import (
	"errors"
	"testing"
)

// syncJobRow is the raw state of one queue row, for assertions the exported
// API cannot make (it deliberately reports a lost claim as success).
type syncJobRow struct {
	status    string
	attempts  int
	claimSeq  int64
	lastError string
	newSHA    string
}

func readSyncJob(t *testing.T, s *Store, id int64) syncJobRow {
	t.Helper()
	var r syncJobRow
	if err := s.db.QueryRow(t.Context(),
		`SELECT status, attempts, claim_seq, last_error, new_sha FROM sync_jobs WHERE id = $1`, id,
	).Scan(&r.status, &r.attempts, &r.claimSeq, &r.lastError, &r.newSHA); err != nil {
		t.Fatalf("read sync job %d: %v", id, err)
	}
	return r
}

// TestIntegrationSyncJobClaimFencedByPush covers the one ordering the attempt
// counter could not fence: a lapsed claim, a sweep, and then a *push* before
// the job is reclaimed.
//
// EnqueueSync resets attempts to 0 on a pending row on purpose, so the next
// claim hands out attempts = 1 again -- the very number the stalled worker is
// still holding. With attempts as the fencing token that worker's late
// FinishSyncJob matched the row the new holder was working: it wrote 'done'
// over live work, dropped the result the new holder was about to report, and
// left the ref with no 'running' row, at which point ClaimSyncJob's NOT
// EXISTS would let a third worker rebuild the same ref alongside the second.
//
// TestIntegrationSyncJobs already covers the fence over sweep-then-reclaim;
// what it never does is push in between, which is exactly what made the token
// repeat.
func TestIntegrationSyncJobClaimFencedByPush(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "fenced-by-push", "dataset", nil)

		if err := s.EnqueueSync(ctx, r.ID, "main", "", "s1"); err != nil {
			t.Fatal(err)
		}
		// W1 claims with a lease already in the past: the stall is what the
		// test is reproducing, so it does not have to wait for one.
		w1, err := s.ClaimSyncJob(ctx, -testLease)
		if err != nil || w1 == nil {
			t.Fatalf("first claim = %+v, %v", w1, err)
		}
		if w1.Attempts != 1 || w1.ClaimSeq != 1 {
			t.Fatalf("first claim attempts=%d claim_seq=%d, want 1 and 1", w1.Attempts, w1.ClaimSeq)
		}

		// The sweeper decides W1 is gone and returns the job to the queue.
		if n, err := s.RequeueExpiredSyncJobs(ctx); err != nil || n != 1 {
			t.Fatalf("sweep the stalled claim = %d, %v", n, err)
		}
		// A push lands on the still-pending row and refunds its budget.
		if err := s.EnqueueSync(ctx, r.ID, "main", "s1", "s2"); err != nil {
			t.Fatal(err)
		}
		if got := readSyncJob(t, s, w1.ID); got.attempts != 0 || got.newSHA != "s2" {
			t.Fatalf("after the push: attempts=%d new_sha=%q, want 0 and s2", got.attempts, got.newSHA)
		}

		// W2 reclaims. The attempt counter is back to the value W1 holds --
		// that is the whole defect, so assert it rather than leaving it
		// implicit: if a future change made attempts monotonic again this
		// test would still pass while testing nothing.
		w2, err := s.ClaimSyncJob(ctx, testLease)
		if err != nil || w2 == nil || w2.ID != w1.ID {
			t.Fatalf("reclaim = %+v, %v", w2, err)
		}
		if w2.Attempts != w1.Attempts {
			t.Fatalf("attempts after the push = %d, want it to repeat W1's %d "+
				"(the premise of this test)", w2.Attempts, w1.Attempts)
		}
		if w2.ClaimSeq <= w1.ClaimSeq {
			t.Fatalf("claim_seq did not advance: W1 %d, W2 %d", w1.ClaimSeq, w2.ClaimSeq)
		}

		// W1 wakes up and reports success against its dead claim.
		if err := s.FinishSyncJob(ctx, w1, nil); err != nil {
			t.Fatalf("stale finish returned an error: %v", err)
		}
		if got := readSyncJob(t, s, w1.ID); got.status != "running" || got.claimSeq != w2.ClaimSeq {
			t.Fatalf("the stale success landed: %+v, want status running at claim_seq %d",
				got, w2.ClaimSeq)
		}
		// A stale *failure* must be just as inert: it would otherwise burn
		// the new holder's budget and stamp an error nobody is retrying.
		if err := s.FinishSyncJob(ctx, w1, errors.New("stale boom")); err != nil {
			t.Fatalf("stale failing finish returned an error: %v", err)
		}
		if got := readSyncJob(t, s, w1.ID); got.status != "running" || got.lastError != "" {
			t.Fatalf("the stale failure landed: %+v", got)
		}

		// The ref is still guarded, so nobody starts a second rebuild of it.
		if n, _ := s.PendingSyncCount(ctx, r.ID); n != 1 {
			t.Fatalf("PendingSyncCount = %d, want the job W2 still holds", n)
		}
		if third, err := s.ClaimSyncJob(ctx, testLease); err != nil || third != nil {
			t.Fatalf("a third worker claimed the ref W2 is syncing: %+v, %v", third, err)
		}

		// And W2's own outcome still lands.
		if err := s.FinishSyncJob(ctx, w2, nil); err != nil {
			t.Fatal(err)
		}
		if got := readSyncJob(t, s, w2.ID); got.status != "done" {
			t.Fatalf("the real holder could not finish: %+v", got)
		}
	})
}

// TestIntegrationSyncJobHeartbeatFencedByPush is the same ordering seen from
// the heartbeat side. A stalled worker whose claim was swept and reclaimed
// after a push must not be able to extend the lease of the claim that
// replaced it: doing so would keep the sweeper off a genuinely dead holder,
// and -- since the heartbeat is what a live worker relies on -- silently tie
// the new holder's lease to the old worker's timer.
func TestIntegrationSyncJobHeartbeatFencedByPush(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "heartbeat-fence", "dataset", nil)

		if err := s.EnqueueSync(ctx, r.ID, "main", "", "s1"); err != nil {
			t.Fatal(err)
		}
		w1, err := s.ClaimSyncJob(ctx, -testLease)
		if err != nil || w1 == nil {
			t.Fatalf("first claim = %+v, %v", w1, err)
		}
		if n, err := s.RequeueExpiredSyncJobs(ctx); err != nil || n != 1 {
			t.Fatalf("sweep the stalled claim = %d, %v", n, err)
		}
		if err := s.EnqueueSync(ctx, r.ID, "main", "s1", "s2"); err != nil {
			t.Fatal(err)
		}
		// W2's own lease is expired too, so the sweeper's verdict below is
		// decided purely by whether a heartbeat pushed it out.
		w2, err := s.ClaimSyncJob(ctx, -testLease)
		if err != nil || w2 == nil || w2.Attempts != w1.Attempts {
			t.Fatalf("reclaim = %+v, %v (attempts should repeat W1's %d)", w2, err, w1.Attempts)
		}

		if err := s.HeartbeatSyncJob(ctx, w1, testLease); err != nil {
			t.Fatalf("stale heartbeat returned an error: %v", err)
		}
		if n, err := s.RequeueExpiredSyncJobs(ctx); err != nil || n != 1 {
			t.Fatalf("the stale heartbeat extended W2's lease: sweep = %d, %v", n, err)
		}

		// The holder's own heartbeat does work, which is what keeps this from
		// passing for the trivial reason that heartbeats never match.
		w3, err := s.ClaimSyncJob(ctx, -testLease)
		if err != nil || w3 == nil {
			t.Fatalf("third claim = %+v, %v", w3, err)
		}
		if err := s.HeartbeatSyncJob(ctx, w3, testLease); err != nil {
			t.Fatal(err)
		}
		if n, err := s.RequeueExpiredSyncJobs(ctx); err != nil || n != 0 {
			t.Fatalf("a live heartbeat did not hold the lease: sweep = %d, %v", n, err)
		}
	})
}

// The claim charges an attempt, but only FinishSyncJob ever turned a spent
// budget into a verdict. A job whose worker never got that far -- it crashed
// on the job, or the database refused to record the outcome -- came back from
// every sweep as 'pending' and was handed out again without end, invisible to
// ListFailedSyncJobs. The sweep now parks it once the budget is gone.
func TestIntegrationRequeueExpiredSyncJobsParksASpentBudget(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "crashy", "dataset", nil)
		if err := s.EnqueueSync(ctx, r.ID, "main", "", "s1"); err != nil {
			t.Fatal(err)
		}

		var last *SyncJob
		for attempt := 1; attempt <= SyncMaxAttempts; attempt++ {
			// A lease already in the past stands in for a worker that died.
			j, err := s.ClaimSyncJob(ctx, -testLease)
			if err != nil || j == nil || j.Attempts != attempt {
				t.Fatalf("claim %d = %+v, %v", attempt, j, err)
			}
			if attempt == 1 {
				// One recorded failure, so the parked row can show it.
				if err := s.FinishSyncJob(ctx, j, errors.New("boom")); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(ctx, `UPDATE sync_jobs SET next_attempt_at = NULL WHERE id = $1`, j.ID); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if n, err := s.RequeueExpiredSyncJobs(ctx); err != nil || n != 1 {
				t.Fatalf("sweep %d = %d, %v", attempt, n, err)
			}
			last = j
			want := "pending"
			if attempt == SyncMaxAttempts {
				want = "failed"
			}
			if got := readSyncJob(t, s, j.ID); got.status != want {
				t.Fatalf("after sweep %d: status %q, want %q", attempt, got.status, want)
			}
		}

		if j, err := s.ClaimSyncJob(ctx, testLease); err != nil || j != nil {
			t.Fatalf("a parked job was claimable: %+v, %v", j, err)
		}
		failed, total, err := s.ListFailedSyncJobs(ctx, 0, 0)
		if err != nil || total != 1 || len(failed) != 1 || failed[0].ID != last.ID {
			t.Fatalf("ListFailedSyncJobs = %+v, total %d, %v", failed, total, err)
		}
		if want := syncLeaseExpiredError + "; last reported error: boom"; failed[0].LastError != want {
			t.Errorf("last_error = %q, want %q", failed[0].LastError, want)
		}
		// And the operator's retry still hands back a fresh budget.
		if ok, err := s.RetrySyncJob(ctx, last.ID); err != nil || !ok {
			t.Fatalf("RetrySyncJob = %v, %v", ok, err)
		}
	})
}
