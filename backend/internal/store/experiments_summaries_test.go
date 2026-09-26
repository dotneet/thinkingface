package store

import (
	"errors"
	"testing"
	"time"
)

// TestIntegrationExpRunSummaryMinMaxHeartbeat pins down the columns added for
// docs/dev/agent-features.md §2.1 / §2.6: summary_min / summary_max follow the
// same "nil keeps what is stored" rule as summary, and heartbeat_secs keeps
// its value when a write carries 0 (the parquet indexer never knows it).
func TestIntegrationExpRunSummaryMinMaxHeartbeat(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		exp := f.repo(t, "alice", "exp", "dataset", nil)
		pid, err := s.UpsertExpProject(ctx, exp.ID, "p")
		if err != nil {
			t.Fatalf("UpsertExpProject: %v", err)
		}

		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{
			Name: "r", Status: "running",
			Summary:       map[string]any{"loss": 0.5},
			SummaryMin:    map[string]any{"loss": 0.4},
			SummaryMax:    map[string]any{"loss": 2.0},
			HeartbeatSecs: 30,
		}); err != nil {
			t.Fatalf("UpsertExpRunWith: %v", err)
		}
		run, err := s.GetExpRun(ctx, pid, "r")
		if err != nil {
			t.Fatalf("GetExpRun: %v", err)
		}
		if run.SummaryMin["loss"] != 0.4 || run.SummaryMax["loss"] != 2.0 || run.HeartbeatSecs != 30 {
			t.Fatalf("min/max/heartbeat = %v/%v/%d", run.SummaryMin, run.SummaryMax, run.HeartbeatSecs)
		}

		// A write that knows none of them (the indexer's positional form, or a
		// status-only upsert) keeps all three.
		if _, err := s.UpsertExpRun(ctx, pid, "r", "", nil, nil, nil, 10, 10, nil); err != nil {
			t.Fatalf("UpsertExpRun: %v", err)
		}
		run, err = s.GetExpRun(ctx, pid, "r")
		if err != nil {
			t.Fatalf("GetExpRun: %v", err)
		}
		if run.SummaryMin["loss"] != 0.4 || run.SummaryMax["loss"] != 2.0 || run.HeartbeatSecs != 30 {
			t.Fatalf("lost on keep-write: %v/%v/%d", run.SummaryMin, run.SummaryMax, run.HeartbeatSecs)
		}

		// A new heartbeat replaces the old one.
		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{Name: "r", HeartbeatSecs: 5}); err != nil {
			t.Fatalf("UpsertExpRunWith: %v", err)
		}
		run, err = s.GetExpRun(ctx, pid, "r")
		if err != nil {
			t.Fatalf("GetExpRun: %v", err)
		}
		if run.HeartbeatSecs != 5 {
			t.Fatalf("heartbeat = %d, want 5", run.HeartbeatSecs)
		}

		// A run created without them reads empty maps and 0, never nil.
		if _, err := s.UpsertExpRun(ctx, pid, "fresh", "running", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatalf("UpsertExpRun: %v", err)
		}
		runs, err := s.ListExpRuns(ctx, pid)
		if err != nil {
			t.Fatalf("ListExpRuns: %v", err)
		}
		for _, r := range runs {
			if r.SummaryMin == nil || r.SummaryMax == nil {
				t.Fatalf("run %q has nil min/max maps", r.Name)
			}
			if r.Name == "fresh" && (len(r.SummaryMin) != 0 || r.HeartbeatSecs != 0) {
				t.Fatalf("fresh run = %v / %d", r.SummaryMin, r.HeartbeatSecs)
			}
		}

		// The annotation path returns runColumns too.
		note := "n"
		annotated, err := s.UpdateExpRunAnnotation(ctx, pid, "r", RunAnnotation{Note: &note})
		if err != nil {
			t.Fatalf("UpdateExpRunAnnotation: %v", err)
		}
		if annotated.SummaryMax["loss"] != 2.0 || annotated.HeartbeatSecs != 5 {
			t.Fatalf("annotation returned %v / %d", annotated.SummaryMax, annotated.HeartbeatSecs)
		}
	})
}

