package tfcli

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

// ---------------------------------------------------------------- fake server

// fakeIngest is an in-memory stand-in for the endpoints import and sync use:
// whoami, repository existence/creation, the experiment ingest API, and the
// tree / preupload / commit trio artifacts go through.
type fakeIngest struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	repos    map[string]bool // "ns/name"
	created  []string
	runs     map[string]map[string]*apitypes.ExpRun // project -> run -> run
	logs     []fakeLogCall
	finishes []fakeFinishCall
	deletes  []string // "project/run"
	models   map[string][]apitypes.ExpRunModelInput
	tree     map[string]string // repo path -> content
	commits  [][]string
	// failLog makes the n-th /log call (1-based, counted over the server's
	// lifetime) answer 500.
	failLog int
	// forbidCreate makes repository creation answer 403, the way it does for
	// a token restricted to other repositories.
	forbidCreate bool
}

type fakeLogCall struct {
	Repo    string
	Project string
	Req     hub.IngestLogRequest
}

type fakeFinishCall struct {
	Repo    string
	Project string
	Req     hub.IngestFinishRequest
}

func newFakeIngest(t *testing.T) *fakeIngest {
	t.Helper()
	f := &fakeIngest{
		t:      t,
		repos:  map[string]bool{},
		runs:   map[string]map[string]*apitypes.ExpRun{},
		models: map[string][]apitypes.ExpRunModelInput{},
		tree:   map[string]string{},
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /api/whoami-v2", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"name": "alice", "auth": map[string]any{"accessToken": map[string]any{"role": "write"}}})
	})
	mux.HandleFunc("GET /api/datasets/{ns}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.repos[r.PathValue("ns")+"/"+r.PathValue("name")] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply(w, map[string]any{"id": r.PathValue("ns") + "/" + r.PathValue("name")})
	})
	mux.HandleFunc("POST /api/repos/create", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Type, Name string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.forbidCreate {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
				"type": "token_restricted", "message": "this token is restricted to specific repositories",
			}})
			return
		}
		if body.Type != "dataset" {
			t.Errorf("create repo type = %q, want dataset", body.Type)
		}
		f.repos[body.Name] = true
		f.created = append(f.created, body.Name)
		reply(w, map[string]any{"ok": true})
	})
	exp := "/api/v1/experiments/{ns}/{repo}/{project}"
	mux.HandleFunc("GET "+exp+"/runs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := apitypes.ExpRunListResponse{Runs: []apitypes.ExpRun{}}
		for _, run := range f.runs[r.PathValue("project")] {
			out.Runs = append(out.Runs, *run)
		}
		reply(w, out)
	})
	mux.HandleFunc("DELETE "+exp+"/runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p, name := r.PathValue("project"), r.PathValue("run")
		if f.runs[p][name] == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.runs[p], name)
		f.deletes = append(f.deletes, p+"/"+name)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PATCH "+exp+"/runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		var body apitypes.ExpRunAnnotationRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		p, name := r.PathValue("project"), r.PathValue("run")
		if f.runs[p][name] == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if body.Models != nil {
			f.models[p+"/"+name] = *body.Models
		}
		reply(w, map[string]any{"run": f.runs[p][name]})
	})
	mux.HandleFunc("POST "+exp+"/log", func(w http.ResponseWriter, r *http.Request) {
		var req hub.IngestLogRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode log: %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(req.Points) > hub.IngestMaxPoints {
			t.Errorf("log batch of %d points exceeds the server cap", len(req.Points))
		}
		f.logs = append(f.logs, fakeLogCall{Repo: r.PathValue("ns") + "/" + r.PathValue("repo"), Project: r.PathValue("project"), Req: req})
		if f.failLog > 0 && len(f.logs) == f.failLog {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		run := f.run(r.PathValue("project"), req.Run)
		run.NumPoints += int64(len(req.Points))
		run.Status = "running"
		if req.Status != "" {
			run.Status = apitypes.RunStatus(req.Status)
		}
		if req.Config != nil {
			run.Config = req.Config
		}
		reply(w, map[string]any{"ok": true, "run": req.Run, "accepted": len(req.Points)})
	})
	mux.HandleFunc("POST "+exp+"/finish", func(w http.ResponseWriter, r *http.Request) {
		var req hub.IngestFinishRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.finishes = append(f.finishes, fakeFinishCall{Repo: r.PathValue("ns") + "/" + r.PathValue("repo"), Project: r.PathValue("project"), Req: req})
		f.run(r.PathValue("project"), req.Run).Status = apitypes.RunStatus(req.Status)
		reply(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/datasets/{ns}/{name}/tree/{rev}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		entries := []map[string]any{}
		for p, content := range f.tree {
			oid, err := hub.GitBlobSHA1(strings.NewReader(content), int64(len(content)))
			if err != nil {
				t.Errorf("hash %s: %v", p, err)
			}
			entries = append(entries, map[string]any{"type": "file", "path": p, "size": len(content), "oid": oid})
		}
		reply(w, entries)
	})
	mux.HandleFunc("POST /api/datasets/{ns}/{name}/preupload/{rev}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Files []struct{ Path string } `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := []map[string]string{}
		for _, f := range req.Files {
			out = append(out, map[string]string{"path": f.Path, "uploadMode": "regular"})
		}
		reply(w, map[string]any{"files": out})
	})
	mux.HandleFunc("POST /api/datasets/{ns}/{name}/commit/{rev}", func(w http.ResponseWriter, r *http.Request) {
		sc := bufio.NewScanner(r.Body)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		var paths []string
		f.mu.Lock()
		defer f.mu.Unlock()
		for sc.Scan() {
			var line struct {
				Key   string `json:"key"`
				Value struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				} `json:"value"`
			}
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				t.Errorf("commit line: %v", err)
				continue
			}
			if line.Key == "file" {
				data, _ := base64.StdEncoding.DecodeString(line.Value.Content)
				f.tree[line.Value.Path] = string(data)
				paths = append(paths, line.Value.Path)
			}
		}
		f.commits = append(f.commits, paths)
		reply(w, map[string]any{"success": true, "commitOid": "abcdef1234567", "commitUrl": "http://x/commit/abcdef1"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
		w.WriteHeader(http.StatusNotFound)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// run returns (creating) a run; the caller holds f.mu.
func (f *fakeIngest) run(project, name string) *apitypes.ExpRun {
	if f.runs[project] == nil {
		f.runs[project] = map[string]*apitypes.ExpRun{}
	}
	if f.runs[project][name] == nil {
		f.runs[project][name] = &apitypes.ExpRun{Name: name}
	}
	return f.runs[project][name]
}

func (f *fakeIngest) addRun(project, name string, config map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run(project, name).Config = config
}

// logSizes is the number of points of every /log call, in order.
func (f *fakeIngest) logSizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, 0, len(f.logs))
	for _, l := range f.logs {
		out = append(out, len(l.Req.Points))
	}
	return out
}

func (f *fakeIngest) client() *hub.Client { return hub.New(f.srv.URL, "t") }

// ------------------------------------------------------------------- helpers

// writeRunJSONL writes lines (each terminated by "\n") plus an optional
// unterminated tail to dir/run.jsonl.
func writeRunJSONL(t *testing.T, dir string, lines []string, tail string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString(tail)
	if err := os.WriteFile(filepath.Join(dir, runJSONLName), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendRunJSONL(t *testing.T, dir string, lines ...string) {
	t.Helper()
	fh, err := os.OpenFile(filepath.Join(dir, runJSONLName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, l := range lines {
		if _, err := fh.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func initLine(repo, project, run, resume string) string {
	r := "null"
	if repo != "" {
		r = fmt.Sprintf("%q", repo)
	}
	return fmt.Sprintf(`{"v":1,"type":"init","time":"2026-09-27T00:00:00Z","repo":%s,"project":%q,"run":%q,"resume":%q,"group":"g","job_type":"train","config":{"lr":0.1}}`,
		r, project, run, resume)
}

// initLineConfig is initLine with the config given as raw JSON ("null", or
// "" to leave the key out).
func initLineConfig(repo, project, run, resume, config string) string {
	line := strings.TrimSuffix(initLine(repo, project, run, resume), `,"config":{"lr":0.1}}`)
	if config != "" {
		line += `,"config":` + config
	}
	return line + "}"
}

// logLine is a log record carrying n points starting at step from.
func logLine(from, n int) string {
	pts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		pts = append(pts, fmt.Sprintf(`{"step":%d,"timestamp":"2026-09-27T00:00:00Z","metrics":{"loss":%d.5}}`, from+i, from+i))
	}
	return `{"v":1,"type":"log","points":[` + strings.Join(pts, ",") + `]}`
}

const finishLine = `{"v":1,"type":"finish","time":"2026-09-27T01:00:00Z","status":"finished"}`

func mustState(t *testing.T, dir string) syncState {
	t.Helper()
	st, err := readSyncState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------- tests

func TestSyncFullRunWithNullRepo(t *testing.T) {
	f := newFakeIngest(t)
	dir := filepath.Join(t.TempDir(), "20260927T000000-p-r-deadbeef")
	writeRunJSONL(t, dir, []string{
		initLine("", "my proj", "run/1", "never"),
		logLine(0, 3),
		`{"v":1,"type":"artifact","name":"media/samples/step_00000010.png","path":"artifacts/media/samples/step_00000010.png"}`,
		`{"v":1,"type":"model","repo_id":"alice/ocr","revision":"abc"}`,
		`{"v":1,"type":"future_thing","x":1}`,
		`{"v":1,"type":"log","points":[{"step":3,"metrics":{"loss":0.1,"acc":null}}],"config":{"lr":0.2}}`,
		finishLine,
	}, "")
	art := filepath.Join(dir, "artifacts", "media", "samples", "step_00000010.png")
	if err := os.MkdirAll(filepath.Dir(art), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(art, []byte("png!"), 0o644); err != nil {
		t.Fatal(err)
	}

	var errOut bytes.Buffer
	s := newOfflineSyncer(f.client(), &errOut)
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatalf("sync error: %s", res.Error)
	}
	if !res.Done || res.Status != "finished" || res.Points != 4 {
		t.Fatalf("result = %+v", res)
	}
	if len(f.created) != 1 || f.created[0] != "alice/trackio-metrics" {
		t.Errorf("created = %v, want alice/trackio-metrics", f.created)
	}
	// The two log records are coalesced into one call carrying the latest config.
	if got := f.logSizes(); !equalInts(got, []int{4}) {
		t.Fatalf("log calls = %v, want [4]", got)
	}
	l := f.logs[0]
	if l.Repo != "alice/trackio-metrics" || l.Project != "my proj" || l.Req.Run != "run/1" {
		t.Errorf("log target = %s %s %s", l.Repo, l.Project, l.Req.Run)
	}
	if l.Req.Config["lr"] != 0.2 || l.Req.Group != "g" || l.Req.JobType != "train" {
		t.Errorf("log req = %+v", l.Req)
	}
	if _, ok := l.Req.Points[3].Metrics["acc"]; ok {
		t.Error("a null metric value must not be sent")
	}
	wantPath := "my proj/artifacts/run/1/media/samples/step_00000010.png"
	if len(f.commits) != 1 || len(f.commits[0]) != 1 || f.commits[0][0] != wantPath {
		t.Errorf("commits = %v, want [[%s]]", f.commits, wantPath)
	}
	if len(f.finishes) != 1 || f.finishes[0].Req.Status != "finished" {
		t.Errorf("finishes = %+v", f.finishes)
	}
	if m := f.models["my proj/run/1"]; len(m) != 1 || m[0].RepoID != "alice/ocr" || m[0].Revision != "abc" {
		t.Errorf("models = %+v", m)
	}
	if !strings.Contains(errOut.String(), "future_thing") {
		t.Errorf("expected a warning about the unknown record type, got %q", errOut.String())
	}
	st := mustState(t, dir)
	if !st.Done || st.SyncedLines != 7 || st.Run != "run/1" || st.Repo != "alice/trackio-metrics" || st.V != 1 {
		t.Errorf("state = %+v", st)
	}

	// A done directory is skipped without a single request.
	before := len(f.logs) + len(f.finishes)
	res = s.syncDir(t.Context(), dir)
	if !res.AlreadyDone || len(f.logs)+len(f.finishes) != before {
		t.Errorf("second pass = %+v", res)
	}
}

func TestSyncResumesAfterPartialSync(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "never"),
		logLine(0, 2),
		logLine(2, 2),
		logLine(4, 2),
	}, "")
	// A previous pass delivered the init and the first log record.
	if err := writeSyncState(dir, syncState{SyncedLines: 2, Run: "r-1", Repo: "alice/exp"}); err != nil {
		t.Fatal(err)
	}
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if got := f.logSizes(); !equalInts(got, []int{4}) {
		t.Fatalf("log calls = %v, want [4]", got)
	}
	l := f.logs[0].Req
	if l.Run != "r-1" {
		t.Errorf("run = %q, want the name recorded in the state (r-1)", l.Run)
	}
	if l.Points[0].Step != 2 || l.Config != nil {
		t.Errorf("resumed batch starts at step %d with config %v; want step 2 and no config", l.Points[0].Step, l.Config)
	}
	if st := mustState(t, dir); st.SyncedLines != 4 || st.Done {
		t.Errorf("state = %+v, want synced_lines 4, not done", st)
	}
}

func TestSyncPersistsProgressPerCallAndStopsOnFailure(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.failLog = 2
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "never"),
		logLine(0, 6000),
		logLine(6000, 6000),
		finishLine,
	}, "")
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	res := s.syncDir(t.Context(), dir)
	if res.Error == "" {
		t.Fatal("expected the failing /log call to fail the directory")
	}
	// The first call (line 2) succeeded, the second failed: the state stops
	// after line 2 and the run is not finished.
	if st := mustState(t, dir); st.SyncedLines != 2 || st.Done {
		t.Errorf("state = %+v, want synced_lines 2, not done", st)
	}
	if len(f.finishes) != 0 {
		t.Error("finish must not be posted after a failed batch")
	}
	// The retry re-sends only the undelivered record.
	res = s.syncDir(t.Context(), dir)
	if res.Error != "" || !res.Done {
		t.Fatalf("retry = %+v", res)
	}
	if got := f.logSizes(); !equalInts(got, []int{6000, 6000, 6000}) {
		t.Errorf("log calls = %v, want [6000 6000 6000]", got)
	}
}

func TestSyncCoalescesAndSplitsBatches(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "never"),
		logLine(0, 4000),
		logLine(4000, 4000),
		logLine(8000, 4000),
		logLine(12000, 15000), // too big for one call on its own
		logLine(27000, 10),
	}, "")
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	want := []int{8000, 4000, 10000, 5010}
	if got := f.logSizes(); !equalInts(got, want) {
		t.Fatalf("log calls = %v, want %v", got, want)
	}
	if res.Points != 27010 {
		t.Errorf("points = %d", res.Points)
	}
	// The first call carries the init config, later ones none.
	if f.logs[0].Req.Config["lr"] != 0.1 || f.logs[1].Req.Config != nil {
		t.Errorf("config placement: %v / %v", f.logs[0].Req.Config, f.logs[1].Req.Config)
	}
	if st := mustState(t, dir); st.SyncedLines != 6 || st.Done {
		t.Errorf("state = %+v", st)
	}
}

