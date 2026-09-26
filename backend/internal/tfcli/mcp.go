package tfcli

// `tf mcp`: the experiment tools served to an AI agent over the Model
// Context Protocol (docs/dev/agent-features.md §4, docs/dev/tf-cli.md).
//
// Transport is MCP's stdio transport: JSON-RPC 2.0 messages, one per line,
// on stdin (client -> server) and stdout (server -> client). Nothing but
// protocol messages may reach stdout; logs go to stderr, and only with
// --verbose. The protocol is hand-rolled here rather than taken from an SDK
// -- it is a few hundred lines of JSON-RPC, and tf stays dependency-light.
//
// Every request is served on its own goroutine, so a wait_for_run that
// holds for minutes does not block ping or other calls; stdout writes are
// serialised by a mutex, and notifications/cancelled cancels the named
// request's context (a cancelled request gets no response, as the spec
// asks). The tools themselves live in mcp_tools.go.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

// mcpUsage is the help text for `tf mcp` (docs/dev/agent-features.md §4).
const mcpUsage = `Usage:
  tf mcp [--endpoint URL] [--token TOKEN] [--verbose]

Serve thinkingface's experiment tools to an AI agent over the Model Context
Protocol (JSON-RPC 2.0, one message per line on stdin/stdout). Uses the same
endpoint and token as every other tf command (flags, then THINKINGFACE_* /
TF_* environment variables, then the credentials 'tf login' saved).

Tools: list_experiment_repos, list_projects, list_runs, get_run, get_metrics,
wait_for_run, config_diff, get_notes, update_notes, annotate_run,
set_metric_goals.

Register it with Claude Code:
  claude mcp add thinkingface -- tf mcp

Flags:
  --endpoint URL / --token TOKEN / --api-key KEY
  --verbose              log credential resolution and each request to stderr
`

// mcpProtocolVersion is the MCP revision this server implements.
const mcpProtocolVersion = "2025-06-18"

// mcpSupportedVersions are the revisions an initialize may ask for and get
// echoed back. The tool surface used here is the same in all of them; the
// newer fields (structuredContent, tool titles) are ignored by older clients.
var mcpSupportedVersions = []string{mcpProtocolVersion, "2025-03-26", "2024-11-05"}

// JSON-RPC 2.0 error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

// mcpInstructions is sent in the initialize result; clients put it in the
// model's context, so it is written for the agent.
const mcpInstructions = `thinkingface is a self-hosted Hugging Face Hub clone with an experiment tracker.

Data model: an experiment repository ("repo", always written ns/name, e.g. "alice/exp") holds projects; a project holds runs. A run has a status (running, finished, failed, or stale = stopped logging without finishing), a last step, a config (hyperparameters), tags, a note, an archived flag, and metric series whose last/min/max values are summarised on the run.

Typical flow: list_experiment_repos -> list_projects -> list_runs (filter and sort server-side) -> get_run / get_metrics / config_diff. Use wait_for_run to block until a run reaches a condition instead of polling get_run.

wait_for_run "until" grammar: cond (("and"|"or") cond)*, "and" binds tighter, parentheses allowed. cond := FIELD OP VALUE, OP one of == != >= <= > <. FIELD: step (last step), points (points logged), status (running|finished|failed|stale, == and != only), metric:<name> (last value; alias last:<name>), min:<name>, max:<name>. Quote a metric name containing spaces, parentheses, quotes or = ! < >: metric:"val loss" < 0.2. A comparison on a metric the run has not logged yet is false. Default: status!=running.

Metric goals: each metric of a project may have a goal, "min" (lower is better, e.g. loss) or "max" (higher is better, e.g. accuracy), set with set_metric_goals. Goals make list_runs report the best run per metric ("best") and allow sort "best:<metric>".

Project notes (get_notes / update_notes) are a Markdown notebook, {project}/NOTES.md in the repository; [text](run:<run name>) links to a run. Always pass get_notes' blob_sha as base_sha when updating, so a concurrent edit is refused instead of overwritten.`

// ------------------------------------------------------------ wire types

// rpcRequest is any incoming message. A request has a method and an id, a
// notification a method and no id; a message with an id and no method is
// a response to a request of ours, which this server never sends.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// nullID is the id of an error response to a message whose id could not
// be read (JSON-RPC 2.0 §5).
var nullID = json.RawMessage("null")

