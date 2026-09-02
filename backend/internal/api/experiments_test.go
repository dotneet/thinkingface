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
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/store"
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

// TestExperimentLog_RejectsMetricNamedAfterAStructuralColumn pins the reserved
// names. A point's metrics and its structural fields share one row of the
// metrics parquet (experiments.mergePoints), so a metric called "run_name"
// would be written into the file's own run column: after the next re-index the
// run would be called "0.5" and would have lost every point it logged in that
// window. "_ingest_id" is worse still -- it is the key that keeps a flush
// retried after a crash from writing the same points twice.
func TestExperimentLog_RejectsMetricNamedAfterAStructuralColumn(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	for _, name := range []string{"run_name", "step", "timestamp", "_ingest_id"} {
		resp := f.do("POST", "/api/v1/experiments/alice/exp/proj/log", tok, map[string]any{
			"run":    "run-1",
			"points": []map[string]any{point(1, map[string]any{name: 0.5})},
		})
		if resp.status() != 400 {
			t.Errorf("metric %q status = %d, want 400 (body %s)", name, resp.status(), resp.rec.Body.String())
		}
	}

	// An ordinary metric is unaffected.
	f.logBatch(tok, map[string]any{
		"run":    "run-1",
		"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
	})
}

// TestExperimentIngest_RejectsAProjectNameThatCannotBeCommitted stops a name
// that would buffer fine and then fail every flush forever: the flush writes
// "{project}/metrics.parquet", and Commit refuses ".", ".." and ".git"
// segments, so such a project would sit in the flush queue warning every ten
// seconds and holding one of the poller's hundred slots.
func TestExperimentIngest_RejectsAProjectNameThatCannotBeCommitted(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	for _, project := range []string{".git", ".GIT", "..", "."} {
		body := map[string]any{
			"run":    "run-1",
			"points": []map[string]any{point(1, map[string]any{"loss": 0.5})},
		}
		resp := f.do("POST", "/api/v1/experiments/alice/exp/"+project+"/log", tok, body)
		if resp.status() != 400 {
			t.Errorf("log into project %q status = %d, want 400 (body %s)",
				project, resp.status(), resp.rec.Body.String())
		}
		// finish creates the project too, so it needs the same guard.
		resp = f.do("POST", "/api/v1/experiments/alice/exp/"+project+"/finish", tok,
			map[string]any{"run": "run-1"})
		if resp.status() != 400 {
			t.Errorf("finish in project %q status = %d, want 400 (body %s)",
				project, resp.status(), resp.rec.Body.String())
		}
	}
}

// maxIngestKeys is a ceiling on the run, not on one request. It used to be
// checked against the batch's own names only -- the stored ones were merged
// in afterwards, and never re-checked -- so a client could walk past it a
// batch at a time: two batches of 600 names left a run carrying 1200 keys and
// a metric_keys array that grows without bound, while a single batch of 1200
// was refused. The cap's own comment, the user documentation and the Python
// client's warning all describe a lifetime limit, so the code is what was
// wrong.
func TestExperimentLog_MetricKeyCapIsPerRunNotPerBatch(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	batch := func(from, count int) map[string]any {
		metrics := map[string]any{}
		for i := from; i < from+count; i++ {
			metrics[fmt.Sprintf("m%05d", i)] = float64(i)
		}
		return map[string]any{"run": "run-1", "points": []map[string]any{point(1, metrics)}}
	}

	// Two batches that together stay inside the cap are both accepted.
	f.logBatch(tok, batch(0, 600))
	f.logBatch(tok, batch(600, maxIngestKeys-600))
	if got := len(f.runNamed(t, tok, "run-1").MetricKeys); got != maxIngestKeys {
		t.Fatalf("metric_keys = %d, want %d", got, maxIngestKeys)
	}

	// One more *new* name would take the run past it, however small the batch.
	resp := f.do("POST", "/api/v1/experiments/alice/exp/proj/log", tok, batch(maxIngestKeys, 1))
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
	}
	// The message has to say where the run stands, or a client cannot tell
	// whether it sent too much or the run was already full.
	body := resp.rec.Body.String()
	for _, want := range []string{
		fmt.Sprintf("at most %d", maxIngestKeys),
		fmt.Sprintf("already has %d", maxIngestKeys),
		"adds 1 more",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("error body %s does not mention %q", body, want)
		}
	}

	// The rejected batch wrote nothing: the run still carries exactly the
	// names it had.
	if got := len(f.runNamed(t, tok, "run-1").MetricKeys); got != maxIngestKeys {
		t.Fatalf("metric_keys = %d after the rejected batch, want %d", got, maxIngestKeys)
	}

	// Re-sending names the run already carries is still fine: the cap counts
	// distinct names, not requests.
	f.logBatch(tok, batch(0, 10))
}