func TestSyncStopsAtTruncatedLine(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	dir := t.TempDir()
	// A torn line in the middle (the writer died) ends the file even though
	// more lines follow; an unterminated tail is never read.
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "never"),
		logLine(0, 2),
		`{"v":1,"type":"log","points":[{"step":2,"met`,
		logLine(3, 2),
	}, logLine(5, 1))
	var errOut bytes.Buffer
	s := newOfflineSyncer(f.client(), &errOut)
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if got := f.logSizes(); !equalInts(got, []int{2}) {
		t.Fatalf("log calls = %v, want [2]", got)
	}
	if st := mustState(t, dir); st.SyncedLines != 2 || st.Done {
		t.Errorf("state = %+v", st)
	}
	if !strings.Contains(errOut.String(), "does not parse") {
		t.Errorf("expected a warning about the torn line, got %q", errOut.String())
	}

	// Only an unterminated tail: nothing past the last newline is read.
	dir2 := t.TempDir()
	writeRunJSONL(t, dir2, []string{initLine("alice/exp", "p", "r2", "never")}, logLine(0, 3))
	res = s.syncDir(t.Context(), dir2)
	if res.Error != "" || res.Points != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestSyncResumeNeverPicksFreeName(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.addRun("p", "r", nil)
	f.addRun("p", "r-1", nil)
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{initLine("alice/exp", "p", "r", "never"), logLine(0, 1)}, "")
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.Run != "r-2" || f.logs[0].Req.Run != "r-2" {
		t.Errorf("run = %q / %q, want r-2", res.Run, f.logs[0].Req.Run)
	}
	if st := mustState(t, dir); st.Run != "r-2" {
		t.Errorf("state run = %q", st.Run)
	}

	// The next pass keeps using the recorded name even though it is now taken
	// (by this very run).
	appendRunJSONL(t, dir, logLine(1, 1))
	res = s.syncDir(t.Context(), dir)
	if res.Error != "" || f.logs[1].Req.Run != "r-2" {
		t.Errorf("second pass run = %q (%s)", f.logs[1].Req.Run, res.Error)
	}
}

