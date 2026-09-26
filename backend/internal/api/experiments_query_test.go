// HTTP-level tests for the agent-facing experiment endpoints
// (docs/dev/agent-features.md §2.1 - §2.6): summary min/max, heartbeats,
// metric goals, the run list's query parameters, the single-run long-poll and
// the config diff.

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

const expBase = "/api/v1/experiments/alice/exp/proj"

func (f *expFixture) listRuns(t *testing.T, token, query string) apitypes.ExpRunListResponse {
	t.Helper()
	resp := f.do("GET", expBase+"/runs"+query, token, nil)
	if resp.status() != 200 {
		t.Fatalf("GET runs%s status = %d, body = %s", query, resp.status(), resp.rec.Body.String())
	}
	var body apitypes.ExpRunListResponse
	resp.json(t, &body)
	return body
}

func runNames(runs []apitypes.ExpRun) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Name)
	}
	return out
}

// ------------------------------------------------------------ ingest

func TestExperimentLog_SummaryMinMaxMergeAcrossBatches(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{
		point(1, map[string]any{"loss": 0.9, "acc": 0.1}),
		point(2, map[string]any{"loss": 0.5}),
	}})
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{
		point(3, map[string]any{"loss": 0.7, "acc": 0.6}),
	}})
	run := f.runNamed(t, tok, "r")
	if run.SummaryMin["loss"] != 0.5 || run.SummaryMax["loss"] != 0.9 || run.Summary["loss"] != 0.7 {
		t.Fatalf("loss last/min/max = %v/%v/%v, want 0.7/0.5/0.9", run.Summary, run.SummaryMin, run.SummaryMax)
	}
	if run.SummaryMin["acc"] != 0.1 || run.SummaryMax["acc"] != 0.6 {
		t.Fatalf("acc min/max = %v/%v, want 0.1/0.6", run.SummaryMin, run.SummaryMax)
	}
}

func TestExperimentLog_HeartbeatValidationAndStorage(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	for _, bad := range []int{-1, 3601} {
		resp := f.do("POST", expBase+"/log", tok, map[string]any{
			"run": "r", "heartbeat_secs": bad, "points": []map[string]any{},
		})
		if resp.status() != 400 {
			t.Fatalf("heartbeat_secs=%d: status = %d, want 400", bad, resp.status())
		}
	}
	f.logBatch(tok, map[string]any{"run": "r", "heartbeat_secs": 30, "points": []map[string]any{}})
	if hb := f.runNamed(t, tok, "r").HeartbeatSecs; hb != 30 {
		t.Fatalf("heartbeat_secs = %d, want 30", hb)
	}
	// Absent keeps it.
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(1, map[string]any{"loss": 1})}})
	if hb := f.runNamed(t, tok, "r").HeartbeatSecs; hb != 30 {
		t.Fatalf("heartbeat_secs after a batch without it = %d, want 30", hb)
	}
}

// An empty batch is the shim's liveness ping: it moves updated_at and keeps
// everything else.
func TestExperimentLog_EmptyBatchIsALivenessPing(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	f.logBatch(tok, map[string]any{
		"run": "r", "config": map[string]any{"lr": 0.1}, "heartbeat_secs": 10,
		"points": []map[string]any{point(4, map[string]any{"loss": 0.5})},
	})
	f.backdateRun("r", time.Hour)
	before := f.runNamed(t, tok, "r")
	if before.Status != apitypes.RunStatusStale {
		t.Fatalf("status before the ping = %q, want stale", before.Status)
	}

	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{}})
	after := f.runNamed(t, tok, "r")
	if !after.UpdatedAt.After(before.UpdatedAt) || time.Since(after.UpdatedAt) > time.Minute {
		t.Fatalf("updated_at = %v (was %v), want moved to now", after.UpdatedAt, before.UpdatedAt)
	}
	if after.Status != apitypes.RunStatusRunning {
		t.Fatalf("status after the ping = %q, want running", after.Status)
	}
	if after.NumPoints != 1 || after.LastStep != 4 || after.Summary["loss"] != 0.5 ||
		after.SummaryMin["loss"] != 0.5 || after.Config["lr"] != 0.1 || after.HeartbeatSecs != 10 ||
		!reflect.DeepEqual(after.MetricKeys, []string{"loss"}) {
		t.Fatalf("the ping changed the run: %+v", after)
	}
}