// TestIntegrationExpRunUpdatedAtIsAHeartbeat pins ExpRunUpsert.Touch:
// updated_at moves on a write that is a sign of life from the run (Touch) or
// that carries a step past the stored one, and on nothing else. The parquet
// indexer re-upserts every run of a repository whenever any of them flushes;
// before Touch, that kept a crashed run's updated_at moving and it never went
// stale.
func TestIntegrationExpRunUpdatedAtIsAHeartbeat(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		exp := f.repo(t, "alice", "exp", "dataset", nil)
		pid, err := s.UpsertExpProject(ctx, exp.ID, "p")
		if err != nil {
			t.Fatalf("UpsertExpProject: %v", err)
		}
		longAgo := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		backdate := func() {
			t.Helper()
			if _, err := s.db.Exec(ctx, `UPDATE exp_runs SET updated_at = $1 WHERE project_id = $2 AND name = $3`,
				longAgo, pid, "r"); err != nil {
				t.Fatalf("backdate: %v", err)
			}
		}
		updatedAt := func() time.Time {
			t.Helper()
			run, err := s.GetExpRun(ctx, pid, "r")
			if err != nil {
				t.Fatalf("GetExpRun: %v", err)
			}
			return run.UpdatedAt
		}
		moved := func(ts time.Time) bool { return time.Since(ts) < time.Minute }

		// An insert takes the column default whatever Touch says.
		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{Name: "r", Status: "running", LastStep: 5}); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if ts := updatedAt(); !moved(ts) {
			t.Fatalf("a fresh run's updated_at = %v, want now", ts)
		}

		// A re-index at the same step, or behind it, is not a sign of life.
		backdate()
		for _, step := range []int64{5, 3, 0} {
			if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{
				Name: "r", Summary: map[string]any{"loss": 0.1}, LastStep: step, NumPoints: 5,
			}); err != nil {
				t.Fatalf("untouched upsert: %v", err)
			}
			if ts := updatedAt(); !ts.Equal(longAgo) {
				t.Fatalf("untouched upsert at step %d moved updated_at to %v", step, ts)
			}
		}

		// A parquet that grew past the stored step is.
		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{Name: "r", LastStep: 6}); err != nil {
			t.Fatalf("growth upsert: %v", err)
		}
		if ts := updatedAt(); !moved(ts) {
			t.Fatalf("a write past the stored step left updated_at at %v", ts)
		}

		// So is any ingest write, even one carrying no step at all (a ping).
		backdate()
		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{Name: "r", Touch: true}); err != nil {
			t.Fatalf("touch upsert: %v", err)
		}
		if ts := updatedAt(); !moved(ts) {
			t.Fatalf("a touching write left updated_at at %v", ts)
		}

		// The positional form stands in for ingest and touches.
		backdate()
		if _, err := s.UpsertExpRun(ctx, pid, "r", "", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatalf("UpsertExpRun: %v", err)
		}
		if ts := updatedAt(); !moved(ts) {
			t.Fatalf("the positional upsert left updated_at at %v", ts)
		}
	})
}

// TestIntegrationExpProjectGoals covers MergeExpProjectGoals: it creates the
// project when needed, merges key by key, and "" removes a goal.
func TestIntegrationExpProjectGoals(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		exp := f.repo(t, "alice", "exp", "dataset", nil)

		p, err := s.MergeExpProjectGoals(ctx, exp.ID, "new-project", map[string]string{"loss": "min", "acc": "max"}, 0)
		if err != nil {
			t.Fatalf("MergeExpProjectGoals (create): %v", err)
		}
		if p.Name != "new-project" || p.NumRuns != 0 || p.MetricGoals["loss"] != "min" || p.MetricGoals["acc"] != "max" {
			t.Fatalf("created project = %+v", p)
		}

		p, err = s.MergeExpProjectGoals(ctx, exp.ID, "new-project", map[string]string{"acc": "", "cer": "min"}, 3)
		if err != nil {
			t.Fatalf("MergeExpProjectGoals (merge): %v", err)
		}
		want := map[string]string{"loss": "min", "cer": "min"}
		if len(p.MetricGoals) != len(want) {
			t.Fatalf("goals = %v, want %v", p.MetricGoals, want)
		}
		for k, v := range want {
			if p.MetricGoals[k] != v {
				t.Fatalf("goals = %v, want %v", p.MetricGoals, want)
			}
		}

		// A merge past the cap is refused and changes nothing.
		if _, err := s.MergeExpProjectGoals(ctx, exp.ID, "new-project",
			map[string]string{"a": "min", "b": "max"}, 3); !errors.Is(err, ErrTooManyMetricGoals) {
			t.Fatalf("merge past the cap: err = %v, want ErrTooManyMetricGoals", err)
		}

		got, err := s.GetExpProject(ctx, exp.ID, "new-project")
		if err != nil {
			t.Fatalf("GetExpProject: %v", err)
		}
		if len(got.MetricGoals) != 2 || got.MetricGoals["cer"] != "min" || got.MetricGoals["acc"] != "" {
			t.Fatalf("GetExpProject goals = %v", got.MetricGoals)
		}
		list, err := s.ListExpProjects(ctx, exp.ID)
		if err != nil {
			t.Fatalf("ListExpProjects: %v", err)
		}
		if len(list) != 1 || list[0].MetricGoals["loss"] != "min" {
			t.Fatalf("ListExpProjects = %+v", list)
		}

		// A project that never set goals reads an empty, non-nil map.
		if _, err := s.UpsertExpProject(ctx, exp.ID, "plain"); err != nil {
			t.Fatalf("UpsertExpProject: %v", err)
		}
		plain, err := s.GetExpProject(ctx, exp.ID, "plain")
		if err != nil {
			t.Fatalf("GetExpProject(plain): %v", err)
		}
		if plain.MetricGoals == nil || len(plain.MetricGoals) != 0 {
			t.Fatalf("plain goals = %#v", plain.MetricGoals)
		}
	})
}
