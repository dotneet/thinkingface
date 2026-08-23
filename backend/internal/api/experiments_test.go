// HTTP-level tests for the live ingest API (POST .../log), driven against a
// real Server the way archive_test.go and transfers_test.go are.
//
// Two invariants live here:
//
//   - a run's summary is *merged* across batches, exactly as metric_keys is.
//     A training loop that logs "loss" every step and "accuracy" only at
//     evaluation time must not lose the accuracy in between, and a status
//     ping carrying no points at all must not empty the summary;
//   - the sweep grouping a run declared at init() survives every later batch
//     that does not repeat it.

package api

import (
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// expFixture is archiveFixture under a name that fits this file: it already
// builds a real Server over a real SQLite store with users and tokens, which
// is all an ingest test needs.
type expFixture = archiveFixture

func newExpFixture(t *testing.T) *expFixture {
	t.Helper()
	return newArchiveFixture(t)
}

// logBatch posts one ingest batch and fails on anything but 200.
func (f *expFixture) logBatch(token string, body map[string]any) {
	f.t.Helper()
	resp := f.do("POST", "/api/v1/experiments/alice/exp/proj/log", token, body)
	if resp.status() != 200 {
		f.t.Fatalf("log status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
}

// runNamed reads the project's run list back through the API.
func (f *expFixture) runNamed(t *testing.T, token, name string) apitypes.ExpRun {
	t.Helper()
	resp := f.do("GET", "/api/v1/experiments/alice/exp/proj/runs", token, nil)
	if resp.status() != 200 {
		t.Fatalf("runs status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.ExpRunListResponse
	resp.json(t, &body)
	for _, run := range body.Runs {
		if run.Name == name {
			return run
		}
	}
	t.Fatalf("run %q not in %+v", name, body.Runs)
	return apitypes.ExpRun{}
}

func point(step int, metrics map[string]any) map[string]any {
	return map[string]any{"step": step, "metrics": metrics}
}

func TestExperimentLog_SummaryMergesAcrossBatches(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	// An evaluation step logs both metrics...
	f.logBatch(tok, map[string]any{
		"run":    "run-1",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.5, "accuracy": 0.8})},
	})
	// ...and the training steps that follow carry only the loss.
	f.logBatch(tok, map[string]any{
		"run":    "run-1",
		"points": []map[string]any{point(2, map[string]any{"loss": 0.4})},
	})

	run := f.runNamed(t, tok, "run-1")
	if got, ok := run.Summary["accuracy"]; !ok || got != 0.8 {
		t.Fatalf("summary lost accuracy after a partial batch: %+v", run.Summary)
	}
	if got := run.Summary["loss"]; got != 0.4 {
		t.Fatalf("summary loss = %v, want the newest value 0.4 (%+v)", got, run.Summary)
	}
	// metric_keys was already merged; it must stay that way.
	if len(run.MetricKeys) != 2 {
		t.Fatalf("metric_keys = %v, want both keys", run.MetricKeys)
	}

	// A batch with no points at all (the shim's status ping) must leave the
	// summary as it is rather than replacing it with an empty object.
	f.logBatch(tok, map[string]any{"run": "run-1", "status": "finished", "points": []map[string]any{}})

	run = f.runNamed(t, tok, "run-1")
	if len(run.Summary) != 2 || run.Summary["loss"] != 0.4 || run.Summary["accuracy"] != 0.8 {
		t.Fatalf("empty batch cleared the summary: %+v", run.Summary)
	}
	if run.Status != apitypes.RunStatusFinished {
		t.Fatalf("status = %q, want finished", run.Status)
	}
}

func TestExperimentLog_GroupingIsRecordedAndKept(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	f.logBatch(tok, map[string]any{
		"run": "lr-0.1", "group": "lr-sweep", "job_type": "train",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
	})
	run := f.runNamed(t, tok, "lr-0.1")
	if run.Group != "lr-sweep" || run.JobType != "train" {
		t.Fatalf("group/job_type = %q/%q, want lr-sweep/train", run.Group, run.JobType)
	}

	// Every later batch may omit them; the run stays in its sweep.
	f.logBatch(tok, map[string]any{
		"run":    "lr-0.1",
		"points": []map[string]any{point(2, map[string]any{"loss": 0.4})},
	})
	run = f.runNamed(t, tok, "lr-0.1")
	if run.Group != "lr-sweep" || run.JobType != "train" {
		t.Fatalf("grouping lost on a later batch: %q/%q", run.Group, run.JobType)
	}

	// A run that declares nothing stays flat -- the backwards-compatible case.
	f.logBatch(tok, map[string]any{
		"run":    "solo",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.9})},
	})
	solo := f.runNamed(t, tok, "solo")
	if solo.Group != "" || solo.JobType != "" {
		t.Fatalf("ungrouped run reported group/job_type %q/%q", solo.Group, solo.JobType)
	}
}

func TestExperimentLog_RejectsControlCharactersInGrouping(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/experiments/alice/exp/proj/log", tok, map[string]any{
		"run": "run-1", "group": "lr\nsweep",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
	})
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
	}
}

// TestListExperiments_Search covers the `search` query parameter added
// alongside pagination for the global /experiments page: the endpoint caps
// results at 100 (handleListExperiments), so once a server holds more
// experiment repositories than that, search is the only way left to reach
// one that isn't on the first page.
func TestListExperiments_Search(t *testing.T) {
	f := newTransferFixture(t)
	markExperiment(t, f, f.repo("alice", "bert-finetune", "dataset"))
	markExperiment(t, f, f.repo("alice", "bert-eval", "dataset"))
	markExperiment(t, f, f.repo("bob", "resnet-runs", "dataset"))

	list := func(query string) apitypes.ExpProjectListResponse {
		t.Helper()
		resp := f.do("GET", "/api/v1/experiments"+query, "", nil)
		if resp.status() != 200 {
			t.Fatalf("GET /experiments%s = %d", query, resp.status())
		}
		var body apitypes.ExpProjectListResponse
		resp.json(t, &body)
		return body
	}

	got := list("?search=bert")
	if len(got.Items) != 2 || got.Total != 2 {
		t.Fatalf("search=bert = %d items, total %d, want 2/2", len(got.Items), got.Total)
	}
	for _, item := range got.Items {
		if item.Namespace != "alice" {
			t.Fatalf("search=bert returned %s", item.FullName)
		}
	}
	// A search combined with author narrows further, same as any other
	// store.RepoFilter combination.
	if narrowed := list("?search=bert&author=alice"); len(narrowed.Items) != 2 || narrowed.Total != 2 {
		t.Fatalf("search=bert&author=alice = %d items, total %d, want 2/2",
			len(narrowed.Items), narrowed.Total)
	}
	if none := list("?search=nonexistentterm"); len(none.Items) != 0 || none.Total != 0 {
		t.Fatalf("search=nonexistentterm = %+v", none)
	}
}