// A ping (or a batch whose every value was null) must not rewrite the run's
// summaries at all: the handler's read and its upsert are not one
// transaction, so writing back the copy it read would clobber whatever a
// concurrent batch or the indexer stored in between. nil is the store's "keep
// what is stored", and it is the only answer that cannot lose anything.
func TestMergeRunState_BatchWithoutValuesKeepsEverything(t *testing.T) {
	stored := runState{
		keys:       []string{"loss", "acc"},
		summary:    map[string]any{"loss": 0.5, "acc": 0.9},
		summaryMin: map[string]any{"loss": 0.1},
		summaryMax: map[string]any{"loss": 2.0},
		status:     "running", lastStep: 40,
	}
	for name, raw := range map[string][]ingestPoint{
		"no points":        nil,
		"only null values": {{Step: 41, Metrics: map[string]*float64{"loss": nil}}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			batch, ok := buildRunPoints(rec, raw)
			if !ok {
				t.Fatalf("buildRunPoints refused: %s", rec.Body.String())
			}
			merged, ok := mergeRunState(rec, "r", stored, batch)
			if !ok {
				t.Fatalf("mergeRunState refused: %s", rec.Body.String())
			}
			if merged.summary != nil || merged.summaryMin != nil || merged.summaryMax != nil || merged.keys != nil {
				t.Fatalf("merged = %+v, want every field nil (keep what is stored)", merged)
			}
		})
	}
	if got := mergeExtreme(stored.summaryMin, map[string]float64{}, func(a, b float64) bool { return a < b }); got != nil {
		t.Fatalf("mergeExtreme with an empty batch = %v, want nil", got)
	}
}

// A pure liveness ping says "running" because the shim believes it, and it can
// land after the /finish it raced. It must neither resurrect the run nor set
// it up to fire run.finished again on the next finish. A batch that carries
// points still moves the status (a resumed run is running again).
func TestExperimentLog_PingKeepsTerminalStatus(t *testing.T) {
	for _, terminal := range []apitypes.RunStatus{apitypes.RunStatusFinished, apitypes.RunStatusFailed} {
		t.Run(string(terminal), func(t *testing.T) {
			f := newExpFixture(t)
			f.repo("alice", "exp", "dataset")
			hooks := &recordingWebhooks{}
			f.s.webhooks = hooks
			tok := f.token(f.alice, "write")

			f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(1, map[string]any{"loss": 0.5})}})
			if resp := f.do("POST", expBase+"/finish", tok, map[string]any{"run": "r", "status": terminal}); resp.status() != 200 {
				t.Fatalf("finish: %d", resp.status())
			}

			f.logBatch(tok, map[string]any{"run": "r", "status": "running", "heartbeat_secs": 30, "points": []map[string]any{}})
			if got := f.runNamed(t, tok, "r").Status; got != terminal {
				t.Fatalf("status after a late ping = %q, want %q", got, terminal)
			}
			// A retried finish is not a transition, so it fires nothing new.
			if resp := f.do("POST", expBase+"/finish", tok, map[string]any{"run": "r", "status": terminal}); resp.status() != 200 {
				t.Fatalf("finish: %d", resp.status())
			}
			event := apitypes.WebhookEventRunFinished
			if terminal == apitypes.RunStatusFailed {
				event = apitypes.WebhookEventRunFailed
			}
			only(t, hooks.snapshot(), event)

			// Points are real activity: the run is running again.
			f.logBatch(tok, map[string]any{"run": "r", "status": "running",
				"points": []map[string]any{point(2, map[string]any{"loss": 0.4})}})
			if got := f.runNamed(t, tok, "r").Status; got != apitypes.RunStatusRunning {
				t.Fatalf("status after a batch with points = %q, want running", got)
			}
		})
	}
}

