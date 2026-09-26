package tfcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// ------------------------------------------------------------ harness

// mcpMsg is any message the server writes.
type mcpMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// mcpToolResult is a tools/call result.
type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// lockedBuffer is a bytes.Buffer safe for the server's concurrent stderr.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// mcpHarness runs `tf mcp` (through Main, flags and all) on in-memory pipes.
type mcpHarness struct {
	t      *testing.T
	in     *io.PipeWriter
	msgs   chan mcpMsg
	done   chan int
	stderr *lockedBuffer
	closed bool
}

func startMCP(t *testing.T, endpoint string, extra ...string) *mcpHarness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &mcpHarness{t: t, in: inW, msgs: make(chan mcpMsg, 64), done: make(chan int, 1), stderr: &lockedBuffer{}}
	args := append([]string{"mcp", "--endpoint", endpoint, "--token", "tok"}, extra...)
	go func() {
		code := Main(args, inR, outW, h.stderr)
		_ = outW.Close()
		h.done <- code
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var m mcpMsg
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("stdout carried a non-JSON line %q: %v", sc.Text(), err)
				continue
			}
			if m.JSONRPC != "2.0" {
				t.Errorf("message without jsonrpc 2.0: %s", sc.Text())
			}
			h.msgs <- m
		}
		close(h.msgs)
	}()
	t.Cleanup(func() {
		if !h.closed {
			h.close()
		}
	})
	return h
}

func (h *mcpHarness) sendRaw(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		h.t.Fatalf("writing to the server: %v", err)
	}
}

func (h *mcpHarness) send(v any) {
	h.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	h.sendRaw(string(b))
}

func (h *mcpHarness) request(id any, method string, params any) {
	h.t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	h.send(m)
}

func (h *mcpHarness) recv() mcpMsg {
	h.t.Helper()
	select {
	case m, ok := <-h.msgs:
		if !ok {
			h.t.Fatalf("server closed stdout; stderr: %s", h.stderr)
		}
		return m
	case <-time.After(5 * time.Second):
		h.t.Fatalf("no message from the server; stderr: %s", h.stderr)
	}
	return mcpMsg{}
}

// expectNothing asserts that no message arrives within d.
func (h *mcpHarness) expectNothing(d time.Duration) {
	h.t.Helper()
	select {
	case m := <-h.msgs:
		h.t.Fatalf("unexpected message: id=%s result=%s error=%+v", m.ID, m.Result, m.Error)
	case <-time.After(d):
	}
}

// call sends a request and returns its response, which must be the next
// message.
func (h *mcpHarness) call(id any, method string, params any) mcpMsg {
	h.t.Helper()
	h.request(id, method, params)
	m := h.recv()
	want, _ := json.Marshal(id)
	if string(m.ID) != string(want) {
		h.t.Fatalf("response id = %s, want %s", m.ID, want)
	}
	return m
}

// tool calls a tool and returns its result, failing on a JSON-RPC error.
func (h *mcpHarness) tool(id any, name string, args any) mcpToolResult {
	h.t.Helper()
	m := h.call(id, "tools/call", map[string]any{"name": name, "arguments": args})
	if m.Error != nil {
		h.t.Fatalf("%s: JSON-RPC error %d %s", name, m.Error.Code, m.Error.Message)
	}
	var r mcpToolResult
	if err := json.Unmarshal(m.Result, &r); err != nil {
		h.t.Fatalf("%s: %v: %s", name, err, m.Result)
	}
	if len(r.Content) != 1 || r.Content[0].Type != "text" {
		h.t.Fatalf("%s: content = %+v", name, r.Content)
	}
	return r
}

func (h *mcpHarness) close() int {
	h.t.Helper()
	h.closed = true
	_ = h.in.Close()
	select {
	case code := <-h.done:
		return code
	case <-time.After(5 * time.Second):
		h.t.Fatalf("server did not exit after stdin closed; stderr: %s", h.stderr)
	}
	return -1
}

// mcpFake is the experiments fake plus GET /api/v1/experiments.
func mcpFake(t *testing.T) (*fakeExpServer, string) {
	t.Helper()
	f := newFakeExpServer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/experiments" {
			f.mu.Lock()
			f.requests = append(f.requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apitypes.ExpProjectListResponse{Items: []apitypes.ExpProjectListItem{}, Total: 0})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/metrics") {
			f.mu.Lock()
			f.requests = append(f.requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(apitypes.ExpMetricsResponse{Series: []apitypes.ExpMetricSeries{
				{Run: "run-a", Key: "loss", Points: [][2]float64{{0, 1}, {10, 0.5}}},
			}})
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fakeExpServer) lastRequest() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return ""
	}
	return f.requests[len(f.requests)-1]
}

