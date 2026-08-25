package store

import (
	"testing"
)

// The flush poller takes a bounded window of candidates every tick, so the
// order ListPendingFlushProjects returns them in decides which projects ever
// get written to parquet at all. Ordering by project id ranks them by creation
// time, which a project that keeps ingesting never loses -- so the projects
// created after it are starved, their exp_points grow without bound, and the
// live chart (which reads every unflushed point) grows with them. Ordering by
// the oldest unflushed point ranks them by how long they have waited, which
// flushing resets and skipping only improves.
func TestIntegrationListPendingFlushProjectsPrefersTheLongestWait(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		repo := f.repo(t, "alice", "runs", "dataset", nil)

		// "busy" is created first, so it holds the lower project id and is
		// what the old ordering handed back on every single poll.
		busy := newFlushProject(t, f, repo.ID, "busy")
		quiet := newFlushProject(t, f, repo.ID, "quiet")

		busy.log(t, 1)
		quiet.log(t, 1)

		// Nothing has been flushed yet, so the longest wait really is busy's.
		if got := pendingNames(t, s, 1); !equalStrings(got, []string{"busy"}) {
			t.Fatalf("first poll = %v, want [busy]", got)
		}

		// busy is flushed and immediately logs again, exactly as a live run
		// does between two ticks. Its wait restarts; quiet's does not.
		busy.flush(t, s)
		busy.log(t, 2)

		if got := pendingNames(t, s, 1); !equalStrings(got, []string{"quiet"}) {
			t.Fatalf("second poll = %v, want [quiet]: the busy project starved it", got)
		}

		// And once quiet is drained the queue hands the turn back rather than
		// sticking to whichever project was last served.
		quiet.flush(t, s)
		if got := pendingNames(t, s, 1); !equalStrings(got, []string{"busy"}) {
			t.Fatalf("third poll = %v, want [busy]", got)
		}

		// A window wide enough for both still reports both, oldest first.
		if got := pendingNames(t, s, 10); !equalStrings(got, []string{"busy"}) {
			t.Fatalf("wide poll = %v, want [busy]: quiet has nothing pending", got)
		}
	})
}

// flushProject is one project plus the single run its points hang off, which
// is all the flush queue looks at.
type flushProject struct {
	id    int64
	runID int64
	name  string
	f     *fixture
	step  int64
}

func newFlushProject(t *testing.T, f *fixture, repoID int64, name string) *flushProject {
	t.Helper()
	id, err := f.s.UpsertExpProject(f.ctx, repoID, name)
	if err != nil {
		t.Fatalf("create project %s: %v", name, err)
	}
	runID, err := f.s.UpsertExpRun(f.ctx, id, "run-1", "running", nil, nil, nil, 0, 0, nil)
	if err != nil {
		t.Fatalf("create run for %s: %v", name, err)
	}
	return &flushProject{id: id, runID: runID, name: name, f: f}
}

// log appends n live points, the way an ingest request does.
func (p *flushProject) log(t *testing.T, n int) {
	t.Helper()
	points := make([]MetricPoint, 0, n)
	for range n {
		p.step++
		points = append(points, MetricPoint{Step: p.step, Metrics: map[string]float64{"loss": 0.5}})
	}
	if err := p.f.s.InsertPoints(p.f.ctx, p.runID, points); err != nil {
		t.Fatalf("insert points for %s: %v", p.name, err)
	}
}

// flush stands in for a completed flush: the points that were written to
// parquet are deleted by id, which is exactly what the flusher does.
func (p *flushProject) flush(t *testing.T, s *Store) {
	t.Helper()
	points, err := s.ListProjectPoints(p.f.ctx, p.id, 0)
	if err != nil {
		t.Fatalf("list points for %s: %v", p.name, err)
	}
	ids := make([]int64, 0, len(points))
	for _, pt := range points {
		ids = append(ids, pt.ID)
	}
	if err := s.DeletePoints(p.f.ctx, ids); err != nil {
		t.Fatalf("delete points for %s: %v", p.name, err)
	}
}

func pendingNames(t *testing.T, s *Store, limit int) []string {
	t.Helper()
	pending, err := s.ListPendingFlushProjects(t.Context(), limit)
	if err != nil {
		t.Fatalf("list pending flush projects: %v", err)
	}
	out := make([]string, 0, len(pending))
	for _, p := range pending {
		out = append(out, p.Project)
	}
	return out
}