// A batch whose steps are all behind the run's last_step (tf experiments sync
// replaying points the shim spilled while newer ones went out online) holds
// older values: it must not replace the "last value" of a metric the run
// already has. New metrics still land, and min / max fold in as usual.
func TestExperimentLog_OutOfOrderBatchKeepsNewerSummary(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(10, map[string]any{"loss": 0.1})}})
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{
		point(4, map[string]any{"loss": 0.9, "acc": 0.2}),
		point(5, map[string]any{"loss": 0.8, "acc": 0.5}),
	}})
	run := f.runNamed(t, tok, "r")
	if run.Summary["loss"] != 0.1 || run.Summary["acc"] != 0.5 {
		t.Fatalf("summary = %v, want loss 0.1 (the newer value) and acc 0.5 (new key)", run.Summary)
	}
	if run.SummaryMin["loss"] != 0.1 || run.SummaryMax["loss"] != 0.9 || run.SummaryMin["acc"] != 0.2 {
		t.Fatalf("min/max = %v/%v, want loss 0.1/0.9, acc min 0.2", run.SummaryMin, run.SummaryMax)
	}
	if run.LastStep != 10 {
		t.Fatalf("last_step = %d, want 10", run.LastStep)
	}

	// A batch at or past the stored step is in order again.
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(10, map[string]any{"loss": 0.05})}})
	if got := f.runNamed(t, tok, "r").Summary["loss"]; got != 0.05 {
		t.Fatalf("summary loss after an in-order batch = %v, want 0.05", got)
	}
}

// ------------------------------------------------------------ staleness

func TestDeriveRunStatus_Heartbeat(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		heartbeat int
		age       time.Duration
		want      apitypes.RunStatus
	}{
		// 4 x 10s is below the 2-minute floor.
		{"short heartbeat, inside the floor", 10, 2 * time.Minute, apitypes.RunStatusRunning},
		{"short heartbeat, past the floor", 10, 2*time.Minute + time.Nanosecond, apitypes.RunStatusStale},
		// 4 x 60s = 4 minutes.
		{"minute heartbeat, inside", 60, 4 * time.Minute, apitypes.RunStatusRunning},
		{"minute heartbeat, past", 60, 4*time.Minute + time.Second, apitypes.RunStatusStale},
		// No heartbeat: the old 30-minute window.
		{"no heartbeat, 10 minutes", 0, 10 * time.Minute, apitypes.RunStatusRunning},
		{"no heartbeat, past 30 minutes", 0, runStaleAfter + time.Second, apitypes.RunStatusStale},
		// The heartbeat may make the window *longer* than 30 minutes too.
		{"hour heartbeat, 2 hours", 3600, 2 * time.Hour, apitypes.RunStatusRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveRunStatus("running", now.Add(-tc.age), tc.heartbeat, now); got != tc.want {
				t.Fatalf("deriveRunStatus(age %v, heartbeat %d) = %q, want %q", tc.age, tc.heartbeat, got, tc.want)
			}
		})
	}
}

func TestStaleRunWithHeartbeatEndToEnd(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	f.logBatch(tok, map[string]any{"run": "fast", "heartbeat_secs": 10, "points": []map[string]any{}})
	f.logBatch(tok, map[string]any{"run": "old-client", "points": []map[string]any{}})
	f.backdateRun("fast", 3*time.Minute)
	f.backdateRun("old-client", 3*time.Minute)

	if got := f.runNamed(t, tok, "fast").Status; got != apitypes.RunStatusStale {
		t.Fatalf("fast heartbeat run silent for 3m: status = %q, want stale", got)
	}
	if got := f.runNamed(t, tok, "old-client").Status; got != apitypes.RunStatusRunning {
		t.Fatalf("run without a heartbeat silent for 3m: status = %q, want running", got)
	}
}

// ------------------------------------------------------------ metric goals

