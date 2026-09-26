package tfcli

// The tools `tf mcp` serves (docs/dev/agent-features.md §4). Each is a thin
// adapter from JSON arguments to a hub/experiments.go call; the answer is
// the API's wire type unchanged (apitypes), so it matches api-contract.md §7
// and `tf experiments ... --json`.
//
// Descriptions and schemas are what the agent reads to decide how to call a
// tool, so they spell out defaults, limits and units.
//
// Argument problems (a missing field, a wrong type, an unknown argument, an
// --until expression that does not parse) and API failures come back as a
// tool result with isError: true and a sentence the agent can act on, not as
// a JSON-RPC error: the model sees tool results, and can correct itself.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/tfcli/hub"
)

// Limits of the tool arguments.
const (
	mcpDefaultMaxPoints   = 200
	mcpMaxMaxPoints       = 5000 // the server's ceiling (maxMetricPoints)
	mcpDefaultWaitTimeout = 600
	mcpMaxWaitTimeout     = 3600
	mcpMaxRunLimit        = 1000
)

// mcpTool is one tool: its advertised definition and its handler.
type mcpTool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	// ReadOnly tools only read; the rest write to the server.
	ReadOnly bool
	// Destructive: a write that replaces what was there (a note, the tag
	// list, the notes file) rather than only adding to it. Idempotent:
	// calling it twice with the same arguments has no further effect. Both
	// are MCP tool annotations, meaningful for writes only.
	Destructive, Idempotent bool
	// call decodes args (already checked to be a JSON object or absent)
	// and returns the object to answer.
	call func(ctx context.Context, env *mcpEnv, args json.RawMessage) (any, error)
}

// mcpEnv is what a tool handler gets besides its arguments.
type mcpEnv struct {
	s        *mcpServer
	endpoint string
	repo     string // "ns/name" once parsed, for error wording
}

// client resolves the hub client (lazily, see mcpServer.client).
func (e *mcpEnv) client() (*hub.Client, error) {
	c, endpoint, err := e.s.client()
	if err != nil {
		return nil, &mcpToolError{msg: err.Error()}
	}
	e.endpoint = endpoint
	return c, nil
}

// mcpToolError is an error whose message is already worded for the agent.
type mcpToolError struct{ msg string }

func (e *mcpToolError) Error() string { return e.msg }

func argError(format string, a ...any) error {
	return &mcpToolError{msg: "invalid arguments: " + fmt.Sprintf(format, a...)}
}

// ------------------------------------------------------------ tools/list

func (s *mcpServer) listTools() any {
	defs := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		ann := map[string]any{
			"title":         t.Title,
			"readOnlyHint":  t.ReadOnly,
			"openWorldHint": false,
		}
		if !t.ReadOnly {
			ann["destructiveHint"] = t.Destructive
			ann["idempotentHint"] = t.Idempotent
		}
		defs = append(defs, map[string]any{
			"name":        t.Name,
			"title":       t.Title,
			"description": t.Description,
			"inputSchema": t.InputSchema,
			"annotations": ann,
		})
	}
	return map[string]any{"tools": defs}
}

// ------------------------------------------------------------ tools/call

func (s *mcpServer) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) == 0 {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "tools/call needs params {name, arguments}"}
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "tools/call params need a tool name"}
	}
	t := s.byName[p.Name]
	if t == nil {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "unknown tool: " + p.Name}
	}
	args := bytes.TrimSpace(p.Arguments)
	if len(args) == 0 || bytes.Equal(args, []byte("null")) {
		args = []byte("{}")
	}
	if args[0] != '{' {
		return s.toolError("invalid arguments: arguments must be a JSON object"), nil
	}

	env := &mcpEnv{s: s}
	start := time.Now()
	out, err := t.call(ctx, env, args)
	if err != nil {
		msg := describeToolError(ctx, err, env)
		s.logf("%s: error after %s: %s", t.Name, time.Since(start).Round(time.Millisecond), msg)
		return s.toolError(msg), nil
	}
	s.logf("%s: ok in %s", t.Name, time.Since(start).Round(time.Millisecond))
	return s.toolResult(out)
}

