package store

import "testing"

// TestIntegrationExpRunGrouping pins down the sweep grouping columns
// (`init(group=..., job_type=...)`): they are written by ingest, but with the
// "an omitted value is kept" rule every other ingest column follows, so
// neither a later batch nor a re-index of the project's parquet -- which
// knows nothing about them -- can pull a run out of its group.
func TestIntegrationExpRunGrouping(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		exp := f.repo(t, "alice", "exp", "dataset", nil)
		pid, err := s.UpsertExpProject(ctx, exp.ID, "sweep")
		if err != nil {
			t.Fatalf("UpsertExpProject: %v", err)
		}

		group, jobType := "lr-sweep", "train"
		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{
			Name: "run-a", Status: "running", Group: &group, JobType: &jobType,
		}); err != nil {
			t.Fatalf("UpsertExpRunWith: %v", err)
		}
		run, err := s.GetExpRun(ctx, pid, "run-a")
		if err != nil {
			t.Fatalf("GetExpRun: %v", err)
		}
		if run.Group != "lr-sweep" || run.JobType != "train" {
			t.Fatalf("group/job_type = %q/%q, want lr-sweep/train", run.Group, run.JobType)
		}

		// A later batch that says nothing about the grouping keeps it, and so
		// does the positional UpsertExpRun the parquet indexer calls.
		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{Name: "run-a", Status: "finished"}); err != nil {
			t.Fatalf("UpsertExpRunWith (no grouping): %v", err)
		}
		if _, err := s.UpsertExpRun(ctx, pid, "run-a", "", nil, nil, nil, 10, 10, nil); err != nil {
			t.Fatalf("UpsertExpRun (re-index): %v", err)
		}
		run, err = s.GetExpRun(ctx, pid, "run-a")
		if err != nil {
			t.Fatalf("GetExpRun after re-index: %v", err)
		}
		if run.Group != "lr-sweep" || run.JobType != "train" {
			t.Fatalf("grouping lost on re-index: %q/%q", run.Group, run.JobType)
		}

		// A run that never declared one reads as "" -- the flat, ungrouped
		// case every run logged before this column existed falls into.
		if _, err := s.UpsertExpRun(ctx, pid, "run-b", "running", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatalf("UpsertExpRun (ungrouped): %v", err)
		}
		runs, err := s.ListExpRuns(ctx, pid)
		if err != nil {
			t.Fatalf("ListExpRuns: %v", err)
		}
		if len(runs) != 2 {
			t.Fatalf("listed %d runs, want 2", len(runs))
		}
		for _, r := range runs {
			if r.Name == "run-b" && (r.Group != "" || r.JobType != "") {
				t.Fatalf("ungrouped run has group/job_type %q/%q", r.Group, r.JobType)
			}
		}

		// Changing the grouping is possible -- a re-run under a new sweep
		// name reuses the run row -- so a non-empty value does overwrite.
		other := "seed-sweep"
		if _, err := s.UpsertExpRunWith(ctx, pid, ExpRunUpsert{Name: "run-a", Group: &other}); err != nil {
			t.Fatalf("UpsertExpRunWith (regroup): %v", err)
		}
		run, err = s.GetExpRun(ctx, pid, "run-a")
		if err != nil {
			t.Fatalf("GetExpRun after regroup: %v", err)
		}
		if run.Group != "seed-sweep" || run.JobType != "train" {
			t.Fatalf("group/job_type = %q/%q, want seed-sweep/train", run.Group, run.JobType)
		}
	})
}