func TestUpdateExperimentProject_Goals(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	write := f.token(f.alice, "write")
	read := f.token(f.alice, "read")

	// A read token may not set goals.
	if resp := f.do("PATCH", expBase, read, map[string]any{"metric_goals": map[string]string{"loss": "min"}}); resp.status() != 403 {
		t.Fatalf("read token: status = %d, want 403 (%s)", resp.status(), resp.rec.Body.String())
	}
	for name, body := range map[string]any{
		"bad goal":       map[string]any{"metric_goals": map[string]string{"loss": "lower"}},
		"control char":   map[string]any{"metric_goals": map[string]string{"lo\x01ss": "min"}},
		"reserved name":  map[string]any{"metric_goals": map[string]string{"step": "min"}},
		"nothing to set": map[string]any{},
	} {
		if resp := f.do("PATCH", expBase, write, body); resp.status() != 400 {
			t.Fatalf("%s: status = %d, want 400 (%s)", name, resp.status(), resp.rec.Body.String())
		}
	}

	// Declared before any run exists: the project is created.
	resp := f.do("PATCH", expBase, write, map[string]any{"metric_goals": map[string]string{"loss": "min", "acc": "max"}})
	if resp.status() != 200 {
		t.Fatalf("PATCH status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var project apitypes.ExpProject
	resp.json(t, &project)
	if project.Name != "proj" || project.MetricGoals["loss"] != apitypes.MetricGoalMin || project.MetricGoals["acc"] != apitypes.MetricGoalMax {
		t.Fatalf("project = %+v", project)
	}

	resp = f.do("PATCH", expBase, write, map[string]any{"metric_goals": map[string]string{"acc": ""}})
	project = apitypes.ExpProject{} // json.Unmarshal merges into an existing map
	resp.json(t, &project)
	if len(project.MetricGoals) != 1 || project.MetricGoals["loss"] != apitypes.MetricGoalMin {
		t.Fatalf("after removing acc: goals = %v", project.MetricGoals)
	}

	// The repository view reports them too.
	repoResp := f.do("GET", "/api/v1/experiments/alice/exp", write, nil)
	var repoBody apitypes.ExpRepoResponse
	repoResp.json(t, &repoBody)
	if len(repoBody.Projects) != 1 || repoBody.Projects[0].MetricGoals["loss"] != apitypes.MetricGoalMin {
		t.Fatalf("repo projects = %+v", repoBody.Projects)
	}
}

// ------------------------------------------------------------ run listing

// seedSweep logs four runs with distinct losses, groups and configs.
func seedSweep(t *testing.T, f *expFixture, tok string) {
	t.Helper()
	runs := []struct {
		name, group string
		lr          float64
		losses      []float64
	}{
		{"a", "sweep", 0.1, []float64{0.9, 0.4, 0.6}}, // min 0.4, last 0.6
		{"b", "sweep", 0.01, []float64{0.8, 0.3}},     // min 0.3, last 0.3
		{"c", "other", 0.001, []float64{0.95, 0.5}},   // min 0.5
		{"d", "", 0.1, nil},                           // no loss at all
	}
	for _, r := range runs {
		points := []map[string]any{}
		for i, l := range r.losses {
			points = append(points, point(i+1, map[string]any{"loss": l}))
		}
		body := map[string]any{
			"run": r.name, "points": points,
			"config": map[string]any{"optimizer": map[string]any{"lr": r.lr}, "_meta": map[string]any{"host": r.name}},
		}
		if r.group != "" {
			body["group"] = r.group
		}
		f.logBatch(tok, body)
	}
	f.logBatch(tok, map[string]any{"run": "c", "status": "finished", "points": []map[string]any{}})
}

func TestExperimentRuns_Query(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	seedSweep(t, f, tok)
	if resp := f.do("PATCH", expBase+"/runs/a", tok, map[string]any{"tags": []string{"keep", "x"}}); resp.status() != 200 {
		t.Fatalf("tag a: %d", resp.status())
	}
	if resp := f.do("PATCH", expBase+"/runs/b", tok, map[string]any{"tags": []string{"keep"}, "archived": true}); resp.status() != 200 {
		t.Fatalf("tag b: %d", resp.status())
	}

	cases := []struct {
		query string
		want  []string
	}{
		{"?group=sweep&sort=name", []string{"a", "b"}},
		{"?group=sweep&group=other&sort=name&order=desc", []string{"c", "b", "a"}},
		{"?status=finished", []string{"c"}},
		{"?status=running&sort=name", []string{"a", "b", "d"}},
		{"?tag=keep&sort=name", []string{"a", "b"}},
		{"?tag=keep&tag=x", []string{"a"}},
		{"?archived=false&sort=name", []string{"a", "c", "d"}},
		{"?archived=true", []string{"b"}},
		// Missing values last in both orders.
		{"?sort=min:loss", []string{"b", "a", "c", "d"}},
		{"?sort=min:loss&order=desc", []string{"c", "a", "b", "d"}},
		{"?sort=last:loss", []string{"b", "c", "a", "d"}},
		{"?sort=max:loss&order=desc&limit=2", []string{"c", "a"}},
		{"?sort=config:optimizer.lr&order=asc", []string{"c", "b", "a", "d"}},
		{"?sort=last_step&order=desc&limit=1", []string{"a"}},
	}
	for _, tc := range cases {
		// Ties (a and d share lr) keep the listing order, started_at: a was
		// logged first.
		got := runNames(f.listRuns(t, tok, tc.query).Runs)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.query, got, tc.want)
		}
	}

	for _, bad := range []string{
		"?sort=loss", "?sort=last:", "?order=up", "?limit=0", "?limit=1001", "?limit=x",
		"?archived=maybe", "?status=queued", "?sort=best:loss",
	} {
		if resp := f.do("GET", expBase+"/runs"+bad, tok, nil); resp.status() != 400 {
			t.Errorf("%s: status = %d, want 400", bad, resp.status())
		}
	}
}