// toolResult wraps an answer: the compact JSON as text (what every client
// shows the model) and, from 2025-06-18 on, the same object as
// structuredContent.
func (s *mcpServer) toolResult(v any) (any, *rpcError) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Keep < > & readable in the text: an --until echo or a note with
	// "loss < 0.2" should not reach the model as <.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, &rpcError{Code: rpcInternalError, Message: "encoding the tool result: " + err.Error()}
	}
	text := strings.TrimSuffix(buf.String(), "\n")
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
	s.mu.Lock()
	version := s.version
	s.mu.Unlock()
	if version == "" || version >= "2025-06-18" {
		res["structuredContent"] = json.RawMessage(text)
	}
	return res, nil
}

func (s *mcpServer) toolError(msg string) any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

// describeToolError words a handler's error for the agent.
func describeToolError(ctx context.Context, err error, env *mcpEnv) string {
	var terr *mcpToolError
	if errors.As(err, &terr) {
		return terr.msg
	}
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return "cancelled"
	}
	var herr *hub.Error
	if errors.As(err, &herr) && herr.Status == http.StatusConflict && strings.HasSuffix(herr.URL, "/notes") {
		return "the notes changed since base_sha was read (someone else saved them); call get_notes again, merge your edit into the current content, and retry with its blob_sha"
	}
	return describeExpError(err, env.endpoint, env.repo)
}

// decodeArgs decodes a JSON object into dst, refusing unknown fields, and
// words the failures for the agent.
func decodeArgs(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &typeErr) && typeErr.Field != "":
			return argError("%q must be %s, got %s", typeErr.Field, jsonTypeName(typeErr.Type.String()), typeErr.Value)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return argError("unknown argument %s", strings.TrimPrefix(err.Error(), "json: unknown field "))
		}
		return argError("%s", strings.TrimPrefix(err.Error(), "json: "))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return argError("trailing data after the arguments object")
	}
	return nil
}

// jsonTypeName maps a Go type name from an UnmarshalTypeError to the JSON
// Schema type the agent was told about.
func jsonTypeName(goType string) string {
	switch {
	case strings.HasPrefix(goType, "[]"):
		return "an array"
	case strings.HasPrefix(goType, "map["):
		return "an object"
	case strings.Contains(goType, "int"):
		return "an integer"
	case strings.Contains(goType, "float"):
		return "a number"
	case strings.Contains(goType, "bool"):
		return "a boolean"
	case strings.Contains(goType, "string"):
		return "a string"
	}
	return goType
}

// parseRepoArg parses "ns/name" (a "datasets/" prefix is accepted).
func (e *mcpEnv) parseRepoArg(arg string) (ns, name string, err error) {
	s := strings.TrimPrefix(strings.TrimSpace(arg), "datasets/")
	ns, name, ok := strings.Cut(s, "/")
	if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
		return "", "", argError(`"repo" must be ns/name (e.g. "alice/exp"), got %q`, arg)
	}
	e.repo = ns + "/" + name
	return ns, name, nil
}

func requireString(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return argError("%q is required and must not be empty", field)
	}
	return nil
}

func requireNames(field string, vs []string) error {
	for i, v := range vs {
		if strings.TrimSpace(v) == "" {
			return argError("%q[%d] must not be empty", field, i)
		}
	}
	return nil
}

// ------------------------------------------------------------ schemas

