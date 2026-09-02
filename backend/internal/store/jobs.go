package store

import (
	"context"
	"math"
	"time"
)

// SyncJob is a unit of post-push work: publishing one ref's blobs into the
// content-addressed blobs/ layer and re-indexing its metadata.
//
// Kind is always "push" (or "" for rows that predate the column); the worker
// reads it only to fail loudly on a row an older binary may have left. The
// table's payload column is unused -- storage keys no longer carry a
// repository's name, so a transfer or a rename moves no object at all and
// there is no second kind of work to parameterise.
type SyncJob struct {
	ID       int64
	RepoID   int64
	Ref      string
	OldSHA   string
	NewSHA   string
	Attempts int
	Kind     string
}

// FailedSyncJob is a parked job joined with the repository it belongs to, for
// the operator listing. A failed job freezes that repository's file index,
// search entry and blobs/ export at the previous push, and nothing retries it
// on its own.
type FailedSyncJob struct {
	ID        int64
	RepoKind  string
	Namespace string
	Name      string
	Ref       string
	Attempts  int
	LastError string
	UpdatedAt time.Time
}

const (
	// SyncMaxAttempts is how many times a job is claimed before it parks as
	// 'failed'. Combined with the backoff below this spans roughly forty
	// minutes, which is what makes it a real verdict rather than a burst:
	// the previous three-attempts-with-no-delay burned every retry within a
	// couple of seconds, so a transient GCS 5xx parked the job before it had
	// any chance to clear.
	SyncMaxAttempts = 5

	// syncRetryBaseDelay is the wait before the second attempt; each further
	// attempt quadruples it (30s, 2m, 8m, 32m) up to syncRetryMaxDelay.
	syncRetryBaseDelay = 30 * time.Second
	syncRetryMaxDelay  = time.Hour
)

// retryDelay is the wait before attempt n+1, given that n attempts have been
// made. attempts is always >= 1 here (the claim incremented it).
func retryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	// 4^(attempts-1) grows fast enough to overflow a Duration well before
	// SyncMaxAttempts would allow it; the cap is applied in float space so
	// a future caller raising SyncMaxAttempts cannot wrap the multiply.
	scaled := float64(syncRetryBaseDelay) * math.Pow(4, float64(attempts-1))
	if scaled >= float64(syncRetryMaxDelay) {
		return syncRetryMaxDelay
	}
	return time.Duration(scaled)
}