func TestExperimentRuns_BestAndGoals(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	seedSweep(t, f, tok)
	f.do("PATCH", expBase, tok, map[string]any{"metric_goals": map[string]string{"loss": "min", "never": "max"}})

	body := f.listRuns(t, tok, "?sort=best:loss&order=desc")
	// best: ignores order -- best first.
	if got := runNames(body.Runs); !reflect.DeepEqual(got, []string{"b", "a", "c", "d"}) {
		t.Fatalf("sort=best:loss = %v", got)
	}
	if body.Best["loss"] != "b" {
		t.Fatalf("best = %v, want loss -> b", body.Best)
	}
	if _, ok := body.Best["never"]; ok {
		t.Fatalf("best names a run for a metric nobody logged: %v", body.Best)
	}
	if body.MetricGoals["loss"] != apitypes.MetricGoalMin || body.MetricGoals["never"] != apitypes.MetricGoalMax {
		t.Fatalf("metric_goals = %v", body.MetricGoals)
	}

	// An archived run is never "best", even when it is listed.
	f.do("PATCH", expBase+"/runs/b", tok, map[string]any{"archived": true})
	if best := f.listRuns(t, tok, "").Best["loss"]; best != "a" {
		t.Fatalf("best with b archived = %q, want a", best)
	}
	// Best is among the filtered runs.
	if best := f.listRuns(t, tok, "?group=other").Best["loss"]; best != "c" {
		t.Fatalf("best within group other = %q, want c", best)
	}
	// A goal of "max" flips the direction.
	f.do("PATCH", expBase, tok, map[string]any{"metric_goals": map[string]string{"loss": "max"}})
	if got := runNames(f.listRuns(t, tok, "?sort=best:loss").Runs); !reflect.DeepEqual(got, []string{"c", "a", "b", "d"}) {
		t.Fatalf("sort=best:loss with goal max = %v", got)
	}
}

func TestExperimentRuns_UnknownProjectIsEmpty(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	body := f.listRuns(t, tok, "?sort=name")
	if len(body.Runs) != 0 || body.MetricGoals == nil || body.Best == nil {
		t.Fatalf("unknown project = %+v", body)
	}
}

// ------------------------------------------------------------ one run

