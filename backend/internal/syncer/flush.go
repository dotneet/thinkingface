// Periodic flushing of the native ingest API's buffer into the dataset
// repositories that own it (docs/dev/thinkingface-design.md §8). The ingest
// endpoint writes points to exp_points so the dashboard can be live; the
// promise the design makes is that the data still ends up as parquet inside
// the dataset repository, git-versioned, published into object storage and
// readable by DuckDB. This file is what keeps that promise.

package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// flushPollInterval is how often the flusher looks for work. It is far
// shorter than the configured flush interval because a run that has just
// finished (or failed) is flushed at once rather than at the end of its
// project's interval: the poll is cheap (one grouped query), the flush is not.
const flushPollInterval = 10 * time.Second

// maxFlushProjects bounds one poll's candidate list, so a server with a very
// large number of live projects still makes steady progress instead of
// building an enormous batch.
const maxFlushProjects = 100

// errRefBusy says a sync worker holds the ref this flush would have indexed.
// It is a deferral, not a failure: the commit is already in place and the
// points are still buffered, so the only thing to do is come back next poll.
var errRefBusy = errors.New("syncer: the ref is being indexed by a sync job")

// EnableFlush turns on the periodic metrics flush. interval is the maximum
// time a still-running project's points stay database-only; a run that has
// reached a terminal status is flushed on the next poll regardless. Calling
// it with a nil flusher leaves the flush disabled, which is what the sync
// tests that only exercise the push pipeline want.
func (s *Syncer) EnableFlush(f *experiments.Flusher, interval time.Duration) {
	if f == nil {
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	s.flusher = f
	s.flushInterval = interval
}

// RunFlush drives the periodic flush until ctx is cancelled. It is a separate
// goroutine from the sync worker pool: a flush must not sit behind a slow
// export, and it takes no sync_jobs row, so two replicas racing on the same
// project resolve it through the commit's path precondition rather than
// through the job queue.
func (s *Syncer) RunFlush(ctx context.Context) {
	if s.flusher == nil {
		return
	}
	ticker := time.NewTicker(flushPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := s.flushDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("flush experiment metrics", "error", err)
		}
	}
}

// flushDue flushes every project whose buffer is due: one that has waited out
// the flush interval, and one holding points for a run that already finished
// or failed.
func (s *Syncer) flushDue(ctx context.Context) error {
	pending, err := s.store.ListPendingFlushProjects(ctx, maxFlushProjects)
	if err != nil {
		return err
	}
	now := time.Now()

	s.flushMu.Lock()
	due := make([]store.PendingFlush, 0, len(pending))
	seen := make(map[int64]bool, len(pending))
	for _, p := range pending {
		seen[p.ProjectID] = true
		last, known := s.lastFlush[p.ProjectID]
		if p.Terminal || !known || now.Sub(last) >= s.flushInterval {
			due = append(due, p)
		}
	}
	// Projects whose buffer is now empty must not keep an entry forever: the
	// map would grow with every project the instance ever saw.
	for id := range s.lastFlush {
		if !seen[id] {
			delete(s.lastFlush, id)
		}
	}
	s.flushMu.Unlock()

	for _, p := range due {
		if err := s.FlushProject(ctx, p.RepoID, p.ProjectID, p.Project); err != nil {
			// A ref a sync job is holding is the ordinary outcome of pushing
			// to a project while it flushes, not something an operator has to
			// see. Leaving the project unstamped is what brings it back on the
			// next poll.
			if !errors.Is(err, errRefBusy) {
				// One project's failure (a stale precondition, a schema this
				// package cannot rewrite) must not stop the others. The points
				// stay buffered and the next poll tries again.
				slog.Warn("flush experiment project", "repo_id", p.RepoID,
					"project", p.Project, "points", p.NumPoints, "error", err)
			}
			continue
		}
		if p.NumPoints > experiments.MaxFlushPoints {
			// The buffer was larger than one flush moves, so the rest is due
			// right now rather than an interval from now: leaving the project
			// unstamped makes the next poll pick it up again.
			continue
		}
		s.flushMu.Lock()
		s.lastFlush[p.ProjectID] = time.Now()
		s.flushMu.Unlock()
	}
	return nil
}

// FlushProject writes one project's buffered points into its dataset
// repository and drops them from the database. The three steps are ordered so
// that no state in between loses a point from the chart:
//
//  1. commit the parquet, which is now the durable copy;
//  2. re-index the repository, so Series can see the new rows (the layout is
//     detected from repo_files, which only this step refreshes);
//  3. delete the buffered rows.
//
// A crash between 1 and 2 leaves the points buffered and the commit in place;
// the retry recognises what it already wrote through the ingest-id column and
// appends nothing twice. A crash between 2 and 3 leaves rows that are already
// in the parquet, which the same check removes on the retry.
//
// All three run under the ref's lock (reflock.go), taken before step 1 rather
// than around step 2 alone. The lock is what stops a sync worker that read the
// tree first from writing its older index over this one -- and taking it early
// is what keeps step 1 from happening at all when the ref is busy. Committing
// the parquet and then giving up would leave the file in git but out of
// repo_files until the next poll, which is the very gap Series reads
// repo_files to close.
func (s *Syncer) FlushProject(ctx context.Context, repoID, projectID int64, project string) error {
	if s.flusher == nil {
		return nil
	}
	repo, err := s.store.GetRepoByID(ctx, repoID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if repo.Archived() {
		return nil
	}

	// Taken without waiting: flushDue walks its candidates one at a time, so
	// blocking here would stall every other project behind whichever
	// repository is mid-publish. Nothing is lost by giving up before the
	// commit -- the points are untouched and the next poll, ten seconds
	// later, tries the whole thing again.
	//
	// The ref is the one the flusher will commit to, asked of the flusher
	// rather than re-derived here: a second copy that drifted would take the
	// lock under one key and commit under another. The check after Flush is
	// the belt to that braces.
	ref := experiments.FlushRef(repo)
	held, ok := s.refLocks.tryLock(repo.ID, ref)
	if !ok {
		return errRefBusy
	}
	defer held.unlock()

	result, err := s.flusher.Flush(ctx, repo, projectID, project)
	if err != nil {
		if errors.Is(err, gitrepo.ErrRepoNotFound) {
			// Nothing to commit into; leave the points where they are rather
			// than dropping data on the floor.
			return nil
		}
		return err
	}
	if result == nil {
		return nil
	}

	if result.Ref != ref {
		return fmt.Errorf("syncer: flush committed to %q but the ref lock is held for %q", result.Ref, ref)
	}

	if _, err := s.runPushPipeline(ctx, repo, &store.SyncJob{
		RepoID: repo.ID, Ref: result.Ref, OldSHA: result.OldSHA, NewSHA: result.NewSHA,
	}, held); err != nil {
		return err
	}
	if err := s.store.DeletePoints(ctx, result.PointIDs); err != nil {
		return err
	}

	slog.Info("flushed experiment metrics", "repo", repo.FullName(), "project", project,
		"path", result.Path, "points", len(result.PointIDs), "appended", result.NumAppended,
		"commit", result.NewSHA)
	return nil
}
