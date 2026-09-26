package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// recorder captures the last request a fake server saw.
type recorder struct {
	method      string
	escapedPath string
	query       url.Values
	body        []byte
}

func recordingServer(t *testing.T, status int, respBody string) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.escapedPath = r.URL.EscapedPath()
		rec.query = r.URL.Query()
		rec.body, _ = io.ReadAll(r.Body)
		if got := r.Header.Get("Authorization"); got != "Bearer tf_token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	return c, rec
}

// Every name segment must arrive escaped on its own: a "/" inside a project
// or run name must not become a path separator, and "%" / spaces / non-ASCII
// must decode back to exactly the name.
func TestExperimentPathEscaping(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"run":{"name":"exp 1/seed-2"}}`)
	run, err := c.GetRun(context.Background(), "alice", "trackio-metrics", "実験/100%", "exp 1/seed-2", WaitOpts{})
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Name != "exp 1/seed-2" {
		t.Errorf("run = %+v", run)
	}
	want := "/api/v1/experiments/alice/trackio-metrics/" +
		url.PathEscape("実験/100%") + "/runs/" + url.PathEscape("exp 1/seed-2")
	if rec.escapedPath != want {
		t.Errorf("path = %s, want %s", rec.escapedPath, want)
	}
	if rec.escapedPath != "/api/v1/experiments/alice/trackio-metrics/%E5%AE%9F%E9%A8%93%2F100%25/runs/exp%201%2Fseed-2" {
		t.Errorf("path = %s", rec.escapedPath)
	}
	if len(rec.query) != 0 {
		t.Errorf("a plain GetRun must send no query, got %v", rec.query)
	}
}

func TestListRunsQuery(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"runs":[{"name":"a","status":"running","summary_min":{"loss":0.1}}],
		"metric_goals":{"loss":"min"},"best":{"loss":"a"}}`)
	archived := false
	resp, err := c.ListRuns(context.Background(), "alice", "exp", "ocr", RunQuery{
		Groups: []string{"sweep,1", "sweep 2"}, Statuses: []string{"running", "stale"},
		Tags: []string{"good"}, Archived: &archived, Sort: "best:loss", Order: "desc", Limit: 5,
	})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if rec.method != http.MethodGet || rec.escapedPath != "/api/v1/experiments/alice/exp/ocr/runs" {
		t.Errorf("%s %s", rec.method, rec.escapedPath)
	}
	wantQ := url.Values{
		"group": {"sweep,1", "sweep 2"}, "status": {"running", "stale"}, "tag": {"good"},
		"archived": {"false"}, "sort": {"best:loss"}, "order": {"desc"}, "limit": {"5"},
	}
	if !reflect.DeepEqual(rec.query, wantQ) {
		t.Errorf("query = %v, want %v", rec.query, wantQ)
	}
	if len(resp.Runs) != 1 || resp.Runs[0].SummaryMin["loss"] != 0.1 ||
		resp.MetricGoals["loss"] != apitypes.MetricGoalMin || resp.Best["loss"] != "a" {
		t.Errorf("resp = %+v", resp)
	}

	// The zero query sends nothing.
	if _, err := c.ListRuns(context.Background(), "alice", "exp", "ocr", RunQuery{}); err != nil {
		t.Fatal(err)
	}
	if len(rec.query) != 0 {
		t.Errorf("zero RunQuery sent %v", rec.query)
	}
}