func TestExperimentRun_Get(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")

	if resp := f.do("GET", expBase+"/runs/nope", tok, nil); resp.status() != 404 {
		t.Fatalf("unknown project: status = %d, want 404", resp.status())
	}
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(1, map[string]any{"loss": 0.5})}})
	// A missing run answers 404 at once, even with a wait.
	start := time.Now()
	if resp := f.do("GET", expBase+"/runs/nope?wait=5s", tok, nil); resp.status() != 404 {
		t.Fatalf("unknown run: status = %d, want 404", resp.status())
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a missing run waited %v", time.Since(start))
	}

	resp := f.do("GET", expBase+"/runs/r", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.ExpRunResponse
	resp.json(t, &body)
	if body.Run.Name != "r" || body.Run.Summary["loss"] != 0.5 || body.Run.Status != apitypes.RunStatusRunning {
		t.Fatalf("run = %+v", body.Run)
	}

	for _, bad := range []string{"?wait=soon", "?wait=-1s", "?since=yesterday", "?status=queued"} {
		if resp := f.do("GET", expBase+"/runs/r"+bad, tok, nil); resp.status() != 400 {
			t.Errorf("%s: status = %d, want 400", bad, resp.status())
		}
	}
}

// serveAsync runs one request on another goroutine, for a test that needs to
// change state while a long-poll is waiting.
func (f *expFixture) serveAsync(method, path, token string, body any) <-chan *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, req)
		done <- rec
	}()
	return done
}

func TestExperimentRun_WaitEndsOnUpdate(t *testing.T) {
	old := runWaitPollInterval
	runWaitPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { runWaitPollInterval = old })

	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(1, map[string]any{"loss": 0.5})}})
	first := f.runNamed(t, tok, "r")
	// SQLite stores milliseconds; make sure the next write lands on a later one.
	time.Sleep(5 * time.Millisecond)

	since := url.QueryEscape(first.UpdatedAt.Format(time.RFC3339Nano))
	start := time.Now()
	waiting := f.serveAsync("GET", expBase+"/runs/r?wait=30s&since="+since, tok, nil)
	time.Sleep(100 * time.Millisecond)
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(2, map[string]any{"loss": 0.4})}})

	select {
	case rec := <-waiting:
		if rec.Code != 200 {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var body apitypes.ExpRunResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Run.LastStep != 2 {
			t.Fatalf("answered with the old run: %+v", body.Run)
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("took %v to notice the update", time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait did not end on an update")
	}
}

func TestExperimentRun_WaitEndsOnStatusAndTimeout(t *testing.T) {
	old := runWaitPollInterval
	runWaitPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { runWaitPollInterval = old })

	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{}})

	// The status already differs from the one the caller holds: no wait.
	start := time.Now()
	resp := f.do("GET", expBase+"/runs/r?wait=30s&status=finished", tok, nil)
	if resp.status() != 200 || time.Since(start) > 2*time.Second {
		t.Fatalf("status mismatch: %d after %v, want an immediate 200", resp.status(), time.Since(start))
	}

	// Nothing changes: the wait runs out and answers 200 with the run as is.
	start = time.Now()
	resp = f.do("GET", expBase+"/runs/r?wait=300ms", tok, nil)
	elapsed := time.Since(start)
	if resp.status() != 200 || elapsed < 250*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("timeout: status %d after %v, want 200 after ~300ms", resp.status(), elapsed)
	}

	// A finish while waiting on status=running ends the wait.
	waiting := f.serveAsync("GET", expBase+"/runs/r?wait=30s&status=running&since="+
		url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339Nano)), tok, nil)
	time.Sleep(100 * time.Millisecond)
	if resp := f.do("POST", expBase+"/finish", tok, map[string]any{"run": "r"}); resp.status() != 200 {
		t.Fatalf("finish: %d", resp.status())
	}
	select {
	case rec := <-waiting:
		var body apitypes.ExpRunResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != 200 || body.Run.Status != apitypes.RunStatusFinished {
			t.Fatalf("status %d, run %+v", rec.Code, body.Run)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait did not end on a status change")
	}
}