func initialize(h *mcpHarness) {
	h.t.Helper()
	m := h.call(0, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"},
	})
	if m.Error != nil {
		h.t.Fatalf("initialize: %+v", m.Error)
	}
	h.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
}

// ------------------------------------------------------------ tests

func TestMCPInitializeVersionNegotiation(t *testing.T) {
	_, endpoint := mcpFake(t)
	h := startMCP(t, endpoint)
	for i, tc := range []struct{ ask, want string }{
		{"2025-06-18", "2025-06-18"},
		{"2025-03-26", "2025-03-26"},
		{"2024-11-05", "2024-11-05"},
		{"2099-01-01", "2025-06-18"},
		{"", "2025-06-18"},
	} {
		m := h.call(i+1, "initialize", map[string]any{
			"protocolVersion": tc.ask, "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "test", "version": "1"},
		})
		if m.Error != nil {
			t.Fatalf("initialize %q: %+v", tc.ask, m.Error)
		}
		var res struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]json.RawMessage `json:"capabilities"`
			ServerInfo      struct{ Name, Version string }
			Instructions    string `json:"instructions"`
		}
		if err := json.Unmarshal(m.Result, &res); err != nil {
			t.Fatal(err)
		}
		if res.ProtocolVersion != tc.want {
			t.Errorf("asked %q: protocolVersion = %q, want %q", tc.ask, res.ProtocolVersion, tc.want)
		}
		if res.ServerInfo.Name != "thinkingface" || res.ServerInfo.Version != Version {
			t.Errorf("serverInfo = %+v", res.ServerInfo)
		}
		if _, ok := res.Capabilities["tools"]; !ok {
			t.Errorf("capabilities = %v, want tools", res.Capabilities)
		}
		for _, want := range []string{"ns/name", "status!=running", "min:<name>", "set_metric_goals", "blob_sha"} {
			if !strings.Contains(res.Instructions, want) {
				t.Errorf("instructions should mention %q", want)
			}
		}
	}
	// notifications/initialized gets no reply: the next message is the ping's.
	h.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	m := h.call("p", "ping", nil)
	if m.Error != nil || string(m.Result) != "{}" {
		t.Errorf("ping = %s %+v", m.Result, m.Error)
	}
	if code := h.close(); code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestMCPToolsList(t *testing.T) {
	_, endpoint := mcpFake(t)
	h := startMCP(t, endpoint)
	initialize(h)
	m := h.call(1, "tools/list", map[string]any{})
	if m.Error != nil {
		t.Fatal(m.Error)
	}
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(m.Result, &res); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"list_experiment_repos", "list_projects", "list_runs", "get_run", "get_metrics", "wait_for_run",
		"config_diff", "get_notes", "update_notes", "annotate_run", "set_metric_goals",
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if len(tool.Description) < 40 {
			t.Errorf("%s: description too short: %q", tool.Name, tool.Description)
		}
		s := tool.InputSchema
		if s["type"] != "object" || s["additionalProperties"] != false {
			t.Errorf("%s: schema must be a closed object: %v", tool.Name, s)
		}
		props, ok := s["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no properties", tool.Name)
		}
		for pname, p := range props {
			pm, ok := p.(map[string]any)
			if !ok || pm["type"] == nil || pm["description"] == nil {
				t.Errorf("%s.%s: property needs a type and a description: %v", tool.Name, pname, p)
			}
		}
		if req, ok := s["required"]; ok {
			for _, r := range req.([]any) {
				if _, ok := props[r.(string)]; !ok {
					t.Errorf("%s: required %q is not a property", tool.Name, r)
				}
			}
		}
		if tool.Name != "list_experiment_repos" {
			if req, _ := s["required"].([]any); !slices.Contains(req, any("repo")) {
				t.Errorf("%s: repo should be required", tool.Name)
			}
		}
		readOnly := !slices.Contains([]string{"update_notes", "annotate_run", "set_metric_goals"}, tool.Name)
		if tool.Annotations["readOnlyHint"] != readOnly {
			t.Errorf("%s: readOnlyHint = %v, want %v", tool.Name, tool.Annotations["readOnlyHint"], readOnly)
		}
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate tool %q", n)
		}
		seen[n] = true
	}
}

