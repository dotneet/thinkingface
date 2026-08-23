package store

import (
	"fmt"
	"testing"
)

// TestIntegrationExpRunModels covers the run-side half of the lineage index:
// what a training script declared it produced (`trackio.log_model`), read both
// from the run and from the model it names.
//
// The invariants it pins down are the ones the feature rests on:
//   - the list is an annotation, so UpsertExpRun (a re-index) must not touch it;
//   - a declaration to a repository that does not exist, or that the viewer may
//     not read, is kept and reported as dangling rather than dropped;
//   - the reverse lookup only shows runs in experiment repositories the viewer
//     may read.
func TestIntegrationExpRunModels(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		exp := f.repo(t, "alice", "exp", "dataset", nil)
		otherExp := f.repo(t, "bob", "other-exp", "dataset", nil)
		f.repo(t, "alice", "bert-ja", "model", nil)
		f.repo(t, "bob", "hidden-model", "model", nil)
		// A *dataset* of the same name must not satisfy a model reference.
		f.repo(t, "alice", "not-a-model", "dataset", nil)

		pid, err := s.UpsertExpProject(ctx, exp.ID, "proj")
		if err != nil {
			t.Fatalf("UpsertExpProject: %v", err)
		}
		if _, err := s.UpsertExpRun(ctx, pid, "run-a", "running", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatalf("UpsertExpRun: %v", err)
		}
		if _, err := s.UpsertExpRun(ctx, pid, "run-b", "running", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatalf("UpsertExpRun: %v", err)
		}

		models := []ExpRunModel{
			{Raw: "alice/bert-ja@abc123", Namespace: "alice", Name: "bert-ja", Revision: "abc123"},
			{Raw: "alice/ghost", Namespace: "alice", Name: "ghost"},
			{Raw: "bob/hidden-model", Namespace: "bob", Name: "hidden-model"},
			{Raw: "alice/not-a-model", Namespace: "alice", Name: "not-a-model"},
		}
		if _, err := s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Models: &models}); err != nil {
			t.Fatalf("UpdateExpRunAnnotation(models): %v", err)
		}

		describe := func(byRun map[string][]ExpRunModel, run string) []string {
			var out []string
			for _, m := range byRun[run] {
				out = append(out, fmt.Sprintf("%s@%s:%v", m.FullName(), m.Revision, m.Exists))
			}
			return out
		}

		byRun, err := s.ListRunModels(ctx, pid)
		if err != nil {
			t.Fatalf("ListRunModels: %v", err)
		}
		want := []string{
			"alice/bert-ja@abc123:true",
			"alice/ghost@:false",
			"bob/hidden-model@:true",
			// A dataset of the same name is not a model.
			"alice/not-a-model@:false",
		}
		if got := describe(byRun, "run-a"); !equalStrings(got, want) {
			t.Fatalf("ListRunModels = %v, want %v", got, want)
		}
		if len(byRun["run-b"]) != 0 {
			t.Fatalf("run-b should have no models, got %v", byRun["run-b"])
		}

		// A re-index must not disturb the annotation.
		if _, err := s.UpsertExpRun(ctx, pid, "run-a", "finished", nil, nil, nil, 9, 90, nil); err != nil {
			t.Fatal(err)
		}
		byRun, _ = s.ListRunModels(ctx, pid)
		if got := describe(byRun, "run-a"); !equalStrings(got, want) {
			t.Fatalf("after re-index = %v, want %v", got, want)
		}

		// ------------------------------------------------- reverse lookup
		producers, err := s.ListModelProducers(ctx, "alice", "bert-ja")
		if err != nil {
			t.Fatalf("ListModelProducers: %v", err)
		}
		if len(producers) != 1 {
			t.Fatalf("ListModelProducers = %d rows, want 1", len(producers))
		}
		p := producers[0]
		if p.Repo.FullName() != "alice/exp" || p.Project != "proj" || p.Run != "run-a" || p.Revision != "abc123" {
			t.Fatalf("producer = %+v", p)
		}
		if rows, _ := s.ListModelProducers(ctx, "alice", "nobody-made-this"); len(rows) != 0 {
			t.Fatalf("unclaimed model has producers: %v", rows)
		}

		// A run in another namespace's experiment repository is a producer
		// just the same.
		privPID, err := s.UpsertExpProject(ctx, otherExp.ID, "hush")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertExpRun(ctx, privPID, "run-x", "running", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatal(err)
		}
		hidden := []ExpRunModel{{Raw: "alice/bert-ja", Namespace: "alice", Name: "bert-ja"}}
		if _, err := s.UpdateExpRunAnnotation(ctx, privPID, "run-x", RunAnnotation{Models: &hidden}); err != nil {
			t.Fatal(err)
		}
		if rows, _ := s.ListModelProducers(ctx, "alice", "bert-ja"); len(rows) != 2 {
			t.Fatalf("ListModelProducers = %d rows, want 2", len(rows))
		}

		// ------------------------------------------------- replace and clear
		one := []ExpRunModel{{Raw: "alice/bert-ja", Namespace: "alice", Name: "bert-ja"}}
		if _, err := s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Models: &one}); err != nil {
			t.Fatal(err)
		}
		byRun, _ = s.ListRunModels(ctx, pid)
		if got := describe(byRun, "run-a"); !equalStrings(got, []string{"alice/bert-ja@:true"}) {
			t.Fatalf("after replace = %v", got)
		}
		// Another annotation field on its own must leave the list alone.
		if _, err := s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Note: ptr("hi")}); err != nil {
			t.Fatal(err)
		}
		byRun, _ = s.ListRunModels(ctx, pid)
		if len(byRun["run-a"]) != 1 {
			t.Fatalf("a note-only PATCH changed the model list: %v", byRun["run-a"])
		}
		empty := []ExpRunModel{}
		if _, err := s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Models: &empty}); err != nil {
			t.Fatal(err)
		}
		byRun, _ = s.ListRunModels(ctx, pid)
		if len(byRun["run-a"]) != 0 {
			t.Fatalf("an empty list should clear the models, got %v", byRun["run-a"])
		}
		// bob/other-exp's run-x still declares it, so only alice/exp's claim
		// goes away.
		if rows, _ := s.ListModelProducers(ctx, "alice", "bert-ja"); len(rows) != 1 {
			t.Fatalf("clearing one run's models = %d producers, want 1: %v", len(rows), rows)
		}

		// Deleting the run takes its declarations with it.
		if _, err := s.UpdateExpRunAnnotation(ctx, pid, "run-b", RunAnnotation{Models: &one}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteExpRun(ctx, pid, "run-b"); err != nil {
			t.Fatal(err)
		}
		if rows, _ := s.ListModelProducers(ctx, "alice", "bert-ja"); len(rows) != 1 {
			t.Fatalf("a deleted run still claims a model: %v", rows)
		}
	})
}

// TestRunAnnotationIsEmpty guards the "at least one field" check the PATCH
// handler makes: a body carrying only `models` is a real update.
func TestRunAnnotationIsEmpty(t *testing.T) {
	if !(RunAnnotation{}).IsEmpty() {
		t.Fatal("the zero annotation should be empty")
	}
	if (RunAnnotation{Models: &[]ExpRunModel{}}).IsEmpty() {
		t.Fatal("a models-only update should not count as empty")
	}
}