// Past maxRunWaiters concurrent waiters, a wait is answered at once with the
// run as it stands, and the waiters already holding a slot keep working.
func TestExperimentRun_WaiterCap(t *testing.T) {
	oldPoll, oldCap := runWaitPollInterval, maxRunWaiters
	runWaitPollInterval = 20 * time.Millisecond
	maxRunWaiters = 1
	t.Cleanup(func() { runWaitPollInterval, maxRunWaiters = oldPoll, oldCap })

	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(1, map[string]any{"loss": 0.5})}})
	first := f.runNamed(t, tok, "r")
	time.Sleep(5 * time.Millisecond)

	since := url.QueryEscape(first.UpdatedAt.Format(time.RFC3339Nano))
	waiting := f.serveAsync("GET", expBase+"/runs/r?wait=30s&since="+since, tok, nil)
	deadline := time.Now().Add(5 * time.Second)
	for runWaiters.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first request never started waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The cap is taken: a second waiter is answered without waiting.
	start := time.Now()
	resp := f.do("GET", expBase+"/runs/r?wait=30s&since="+since, tok, nil)
	if resp.status() != 200 || time.Since(start) > 2*time.Second {
		t.Fatalf("over the cap: status %d after %v, want an immediate 200", resp.status(), time.Since(start))
	}
	var over apitypes.ExpRunResponse
	resp.json(t, &over)
	if over.Run.Name != "r" || over.Run.LastStep != 1 {
		t.Fatalf("over the cap answered %+v, want the run as it is", over.Run)
	}

	// The waiter holding the slot still ends on the update, and gives it back.
	f.logBatch(tok, map[string]any{"run": "r", "points": []map[string]any{point(2, map[string]any{"loss": 0.4})}})
	select {
	case rec := <-waiting:
		var body apitypes.ExpRunResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != 200 || body.Run.LastStep != 2 {
			t.Fatalf("waiter answered %d with %+v", rec.Code, body.Run)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter holding the slot did not end on the update")
	}
	if n := runWaiters.Load(); n != 0 {
		t.Fatalf("runWaiters = %d after every waiter answered, want 0", n)
	}
}