func TestMCPToolCalls(t *testing.T) {
	f, endpoint := mcpFake(t)
	f.runs = sampleRuns()
	f.best = map[string]string{"loss": "run b/2"}
	h := startMCP(t, endpoint)
	initialize(h)

	// list_runs: filters reach the server; text and structuredContent carry
	// the API answer.
	r := h.tool(1, "list_runs", map[string]any{
		"repo": "alice/exp", "project": "ocr", "status": []string{"running", "finished"},
		"tag": []string{"x"}, "archived": false, "sort": "min:loss", "order": "desc", "limit": 5, "group": "sweep-1",
	})
	if r.IsError {
		t.Fatalf("list_runs error: %s", r.Content[0].Text)
	}
	var runs apitypes.ExpRunListResponse
	if err := json.Unmarshal([]byte(r.Content[0].Text), &runs); err != nil {
		t.Fatalf("text is not the API JSON: %v: %s", err, r.Content[0].Text)
	}
	if len(runs.Runs) != 2 || runs.Best["loss"] != "run b/2" {
		t.Errorf("runs = %+v", runs)
	}
	var structured apitypes.ExpRunListResponse
	if err := json.Unmarshal(r.StructuredContent, &structured); err != nil || !reflect.DeepEqual(structured, runs) {
		t.Errorf("structuredContent differs from text: %v %s", err, r.StructuredContent)
	}
	if strings.Contains(r.Content[0].Text, "\n") {
		t.Errorf("text should be compact JSON")
	}
	q := f.lastRequest()
	for _, want := range []string{"status=running", "status=finished", "tag=x", "archived=false", "sort=min%3Aloss", "order=desc", "limit=5", "group=sweep-1"} {
		if !strings.Contains(q, want) {
			t.Errorf("request %q lacks %q", q, want)
		}
	}

	// get_metrics defaults max_points to 200.
	r = h.tool(2, "get_metrics", map[string]any{"repo": "alice/exp", "project": "ocr", "runs": []string{"run-a"}, "keys": []string{"loss"}})
	if r.IsError {
		t.Fatalf("get_metrics error: %s", r.Content[0].Text)
	}
	if q := f.lastRequest(); !strings.Contains(q, "max_points=200") || !strings.Contains(q, "run=run-a") || !strings.Contains(q, "key=loss") {
		t.Errorf("metrics request = %q", q)
	}

	// set_metric_goals, then annotate_run.
	r = h.tool(3, "set_metric_goals", map[string]any{"repo": "alice/exp", "project": "ocr", "goals": map[string]string{"loss": "min"}})
	if r.IsError || !strings.Contains(r.Content[0].Text, `"loss":"min"`) {
		t.Errorf("set_metric_goals = %+v", r)
	}
	r = h.tool(4, "annotate_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "run b/2", "tags": []string{}, "note": "a < b"})
	if r.IsError {
		t.Fatalf("annotate_run error: %s", r.Content[0].Text)
	}
	if !strings.Contains(r.Content[0].Text, `"note":"a < b"`) {
		t.Errorf("text should keep < unescaped: %s", r.Content[0].Text)
	}
	if got := f.lastRequest(); !strings.HasPrefix(got, "PATCH /api/v1/experiments/alice/exp/ocr/runs/run%20b%2F2") {
		t.Errorf("annotate request = %q", got)
	}

	// get_notes -> update_notes with its blob_sha; a stale base_sha is an
	// isError result that tells the agent what to do.
	r = h.tool(5, "update_notes", map[string]any{"repo": "alice/exp", "project": "ocr", "content": "# v1", "base_sha": ""})
	if r.IsError {
		t.Fatalf("update_notes error: %s", r.Content[0].Text)
	}
	r = h.tool(6, "get_notes", map[string]any{"repo": "alice/exp", "project": "ocr"})
	var notes apitypes.ExpNotesResponse
	_ = json.Unmarshal([]byte(r.Content[0].Text), &notes)
	if notes.Content != "# v1" || notes.BlobSHA == "" {
		t.Fatalf("notes = %+v", notes)
	}
	r = h.tool(7, "update_notes", map[string]any{"repo": "alice/exp", "project": "ocr", "content": "# v2", "base_sha": "stale"})
	if !r.IsError || !strings.Contains(r.Content[0].Text, "get_notes again") {
		t.Errorf("conflict = %+v", r)
	}

	// list_experiment_repos passes author/search through.
	r = h.tool(8, "list_experiment_repos", map[string]any{"author": "alice"})
	if r.IsError || !strings.Contains(f.lastRequest(), "author=alice") {
		t.Errorf("list_experiment_repos = %+v, %s", r, f.lastRequest())
	}
	// Arguments may be omitted entirely for a tool with no required ones.
	m := h.call(9, "tools/call", map[string]any{"name": "list_experiment_repos"})
	if m.Error != nil || strings.Contains(string(m.Result), `"isError":true`) {
		t.Errorf("no-arguments call = %s %+v", m.Result, m.Error)
	}
}

func TestMCPToolAPIError(t *testing.T) {
	f, endpoint := mcpFake(t)
	f.runs = sampleRuns()
	h := startMCP(t, endpoint)
	initialize(h)
	r := h.tool(1, "get_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "nope"})
	if !r.IsError || !strings.Contains(r.Content[0].Text, "run not found") {
		t.Errorf("404 = %+v", r)
	}
	if r.StructuredContent != nil {
		t.Errorf("an error result has no structuredContent: %s", r.StructuredContent)
	}
	// The server keeps serving.
	r = h.tool(2, "get_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "run-a"})
	if r.IsError || !strings.Contains(r.Content[0].Text, `"name":"run-a"`) {
		t.Errorf("get_run = %+v", r)
	}
}

func TestMCPInvalidParams(t *testing.T) {
	f, endpoint := mcpFake(t)
	f.runs = sampleRuns()
	h := startMCP(t, endpoint)
	initialize(h)

	// Bad arguments are tool results with isError, worded for the agent.
	for i, tc := range []struct {
		tool string
		args any
		want string
	}{
		{"get_run", map[string]any{"project": "ocr", "run": "a"}, `"repo" must be ns/name`},
		{"get_run", map[string]any{"repo": "alice", "project": "ocr", "run": "a"}, `"repo" must be ns/name`},
		{"get_run", map[string]any{"repo": "alice/exp", "project": "ocr"}, `"run" is required`},
		{"get_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "a", "bogus": 1}, `unknown argument "bogus"`},
		{"list_runs", map[string]any{"repo": "alice/exp", "project": "ocr", "limit": "5"}, `"limit" must be an integer`},
		{"list_runs", map[string]any{"repo": "alice/exp", "project": "ocr", "limit": 0}, `"limit" must be between`},
		{"list_runs", map[string]any{"repo": "alice/exp", "project": "ocr", "status": []string{"done"}}, `"status" entries`},
		{"get_metrics", map[string]any{"repo": "alice/exp", "project": "ocr"}, `"runs" is required`},
		{"wait_for_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "a", "until": "step >>= 3"}, `"until" does not parse`},
		{"wait_for_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "a", "timeout_seconds": 7200}, `"timeout_seconds" must be between 1 and 3600`},
		{"annotate_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "a"}, "at least one of"},
		{"set_metric_goals", map[string]any{"repo": "alice/exp", "project": "ocr", "goals": map[string]string{"loss": "down"}}, `must be "min", "max"`},
		{"update_notes", map[string]any{"repo": "alice/exp", "project": "ocr"}, `"content" is required`},
		{"get_run", []int{1}, "must be a JSON object"},
	} {
		r := h.tool(i+1, tc.tool, tc.args)
		if !r.IsError || !strings.Contains(r.Content[0].Text, tc.want) {
			t.Errorf("%s %v: got %+v, want an error containing %q", tc.tool, tc.args, r, tc.want)
		}
	}
	f.mu.Lock()
	for _, req := range f.requests {
		t.Errorf("an invalid call reached the server: %s", req)
	}
	f.mu.Unlock()

	// Protocol-level problems are JSON-RPC errors.
	for i, params := range []any{
		map[string]any{"name": "no_such_tool", "arguments": map[string]any{}},
		map[string]any{"arguments": map[string]any{}},
		"not an object",
		nil,
	} {
		m := h.call(100+i, "tools/call", params)
		if m.Error == nil || m.Error.Code != rpcInvalidParams {
			t.Errorf("tools/call %v: error = %+v, want -32602", params, m.Error)
		}
	}
}

func TestMCPProtocolErrors(t *testing.T) {
	_, endpoint := mcpFake(t)
	h := startMCP(t, endpoint)

	m := h.call(1, "resources/list", nil)
	if m.Error == nil || m.Error.Code != rpcMethodNotFound {
		t.Errorf("unknown method: %+v", m.Error)
	}

	h.sendRaw(`{"jsonrpc": "2.0", "id": 2, "method": `)
	m = h.recv()
	if m.Error == nil || m.Error.Code != rpcParseError || string(m.ID) != "null" {
		t.Errorf("malformed line: id=%s %+v", m.ID, m.Error)
	}

	h.sendRaw(`{"jsonrpc": "1.0", "id": 3, "method": "ping"}`)
	m = h.recv()
	if m.Error == nil || m.Error.Code != rpcInvalidRequest || string(m.ID) != "3" {
		t.Errorf("wrong jsonrpc version: id=%s %+v", m.ID, m.Error)
	}

	h.sendRaw(`[{"jsonrpc": "2.0", "id": 4, "method": "ping"}]`)
	if m = h.recv(); m.Error == nil || m.Error.Code != rpcInvalidRequest {
		t.Errorf("batch: %+v", m.Error)
	}

	// Blank lines are skipped; the server is still alive.
	h.sendRaw("")
	h.sendRaw("   ")
	if m = h.call("s", "ping", nil); m.Error != nil {
		t.Errorf("ping after errors: %+v", m.Error)
	}
}

func TestMCPNotificationsGetNoReply(t *testing.T) {
	_, endpoint := mcpFake(t)
	h := startMCP(t, endpoint)
	for _, n := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/whatever","params":{"x":1}}`,
		`{"jsonrpc":"2.0","method":"ping"}`,                                              // a notification even for a request method
		`{"jsonrpc":"2.0","method":"no/such/method"}`,                                    // not answered with -32601 either
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`, // unknown id
		`{"jsonrpc":"2.0","id":5,"result":{}}`,                                           // a response: we sent no request
	} {
		h.sendRaw(n)
	}
	m := h.call(1, "ping", nil)
	if m.Error != nil {
		t.Errorf("ping: %+v", m.Error)
	}
	h.expectNothing(100 * time.Millisecond)
}

// blockingRun makes the fake's GET .../runs/{run} hold until release is
// closed, then answer a finished run. started is signalled on every call,
// aborted when the client went away while it was holding.
func blockingRun(f *fakeExpServer) (release chan struct{}, started, aborted chan struct{}) {
	release, started, aborted = make(chan struct{}), make(chan struct{}, 8), make(chan struct{}, 8)
	f.getRun = func(w http.ResponseWriter, r *http.Request, _ int) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			aborted <- struct{}{}
			return
		}
		run := apitypes.ExpRun{Name: "r", Status: "finished", LastStep: 10, UpdatedAt: time.Now()}
		_ = json.NewEncoder(w).Encode(apitypes.ExpRunResponse{Run: run})
	}
	return release, started, aborted
}

func TestMCPWaitForRunDoesNotBlockOtherCalls(t *testing.T) {
	fastWait(t)
	f, endpoint := mcpFake(t)
	release, started, _ := blockingRun(f)
	h := startMCP(t, endpoint)
	initialize(h)

	h.request(1, "tools/call", map[string]any{"name": "wait_for_run", "arguments": map[string]any{
		"repo": "alice/exp", "project": "ocr", "run": "r", "timeout_seconds": 30,
	}})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("wait_for_run never reached the server")
	}
	// ping and tools/list are answered while the wait holds.
	if m := h.call(2, "ping", nil); m.Error != nil {
		t.Fatalf("ping: %+v", m.Error)
	}
	if m := h.call(3, "tools/list", nil); m.Error != nil {
		t.Fatalf("tools/list: %+v", m.Error)
	}
	close(release)
	m := h.recv()
	if string(m.ID) != "1" || m.Error != nil {
		t.Fatalf("wait_for_run response = id %s %+v", m.ID, m.Error)
	}
	var r mcpToolResult
	_ = json.Unmarshal(m.Result, &r)
	var res runWaitResult
	if err := json.Unmarshal(r.StructuredContent, &res); err != nil {
		t.Fatalf("%v: %s", err, m.Result)
	}
	if r.IsError || !res.Met || res.Reason != "met" || res.Until != "status!=running" || res.Run == nil || res.Run.Status != "finished" {
		t.Errorf("wait result = %+v (%+v)", res, r)
	}
}

func TestMCPWaitForRunTimeoutAndStopped(t *testing.T) {
	fastWait(t)
	f, endpoint := mcpFake(t)
	f.runs = []apitypes.ExpRun{{Name: "done", Status: "finished", LastStep: 5000, UpdatedAt: time.Now()}}
	h := startMCP(t, endpoint)
	initialize(h)

	r := h.tool(1, "wait_for_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "done", "until": "step >= 12000"})
	var res runWaitResult
	_ = json.Unmarshal(r.StructuredContent, &res)
	if r.IsError || res.Met || res.Reason != "stopped" {
		t.Errorf("stopped run: %+v %+v", res, r)
	}
	r = h.tool(2, "wait_for_run", map[string]any{"repo": "alice/exp", "project": "ocr", "run": "never", "timeout_seconds": 1})
	res = runWaitResult{}
	_ = json.Unmarshal(r.StructuredContent, &res)
	if r.IsError || res.Met || res.Reason != "timeout" || res.Run != nil {
		t.Errorf("timeout: %+v %+v", res, r)
	}
}

func TestMCPCancellation(t *testing.T) {
	fastWait(t)
	savedGrace := mcpShutdownGrace
	mcpShutdownGrace = 50 * time.Millisecond
	t.Cleanup(func() { mcpShutdownGrace = savedGrace })
	f, endpoint := mcpFake(t)
	release, started, aborted := blockingRun(f)
	defer close(release)
	h := startMCP(t, endpoint, "--verbose")
	initialize(h)

	h.request("w", "tools/call", map[string]any{"name": "wait_for_run", "arguments": map[string]any{
		"repo": "alice/exp", "project": "ocr", "run": "r",
	}})
	<-started
	h.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled",
		"params": map[string]any{"requestId": "w", "reason": "user pressed stop"}})
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did not abort the HTTP request")
	}
	// The cancelled request is not answered; others still are.
	if m := h.call(2, "ping", nil); m.Error != nil {
		t.Fatalf("ping: %+v", m.Error)
	}
	h.expectNothing(150 * time.Millisecond)

	// Closing stdin ends the server promptly even with a wait in flight.
	h.request("w2", "tools/call", map[string]any{"name": "wait_for_run", "arguments": map[string]any{
		"repo": "alice/exp", "project": "ocr", "run": "r",
	}})
	<-started
	if code := h.close(); code != 0 {
		t.Errorf("exit %d", code)
	}
	// It was cancelled after the grace period, and still answered (stdout
	// is open).
	if m := h.recv(); string(m.ID) != `"w2"` || !strings.Contains(string(m.Result), `"isError":true`) {
		t.Errorf("w2 after stdin closed = %s %s", m.ID, m.Result)
	}
	if !strings.Contains(h.stderr.String(), "cancelling request \"w\"") {
		t.Errorf("--verbose should log the cancellation: %s", h.stderr)
	}
}

func TestMCPUsage(t *testing.T) {
	isolateEnv(t)
	code, out, _ := runMain(t, []string{"mcp", "--help"}, "")
	if code != 0 || !strings.Contains(out, "claude mcp add thinkingface -- tf mcp") {
		t.Errorf("--help: %d %q", code, out)
	}
	code, _, errOut := runMain(t, []string{"mcp", "extra"}, "")
	if code != exitUsage || !strings.Contains(errOut, "unexpected arguments") {
		t.Errorf("positional: %d %q", code, errOut)
	}
}

// An unresolvable endpoint does not stop the server: tools answer isError
// with the remedy.
func TestMCPNoEndpoint(t *testing.T) {
	isolateEnv(t)
	inR, inW := io.Pipe()
	var out lockedBuffer
	done := make(chan int)
	go func() { done <- Main([]string{"mcp"}, inR, &out, io.Discard) }()
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_experiment_repos","arguments":{}}}`+"\n")
	_ = inW.Close()
	if code := <-done; code != 0 {
		t.Errorf("exit %d", code)
	}
	var m mcpMsg
	if err := json.Unmarshal([]byte(out.String()), &m); err != nil {
		t.Fatalf("%v: %q", err, out.String())
	}
	var r mcpToolResult
	_ = json.Unmarshal(m.Result, &r)
	if !r.IsError || !strings.Contains(r.Content[0].Text, "THINKINGFACE_ENDPOINT") {
		t.Errorf("result = %s", m.Result)
	}
}
