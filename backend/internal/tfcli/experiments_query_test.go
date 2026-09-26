package tfcli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// fakeExpServer is an in-memory stand-in for /api/v1/experiments, enough for
// the query subcommands. Handlers can be overridden per test.
type fakeExpServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	runs     []apitypes.ExpRun
	goals    map[string]apitypes.MetricGoal
	best     map[string]string
	notes    *apitypes.ExpNotesResponse
	lastReq  *http.Request
	lastBody []byte
	requests []string // "METHOD escaped-path?query"

	// getRun, when set, answers GET .../runs/{run} instead of the default.
	getRun func(w http.ResponseWriter, r *http.Request, call int)
	calls  int
}

func newFakeExpServer(t *testing.T) *fakeExpServer {
	t.Helper()
	isolateEnv(t)
	f := &fakeExpServer{
		t:     t,
		goals: map[string]apitypes.MetricGoal{},
		notes: &apitypes.ExpNotesResponse{Path: "ocr/NOTES.md"},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeExpServer) args(extra ...string) []string {
	return append(extra, "--endpoint", f.srv.URL, "--token", "tok")
}

func (f *fakeExpServer) findRun(name string) *apitypes.ExpRun {
	for i := range f.runs {
		if f.runs[i].Name == name {
			return &f.runs[i]
		}
	}
	return nil
}

func (f *fakeExpServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.lastReq, f.lastBody = r, body
	f.requests = append(f.requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	f.mu.Unlock()

	const prefix = "/api/v1/experiments/"
	path := r.URL.EscapedPath()
	if !strings.HasPrefix(path, prefix) {
		http.NotFound(w, r)
		return
	}
	var segs []string
	for _, s := range strings.Split(strings.TrimPrefix(path, prefix), "/") {
		u, err := url.PathUnescape(s)
		if err != nil {
			f.t.Errorf("bad escape in %s", path)
		}
		segs = append(segs, u)
	}
	send := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, typ, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": typ, "message": msg}})
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(segs) == 2 && r.Method == http.MethodGet:
		send(apitypes.ExpRepoResponse{Projects: []apitypes.ExpProject{{Name: "ocr", NumRuns: len(f.runs), MetricGoals: f.goals}}})
	case len(segs) == 3 && r.Method == http.MethodPatch:
		var req apitypes.ExpProjectUpdateRequest
		_ = json.Unmarshal(body, &req)
		for k, v := range req.MetricGoals {
			if v == "" {
				delete(f.goals, k)
			} else {
				f.goals[k] = apitypes.MetricGoal(v)
			}
		}
		send(apitypes.ExpProject{Name: segs[2], MetricGoals: f.goals})
	case len(segs) == 4 && segs[3] == "runs":
		send(apitypes.ExpRunListResponse{Runs: f.runs, MetricGoals: f.goals, Best: f.best})
	case len(segs) == 5 && segs[3] == "runs" && r.Method == http.MethodGet:
		f.calls++
		if f.getRun != nil {
			f.getRun(w, r, f.calls)
			return
		}
		run := f.findRun(segs[4])
		if run == nil {
			fail(404, "not_found", "run not found")
			return
		}
		send(apitypes.ExpRunResponse{Run: *run})
	case len(segs) == 5 && segs[3] == "runs" && r.Method == http.MethodPatch:
		run := f.findRun(segs[4])
		if run == nil {
			fail(404, "not_found", "run not found")
			return
		}
		var req apitypes.ExpRunAnnotationRequest
		_ = json.Unmarshal(body, &req)
		if req.Tags != nil {
			run.Tags = *req.Tags
		}
		if req.Archived != nil {
			run.Archived = *req.Archived
		}
		if req.Note != nil {
			run.Note = *req.Note
		}
		send(apitypes.ExpRunAnnotationResponse{Run: *run})
	case len(segs) == 4 && segs[3] == "config-diff":
		send(apitypes.ExpConfigDiffResponse{
			Runs: []string{"a", "b"},
			Keys: []apitypes.ExpConfigDiffKey{
				{Key: "lr", Values: map[string]any{"a": 0.001, "b": 0.01}},
				{Key: "optim.name", Values: map[string]any{"a": "adam"}},
			},
		})
	case len(segs) == 4 && segs[3] == "notes" && r.Method == http.MethodGet:
		send(f.notes)
	case len(segs) == 4 && segs[3] == "notes" && r.Method == http.MethodPut:
		var req apitypes.ExpNotesUpdateRequest
		_ = json.Unmarshal(body, &req)
		if req.BaseSHA != nil && *req.BaseSHA != f.notes.BlobSHA {
			fail(409, "conflict", "notes changed")
			return
		}
		f.notes = &apitypes.ExpNotesResponse{
			Path: "ocr/NOTES.md", Content: req.Content, Exists: true,
			BlobSHA: "sha-" + req.Content, CommitSHA: "0123456789abcdef",
		}
		send(f.notes)
	default:
		fail(404, "not_found", "no route "+r.Method+" "+path)
	}
}

func sampleRuns() []apitypes.ExpRun {
	now := time.Now()
	return []apitypes.ExpRun{
		{
			Name: "run-a", Status: "finished", LastStep: 1000, NumPoints: 100, UpdatedAt: now.Add(-3 * time.Hour),
			Config:     map[string]any{"lr": 0.001, "optim": map[string]any{"name": "adam"}},
			MetricKeys: []string{"loss", "acc", "_runtime", "zeta", "wer", "zz"},
			Summary:    map[string]float64{"loss": 0.3, "acc": 0.8, "zeta": 1, "wer": 0.2},
			SummaryMin: map[string]float64{"loss": 0.25},
			SummaryMax: map[string]float64{"acc": 0.85},
			Group:      "sweep-1",
		},
		{
			Name: "run b/2", Status: "running", LastStep: 12000, NumPoints: 900, UpdatedAt: now.Add(-30 * time.Second),
			Config:     map[string]any{"lr": 0.01},
			MetricKeys: []string{"loss", "acc"},
			Summary:    map[string]float64{"loss": 0.2, "acc": 0.9},
			SummaryMin: map[string]float64{"loss": 0.15},
			SummaryMax: map[string]float64{"acc": 0.92},
			Archived:   true,
		},
	}
}

func TestExpRunsDefaultTable(t *testing.T) {
	f := newFakeExpServer(t)
	f.runs = sampleRuns()
	f.goals = map[string]apitypes.MetricGoal{"loss": "min"}
	f.best = map[string]string{"loss": "run b/2"}

	code, out, errOut := runMain(t, f.args("experiments", "runs", "alice/exp", "ocr", "--status", "running,finished", "--tag", "x"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("output:\n%s", out)
	}
	// Default columns: group (a run has one), the goal metric in its
	// direction, then last: of up to 3 others, alphabetically, _ skipped.
	header := strings.Fields(lines[0])
	want := []string{"NAME", "STATUS", "STEP", "UPDATED", "group", "min:loss", "last:acc", "last:wer", "last:zeta"}
	if !reflect.DeepEqual(header, want) {
		t.Errorf("header = %v, want %v", header, want)
	}
	if !strings.HasPrefix(lines[1], "run-a ") || !strings.Contains(lines[1], "finished") || !strings.Contains(lines[1], "3h ago") || !strings.Contains(lines[1], "sweep-1") {
		t.Errorf("row 1 = %q", lines[1])
	}
	if !strings.Contains(lines[2], "0.15 *") || !strings.Contains(lines[2], "running,archived") {
		t.Errorf("row 2 should mark the best loss and the archived flag: %q", lines[2])
	}
	if !strings.Contains(lines[3], "best run") {
		t.Errorf("legend missing: %q", lines[3])
	}
	// Columns are aligned: every row's STATUS starts at the same offset.
	col := strings.Index(lines[0], "STATUS")
	if strings.Index(lines[1], "finished") != col || strings.Index(lines[2], "running") != col {
		t.Errorf("columns not aligned:\n%s", out)
	}
	if !strings.Contains(errOut, "1 more metric") {
		t.Errorf("stderr should mention the hidden metric: %q", errOut)
	}
	q := f.lastReq.URL.Query()
	if !reflect.DeepEqual(q["status"], []string{"running", "finished"}) || !reflect.DeepEqual(q["tag"], []string{"x"}) {
		t.Errorf("query = %v", q)
	}
}

func TestExpRunsColumnsAndJSON(t *testing.T) {
	f := newFakeExpServer(t)
	f.runs = sampleRuns()

	code, out, errOut := runMain(t, f.args("exp", "runs", "alice/exp", "ocr", "--columns", "config:lr,config:optim.name,max:acc,points,tags"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if got := strings.Fields(lines[0]); !reflect.DeepEqual(got[4:], []string{"config:lr", "config:optim.name", "max:acc", "points", "tags"}) {
		t.Errorf("header = %v", got)
	}
	if f := strings.Fields(lines[1]); !reflect.DeepEqual(f[len(f)-5:], []string{"0.001", "adam", "0.85", "100", "-"}) {
		t.Errorf("row 1 = %v", f)
	}
	if f := strings.Fields(lines[2]); !reflect.DeepEqual(f[len(f)-5:], []string{"0.01", "-", "0.92", "900", "-"}) {
		t.Errorf("row 2 = %v", f)
	}

	code, out, errOut = runMain(t, f.args("exp", "runs", "alice/exp", "ocr", "--json", "--archived", "false", "--sort", "best:loss", "--limit", "3"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var resp apitypes.ExpRunListResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("--json output is not the API response: %v\n%s", err, out)
	}
	if len(resp.Runs) != 2 || resp.Runs[1].Name != "run b/2" {
		t.Errorf("resp = %+v", resp)
	}
	q := f.lastReq.URL.Query()
	if q.Get("archived") != "false" || q.Get("sort") != "best:loss" || q.Get("limit") != "3" {
		t.Errorf("query = %v", q)
	}
}

func TestExpRunsUsageErrors(t *testing.T) {
	f := newFakeExpServer(t)
	for _, args := range [][]string{
		{"exp", "runs", "alice/exp"},
		{"exp", "runs", "alice", "ocr"},
		{"exp", "runs", "a/b/c", "ocr"},
		{"exp", "runs", "alice/exp", "ocr", "--archived", "maybe"},
		{"exp", "runs", "alice/exp", "ocr", "--columns", "bogus:x"},
		{"exp", "runs", "alice/exp", "ocr", "--columns", "last:"},
		{"exp", "runs", "alice/exp", "ocr", "--no-such-flag"},
		{"exp", "frobnicate"},
	} {
		code, _, errOut := runMain(t, f.args(args...), "")
		if code != 2 {
			t.Errorf("%v: exit %d, want 2 (%s)", args, code, errOut)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("usage errors must not reach the server: %v", f.requests)
	}
}

func TestExpRunsAcceptsDatasetsPrefix(t *testing.T) {
	f := newFakeExpServer(t)
	f.runs = sampleRuns()
	code, _, errOut := runMain(t, f.args("exp", "runs", "datasets/alice/exp", "ocr"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if got := f.lastReq.URL.EscapedPath(); got != "/api/v1/experiments/alice/exp/ocr/runs" {
		t.Errorf("path = %s", got)
	}
}

func TestExpRunHumanAndJSON(t *testing.T) {
	f := newFakeExpServer(t)
	f.runs = sampleRuns()
	f.runs[1].Tags = []string{"good", "v2"}
	f.runs[1].Note = "first line\nsecond"

	code, out, errOut := runMain(t, f.args("exp", "run", "alice/exp", "ocr", "run b/2"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"name:      run b/2", "status:    running", "step:      12000 (900 points)", "tags:      good, v2", "archived:  yes", "  second", "config:", "  lr  0.01", "NAME", "LAST", "MIN", "MAX"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if got := f.lastReq.URL.EscapedPath(); got != "/api/v1/experiments/alice/exp/ocr/runs/run%20b%2F2" {
		t.Errorf("path = %s", got)
	}

	code, out, _ = runMain(t, f.args("exp", "run", "alice/exp", "ocr", "run b/2", "--json"), "")
	if code != 0 {
		t.Fatal(code)
	}
	var resp apitypes.ExpRunResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Run.Name != "run b/2" {
		t.Errorf("json = %s (%v)", out, err)
	}

	code, _, errOut = runMain(t, f.args("exp", "run", "alice/exp", "ocr", "nope"), "")
	if code != 1 || !strings.Contains(errOut, "run not found") {
		t.Errorf("missing run: exit %d, stderr %q", code, errOut)
	}
}

// fastWait shrinks the wait loop's timing for the duration of a test.
func fastWait(t *testing.T) {
	t.Helper()
	saved := []time.Duration{waitPollWindow, waitNotFoundInterval, waitMinInterval, waitInitialBackoff, waitMaxBackoff}
	waitPollWindow, waitNotFoundInterval, waitMinInterval = 50*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond
	waitInitialBackoff, waitMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		waitPollWindow, waitNotFoundInterval, waitMinInterval = saved[0], saved[1], saved[2]
		waitInitialBackoff, waitMaxBackoff = saved[3], saved[4]
	})
}

func TestExpWaitProgressesUntilMet(t *testing.T) {
	fastWait(t)
	f := newFakeExpServer(t)
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	var queries []url.Values
	f.getRun = func(w http.ResponseWriter, r *http.Request, call int) {
		queries = append(queries, r.URL.Query())
		switch {
		case call <= 2:
			// Not created yet.
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"type":"not_found","message":"run not found"}}`)
			return
		case call == 3:
			w.WriteHeader(503)
			return
		}
		run := apitypes.ExpRun{
			Name: "r", Status: "running", LastStep: int64(call) * 1000,
			UpdatedAt: base.Add(time.Duration(call) * time.Second),
			Summary:   map[string]float64{"val/CER": 0.5 / float64(call)},
		}
		_ = json.NewEncoder(w).Encode(apitypes.ExpRunResponse{Run: run})
	}
	code, out, errOut := runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "r", "--until", `step>=6000 and metric:"val/CER"<0.1`, "--json"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var res runWaitResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !res.Met || res.Reason != "met" || res.Run == nil || res.Run.LastStep != 6000 {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(errOut, "does not exist yet") || strings.Count(errOut, "does not exist yet") != 1 {
		t.Errorf("stderr should say once that the run does not exist yet: %q", errOut)
	}
	if !strings.Contains(errOut, "retrying") {
		t.Errorf("stderr should mention the retry after a 503: %q", errOut)
	}
	// The first successful call has no wait; later ones long-poll from the
	// last state seen.
	if queries[3].Get("wait") != "" {
		t.Errorf("first fetch should not wait: %v", queries[3])
	}
	last := queries[len(queries)-1]
	if last.Get("wait") == "" || last.Get("status") != "running" || last.Get("since") != base.Add(5*time.Second).Format(time.RFC3339Nano) {
		t.Errorf("long poll query = %v", last)
	}
}

func TestExpWaitDefaultUntilHuman(t *testing.T) {
	fastWait(t)
	f := newFakeExpServer(t)
	f.runs = sampleRuns()
	code, out, errOut := runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "run-a"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "status:    finished") {
		t.Errorf("stdout should show the run:\n%s", out)
	}
}

func TestExpWaitStopsOnStoppedRun(t *testing.T) {
	fastWait(t)
	f := newFakeExpServer(t)
	f.runs = []apitypes.ExpRun{{Name: "r", Status: "stale", LastStep: 5000, UpdatedAt: time.Now()}}
	code, out, errOut := runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "r", "--until", "step>=12000", "--json"), "")
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut)
	}
	var res runWaitResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Reason != "stopped" || res.Met {
		t.Errorf("result = %s (%v)", out, err)
	}
	if !strings.Contains(errOut, "stale") || !strings.Contains(errOut, "--ignore-stale") {
		t.Errorf("stderr = %q", errOut)
	}

	// A condition about status keeps waiting (and here times out).
	code, out, errOut = runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "r", "--until", "status==finished", "--timeout", "100ms"), "")
	if code != 1 || !strings.Contains(errOut, "timed out") || !strings.Contains(out, "status:    stale") {
		t.Errorf("status condition: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	// --ignore-stale keeps waiting too.
	code, out, errOut = runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "r", "--until", "step>=12000", "--ignore-stale", "--timeout", "100ms", "--json"), "")
	if code != 1 || !strings.Contains(errOut, "timed out") {
		t.Errorf("--ignore-stale: exit %d, stderr %q", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Reason != "timeout" || res.Run == nil {
		t.Errorf("--ignore-stale result = %s (%v)", out, err)
	}
}

func TestExpWaitTimeoutNeverAppeared(t *testing.T) {
	fastWait(t)
	f := newFakeExpServer(t)
	code, out, errOut := runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "ghost", "--timeout", "50ms", "--json"), "")
	if code != 1 || !strings.Contains(errOut, "never appeared") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if strings.TrimSpace(out) != `{"run":null,"met":false,"reason":"timeout","until":"status!=running"}` {
		t.Errorf("json = %s", out)
	}
}

func TestExpWaitFatalErrorsAndUsage(t *testing.T) {
	fastWait(t)
	f := newFakeExpServer(t)
	f.getRun = func(w http.ResponseWriter, r *http.Request, call int) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"type":"authentication_required","message":"login"}}`)
	}
	code, _, errOut := runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "r"), "")
	if code != 1 || !strings.Contains(errOut, "tf login") || f.calls != 1 {
		t.Errorf("401: exit %d, calls %d, stderr %q", code, f.calls, errOut)
	}

	code, _, errOut = runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "r", "--until", "step >= soon"), "")
	if code != 2 || !strings.Contains(errOut, "column 9") || !strings.Contains(errOut, "        ^") {
		t.Errorf("bad --until: exit %d, stderr %q", code, errOut)
	}
	code, _, _ = runMain(t, f.args("exp", "wait", "alice/exp", "ocr", "r", "--timeout", "-1s"), "")
	if code != 2 {
		t.Errorf("negative timeout: exit %d", code)
	}
}

func TestExpDiff(t *testing.T) {
	f := newFakeExpServer(t)
	code, out, errOut := runMain(t, f.args("exp", "diff", "alice/exp", "ocr", "a", "b", "--include-meta"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !reflect.DeepEqual(strings.Fields(lines[0]), []string{"KEY", "a", "b"}) ||
		!reflect.DeepEqual(strings.Fields(lines[1]), []string{"lr", "0.001", "0.01"}) ||
		!reflect.DeepEqual(strings.Fields(lines[2]), []string{"optim.name", "adam", "-"}) {
		t.Errorf("output:\n%s", out)
	}
	q := f.lastReq.URL.Query()
	if !reflect.DeepEqual(q["run"], []string{"a", "b"}) || q.Get("include_meta") != "true" {
		t.Errorf("query = %v", q)
	}
	code, out, _ = runMain(t, f.args("exp", "diff", "alice/exp", "ocr", "--json"), "")
	var resp apitypes.ExpConfigDiffResponse
	if code != 0 || json.Unmarshal([]byte(out), &resp) != nil || len(resp.Keys) != 2 {
		t.Errorf("json: exit %d %s", code, out)
	}
	if f.lastReq.URL.RawQuery != "" {
		t.Errorf("no runs given must send no run= params: %q", f.lastReq.URL.RawQuery)
	}
}

func TestExpGoals(t *testing.T) {
	f := newFakeExpServer(t)
	code, _, errOut := runMain(t, f.args("exp", "goals", "alice/exp", "ocr"), "")
	if code != 0 || !strings.Contains(errOut, "no metric goals") {
		t.Errorf("empty: exit %d, stderr %q", code, errOut)
	}

	code, out, errOut := runMain(t, f.args("exp", "goals", "alice/exp", "ocr", "val/CER=min", "acc=MAX", "a=b=none"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var body apitypes.ExpProjectUpdateRequest
	_ = json.Unmarshal(f.lastBody, &body)
	if !reflect.DeepEqual(body.MetricGoals, map[string]string{"val/CER": "min", "acc": "max", "a=b": ""}) {
		t.Errorf("PATCH body = %s", f.lastBody)
	}
	if !strings.Contains(out, "val/CER  min") || !strings.Contains(out, "acc      max") {
		t.Errorf("output:\n%s", out)
	}

	code, out, _ = runMain(t, f.args("exp", "goals", "alice/exp", "ocr", "--json"), "")
	var p apitypes.ExpProject
	if code != 0 || json.Unmarshal([]byte(out), &p) != nil || p.MetricGoals["acc"] != "max" {
		t.Errorf("json: exit %d %s", code, out)
	}

	for _, bad := range []string{"acc", "=min", "acc=best"} {
		if code, _, _ := runMain(t, f.args("exp", "goals", "alice/exp", "ocr", bad), ""); code != 2 {
			t.Errorf("%q: exit %d, want 2", bad, code)
		}
	}
}

func TestExpNotes(t *testing.T) {
	f := newFakeExpServer(t)
	code, out, errOut := runMain(t, f.args("exp", "notes", "alice/exp", "ocr"), "")
	if code != 0 || out != "" || !strings.Contains(errOut, "no notes yet") {
		t.Errorf("missing notes: exit %d, out %q, err %q", code, out, errOut)
	}

	// First write: base_sha "" (must not exist yet), read from stdin.
	code, out, errOut = runMain(t, f.args("exp", "notes", "alice/exp", "ocr", "--set", "-", "-m", "init"), "# Notes\nsee [r](run:r1)\n")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var put apitypes.ExpNotesUpdateRequest
	_ = json.Unmarshal(f.lastBody, &put)
	if put.BaseSHA == nil || *put.BaseSHA != "" || put.Message != "init" || put.Content != "# Notes\nsee [r](run:r1)\n" {
		t.Errorf("PUT body = %s", f.lastBody)
	}
	if !strings.Contains(out, "Updated ocr/NOTES.md (commit 0123456)") {
		t.Errorf("out = %q", out)
	}

	code, out, _ = runMain(t, f.args("exp", "notes", "alice/exp", "ocr"), "")
	if code != 0 || out != "# Notes\nsee [r](run:r1)\n" {
		t.Errorf("read back: %q", out)
	}

	// Second write picks up the current sha automatically.
	if code, _, errOut := runMain(t, f.args("exp", "notes", "alice/exp", "ocr", "--set", "-"), "v2"); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	_ = json.Unmarshal(f.lastBody, &put)
	if put.BaseSHA == nil || *put.BaseSHA != "sha-# Notes\nsee [r](run:r1)\n" {
		t.Errorf("second PUT base = %v", put.BaseSHA)
	}

	// A stale --base-sha is refused with a clear message.
	code, _, errOut = runMain(t, f.args("exp", "notes", "alice/exp", "ocr", "--set", "-", "--base-sha", "old"), "v3")
	if code != 1 || !strings.Contains(errOut, "changed since they were read") {
		t.Errorf("conflict: exit %d, stderr %q", code, errOut)
	}

	// --force sends no base_sha at all.
	code, out, _ = runMain(t, f.args("exp", "notes", "alice/exp", "ocr", "--set", "-", "--force", "--json"), "v4")
	if code != 0 || strings.Contains(string(f.lastBody), "base_sha") {
		t.Errorf("--force: exit %d, body %s", code, f.lastBody)
	}
	var notes apitypes.ExpNotesResponse
	if json.Unmarshal([]byte(out), &notes) != nil || notes.Content != "v4" {
		t.Errorf("--json = %s", out)
	}

	for _, args := range [][]string{
		{"--force"},
		{"--set", "-", "--force", "--base-sha", "x"},
	} {
		if code, _, _ := runMain(t, f.args(append([]string{"exp", "notes", "alice/exp", "ocr"}, args...)...), ""); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestExpAnnotate(t *testing.T) {
	f := newFakeExpServer(t)
	f.runs = sampleRuns()
	f.runs[0].Tags = []string{"keep", "drop"}

	code, out, errOut := runMain(t, f.args("exp", "annotate", "alice/exp", "ocr", "run-a", "--add-tag", "new", "--remove-tag", "drop", "--archive", "--note", "bad lr"), "")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var req apitypes.ExpRunAnnotationRequest
	_ = json.Unmarshal(f.lastBody, &req)
	if req.Tags == nil || !reflect.DeepEqual(*req.Tags, []string{"keep", "new"}) || req.Archived == nil || !*req.Archived || req.Note == nil || *req.Note != "bad lr" {
		t.Errorf("PATCH body = %s", f.lastBody)
	}
	if !strings.Contains(out, "tags:      keep, new") {
		t.Errorf("out:\n%s", out)
	}

	// --clear-tags sends [], --unarchive false, and nothing else.
	code, out, _ = runMain(t, f.args("exp", "annotate", "alice/exp", "ocr", "run-a", "--clear-tags", "--unarchive", "--json"), "")
	if code != 0 || string(f.lastBody) != `{"tags":[],"archived":false}` {
		t.Errorf("exit %d, body %s", code, f.lastBody)
	}
	var resp apitypes.ExpRunAnnotationResponse
	if json.Unmarshal([]byte(out), &resp) != nil || resp.Run.Archived {
		t.Errorf("json = %s", out)
	}

	// --tag replaces; --note-file - reads stdin.
	code, _, _ = runMain(t, f.args("exp", "annotate", "alice/exp", "ocr", "run-a", "--tag", "a,b", "--tag", "c", "--note-file", "-"), "from stdin\n")
	_ = json.Unmarshal(f.lastBody, &req)
	if code != 0 || !reflect.DeepEqual(*req.Tags, []string{"a,b", "c"}) || *req.Note != "from stdin\n" {
		t.Errorf("exit %d, body %s", code, f.lastBody)
	}

	for _, args := range [][]string{
		{},
		{"--archive", "--unarchive"},
		{"--tag", "x", "--add-tag", "y"},
		{"--tag", "x", "--clear-tags"},
		{"--note", "a", "--note-file", "-"},
	} {
		if code, _, _ := runMain(t, f.args(append([]string{"exp", "annotate", "alice/exp", "ocr", "run-a"}, args...)...), ""); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestExpHelp(t *testing.T) {
	isolateEnv(t)
	for _, args := range [][]string{
		{"experiments", "help", "wait"},
		{"exp", "wait", "--help"},
	} {
		code, out, _ := runMain(t, args, "")
		if code != 0 || !strings.Contains(out, "--until EXPR") || !strings.Contains(out, "--ignore-stale") {
			t.Errorf("%v: exit %d, out %q", args, code, out)
		}
	}
	code, out, _ := runMain(t, []string{"help", "experiments"}, "")
	if code != 0 || !strings.Contains(out, "annotate") {
		t.Errorf("tf help experiments: %d %q", code, out)
	}
	if code, _, _ := runMain(t, []string{"exp", "help", "nope"}, ""); code != 2 {
		t.Errorf("unknown help topic: exit %d", code)
	}
}

func TestEditTagList(t *testing.T) {
	got := editTagList([]string{"a", "b", "c"}, []string{"d", "a", " e "}, []string{"b"})
	if !reflect.DeepEqual(got, []string{"a", "c", "d", "e"}) {
		t.Errorf("editTagList = %v", got)
	}
	if got := editTagList(nil, nil, []string{"x"}); got == nil || len(got) != 0 {
		t.Errorf("removing from nothing must give an empty (non-nil) list: %#v", got)
	}
}

func TestFormatHelpers(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		5 * time.Second: "5s ago", 5 * time.Minute: "5m ago", 5 * time.Hour: "5h ago",
		50 * time.Hour: "2d ago", 10 * 24 * time.Hour: "2026-09-17",
	} {
		if got := formatAge(now.Add(-d), now); got != want {
			t.Errorf("formatAge(%s) = %q, want %q", d, got, want)
		}
	}
	for v, want := range map[float64]string{12000: "12000", 0.1234567: "0.123457", 1e-7: "1e-07"} {
		if got := formatFloat(v); got != want {
			t.Errorf("formatFloat(%v) = %q, want %q", v, got, want)
		}
	}
	for _, tc := range []struct {
		v    any
		want string
	}{
		{float64(3), "3"}, {0.001, "0.001"}, {"adam", "adam"}, {true, "true"}, {nil, "null"},
		{[]any{float64(1), "x"}, `[1,"x"]`},
	} {
		if got := formatValue(tc.v); got != tc.want {
			t.Errorf("formatValue(%#v) = %q, want %q", tc.v, got, tc.want)
		}
	}
	cfg := map[string]any{"a.b": 1.0, "opt": map[string]any{"lr": 0.1}}
	if v, ok := configLookup(cfg, "a.b"); !ok || v != 1.0 {
		t.Errorf("literal dotted key: %v %v", v, ok)
	}
	if v, ok := configLookup(cfg, "opt.lr"); !ok || v != 0.1 {
		t.Errorf("nested key: %v %v", v, ok)
	}
	if _, ok := configLookup(cfg, "opt.lr.x"); ok {
		t.Error("walking past a leaf must fail")
	}
}
