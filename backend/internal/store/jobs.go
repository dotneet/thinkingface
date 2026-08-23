package store

import "context"

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

// EnqueueSync records work for the sync worker. Repeated pushes to the same ref
// collapse into the pending job rather than queueing redundant exports.
func (s *Store) EnqueueSync(ctx context.Context, repoID int64, ref, oldSHA, newSHA string) error {
	// The pending job already carries the right old_sha: it is the state the
	// export was last built from, which the newer push does not change.
	n, err := s.db.Exec(ctx,
		`UPDATE sync_jobs SET new_sha = $3, updated_at = now()
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

// ClaimSyncJob atomically takes the next pending job whose repository and ref
// no other worker is already syncing. It returns nil, nil when nothing is
// claimable. On Postgres SKIP LOCKED lets several workers claim jobs for
// *different* refs concurrently; on SQLite the statement runs alone on the
// writer connection, which is what makes the sub-select + UPDATE atomic.
//
// The NOT EXISTS clause serialises work per repo+ref, and both of its halves
// are load bearing. Syncer.publishBlob walks the OldSHA..NewSHA diff rather
// than the whole tree, so two jobs for one ref running at once publish two
// disjoint sets of blobs; a file touched only by the job that finishes first
// (.gitattributes and the seeded README, which only ever appear in the
// repo-creation commit) is then left with no blobs/{sha} object at all, and
// nothing republishes it afterwards. EnqueueSync collapses repeated pushes
// into a single pending row, but only while that row is still pending -- a
// push arriving while the previous job runs inserts a second row for the ref.
//
//   - No 'running' sibling: the ordinary case, once the earlier claim committed.
//   - No lower-id 'pending' sibling: closes the window in which the earlier
//     claim has not committed yet, so a second worker still sees that job as
//     pending rather than running. It also keeps a ref's jobs in id order,
//     which is what makes each job's old_sha describe the state the previous
//     one actually left behind.
func (s *Store) ClaimSyncJob(ctx context.Context) (*SyncJob, error) {
	j := &SyncJob{}
	err := s.db.QueryRow(ctx,
		`UPDATE sync_jobs SET status = 'running', attempts = attempts + 1, updated_at = now()
		 WHERE id = (SELECT j.id FROM sync_jobs j
		              WHERE j.status = 'pending'
		                AND NOT EXISTS (SELECT 1 FROM sync_jobs s
		                                 WHERE s.repo_id = j.repo_id AND s.ref = j.ref
		                                   AND (s.status = 'running'
		                                        OR (s.status = 'pending' AND s.id < j.id)))
		              ORDER BY j.id`+
			s.d.forUpdate(" SKIP LOCKED")+` LIMIT 1)
		 RETURNING id, repo_id, ref, old_sha, new_sha, attempts, kind`,
	).Scan(&j.ID, &j.RepoID, &j.Ref, &j.OldSHA, &j.NewSHA, &j.Attempts, &j.Kind)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

func (s *Store) FinishSyncJob(ctx context.Context, id int64, jobErr error) error {
	if jobErr == nil {
		_, err := s.db.Exec(ctx,
			`UPDATE sync_jobs SET status = 'done', last_error = '', updated_at = now() WHERE id = $1`, id)
		return err
	}
	// Three attempts, then park it as failed so it stops burning the worker.
	_, err := s.db.Exec(ctx,
		`UPDATE sync_jobs
		 SET status = CASE WHEN attempts >= 3 THEN 'failed' ELSE 'pending' END,
		     last_error = $2, updated_at = now()
		 WHERE id = $1`, id, jobErr.Error())
	return err
}

// RequeueRunningJobs returns jobs to the queue that a previous process claimed
// but never finished. Without this a restart mid-sync would silently drop the
// export and leave the bucket out of step with git forever.
func (s *Store) RequeueRunningJobs(ctx context.Context) (int64, error) {
	return s.db.Exec(ctx,
		`UPDATE sync_jobs SET status = 'pending', updated_at = now() WHERE status = 'running'`)
}

// PendingSyncCount powers the "indexing…" hint in the UI.
func (s *Store) PendingSyncCount(ctx context.Context, repoID int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM sync_jobs WHERE repo_id = $1 AND status IN ('pending', 'running')`,
		repoID).Scan(&n)
	return n, err
}