// A run that grew past the cap while the check was still per-batch is data
// that already exists on any instance running this branch's predecessor. The
// fix must not turn those runs into runs that can never be written to again:
// the ping that marks such a run finished adds no metric name at all, and
// refusing it would leave the run "running" forever with no way out.
func TestExperimentLog_ARunAlreadyOverTheCapCanStillBeFinished(t *testing.T) {
	f := newExpFixture(t)
	repo := f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	// Seeded through the store, because the API is exactly what can no longer
	// produce this state.
	ctx := context.Background()
	projectID, err := f.st.UpsertExpProject(ctx, repo.ID, "proj")
	if err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	overCap := make([]string, 0, maxIngestKeys+200)
	for i := range maxIngestKeys + 200 {
		overCap = append(overCap, fmt.Sprintf("m%05d", i))
	}
	if _, err := f.st.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
		Name: "legacy", Status: "running", MetricKeys: overCap,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// A status ping carries no names, so it changes nothing about the run's
	// size and is accepted.
	f.logBatch(tok, map[string]any{"run": "legacy", "status": "finished", "points": []map[string]any{}})
	if got := f.runNamed(t, tok, "legacy").Status; got != apitypes.RunStatusFinished {
		t.Fatalf("status = %q, want finished", got)
	}
	// So is a point for a metric the run already carries.
	f.logBatch(tok, map[string]any{
		"run":    "legacy",
		"points": []map[string]any{point(1, map[string]any{"m00000": 0.5})},
	})

	// Growing it further is not.
	resp := f.do("POST", "/api/v1/experiments/alice/exp/proj/log", tok, map[string]any{
		"run":    "legacy",
		"points": []map[string]any{point(2, map[string]any{"brand-new": 0.5})},
	})
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
	}
}

// A batch rejected for the metric-key cap must not leave a project row
// behind, exactly like one rejected for a bad name: the check now reads the
// stored keys before anything is written, and that ordering is the point.
func TestExperimentLog_RejectedBatchCreatesNoProject(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	metrics := map[string]any{}
	for i := range maxIngestKeys + 1 {
		metrics[fmt.Sprintf("m%05d", i)] = float64(i)
	}
	resp := f.do("POST", "/api/v1/experiments/alice/exp/fresh/log", tok,
		map[string]any{"run": "run-1", "points": []map[string]any{point(1, metrics)}})
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
	}

	resp = f.do("GET", "/api/v1/experiments/alice/exp", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("experiment repo status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if strings.Contains(resp.rec.Body.String(), `"fresh"`) {
		t.Errorf("a rejected batch created the project: %s", resp.rec.Body.String())
	}
}

// TestExperimentMetricsClampsMaxPoints covers the one caller-supplied limit on
// this endpoint. Downsampling is the only thing keeping a long run's series
// out of the response body, and the metrics endpoint needs no authentication,
// so max_points went straight through to experiments.SeriesRequest -- which
// only has a floor -- and ?max_points=100000000 meant "serialise every point
// you have".
func TestExperimentMetricsClampsMaxPoints(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	// The shared fixture leaves the indexer out -- the ingest tests never
	// read a series back -- so this one wires it up. Live points come from
	// the store, so the parquet reader is not needed.
	f.s.exp = experiments.NewIndexer(f.st, f.git, newMemStore(), nil)

	const logged = maxMetricPoints + 100
	points := make([]map[string]any, 0, logged)
	for i := range logged {
		points = append(points, point(i+1, map[string]any{"loss": float64(logged - i)}))
	}
	f.logBatch(tok, map[string]any{"run": "run-1", "points": points})

	series := func(query string) int {
		t.Helper()
		resp := f.do("GET", "/api/v1/experiments/alice/exp/proj/metrics"+query, "", nil)
		if resp.status() != 200 {
			t.Fatalf("metrics%s status = %d, body = %s", query, resp.status(), resp.rec.Body.String())
		}
		var body apitypes.ExpMetricsResponse
		resp.json(t, &body)
		if len(body.Series) != 1 {
			t.Fatalf("metrics%s returned %d series, want 1", query, len(body.Series))
		}
		return len(body.Series[0].Points)
	}

	// Unauthenticated, and asking for far more than exists.
	if got := series("?max_points=100000000"); got != maxMetricPoints {
		t.Fatalf("max_points=100000000 returned %d points, want the ceiling %d", got, maxMetricPoints)
	}
	// A value under the ceiling is still honoured, and omitting it still
	// falls back to the package default of 1000 rather than the ceiling.
	if got := series("?max_points=50"); got != 50 {
		t.Fatalf("max_points=50 returned %d points", got)
	}
	if got := series(""); got != 1000 {
		t.Fatalf("no max_points returned %d points, want the default 1000", got)
	}
}