func TestGetRunWaitParams(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"run":{"name":"r"}}`)
	since := time.Date(2026, 9, 27, 10, 0, 0, 123456789, time.FixedZone("JST", 9*3600))
	_, err := c.GetRun(context.Background(), "a", "b", "p", "r", WaitOpts{Wait: 55 * time.Second, Since: since, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.query.Get("wait"); got != "55s" {
		t.Errorf("wait = %q", got)
	}
	// Full precision, in UTC: a since truncated to seconds would be older
	// than the updated_at it came from and every poll would return at once.
	if got := rec.query.Get("since"); got != "2026-09-27T01:00:00.123456789Z" {
		t.Errorf("since = %q", got)
	}
	if got := rec.query.Get("status"); got != "running" {
		t.Errorf("status = %q", got)
	}

	// Clamped to MaxRunWait; sub-second waits stay parseable durations.
	if _, err := c.GetRun(context.Background(), "a", "b", "p", "r", WaitOpts{Wait: 5 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if got := rec.query.Get("wait"); got != "60s" {
		t.Errorf("clamped wait = %q", got)
	}
	if _, err := c.GetRun(context.Background(), "a", "b", "p", "r", WaitOpts{Wait: 1500 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got := rec.query.Get("wait"); got != "1.5s" {
		t.Errorf("sub-second wait = %q", got)
	}
	if _, err := time.ParseDuration(rec.query.Get("wait")); err != nil {
		t.Errorf("wait is not a Go duration: %v", err)
	}
}

func TestGetRunNotFound(t *testing.T) {
	c, _ := recordingServer(t, 404, `{"error":{"type":"not_found","message":"run not found"}}`)
	_, err := c.GetRun(context.Background(), "a", "b", "p", "r", WaitOpts{})
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want a 404", err)
	}
}

func TestGetMetricsAndConfigDiffQuery(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"series":[{"run":"a,b","key":"loss","points":[[0,1.5],[10,0.5]]}]}`)
	resp, err := c.GetMetrics(context.Background(), "a", "b", "p", []string{"a,b", "c"}, []string{"loss", "val/CER"}, "time", 200)
	if err != nil {
		t.Fatal(err)
	}
	if rec.escapedPath != "/api/v1/experiments/a/b/p/metrics" {
		t.Errorf("path = %s", rec.escapedPath)
	}
	wantQ := url.Values{"run": {"a,b", "c"}, "key": {"loss", "val/CER"}, "x": {"time"}, "max_points": {"200"}}
	if !reflect.DeepEqual(rec.query, wantQ) {
		t.Errorf("query = %v, want %v", rec.query, wantQ)
	}
	if len(resp.Series) != 1 || resp.Series[0].Points[1] != [2]float64{10, 0.5} {
		t.Errorf("series = %+v", resp.Series)
	}

	c2, rec2 := recordingServer(t, 200, `{"runs":["a","b"],"keys":[{"key":"lr","values":{"a":0.1,"b":0.2}}]}`)
	diff, err := c2.ConfigDiff(context.Background(), "a", "b", "p", []string{"a", "b"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if rec2.escapedPath != "/api/v1/experiments/a/b/p/config-diff" {
		t.Errorf("path = %s", rec2.escapedPath)
	}
	if !reflect.DeepEqual(rec2.query, url.Values{"run": {"a", "b"}, "include_meta": {"true"}}) {
		t.Errorf("query = %v", rec2.query)
	}
	if len(diff.Keys) != 1 || diff.Keys[0].Values["b"] != 0.2 {
		t.Errorf("diff = %+v", diff)
	}
}

func TestUpdateProjectBody(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"name":"ocr","num_runs":0,"metric_goals":{"cer":"min"}}`)
	p, err := c.UpdateProject(context.Background(), "a", "b", "ocr", map[string]string{"cer": "min", "old": ""})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch || rec.escapedPath != "/api/v1/experiments/a/b/ocr" {
		t.Errorf("%s %s", rec.method, rec.escapedPath)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(rec.body, &body); err != nil {
		t.Fatal(err)
	}
	// "" must be sent (it removes the goal), not dropped.
	if !reflect.DeepEqual(body["metric_goals"], map[string]string{"cer": "min", "old": ""}) {
		t.Errorf("body = %s", rec.body)
	}
	if p.MetricGoals["cer"] != apitypes.MetricGoalMin {
		t.Errorf("project = %+v", p)
	}
}

func TestNotes(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"path":"ocr/NOTES.md","content":"# hi\n","exists":true,"blob_sha":"abc","commit_sha":"def"}`)
	n, err := c.GetNotes(context.Background(), "a", "b", "ocr")
	if err != nil {
		t.Fatal(err)
	}
	if rec.escapedPath != "/api/v1/experiments/a/b/ocr/notes" || !n.Exists || n.BlobSHA != "abc" {
		t.Errorf("%s %+v", rec.escapedPath, n)
	}

	// base_sha: nil is omitted (overwrite); "" is sent (must not exist yet).
	for _, tc := range []struct {
		base *string
		want string
	}{
		{nil, `{"content":"x","message":"m"}`},
		{expPtr(""), `{"content":"x","base_sha":"","message":"m"}`},
		{expPtr("abc"), `{"content":"x","base_sha":"abc","message":"m"}`},
	} {
		if _, err := c.PutNotes(context.Background(), "a", "b", "ocr", "x", tc.base, "m"); err != nil {
			t.Fatal(err)
		}
		if rec.method != http.MethodPut || string(rec.body) != tc.want {
			t.Errorf("%s body = %s, want %s", rec.method, rec.body, tc.want)
		}
	}

	c2, _ := recordingServer(t, 409, `{"error":{"type":"conflict","message":"notes changed"}}`)
	if _, err := c2.PutNotes(context.Background(), "a", "b", "ocr", "x", expPtr("old"), ""); !IsConflict(err) {
		t.Errorf("err = %v, want a 409", err)
	}
}

func TestAnnotateRunBody(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"run":{"name":"r","tags":[],"archived":true}}`)
	tags := []string{}
	archived := true
	run, err := c.AnnotateRun(context.Background(), "a", "b", "p", "r/1", apitypes.ExpRunAnnotationRequest{Tags: &tags, Archived: &archived})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPatch || rec.escapedPath != "/api/v1/experiments/a/b/p/runs/r%2F1" {
		t.Errorf("%s %s", rec.method, rec.escapedPath)
	}
	// An empty tag list is sent as [] (clears), fields not set are omitted.
	if string(rec.body) != `{"tags":[],"archived":true}` {
		t.Errorf("body = %s", rec.body)
	}
	if !run.Archived {
		t.Errorf("run = %+v", run)
	}
}

func TestListAndGetExperimentRepos(t *testing.T) {
	c, rec := recordingServer(t, 200, `{"items":[{"namespace":"a","name":"b","full_name":"a/b","num_projects":2}],"total":1}`)
	list, err := c.ListExperimentRepos(context.Background(), "a", "ocr models")
	if err != nil {
		t.Fatal(err)
	}
	if rec.escapedPath != "/api/v1/experiments" || !reflect.DeepEqual(rec.query, url.Values{"author": {"a"}, "search": {"ocr models"}}) {
		t.Errorf("%s %v", rec.escapedPath, rec.query)
	}
	if list.Total != 1 || list.Items[0].FullName != "a/b" {
		t.Errorf("list = %+v", list)
	}

	c2, rec2 := recordingServer(t, 200, `{"repo":{"full_name":"a/b"},"projects":[{"name":"ocr","metric_goals":{"cer":"min"}}]}`)
	repo, err := c2.GetExperimentRepo(context.Background(), "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if rec2.escapedPath != "/api/v1/experiments/a/b" {
		t.Errorf("path = %s", rec2.escapedPath)
	}
	if len(repo.Projects) != 1 || repo.Projects[0].MetricGoals["cer"] != "min" {
		t.Errorf("repo = %+v", repo)
	}
}

func expPtr[T any](v T) *T { return &v }