// EnqueueSync records work for the sync worker. Repeated pushes to the same ref
// collapse into the pending job rather than queueing redundant exports.
func (s *Store) EnqueueSync(ctx context.Context, repoID int64, ref, oldSHA, newSHA string) error {
	// The pending job already carries the right old_sha: it is the state the
	// export was last built from, which the newer push does not change.
	//
	// The attempt counter and the retry backoff are cleared, though. A new
	// push is new work aimed at a new target SHA, and inheriting the penalty
	// of a previous failure would let two unlucky retries park a job that
	// the fresh commit would have exported cleanly. This cannot spin: the
	// budget is reset by a push, not by the worker, so the retry rate is
	// bounded by how often somebody actually pushes.
	//
	// The trade-off is real and worth naming. Because the budget only ever
	// runs out between pushes, a repository pushed more often than the
	// backoff is long (30s, then 2m, 8m, 32m) can fail every single sync and
	// never reach 'failed' -- so it stays out of ListFailedSyncJobs and
	// FailedSyncCount, and the operator surface added alongside this shows
	// nothing while that repository's index sits frozen. The alternative,
	// carrying the count across pushes, trades that for parking repositories
	// whose next commit would have worked, which is the worse failure: a
	// parked job needs a human, an unparked one keeps trying. The gap is
	// covered from the other side instead -- `thinkingface resync` finds an
	// index that has drifted regardless of what the queue thinks.
	n, err := s.db.Exec(ctx,
		`UPDATE sync_jobs
		 SET new_sha = $3, attempts = 0, next_attempt_at = NULL, last_error = '', updated_at = now()
		 WHERE repo_id = $1 AND ref = $2 AND status = 'pending'`, repoID, ref, newSHA)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO sync_jobs (repo_id, ref, old_sha, new_sha) VALUES ($1, $2, $3, $4)`,
		repoID, ref, oldSHA, newSHA)
	return err
}

// ClaimSyncJob atomically takes the next pending job that is due and whose
// repository and ref no other worker is already syncing. It returns nil, nil
// when nothing is claimable. On Postgres SKIP LOCKED lets several workers
// claim jobs for *different* refs concurrently; on SQLite the statement runs
// alone on the writer connection, which is what makes the sub-select + UPDATE
// atomic.
//
// The claim takes a lease for leaseDuration. The worker extends it with
// HeartbeatSyncJob while it works, and RequeueExpiredSyncJobs returns the job
// to the queue once the lease lapses. That is the whole reason the lease
// exists: 'running' alone could not distinguish a live claim from one held by
// a process that died, so the old startup sweep reset every running row and a
// second replica booting mid-sync stole a job that was still being worked --
// see the migration (postgres 0030) and the disjoint-blobs hazard below.
//
// The NOT EXISTS clause serialises work per repo+ref, and both of its halves
// are load bearing. Two jobs for one ref running at once each rebuild that
// ref's index from the tree they read, and Syncer.processPush finishes by
// replacing the whole index for the ref -- so whichever job commits last wins
// outright, and if that is the one that started from the *older* commit the
// repository is left indexed at a state it has moved past. The file listing,
// the search entry and the gcloud script then describe a push that is no
// longer the tip, and nothing recomputes them until somebody pushes again.
// EnqueueSync collapses repeated pushes into a single pending row, but only
// while that row is still pending -- a push arriving while the previous job
// runs inserts a second row for the ref.
//
//   - No 'running' sibling: the ordinary case, once the earlier claim committed.
//     An expired sibling still blocks here rather than being stolen inline; the
//     sweeper flips it to 'pending' first, so there is never a moment where two
//     workers believe they hold the same ref.
//   - No lower-id 'pending' sibling: closes the window in which the earlier
//     claim has not committed yet, so a second worker still sees that job as
//     pending rather than running. It also keeps a ref's jobs in id order,
//     which is what makes each job's old_sha describe the state the previous
//     one actually left behind.
//
// next_attempt_at IS NULL means "due now": it is unset on a fresh row and
// cleared by EnqueueSync, and only FinishSyncJob ever sets it (see the
// migration for why there is no DEFAULT now()).
func (s *Store) ClaimSyncJob(ctx context.Context, leaseDuration time.Duration) (*SyncJob, error) {
	j := &SyncJob{}
	err := s.db.QueryRow(ctx,
		`UPDATE sync_jobs
		 SET status = 'running', attempts = attempts + 1, updated_at = now(),
		     lease_expires_at = `+s.d.nowPlusSeconds("$1")+`
		 WHERE id = (SELECT j.id FROM sync_jobs j
		              WHERE j.status = 'pending'
		                AND (j.next_attempt_at IS NULL OR j.next_attempt_at <= now())
		                AND NOT EXISTS (SELECT 1 FROM sync_jobs s
		                                 WHERE s.repo_id = j.repo_id AND s.ref = j.ref
		                                   AND (s.status = 'running'
		                                        OR (s.status = 'pending' AND s.id < j.id)))
		              ORDER BY j.id`+
			s.d.forUpdate(" SKIP LOCKED")+` LIMIT 1)
		 RETURNING id, repo_id, ref, old_sha, new_sha, attempts, kind`,
		leaseDuration.Seconds(),
	).Scan(&j.ID, &j.RepoID, &j.Ref, &j.OldSHA, &j.NewSHA, &j.Attempts, &j.Kind)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// HeartbeatSyncJob pushes a held job's lease out by leaseDuration. A worker
// calls it periodically so that a sync legitimately taking longer than one
// lease (a first push of a large repository) is not mistaken for a dead claim
// and requeued underneath itself.
//
// It touches only a row this worker still holds: once the sweeper has taken
// the job back the status is no longer 'running' and the update matches
// nothing, so a heartbeat arriving late cannot resurrect a stolen claim.
func (s *Store) HeartbeatSyncJob(ctx context.Context, id int64, attempts int, leaseDuration time.Duration) error {
	_, err := s.db.Exec(ctx,
		`UPDATE sync_jobs SET lease_expires_at = `+s.d.nowPlusSeconds("$3")+`
		 WHERE id = $1 AND status = 'running' AND attempts = $2`,
		id, attempts, leaseDuration.Seconds())
	return err
}

// FinishSyncJob records the outcome of a claimed job and drops its lease.
//
// attempts is the count the claim returned, and it is the fencing token: every
// statement here also requires status = 'running' AND attempts = that value, so
// a worker can only write the outcome of the claim it still holds. Without it
// the lease closes only half the race. A worker whose lease lapsed, was swept
// back to 'pending' and reclaimed by somebody else would still write 'done'
// over the row the new holder is working -- and once that ref has no 'running'
// row, ClaimSyncJob's NOT EXISTS lets a third worker start on it alongside the
// second, which is exactly the concurrent-index-rebuild the lease exists to
// prevent.
//
// A no-op update therefore means the claim was lost, which is not an error:
// whoever holds it now is responsible for the outcome.
func (s *Store) FinishSyncJob(ctx context.Context, id int64, attempts int, jobErr error) error {
	if jobErr == nil {
		_, err := s.db.Exec(ctx,
			`UPDATE sync_jobs
			 SET status = 'done', last_error = '', lease_expires_at = NULL, updated_at = now()
			 WHERE id = $1 AND status = 'running' AND attempts = $2`, id, attempts)
		return err
	}
	if attempts >= SyncMaxAttempts {
		_, err := s.db.Exec(ctx,
			`UPDATE sync_jobs
			 SET status = 'failed', last_error = $3, lease_expires_at = NULL, updated_at = now()
			 WHERE id = $1 AND status = 'running' AND attempts = $2`, id, attempts, jobErr.Error())
		return err
	}
	_, err := s.db.Exec(ctx,
		`UPDATE sync_jobs
		 SET status = 'pending', last_error = $3, lease_expires_at = NULL, updated_at = now(),
		     next_attempt_at = `+s.d.nowPlusSeconds("$4")+`
		 WHERE id = $1 AND status = 'running' AND attempts = $2`,
		id, attempts, jobErr.Error(), retryDelay(attempts).Seconds())
	return err
}

// RequeueExpiredSyncJobs returns to the queue every job whose lease has
// lapsed, meaning the process that claimed it is gone. It runs at startup and
// then periodically, so a replica that crashes mid-sync is recovered without
// waiting for anyone to restart the survivors.
//
// The lease is what makes this safe to run while other replicas are working.
// The predecessor reset *every* running row unconditionally, so a scale-up
// event handed a live job to a second worker and the two published disjoint
// halves of one diff.
//
// A row with no lease at all is treated as expired: those are jobs claimed by
// a binary from before this migration, and leaving them stuck 'running'
// forever would be worse than one requeue at upgrade time.
func (s *Store) RequeueExpiredSyncJobs(ctx context.Context) (int64, error) {
	return s.db.Exec(ctx,
		`UPDATE sync_jobs
		 SET status = 'pending', lease_expires_at = NULL, updated_at = now()
		 WHERE status = 'running'
		   AND (lease_expires_at IS NULL OR lease_expires_at <= now())`)
}

const (
	defaultSyncJobPageSize = 50
	maxSyncJobPageSize     = 200
)

// ListFailedSyncJobs returns one page of parked jobs, newest failure first,
// with the total ignoring the page window. Only 'failed' rows are listed: a
// job still retrying is not an operator's problem yet, and pending/done rows
// are high churn.
func (s *Store) ListFailedSyncJobs(ctx context.Context, limit, offset int) ([]FailedSyncJob, int64, error) {
	if limit <= 0 {
		limit = defaultSyncJobPageSize
	}
	if limit > maxSyncJobPageSize {
		limit = maxSyncJobPageSize
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM sync_jobs WHERE status = 'failed'`).Scan(&total); err != nil {
		return nil, 0, err
	}

	// The namespace name lives on namespaces, not repositories -- repo rows
	// carry namespace_id, which is what makes a namespace rename a one-row
	// update rather than a rewrite of every repository.
	rows, err := s.db.Query(ctx,
		`SELECT j.id, r.kind, n.name, r.name, j.ref, j.attempts, j.last_error, j.updated_at
		   FROM sync_jobs j
		   JOIN repositories r ON r.id = j.repo_id
		   JOIN namespaces n ON n.id = r.namespace_id
		  WHERE j.status = 'failed'
		  ORDER BY j.updated_at DESC, j.id DESC
		  LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]FailedSyncJob, 0, limit)
	for rows.Next() {
		var j FailedSyncJob
		if err := rows.Scan(&j.ID, &j.RepoKind, &j.Namespace, &j.Name,
			&j.Ref, &j.Attempts, &j.LastError, &j.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

// RetrySyncJob puts a parked job back in the queue with a fresh attempt
// budget, reporting whether it matched. It is deliberately restricted to
// 'failed' rows: retrying a job that is pending or running would reset the
// attempt counter of work already in flight.
func (s *Store) RetrySyncJob(ctx context.Context, id int64) (bool, error) {
	n, err := s.db.Exec(ctx,
		`UPDATE sync_jobs
		 SET status = 'pending', attempts = 0, last_error = '',
		     next_attempt_at = NULL, lease_expires_at = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'failed'`, id)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FailedSyncCount is the number of parked jobs across the instance. It exists
// so an operator surface can show the badge without paging the listing.
func (s *Store) FailedSyncCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM sync_jobs WHERE status = 'failed'`).Scan(&n)
	return n, err
}