func TestSyncResumeAllowAndMust(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.addRun("p", "r", map[string]any{"old": "kept", "lr": 1.0})

	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{initLine("alice/exp", "p", "r", "allow"), logLine(0, 1)}, "")
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" || res.Run != "r" {
		t.Fatalf("allow: %+v", res)
	}
	cfg := f.logs[0].Req.Config
	if cfg["old"] != "kept" || cfg["lr"] != 0.1 {
		t.Errorf("merged config = %v, want old kept and lr overridden", cfg)
	}

	dir2 := t.TempDir()
	writeRunJSONL(t, dir2, []string{initLine("alice/exp", "p", "missing", "must"), logLine(0, 1)}, "")
	res = s.syncDir(t.Context(), dir2)
	if res.Error == "" || !strings.Contains(res.Error, "must") {
		t.Fatalf("must on a missing run: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir2, syncStateName)); !os.IsNotExist(err) {
		t.Error("a failed resolution must not write a sync state")
	}
}

func TestSyncSkipsArtifactsAlreadyPresent(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.tree["p/artifacts/r/model.bin"] = "12345"
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "never"),
		`{"v":1,"type":"artifact","name":"model.bin","path":"artifacts/model.bin"}`,
		`{"v":1,"type":"artifact","name":"notes.txt","path":"artifacts/notes.txt"}`,
		`{"v":1,"type":"artifact","name":"gone.txt","path":"artifacts/gone.txt"}`,
		`{"v":1,"type":"artifact","name":"../escape","path":"../escape"}`,
		finishLine,
	}, "")
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"model.bin": "12345", "notes.txt": "hi"} {
		if err := os.WriteFile(filepath.Join(dir, "artifacts", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var errOut bytes.Buffer
	s := newOfflineSyncer(f.client(), &errOut)
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.ArtifactsSkipped != 1 || res.ArtifactsUploaded != 1 {
		t.Errorf("artifacts uploaded/skipped = %d/%d, want 1/1", res.ArtifactsUploaded, res.ArtifactsSkipped)
	}
	if len(f.commits) != 1 || len(f.commits[0]) != 1 || f.commits[0][0] != "p/artifacts/r/notes.txt" {
		t.Errorf("commits = %v", f.commits)
	}
	for _, want := range []string{"gone.txt", "escape"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("expected a warning mentioning %q, got %q", want, errOut.String())
		}
	}
	// No point was ever logged: /finish is what creates the run.
	if len(f.logs) != 1 || len(f.logs[0].Req.Points) != 0 {
		t.Errorf("logs = %+v, want one config-only call", f.logs)
	}

	// Everything already present: no commit at all.
	f.tree["p/artifacts/r2/model.bin"] = "12345"
	dir2 := t.TempDir()
	writeRunJSONL(t, dir2, []string{
		initLine("alice/exp", "p", "r2", "never"),
		`{"v":1,"type":"artifact","name":"model.bin","path":"artifacts/model.bin"}`,
		finishLine,
	}, "")
	_ = os.MkdirAll(filepath.Join(dir2, "artifacts"), 0o755)
	_ = os.WriteFile(filepath.Join(dir2, "artifacts", "model.bin"), []byte("12345"), 0o644)
	res = s.syncDir(t.Context(), dir2)
	if res.Error != "" || len(f.commits) != 1 || res.ArtifactsSkipped != 1 {
		t.Errorf("second run: %+v, commits %v", res, f.commits)
	}
}

func TestSyncFollowsOpenRunAcrossPasses(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	parent := t.TempDir()
	dir := filepath.Join(parent, "b-run")
	writeRunJSONL(t, dir, []string{initLine("alice/exp", "p", "r", "never"), logLine(0, 2)}, "")
	// A sibling that is not a run directory is ignored.
	_ = os.MkdirAll(filepath.Join(parent, "a-not-a-run"), 0o755)

	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	results := s.syncPass(t.Context(), []string{parent}, false)
	if len(results) != 1 || results[0].Error != "" || results[0].Done {
		t.Fatalf("pass 1 = %+v", results)
	}

	// Nothing new: a pass sends nothing.
	results = s.syncPass(t.Context(), []string{parent}, false)
	if results[0].Points != 0 || len(f.logs) != 1 {
		t.Fatalf("idle pass = %+v, logs %d", results, len(f.logs))
	}

	appendRunJSONL(t, dir, logLine(2, 3), finishLine)
	results = s.syncPass(t.Context(), []string{parent}, false)
	if !results[0].Done || results[0].Points != 3 {
		t.Fatalf("pass 3 = %+v", results)
	}
	results = s.syncPass(t.Context(), []string{parent}, false)
	if !results[0].AlreadyDone {
		t.Fatalf("pass 4 = %+v", results)
	}
	if len(f.finishes) != 1 {
		t.Errorf("finishes = %d, want 1", len(f.finishes))
	}
}

func TestFindRunDirs(t *testing.T) {
	parent := t.TempDir()
	for _, n := range []string{"c", "a", "b"} {
		writeRunJSONL(t, filepath.Join(parent, n), []string{"{}"}, "")
	}
	_ = os.MkdirAll(filepath.Join(parent, "empty"), 0o755)
	dirs, errs := findRunDirs([]string{parent, filepath.Join(parent, "a")}, false)
	want := []string{filepath.Join(parent, "a"), filepath.Join(parent, "b"), filepath.Join(parent, "c")}
	if len(errs) != 0 || strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Errorf("dirs = %v errs = %v, want %v", dirs, errs, want)
	}
	// A missing default directory is empty; a missing explicit one is an error.
	missing := filepath.Join(parent, "nope")
	if dirs, errs := findRunDirs([]string{missing}, true); len(dirs)+len(errs) != 0 {
		t.Errorf("default missing: %v %v", dirs, errs)
	}
	if _, errs := findRunDirs([]string{missing}, false); len(errs) != 1 {
		t.Errorf("explicit missing: %v", errs)
	}
}