func TestParseRunWait(t *testing.T) {
	cases := map[string]time.Duration{
		"":      0,
		"30s":   30 * time.Second,
		"15":    15 * time.Second,
		"0.5":   500 * time.Millisecond,
		"2m":    maxRunWait,
		"10000": maxRunWait,
		// Past what an int64 of nanoseconds holds: the conversion used to
		// overflow, which amd64 turned into a negative wait and a 400.
		"1e12": maxRunWait,
	}
	for raw, want := range cases {
		got, err := parseRunWait(raw)
		if err != nil || got != want {
			t.Errorf("parseRunWait(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "-1s", "NaN", "Inf", "-1e12"} {
		if _, err := parseRunWait(bad); err == nil {
			t.Errorf("parseRunWait(%q) accepted", bad)
		}
	}
}

// The long-poll must not be cut off by handlerTimeout, and only it: the
// PATCH on the same pattern, and a GET without wait, stay timed.
func TestLongPollRouteExemption(t *testing.T) {
	routes := defaultRoutes()
	cases := []struct {
		method, target string
		want           bool
	}{
		{http.MethodGet, expBase + "/runs/r?wait=30s", true},
		{http.MethodGet, expBase + "/runs/sweep%2Fseed-1?wait=30s", true},
		{http.MethodGet, expBase + "/runs/r", false},
		{http.MethodPatch, expBase + "/runs/r?wait=30s", false},
		{http.MethodGet, expBase + "/runs?wait=30s", false},
		{http.MethodGet, expBase + "/runs/r/artifacts?wait=30s", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		if got := longPollRoute(routes, req); got != tc.want {
			t.Errorf("%s %s: longPollRoute = %v, want %v", tc.method, tc.target, got, tc.want)
		}
	}
}

// ------------------------------------------------------------ config diff

func TestExperimentConfigDiff(t *testing.T) {
	f := newExpFixture(t)
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	for name, cfg := range map[string]map[string]any{
		"a": {"optimizer": map[string]any{"lr": 0.1, "name": "adam"}, "seed": 1, "_meta": map[string]any{"host": "x"}},
		"b": {"optimizer": map[string]any{"lr": 0.01, "name": "adam"}, "seed": 1, "_meta": map[string]any{"host": "y"}},
		"c": {"optimizer": map[string]any{"lr": 0.1, "name": "adam"}, "extra": true},
	} {
		f.logBatch(tok, map[string]any{"run": name, "config": cfg, "points": []map[string]any{}})
	}
	f.do("PATCH", expBase+"/runs/c", tok, map[string]any{"archived": true})

	get := func(query string) apitypes.ExpConfigDiffResponse {
		t.Helper()
		resp := f.do("GET", expBase+"/config-diff"+query, tok, nil)
		if resp.status() != 200 {
			t.Fatalf("config-diff%s: status = %d, body = %s", query, resp.status(), resp.rec.Body.String())
		}
		var body apitypes.ExpConfigDiffResponse
		resp.json(t, &body)
		return body
	}
	keysOf := func(b apitypes.ExpConfigDiffResponse) []string {
		out := []string{}
		for _, k := range b.Keys {
			out = append(out, k.Key)
		}
		return out
	}

	// Default: every non-archived run (a, b); _meta left out.
	body := get("")
	runs := append([]string(nil), body.Runs...)
	sort.Strings(runs)
	if !reflect.DeepEqual(runs, []string{"a", "b"}) {
		t.Fatalf("runs = %v, want a, b", body.Runs)
	}
	if got := keysOf(body); !reflect.DeepEqual(got, []string{"optimizer.lr"}) {
		t.Fatalf("keys = %v, want [optimizer.lr]", got)
	}
	if v := body.Keys[0].Values; v["a"] != 0.1 || v["b"] != 0.01 {
		t.Fatalf("values = %v", v)
	}

	// include_meta brings _meta back.
	if got := keysOf(get("?include_meta=true")); !reflect.DeepEqual(got, []string{"_meta.host", "optimizer.lr"}) {
		t.Fatalf("keys with meta = %v", got)
	}

	// Explicit runs, archived included; a key one run lacks is a difference.
	body = get("?run=a&run=c")
	if !reflect.DeepEqual(body.Runs, []string{"a", "c"}) {
		t.Fatalf("runs = %v, want [a c] in the order asked", body.Runs)
	}
	if got := keysOf(body); !reflect.DeepEqual(got, []string{"extra", "seed"}) {
		t.Fatalf("keys = %v, want [extra seed]", got)
	}
	for _, k := range body.Keys {
		if k.Key == "seed" {
			if _, ok := k.Values["c"]; ok || k.Values["a"] != float64(1) {
				t.Fatalf("seed values = %v, want only a", k.Values)
			}
		}
	}

	if resp := f.do("GET", expBase+"/config-diff?run=a&run=ghost", tok, nil); resp.status() != 404 {
		t.Fatalf("unknown run: status = %d, want 404", resp.status())
	}
	if resp := f.do("GET", expBase+"/config-diff?include_meta=perhaps", tok, nil); resp.status() != 400 {
		t.Fatalf("bad include_meta: status = %d, want 400", resp.status())
	}
	if resp := f.do("GET", "/api/v1/experiments/alice/exp/none/config-diff", tok, nil); resp.status() != 200 {
		t.Fatalf("unknown project, no runs: status = %d, want 200", resp.status())
	}
}

func TestSummarizeProjectCountsStatusesAndBest(t *testing.T) {
	p := apitypes.ExpProject{
		MetricGoals:  map[string]apitypes.MetricGoal{"cer": apitypes.MetricGoalMin},
		StatusCounts: map[apitypes.RunStatus]int{},
		Best:         []apitypes.ExpProjectBest{},
	}
	runs := []apitypes.ExpRun{
		{Name: "a", Status: apitypes.RunStatusRunning, SummaryMin: map[string]float64{"cer": 12}},
		{Name: "b", Status: apitypes.RunStatusFinished, SummaryMin: map[string]float64{"cer": 10}},
		{Name: "c", Status: apitypes.RunStatusFinished, Archived: true, SummaryMin: map[string]float64{"cer": 5}},
	}
	summarizeProject(&p, runs)
	if p.StatusCounts[apitypes.RunStatusRunning] != 1 || p.StatusCounts[apitypes.RunStatusFinished] != 1 {
		t.Fatalf("status counts = %v; archived runs must not count", p.StatusCounts)
	}
	if len(p.Best) != 1 || p.Best[0].Run != "b" || p.Best[0].Value != 10 {
		t.Fatalf("best = %+v; want b at 10 (the archived c is ignored)", p.Best)
	}
}