// PendingSyncCount powers the "indexing…" hint in the UI.
func (s *Store) PendingSyncCount(ctx context.Context, repoID int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM sync_jobs WHERE repo_id = $1 AND status IN ('pending', 'running')`,
		repoID).Scan(&n)
	return n, err
}

// HasUnsettledSyncJobs reports whether the repository has any sync job other
// than exceptID that has not reached 'done' -- pending, running, or parked as
// 'failed'.
//
// It gates PruneRepoLFSLinks. That prune releases links no commit has been
// seen to name, and the sync pipeline is what does the seeing: LinkLFSObjects
// stamps a revision's objects as committed when the job for that ref runs. A
// ref whose job is still queued, or one parked after five failures, has
// pointers nothing has vouched for yet. Their links would look like abandoned
// uploads, get pruned once they aged past the grace window, and `thinkingface
// gc` would then delete bytes a perfectly live pointer names -- discovered
// only when the operator finally retried that job.
//
// So the reconciliation waits until the queue is quiet for this repository.
// The cost is that a repository with a permanently parked job never has its
// abandoned uploads collected, which over-counts its usage; that is the
// direction to err in, and the parked job is already an operator-visible fault
// (ListFailedSyncJobs).
func (s *Store) HasUnsettledSyncJobs(ctx context.Context, repoID, exceptID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM sync_jobs
		               WHERE repo_id = $1 AND id <> $2 AND status <> 'done')`,
		repoID, exceptID).Scan(&exists)
	return exists, err
}