func TestSyncCommandJSON(t *testing.T) {
	isolateEnv(t)
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	parent := t.TempDir()
	writeRunJSONL(t, filepath.Join(parent, "ok"), []string{initLine("alice/exp", "p", "r", "never"), logLine(0, 1), finishLine}, "")
	writeRunJSONL(t, filepath.Join(parent, "zbad"), []string{`{"v":1,"type":"log","points":[]}`}, "")

	var out, errOut bytes.Buffer
	code := Main([]string{"experiments", "sync", parent, "--json", "--endpoint", f.srv.URL, "--token", "t"}, nil, &out, &errOut)
	if code != exitError {
		t.Fatalf("exit = %d, want 1 (one directory failed); stderr=%s", code, errOut.String())
	}
	var got struct {
		Runs []syncDirResult `json:"runs"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if len(got.Runs) != 2 || !got.Runs[0].Done || got.Runs[1].Error == "" {
		t.Errorf("runs = %+v", got.Runs)
	}
}

// Artifacts are compared by content: a new checkpoint the same size as the
// one already committed is still uploaded.
func TestSyncCommitsSameSizeNewArtifactVersion(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.tree["p/artifacts/r/model.bin"] = "AAAAA"
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "allow"),
		`{"v":1,"type":"artifact","name":"model.bin","path":"artifacts/model.bin"}`,
		finishLine,
	}, "")
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "artifacts", "model.bin"), []byte("BBBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.ArtifactsUploaded != 1 || res.ArtifactsSkipped != 0 {
		t.Errorf("artifacts uploaded/skipped = %d/%d, want 1/0", res.ArtifactsUploaded, res.ArtifactsSkipped)
	}
	if got := f.tree["p/artifacts/r/model.bin"]; got != "BBBBB" {
		t.Errorf("server holds %q, want the new version", got)
	}
}

// The init config is part of line 0, which stays undelivered until a /log
// succeeds: a failed first call must not lose it.
func TestSyncDeliversInitConfigAfterFailedFirstLog(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.failLog = 1
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{initLine("alice/exp", "p", "r", "never"), logLine(0, 2)}, "")
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	if res := s.syncDir(t.Context(), dir); res.Error == "" {
		t.Fatal("expected the failing first /log to fail the directory")
	}
	// The resolution is recorded, the init line is not.
	if st := mustState(t, dir); st.SyncedLines != 0 || st.Run != "r" || st.Repo != "alice/exp" {
		t.Fatalf("state after the failure = %+v, want synced_lines 0 with run/repo resolved", st)
	}

	res := newOfflineSyncer(f.client(), &bytes.Buffer{}).syncDir(t.Context(), dir)
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if len(f.logs) != 2 {
		t.Fatalf("log calls = %d, want 2", len(f.logs))
	}
	l := f.logs[1].Req
	if l.Run != "r" || len(l.Points) != 2 || l.Config["lr"] != 0.1 {
		t.Errorf("retry = run %q, %d points, config %v; want r, 2, the init config", l.Run, len(l.Points), l.Config)
	}
	if st := mustState(t, dir); st.SyncedLines != 2 {
		t.Errorf("state = %+v, want synced_lines 2", st)
	}
}

// A run that never logged a point still delivers its init config: as a
// config-only /log before /finish, or on its own while the run is open.
func TestSyncInitOnlyRunDeliversConfig(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{initLine("alice/exp", "p", "r", "never"), finishLine}, "")
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	if res := s.syncDir(t.Context(), dir); res.Error != "" || !res.Done {
		t.Fatalf("result = %+v", res)
	}
	if len(f.logs) != 1 || len(f.logs[0].Req.Points) != 0 || f.logs[0].Req.Config["lr"] != 0.1 {
		t.Fatalf("logs = %+v, want one config-only call with the init config", f.logs)
	}
	if len(f.finishes) != 1 {
		t.Errorf("finishes = %d, want 1", len(f.finishes))
	}

	open := t.TempDir()
	writeRunJSONL(t, open, []string{initLine("alice/exp", "p", "open", "never")}, "")
	if res := s.syncDir(t.Context(), open); res.Error != "" || res.Done {
		t.Fatalf("open run = %+v", res)
	}
	if len(f.logs) != 2 || f.logs[1].Req.Run != "open" || f.logs[1].Req.Config["lr"] != 0.1 {
		t.Fatalf("logs = %+v, want a config-only call for the open run", f.logs)
	}
	if st := mustState(t, open); st.SyncedLines != 1 {
		t.Errorf("state = %+v, want synced_lines 1", st)
	}
	// Delivered: the next pass sends nothing.
	if res := s.syncDir(t.Context(), open); res.Error != "" || len(f.logs) != 2 {
		t.Errorf("idle pass = %+v, logs %d", res, len(f.logs))
	}
}

// Continuing an existing run merges its stored config under every config the
// directory sends, across passes; a null or absent config sends none.
func TestSyncContinuedRunMergesEveryConfig(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.addRun("p", "r", map[string]any{"old": "kept", "lr": 1.0})
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{initLine("alice/exp", "p", "r", "allow"), logLine(0, 1)}, "")
	if res := newOfflineSyncer(f.client(), &bytes.Buffer{}).syncDir(t.Context(), dir); res.Error != "" {
		t.Fatal(res.Error)
	}
	if cfg := f.logs[0].Req.Config; cfg["old"] != "kept" || cfg["lr"] != 0.1 {
		t.Errorf("first config = %v, want old kept and lr 0.1", cfg)
	}

	// A later record (and a fresh process) still merges over the stored
	// config, not over nothing.
	appendRunJSONL(t, dir, `{"v":1,"type":"log","points":[{"step":1,"metrics":{"loss":1}}],"config":{"lr":0.3,"new":1}}`)
	if res := newOfflineSyncer(f.client(), &bytes.Buffer{}).syncDir(t.Context(), dir); res.Error != "" {
		t.Fatal(res.Error)
	}
	cfg := f.logs[1].Req.Config
	if cfg["old"] != "kept" || cfg["lr"] != 0.3 || cfg["new"] != 1.0 {
		t.Errorf("second config = %v, want old kept, lr 0.3, new 1", cfg)
	}

	// null / absent config: nothing is sent, the stored config stays.
	for i, init := range []string{
		initLineConfig("alice/exp", "p", "r", "must", "null"),
		initLineConfig("alice/exp", "p", "r", "must", ""),
	} {
		d := t.TempDir()
		writeRunJSONL(t, d, []string{init, `{"v":1,"type":"log","points":[{"step":5,"metrics":{"loss":1}}],"config":null}`}, "")
		before := len(f.logs)
		if res := newOfflineSyncer(f.client(), &bytes.Buffer{}).syncDir(t.Context(), d); res.Error != "" {
			t.Fatalf("case %d: %s", i, res.Error)
		}
		if len(f.logs) != before+1 || f.logs[before].Req.Config != nil {
			t.Errorf("case %d: config sent = %v, want none", i, f.logs[len(f.logs)-1].Req.Config)
		}
	}
}

// A directory holding its finish record is replayed at the final status, so
// a spilled run whose online /finish succeeded never reads "running" again.
func TestSyncReplaysFinishedRunAtFinalStatus(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	f.addRun("p", "r", nil)
	f.runs["p"]["r"].Status = "failed"
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "allow"),
		logLine(0, 6000),
		logLine(6000, 6000), // two /log calls
		`{"v":1,"type":"finish","time":"2026-09-27T01:00:00Z","status":"failed"}`,
	}, "")
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})
	if res := s.syncDir(t.Context(), dir); res.Error != "" || res.Status != "failed" {
		t.Fatalf("result = %+v", res)
	}
	if len(f.logs) != 2 {
		t.Fatalf("log calls = %d, want 2", len(f.logs))
	}
	for i, l := range f.logs {
		if l.Req.Status != "failed" {
			t.Errorf("log call %d status = %q, want failed", i, l.Req.Status)
		}
	}
	if len(f.finishes) != 1 || f.finishes[0].Req.Status != "failed" {
		t.Errorf("finishes = %+v", f.finishes)
	}

	// Without a finish record the run is live: "running".
	open := t.TempDir()
	writeRunJSONL(t, open, []string{initLine("alice/exp", "p", "live", "never"), logLine(0, 1)}, "")
	if res := s.syncDir(t.Context(), open); res.Error != "" {
		t.Fatal(res.Error)
	}
	if got := f.logs[len(f.logs)-1].Req.Status; got != "running" {
		t.Errorf("open run status = %q, want running", got)
	}
}

// A complete line of valid JSON that does not fit the record shape is skipped
// with a warning; the lines after it are still synced.
func TestSyncSkipsWronglyTypedLine(t *testing.T) {
	f := newFakeIngest(t)
	f.repos["alice/exp"] = true
	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{
		initLine("alice/exp", "p", "r", "never"),
		logLine(0, 1),
		`{"v":1,"type":"log","points":[{"step":true,"metrics":{"loss":1}}]}`,
		`42`,
		logLine(1, 1),
		finishLine,
	}, "")
	var errOut bytes.Buffer
	s := newOfflineSyncer(f.client(), &errOut)
	res := s.syncDir(t.Context(), dir)
	if res.Error != "" || !res.Done || res.Points != 2 {
		t.Fatalf("result = %+v", res)
	}
	if st := mustState(t, dir); st.SyncedLines != 6 || !st.Done {
		t.Errorf("state = %+v, want synced_lines 6, done", st)
	}
	for _, want := range []string{"skipping line 3", "skipping line 4"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("expected a warning %q, got %q", want, errOut.String())
		}
	}

	// A bad line an earlier pass already moved past is not reported again.
	open := t.TempDir()
	writeRunJSONL(t, open, []string{initLine("alice/exp", "p", "o", "never"), `{"v":1,"type":"log","points":"no"}`}, "")
	errOut.Reset()
	if res := s.syncDir(t.Context(), open); res.Error != "" || mustState(t, open).SyncedLines != 2 {
		t.Fatalf("open run = %+v", res)
	}
	errOut.Reset()
	appendRunJSONL(t, open, logLine(0, 1))
	if res := s.syncDir(t.Context(), open); res.Error != "" || res.Points != 1 {
		t.Fatalf("second pass = %+v", res)
	}
	if strings.Contains(errOut.String(), "skipping") {
		t.Errorf("an already-passed line was reported again: %q", errOut.String())
	}
}

// A repository the token cannot write is named in the error; for a run
// recorded with "repo": null the message points at THINKINGFACE_REPO.
func TestSyncForbiddenRepoMessage(t *testing.T) {
	f := newFakeIngest(t)
	f.forbidCreate = true
	s := newOfflineSyncer(f.client(), &bytes.Buffer{})

	dir := t.TempDir()
	writeRunJSONL(t, dir, []string{initLine("", "p", "r", "never"), logLine(0, 1)}, "")
	res := s.syncDir(t.Context(), dir)
	for _, want := range []string{"write access to alice/trackio-metrics", "restricted", "THINKINGFACE_REPO"} {
		if !strings.Contains(res.Error, want) {
			t.Errorf("error %q does not mention %q", res.Error, want)
		}
	}

	dir2 := t.TempDir()
	writeRunJSONL(t, dir2, []string{initLine("bob/exp", "p", "r", "never"), logLine(0, 1)}, "")
	res = s.syncDir(t.Context(), dir2)
	if !strings.Contains(res.Error, "write access to bob/exp") || strings.Contains(res.Error, "THINKINGFACE_REPO") {
		t.Errorf("explicit repo error = %q", res.Error)
	}
}