func schemaObject(required []string, props map[string]any) map[string]any {
	s := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func strArrayProp(desc string, extra map[string]any) map[string]any {
	items := map[string]any{"type": "string"}
	for k, v := range extra {
		items[k] = v
	}
	return map[string]any{"type": "array", "items": items, "description": desc}
}

var (
	repoProp    = map[string]any{"type": "string", "description": `Experiment repository as ns/name, e.g. "alice/exp".`, "pattern": `^[^/\s]+/[^/\s]+$`}
	projectProp = strProp("Project name inside the repository (free text, passed verbatim).")
	runProp     = strProp("Run name (free text, passed verbatim).")
)

// ------------------------------------------------------------ the tools

// Argument structs. Pointers mark optional fields whose absence differs
// from their zero value.
type (
	listReposArgs struct {
		Author string `json:"author"`
		Search string `json:"search"`
	}
	repoArgs struct {
		Repo string `json:"repo"`
	}
	projectArgs struct {
		Repo    string `json:"repo"`
		Project string `json:"project"`
	}
	runArgs struct {
		Repo    string `json:"repo"`
		Project string `json:"project"`
		Run     string `json:"run"`
	}
	listRunsArgs struct {
		Repo     string   `json:"repo"`
		Project  string   `json:"project"`
		Group    string   `json:"group"`
		Status   []string `json:"status"`
		Tag      []string `json:"tag"`
		Archived *bool    `json:"archived"`
		Sort     string   `json:"sort"`
		Order    string   `json:"order"`
		Limit    *int     `json:"limit"`
	}
	metricsArgs struct {
		Repo      string   `json:"repo"`
		Project   string   `json:"project"`
		Runs      []string `json:"runs"`
		Keys      []string `json:"keys"`
		X         string   `json:"x"`
		MaxPoints *int     `json:"max_points"`
	}
	waitArgs struct {
		Repo           string `json:"repo"`
		Project        string `json:"project"`
		Run            string `json:"run"`
		Until          string `json:"until"`
		TimeoutSeconds *int   `json:"timeout_seconds"`
		IgnoreStale    bool   `json:"ignore_stale"`
	}
	configDiffArgs struct {
		Repo        string   `json:"repo"`
		Project     string   `json:"project"`
		Runs        []string `json:"runs"`
		IncludeMeta bool     `json:"include_meta"`
	}
	updateNotesArgs struct {
		Repo    string  `json:"repo"`
		Project string  `json:"project"`
		Content *string `json:"content"`
		BaseSHA *string `json:"base_sha"`
		Message string  `json:"message"`
	}
	annotateArgs struct {
		Repo     string    `json:"repo"`
		Project  string    `json:"project"`
		Run      string    `json:"run"`
		Note     *string   `json:"note"`
		Tags     *[]string `json:"tags"`
		Archived *bool     `json:"archived"`
	}
	goalsArgs struct {
		Repo    string            `json:"repo"`
		Project string            `json:"project"`
		Goals   map[string]string `json:"goals"`
	}
)

// decodeProject decodes args into dst and checks repo + project, which
// every tool but list_experiment_repos / list_projects has.
func (e *mcpEnv) decodeProject(raw json.RawMessage, dst any, repo, project *string) (ns, name string, err error) {
	if err := decodeArgs(raw, dst); err != nil {
		return "", "", err
	}
	if ns, name, err = e.parseRepoArg(*repo); err != nil {
		return "", "", err
	}
	if err := requireString("project", *project); err != nil {
		return "", "", err
	}
	return ns, name, nil
}

func mcpTools() []*mcpTool {
	return []*mcpTool{
		{
			Name:  "list_experiment_repos",
			Title: "List experiment repositories",
			Description: "List the experiment repositories visible to the configured token (at most 100). " +
				"Each item names a repository (use its ns/name as `repo` in the other tools). " +
				"Start here when you do not know which repository holds the runs.",
			InputSchema: schemaObject(nil, map[string]any{
				"author": strProp("Only repositories of this namespace (user or organisation)."),
				"search": strProp("Full-text filter on the repository name/description."),
			}),
			ReadOnly: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a listReposArgs
				if err := decodeArgs(raw, &a); err != nil {
					return nil, err
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.ListExperimentRepos(ctx, a.Author, a.Search)
			},
		},
		{
			Name:  "list_projects",
			Title: "List projects",
			Description: "List the projects of an experiment repository, each with its run count, last update and " +
				"metric goals (metric -> \"min\"|\"max\").",
			InputSchema: schemaObject([]string{"repo"}, map[string]any{"repo": repoProp}),
			ReadOnly:    true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a repoArgs
				if err := decodeArgs(raw, &a); err != nil {
					return nil, err
				}
				ns, name, err := e.parseRepoArg(a.Repo)
				if err != nil {
					return nil, err
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.GetExperimentRepo(ctx, ns, name)
			},
		},
		{
			Name:  "list_runs",
			Title: "List runs",
			Description: "List a project's runs with their summaries (status, last_step, config, tags, note, and the " +
				"last/min/max value of every metric in summary/summary_min/summary_max), filtered and sorted on the server. " +
				"The answer also has metric_goals and best (metric -> name of the best non-archived run). " +
				"A project with no runs yet answers an empty list. Runs carry their full config, so use limit on large projects.\n\n" +
				"sort is one of: name, started_at, updated_at, last_step, last:<metric> (last value), min:<metric>, " +
				"max:<metric>, best:<metric> (best first by the metric's goal; needs a goal, see set_metric_goals), " +
				"config:<dotted.key> (e.g. config:optimizer.lr). Example: sort \"min:val/loss\", order \"asc\", limit 5 " +
				"for the five lowest validation losses.",
			InputSchema: schemaObject([]string{"repo", "project"}, map[string]any{
				"repo":    repoProp,
				"project": projectProp,
				"group":   strProp("Only runs of this sweep group (exact match)."),
				"status": strArrayProp("Only runs whose status is any of these.",
					map[string]any{"enum": []string{"running", "finished", "failed", "stale"}}),
				"tag":      strArrayProp("Only runs carrying every one of these tags.", nil),
				"archived": map[string]any{"type": "boolean", "description": "true = only archived runs, false = only unarchived runs; omit for both."},
				"sort":     strProp("Sort key (grammar in the tool description). Omit for the server's default order."),
				"order":    map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "description": "Sort direction (default asc; best: is always best first)."},
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": mcpMaxRunLimit, "description": "Return at most this many runs (1-1000). Omit for all."},
			}),
			ReadOnly: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a listRunsArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				for _, st := range a.Status {
					switch st {
					case "running", "finished", "failed", "stale":
					default:
						return nil, argError(`"status" entries must be running, finished, failed or stale, got %q`, st)
					}
				}
				if a.Order != "" && a.Order != "asc" && a.Order != "desc" {
					return nil, argError(`"order" must be "asc" or "desc", got %q`, a.Order)
				}
				q := hub.RunQuery{Statuses: a.Status, Tags: a.Tag, Archived: a.Archived, Sort: a.Sort, Order: a.Order}
				if a.Group != "" {
					q.Groups = []string{a.Group}
				}
				if a.Limit != nil {
					if *a.Limit < 1 || *a.Limit > mcpMaxRunLimit {
						return nil, argError(`"limit" must be between 1 and %d, got %d`, mcpMaxRunLimit, *a.Limit)
					}
					q.Limit = *a.Limit
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.ListRuns(ctx, ns, name, a.Project, q)
			},
		},
		{
			Name:  "get_run",
			Title: "Get a run",
			Description: "Get one run as it stands now: status, last_step, num_points, timestamps, group, tags, note, " +
				"archived, config, and the last/min/max of every metric. Answers {\"run\": {...}}; an unknown run is an error. " +
				"To wait for a change, use wait_for_run rather than calling this repeatedly.",
			InputSchema: schemaObject([]string{"repo", "project", "run"}, map[string]any{
				"repo": repoProp, "project": projectProp, "run": runProp,
			}),
			ReadOnly: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a runArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				if err := requireString("run", a.Run); err != nil {
					return nil, err
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				run, err := c.GetRun(ctx, ns, name, a.Project, a.Run, hub.WaitOpts{})
				if err != nil {
					return nil, err
				}
				return apitypes.ExpRunResponse{Run: *run}, nil
			},
		},
		{
			Name:  "get_metrics",
			Title: "Get metric series",
			Description: "Get metric traces for some runs as {\"series\": [{\"run\", \"key\", \"points\": [[x, y], ...]}]}. " +
				"Each series is downsampled on the server to at most max_points points (default 200, to keep the answer small; " +
				"up to 5000). For a single number per run (last/min/max) prefer list_runs or get_run, whose summaries are exact.",
			InputSchema: schemaObject([]string{"repo", "project", "runs"}, map[string]any{
				"repo":    repoProp,
				"project": projectProp,
				"runs":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "description": "Run names to fetch."},
				"keys":    strArrayProp("Metric names to fetch (e.g. [\"train/loss\", \"val/loss\"]). Omit for every metric -- which can be large.", nil),
				"x":       map[string]any{"type": "string", "enum": []string{"step", "time"}, "description": "x axis: step (default) or time (unix seconds)."},
				"max_points": map[string]any{"type": "integer", "minimum": 1, "maximum": mcpMaxMaxPoints,
					"description": "Downsample each series to at most this many points (default 200)."},
			}),
			ReadOnly: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a metricsArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				if len(a.Runs) == 0 {
					return nil, argError(`"runs" is required and must name at least one run`)
				}
				if err := requireNames("runs", a.Runs); err != nil {
					return nil, err
				}
				if err := requireNames("keys", a.Keys); err != nil {
					return nil, err
				}
				if a.X != "" && a.X != "step" && a.X != "time" {
					return nil, argError(`"x" must be "step" or "time", got %q`, a.X)
				}
				maxPoints := mcpDefaultMaxPoints
				if a.MaxPoints != nil {
					if *a.MaxPoints < 1 || *a.MaxPoints > mcpMaxMaxPoints {
						return nil, argError(`"max_points" must be between 1 and %d, got %d`, mcpMaxMaxPoints, *a.MaxPoints)
					}
					maxPoints = *a.MaxPoints
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.GetMetrics(ctx, ns, name, a.Project, a.Runs, a.Keys, a.X, maxPoints)
			},
		},
		{
			Name:  "wait_for_run",
			Title: "Wait for a run",
			Description: "Block until a run satisfies a condition, using the server's long poll (no busy polling), " +
				"then answer {\"met\": bool, \"reason\": \"met\"|\"timeout\"|\"stopped\", \"run\": {...}|null, \"until\": \"...\"}. " +
				"reason \"timeout\": timeout_seconds passed first -- call again to keep waiting (run is the last state seen, " +
				"null if the run never appeared). reason \"stopped\": the run is no longer running (finished, failed, or stale " +
				"= stopped logging) and the condition does not hold and does not mention status, so it cannot become true; " +
				"inspect the run instead of waiting again. The run does not have to exist yet: a missing run is retried " +
				"every 5 seconds until the timeout.\n\n" +
				"until grammar: cond ((and|or) cond)*, \"and\" binds tighter, parentheses allowed; cond := FIELD OP VALUE, " +
				"OP one of == != >= <= > <; FIELD: step, points, status (running|finished|failed|stale, == and != only), " +
				"metric:<name> (last value), min:<name>, max:<name>. Quote a metric name with spaces or = ! < > ( ): " +
				"metric:\"val loss\" < 0.2. A comparison on a metric not logged yet is false. " +
				"Examples: \"status!=running\" (default: until the run ends), \"step>=12000 or status!=running\", " +
				"\"min:val/CER < 0.05 and step >= 1000\".",
			InputSchema: schemaObject([]string{"repo", "project", "run"}, map[string]any{
				"repo":    repoProp,
				"project": projectProp,
				"run":     runProp,
				"until":   strProp("Condition to wait for (grammar in the tool description). Default \"status!=running\"."),
				"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": mcpMaxWaitTimeout,
					"description": "Give up after this many seconds and answer reason \"timeout\" (default 600, at most 3600)."},
				"ignore_stale": map[string]any{"type": "boolean",
					"description": "Keep waiting when the run stops without meeting the condition (a stale run may resume). Default false."},
			}),
			ReadOnly: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a waitArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				if err := requireString("run", a.Run); err != nil {
					return nil, err
				}
				untilText := strings.TrimSpace(a.Until)
				if untilText == "" {
					untilText = "status!=running"
				}
				until, err := parseUntil(untilText)
				if err != nil {
					var uerr *untilError
					if errors.As(err, &uerr) {
						return nil, argError("\"until\" does not parse: %s\n%s", uerr, uerr.caret())
					}
					return nil, argError("\"until\" does not parse: %s", err)
				}
				timeout := mcpDefaultWaitTimeout
				if a.TimeoutSeconds != nil {
					if *a.TimeoutSeconds < 1 || *a.TimeoutSeconds > mcpMaxWaitTimeout {
						return nil, argError(`"timeout_seconds" must be between 1 and %d, got %d`, mcpMaxWaitTimeout, *a.TimeoutSeconds)
					}
					timeout = *a.TimeoutSeconds
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				w := &runWaiter{
					Client: c, NS: ns, Name: name, Project: a.Project, Run: a.Run,
					Until: until, UntilText: untilText, Timeout: time.Duration(timeout) * time.Second,
					IgnoreStale: a.IgnoreStale,
					Logf: func(format string, args ...any) {
						e.s.logf("wait_for_run %s: "+format, append([]any{a.Run}, args...)...)
					},
				}
				res, err := w.Wait(ctx)
				if err != nil {
					return nil, err
				}
				return res, nil
			},
		},
		{
			Name:  "config_diff",
			Title: "Diff run configs",
			Description: "Show which config keys (flattened to dotted paths, e.g. optimizer.lr) differ between runs: " +
				"{\"runs\": [...], \"keys\": [{\"key\", \"values\": {run: value}}]}; a run missing from values lacks the key. " +
				"Keys equal in every run are left out. Omit runs to compare every non-archived run (at most 200).",
			InputSchema: schemaObject([]string{"repo", "project"}, map[string]any{
				"repo":         repoProp,
				"project":      projectProp,
				"runs":         strArrayProp("Run names to compare. Omit for every non-archived run.", nil),
				"include_meta": map[string]any{"type": "boolean", "description": "Also compare the _meta and _resume subtrees (left out by default)."},
			}),
			ReadOnly: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a configDiffArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				if err := requireNames("runs", a.Runs); err != nil {
					return nil, err
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.ConfigDiff(ctx, ns, name, a.Project, a.Runs, a.IncludeMeta)
			},
		},
		{
			Name:  "get_notes",
			Title: "Read project notes",
			Description: "Read a project's experiment notebook, the Markdown file {project}/NOTES.md in the repository: " +
				"{\"path\", \"content\", \"exists\", \"blob_sha\", \"commit_sha\"}. A project without notes answers exists false " +
				"and blob_sha \"\". Keep blob_sha: pass it to update_notes as base_sha.",
			InputSchema: schemaObject([]string{"repo", "project"}, map[string]any{"repo": repoProp, "project": projectProp}),
			ReadOnly:    true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a projectArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.GetNotes(ctx, ns, name, a.Project)
			},
		},
		{
			Name:  "update_notes",
			Title: "Replace project notes",
			Description: "Replace a project's notebook ({project}/NOTES.md) with content and commit it (needs write access). " +
				"content is the whole new file, not a patch: read with get_notes, edit, then write back. " +
				"Pass the blob_sha from get_notes as base_sha so a concurrent edit is refused instead of clobbered " +
				"(base_sha \"\" = the notes must not exist yet; omitting base_sha overwrites unconditionally). " +
				"On a conflict, get_notes again, merge, and retry. Links written [text](run:<run name>) link to a run. " +
				"Answers the notes as saved (with the new blob_sha). At most 256 KiB.",
			InputSchema: schemaObject([]string{"repo", "project", "content"}, map[string]any{
				"repo":     repoProp,
				"project":  projectProp,
				"content":  strProp("The complete new Markdown content."),
				"base_sha": strProp("blob_sha from get_notes that this edit is based on (\"\" = the file must not exist yet)."),
				"message":  strProp("Commit message (default: the server's)."),
			}),
			Destructive: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a updateNotesArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				if a.Content == nil {
					return nil, argError(`"content" is required`)
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.PutNotes(ctx, ns, name, a.Project, *a.Content, a.BaseSHA, a.Message)
			},
		},
		{
			Name:  "annotate_run",
			Title: "Annotate a run",
			Description: "Change a run's hand-maintained metadata (needs write access). Only the fields you pass change: " +
				"note replaces the run's Markdown note (\"\" clears it), tags replaces the whole tag list ([] clears it; " +
				"to add one tag, read the run's tags first and pass the extended list), archived hides the run from " +
				"default listings (true) or shows it again (false). Pass at least one of them. Answers {\"run\": {...}}.",
			InputSchema: schemaObject([]string{"repo", "project", "run"}, map[string]any{
				"repo":     repoProp,
				"project":  projectProp,
				"run":      runProp,
				"note":     strProp("New note (Markdown); \"\" clears it."),
				"tags":     strArrayProp("New complete tag list; [] clears every tag.", nil),
				"archived": map[string]any{"type": "boolean", "description": "true archives the run, false unarchives it."},
			}),
			Destructive: true,
			Idempotent:  true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a annotateArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				if err := requireString("run", a.Run); err != nil {
					return nil, err
				}
				if a.Note == nil && a.Tags == nil && a.Archived == nil {
					return nil, argError("pass at least one of note, tags, archived")
				}
				if a.Tags != nil {
					if err := requireNames("tags", *a.Tags); err != nil {
						return nil, err
					}
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				run, err := c.AnnotateRun(ctx, ns, name, a.Project, a.Run, apitypes.ExpRunAnnotationRequest{
					Note: a.Note, Tags: a.Tags, Archived: a.Archived,
				})
				if err != nil {
					return nil, err
				}
				return apitypes.ExpRunAnnotationResponse{Run: *run}, nil
			},
		},
		{
			Name:  "set_metric_goals",
			Title: "Set metric goals",
			Description: "Set the direction each metric improves in, for a project (needs write access): \"min\" (lower is " +
				"better, e.g. loss), \"max\" (higher is better, e.g. accuracy), or \"\" to remove a metric's goal. Goals are " +
				"merged: metrics not named keep theirs. Goals drive list_runs' best map and sort best:<metric>. Works before " +
				"the project has any run. Answers the project with all its goals.",
			InputSchema: schemaObject([]string{"repo", "project", "goals"}, map[string]any{
				"repo":    repoProp,
				"project": projectProp,
				"goals": map[string]any{
					"type":                 "object",
					"description":          `Metric name -> "min" | "max" | "" (remove), e.g. {"val/loss": "min", "val/acc": "max"}.`,
					"additionalProperties": map[string]any{"type": "string", "enum": []string{"min", "max", ""}},
					"minProperties":        1,
				},
			}),
			Idempotent: true,
			call: func(ctx context.Context, e *mcpEnv, raw json.RawMessage) (any, error) {
				var a goalsArgs
				ns, name, err := e.decodeProject(raw, &a, &a.Repo, &a.Project)
				if err != nil {
					return nil, err
				}
				if len(a.Goals) == 0 {
					return nil, argError(`"goals" is required and must name at least one metric`)
				}
				goals := make(map[string]string, len(a.Goals))
				for metric, g := range a.Goals {
					if strings.TrimSpace(metric) == "" {
						return nil, argError(`"goals" has an empty metric name`)
					}
					switch g {
					case "min", "max", "":
					case "none":
						g = ""
					default:
						return nil, argError(`goal for %q must be "min", "max" or "" (remove), got %q`, metric, g)
					}
					goals[metric] = g
				}
				c, err := e.client()
				if err != nil {
					return nil, err
				}
				return c.UpdateProject(ctx, ns, name, a.Project, goals)
			},
		},
	}
}