// ---------------------------------------------------------------- server

// mcpServer is one stdio session.
type mcpServer struct {
	out   io.Writer
	outMu sync.Mutex
	logf  func(format string, args ...any) // never nil

	// client returns the hub client and the endpoint it talks to. It is
	// resolved on first use (not at startup), so a server started before
	// `tf login` recovers without a restart, and an unresolvable endpoint is
	// a tool error the agent can read rather than a server that won't start.
	client func() (*hub.Client, string, error)

	tools  []*mcpTool
	byName map[string]*mcpTool

	mu       sync.Mutex
	inflight map[string]*mcpCall
	version  string // negotiated protocol version, set by initialize
	wg       sync.WaitGroup
}

// mcpCall is one request being served.
type mcpCall struct {
	cancel    context.CancelFunc
	cancelled atomic.Bool // by notifications/cancelled: answer nothing
}

func newMCPServer(out io.Writer, client func() (*hub.Client, string, error), logf func(string, ...any)) *mcpServer {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &mcpServer{
		out:      out,
		logf:     logf,
		client:   client,
		inflight: map[string]*mcpCall{},
		tools:    mcpTools(),
		byName:   map[string]*mcpTool{},
	}
	for _, t := range s.tools {
		s.byName[t.Name] = t
	}
	return s
}

func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, mcpUsage)
		return exitOK
	}
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cf := addCommonFlags(fs)
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) > 0 {
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		fmt.Fprintf(stderr, "tf mcp: %s\n", msg)
		fmt.Fprint(stderr, mcpUsage)
		return exitUsage
	}

	var logMu sync.Mutex
	logf := func(format string, a ...any) {
		if !cf.verbose {
			return
		}
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(stderr, "tf mcp: "+format+"\n", a...)
	}

	// Resolve credentials lazily and cache the first success.
	var (
		clientMu sync.Mutex
		cached   *hub.Client
		endpoint string
	)
	client := func() (*hub.Client, string, error) {
		clientMu.Lock()
		defer clientMu.Unlock()
		if cached != nil {
			return cached, endpoint, nil
		}
		resolved, err := resolveCreds(cf, lockedWriter{&logMu, stderr, cf.verbose})
		if err != nil {
			return nil, "", fmt.Errorf("%w (run `tf login <url>`, or set THINKINGFACE_ENDPOINT and THINKINGFACE_API_KEY in the MCP server's environment)", err)
		}
		cached = hub.New(resolved.Endpoint, resolved.Token, hub.WithUserAgent(userAgent()))
		endpoint = resolved.Endpoint
		return cached, endpoint, nil
	}

	s := newMCPServer(stdout, client, logf)
	if err := s.serve(context.Background(), stdin); err != nil {
		fmt.Fprintf(stderr, "tf mcp: %s\n", err)
		return exitError
	}
	return exitOK
}

// lockedWriter lets resolveCreds' --verbose output share the log mutex,
// and drops it when not verbose.
type lockedWriter struct {
	mu      *sync.Mutex
	w       io.Writer
	enabled bool
}

func (l lockedWriter) Write(p []byte) (int, error) {
	if !l.enabled {
		return len(p), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// mcpShutdownGrace is how long requests still in flight when stdin closes
// may run on before they are cancelled: long enough for a piped
// `printf ... | tf mcp` to get its quick answers, short enough that a
// client closing stdin to stop the server (the MCP stdio shutdown) is not
// kept waiting on a wait_for_run. A var for tests.
var mcpShutdownGrace = 5 * time.Second

// serve reads messages until stdin ends, then gives whatever is still in
// flight mcpShutdownGrace to finish before cancelling it, and waits for it.
// A read error other than EOF is returned.
func (s *mcpServer) serve(ctx context.Context, in io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := bufio.NewReader(in)
	var readErr error
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			s.handleLine(ctx, line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	s.logf("stdin closed; stopping")
	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()
	grace := time.NewTimer(mcpShutdownGrace)
	defer grace.Stop()
	select {
	case <-finished:
	case <-grace.C:
		s.logf("cancelling the requests still in flight")
		cancel()
		<-finished
	}
	return readErr
}

// handleLine dispatches one line. Requests run on their own goroutine.
func (s *mcpServer) handleLine(ctx context.Context, line []byte) {
	line = bytes.TrimSpace(line)
	if line[0] == '[' {
		// Batches were removed from MCP in 2025-06-18.
		s.writeError(nullID, rpcInvalidRequest, "batch requests are not supported")
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.logf("parse error: %s", err)
		s.writeError(nullID, rpcParseError, "parse error: "+err.Error())
		return
	}
	isNotification := len(req.ID) == 0
	if req.JSONRPC != "2.0" {
		if !isNotification {
			s.writeError(req.ID, rpcInvalidRequest, `invalid request: "jsonrpc" must be "2.0"`)
		}
		return
	}
	if req.Method == "" {
		// A response to a server -> client request. We send none, so
		// there is nothing to match it with.
		return
	}
	if isNotification {
		s.handleNotification(req)
		return
	}
	if !validID(req.ID) {
		s.writeError(nullID, rpcInvalidRequest, "invalid request: id must be a string or a number")
		return
	}

	key := idKey(req.ID)
	callCtx, cancel := context.WithCancel(ctx)
	call := &mcpCall{cancel: cancel}
	s.mu.Lock()
	if _, dup := s.inflight[key]; dup {
		s.mu.Unlock()
		cancel()
		s.writeError(req.ID, rpcInvalidRequest, "invalid request: id "+key+" is already in use by a request in flight")
		return
	}
	s.inflight[key] = call
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
			cancel()
		}()
		result, rerr := s.dispatch(callCtx, req)
		if call.cancelled.Load() {
			s.logf("%s (id %s) cancelled; not answering", req.Method, key)
			return
		}
		if rerr != nil {
			s.logf("%s (id %s): error %d: %s", req.Method, key, rerr.Code, rerr.Message)
			s.writeError(req.ID, rerr.Code, rerr.Message)
			return
		}
		s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	}()
}

func (s *mcpServer) handleNotification(req rpcRequest) {
	switch req.Method {
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || len(p.RequestID) == 0 {
			s.logf("ignoring malformed notifications/cancelled")
			return
		}
		key := idKey(p.RequestID)
		s.mu.Lock()
		call := s.inflight[key]
		s.mu.Unlock()
		if call == nil {
			return // already answered, or never existed: nothing to do
		}
		s.logf("cancelling request %s (%s)", key, p.Reason)
		call.cancelled.Store(true)
		call.cancel()
	default:
		// notifications/initialized, progress, roots/list_changed, ...:
		// nothing to do, and a notification is never answered.
		s.logf("notification %s", req.Method)
	}
}

// dispatch serves one request, answering its result or a JSON-RPC error.
func (s *mcpServer) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	s.logf("-> %s (id %s)", req.Method, idKey(req.ID))
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params)
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return s.listTools(), nil
	case "tools/call":
		return s.callTool(ctx, req.Params)
	}
	return nil, &rpcError{Code: rpcMethodNotFound, Message: "method not found: " + req.Method}
}

func (s *mcpServer) initialize(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	version := mcpProtocolVersion
	if slices.Contains(mcpSupportedVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	s.mu.Lock()
	s.version = version
	s.mu.Unlock()
	s.logf("initialize from %s %s, protocol %q -> %q", p.ClientInfo.Name, p.ClientInfo.Version, p.ProtocolVersion, version)
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "thinkingface", "version": Version},
		"instructions":    mcpInstructions,
	}, nil
}

// idKey is a request id's map key: its compact JSON text, so 7 and "7" are
// different requests, as they are to the client.
func idKey(id json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, id); err != nil {
		return string(id)
	}
	return b.String()
}

// validID reports whether id is a JSON string or number (JSON-RPC allows
// null too, but MCP does not).
func validID(id json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return false
	}
	switch v.(type) {
	case string, float64:
		return true
	}
	return false
}

func (s *mcpServer) writeError(id json.RawMessage, code int, msg string) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// write sends one message as one line. json.Marshal never emits a raw
// newline, so the framing cannot break.
func (s *mcpServer) write(msg rpcResponse) {
	b, err := json.Marshal(msg)
	if err != nil {
		// Only a result that cannot be encoded gets here.
		b, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: msg.ID,
			Error: &rpcError{Code: rpcInternalError, Message: "encoding the response: " + err.Error()}})
	}
	b = append(b, '\n')
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if _, err := s.out.Write(b); err != nil {
		s.logf("writing to stdout: %s", err)
	}
}
