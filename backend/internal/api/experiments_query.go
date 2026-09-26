// The experiment endpoints an agent drives a sweep with rather than a person
// reads (docs/dev/agent-features.md §2.2 - §2.5): metric goals on a project,
// server-side filtering and sorting of the run list, a single run with an
// optional long-poll, and the config diff across runs.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

const (
	// maxMetricGoals bounds how many goals one project may carry. A goal is
	// declared per metric a human or agent compares runs by, which is a
	// handful; the cap only stops the JSON column from growing without bound.
	maxMetricGoals = 256
	// maxRunListLimit is the ceiling on the run list's `limit`.
	maxRunListLimit = 1000
	// maxRunWait is the longest a GET .../runs/{run} may hold the request.
	// The route is exempt from handlerTimeout (longPollRoute), so this is its
	// only bound.
	maxRunWait = 60 * time.Second
	// maxConfigDiffRuns bounds a config diff, both the explicit run list and
	// the "every non-archived run" default.
	maxConfigDiffRuns = 200
	// defaultMaxRunWaiters bounds how many GET .../runs/{run}?wait= requests
	// may hold a connection at once, process-wide. Each waiter is a goroutine,
	// an open connection and a store read a second for up to maxRunWait, and
	// the route is exempt from handlerTimeout -- so without a cap, any token
	// that can read one run can pin an unbounded number of them. Past it, a
	// request is answered at once as if its wait had run out; the client
	// compares and polls again, which is what it does after a timeout anyway.
	// 256 is far more concurrent agents than one deployment serves, and far
	// fewer goroutines and pool round trips than would hurt it.
	defaultMaxRunWaiters = 256
)

// runWaitPollInterval is how often a waiting GET .../runs/{run} re-reads the
// store. A var so tests can shorten it; nothing changes it at runtime.
var runWaitPollInterval = time.Second

// maxRunWaiters is defaultMaxRunWaiters as a var, so a test can lower it;
// nothing changes it at runtime. runWaiters counts the requests currently
// waiting against it.
var (
	maxRunWaiters int64 = defaultMaxRunWaiters
	runWaiters    atomic.Int64
)

// acquireRunWaiter reserves one of the maxRunWaiters slots, reporting false
// when they are all taken. A true answer must be paired with releaseRunWaiter.
func acquireRunWaiter() bool {
	if runWaiters.Add(1) > maxRunWaiters {
		runWaiters.Add(-1)
		return false
	}
	return true
}

func releaseRunWaiter() { runWaiters.Add(-1) }

// ------------------------------------------------------------ metric goals

// handleUpdateExperimentProject answers PATCH /api/v1/experiments/{ns}/{repo}/{project}:
// a key-by-key merge of the project's metric goals ("" removes one). The
// project row is created when it does not exist, so goals can be declared
// before the first run logs anything -- which is why the name is held to the
// same rule ingest applies (it must be usable as a directory).
func (s *Server) handleUpdateExperimentProject(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, "dataset", chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "repo")), redirectUI)
	if !ok {
		return
	}
	projectName, ok := expNameParam(w, r, "project", "project")
	if !ok {
		return
	}
	if err := validateIngestProject(projectName); err != nil {
		badRequest(w, "project "+err.Error())
		return
	}
	var req apitypes.ExpProjectUpdateRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with metric_goals") {
		return
	}
	if req.MetricGoals == nil {
		badRequest(w, "nothing to update: send metric_goals")
		return
	}
	if len(req.MetricGoals) > maxMetricGoals {
		badRequest(w, fmt.Sprintf("a project may carry at most %d metric goals", maxMetricGoals))
		return
	}
	for metric, goal := range req.MetricGoals {
		if err := validateIngestMetricName(metric); err != nil {
			badRequest(w, "metric name "+strconv.Quote(metric)+" "+err.Error())
			return
		}
		switch apitypes.MetricGoal(goal) {
		case apitypes.MetricGoalMin, apitypes.MetricGoalMax, "":
		default:
			badRequest(w, fmt.Sprintf(`goal for %q must be "min", "max" or "" (to remove it)`, metric))
			return
		}
	}
	project, err := s.store.MergeExpProjectGoals(r.Context(), repo.ID, projectName, req.MetricGoals, maxMetricGoals)
	if err != nil {
		if errors.Is(err, store.ErrTooManyMetricGoals) {
			badRequest(w, fmt.Sprintf("a project may carry at most %d metric goals", maxMetricGoals))
			return
		}
		internalError(w, "update metric goals", err)
		return
	}
	writeJSON(w, http.StatusOK, toExpProject(*project))
}

// ------------------------------------------------------------ run listing

// runSortKind is what a run list is sorted by.
type runSortKind int

const (
	sortNone runSortKind = iota
	sortName
	sortStartedAt
	sortUpdatedAt
	sortLastStep
	sortLast   // last:<metric>, the summary
	sortMin    // min:<metric>
	sortMax    // max:<metric>
	sortBest   // best:<metric>, min or max by the project's goal
	sortConfig // config:<dotted key>
)

// runQuery is the parsed query string of GET .../{project}/runs.
type runQuery struct {
	groups   map[string]bool // nil = any group
	statuses map[apitypes.RunStatus]bool
	tags     []string
	archived *bool
	sort     runSortKind
	sortArg  string // the metric or config key
	desc     bool
	limit    int // 0 = no limit
}

// parseRunQuery reads and validates the listing parameters. Everything
// malformed is an error (400) rather than silently ignored: an agent that
// misspells a sort and gets the default order back draws the wrong
// conclusion from it.
func parseRunQuery(q url.Values) (runQuery, error) {
	var rq runQuery
	if groups, ok := q["group"]; ok {
		rq.groups = make(map[string]bool, len(groups))
		for _, g := range groups {
			rq.groups[g] = true
		}
	}
	for _, st := range q["status"] {
		status, err := parseDerivedStatus(st)
		if err != nil {
			return rq, err
		}
		if rq.statuses == nil {
			rq.statuses = map[apitypes.RunStatus]bool{}
		}
		rq.statuses[status] = true
	}
	for _, tag := range q["tag"] {
		if tag = strings.TrimSpace(tag); tag != "" {
			rq.tags = append(rq.tags, tag)
		}
	}
	if raw := q.Get("archived"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return rq, errors.New(`archived must be "true" or "false"`)
		}
		rq.archived = &b
	}
	if raw := q.Get("sort"); raw != "" {
		kind, arg, err := parseRunSort(raw)
		if err != nil {
			return rq, err
		}
		rq.sort, rq.sortArg = kind, arg
	}
	switch q.Get("order") {
	case "", "asc":
	case "desc":
		rq.desc = true
	default:
		return rq, errors.New(`order must be "asc" or "desc"`)
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxRunListLimit {
			return rq, fmt.Errorf("limit must be an integer between 1 and %d", maxRunListLimit)
		}
		rq.limit = n
	}
	return rq, nil
}

// parseDerivedStatus accepts the four statuses a run can be reported with.
func parseDerivedStatus(raw string) (apitypes.RunStatus, error) {
	switch st := apitypes.RunStatus(raw); st {
	case apitypes.RunStatusRunning, apitypes.RunStatusFinished, apitypes.RunStatusFailed, apitypes.RunStatusStale:
		return st, nil
	}
	return "", fmt.Errorf(`status %q must be one of "running", "finished", "failed", "stale"`, raw)
}

// parseRunSort parses the `sort` parameter.
func parseRunSort(raw string) (runSortKind, string, error) {
	switch raw {
	case "name":
		return sortName, "", nil
	case "started_at":
		return sortStartedAt, "", nil
	case "updated_at":
		return sortUpdatedAt, "", nil
	case "last_step":
		return sortLastStep, "", nil
	}
	prefix, arg, ok := strings.Cut(raw, ":")
	if ok && arg != "" {
		switch prefix {
		case "last":
			return sortLast, arg, nil
		case "min":
			return sortMin, arg, nil
		case "max":
			return sortMax, arg, nil
		case "best":
			return sortBest, arg, nil
		case "config":
			return sortConfig, arg, nil
		}
	}
	return sortNone, "", fmt.Errorf("sort %q is not one of name, started_at, updated_at, last_step, "+
		"last:<metric>, min:<metric>, max:<metric>, best:<metric>, config:<key>", raw)
}

// checkGoals rejects best:<metric> for a metric the project has no goal for.
func (rq runQuery) checkGoals(goals map[string]string) error {
	if rq.sort == sortBest && goals[rq.sortArg] == "" {
		return fmt.Errorf("sort best:%s needs a metric goal for %q: set one with PATCH on the project", rq.sortArg, rq.sortArg)
	}
	return nil
}

// apply filters, sorts and truncates runs, and names the best non-archived
// run per goal among the filtered runs (before the limit, so `best` does not
// depend on how many rows the caller asked to see).
func (rq runQuery) apply(runs []apitypes.ExpRun, goals map[string]string) ([]apitypes.ExpRun, map[string]string) {
	filtered := make([]apitypes.ExpRun, 0, len(runs))
	for _, run := range runs {
		if rq.matches(run) {
			filtered = append(filtered, run)
		}
	}
	best := bestRuns(filtered, goals)

	if rq.sort != sortNone {
		// Configs are flattened once per run rather than once per comparison.
		var flat map[string]map[string]any
		if rq.sort == sortConfig {
			flat = make(map[string]map[string]any, len(filtered))
			for _, run := range filtered {
				flat[run.Name] = flattenConfig(run.Config, true)
			}
		}
		desc := rq.desc
		if rq.sort == sortBest {
			// Best first, whatever `order` says.
			desc = goals[rq.sortArg] == string(apitypes.MetricGoalMax)
		}
		keyOf := func(run apitypes.ExpRun) (any, bool) { return rq.sortValue(run, goals, flat) }
		sort.SliceStable(filtered, func(i, j int) bool {
			a, aok := keyOf(filtered[i])
			b, bok := keyOf(filtered[j])
			switch {
			case !aok || !bok:
				// Missing values sort last in both orders.
				return aok && !bok
			case desc:
				return compareSortValues(b, a) < 0
			default:
				return compareSortValues(a, b) < 0
			}
		})
	}
	if rq.limit > 0 && len(filtered) > rq.limit {
		filtered = filtered[:rq.limit]
	}
	return filtered, best
}

func (rq runQuery) matches(run apitypes.ExpRun) bool {
	if rq.groups != nil && !rq.groups[run.Group] {
		return false
	}
	if rq.statuses != nil && !rq.statuses[run.Status] {
		return false
	}
	if rq.archived != nil && run.Archived != *rq.archived {
		return false
	}
	for _, want := range rq.tags {
		found := false
		for _, have := range run.Tags {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// sortValue is the value a run is sorted by, and false when it has none.
func (rq runQuery) sortValue(run apitypes.ExpRun, goals map[string]string, flat map[string]map[string]any) (any, bool) {
	metric := func(m map[string]float64) (any, bool) {
		v, ok := m[rq.sortArg]
		return v, ok
	}
	switch rq.sort {
	case sortName:
		return run.Name, true
	case sortStartedAt:
		if run.StartedAt == nil {
			return nil, false
		}
		return *run.StartedAt, true
	case sortUpdatedAt:
		return run.UpdatedAt, true
	case sortLastStep:
		return float64(run.LastStep), true
	case sortLast:
		return metric(run.Summary)
	case sortMin:
		return metric(run.SummaryMin)
	case sortMax:
		return metric(run.SummaryMax)
	case sortBest:
		if goals[rq.sortArg] == string(apitypes.MetricGoalMax) {
			return metric(run.SummaryMax)
		}
		return metric(run.SummaryMin)
	case sortConfig:
		v, ok := flat[run.Name][rq.sortArg]
		if !ok || v == nil {
			return nil, false
		}
		return v, true
	}
	return nil, false
}

// compareSortValues orders two sort values. Values of one kind compare
// naturally; across kinds (a config key that is a number in one run and a
// string in another) numbers come before strings, strings before booleans,
// and anything else (a list, an object) last, compared by its JSON.
func compareSortValues(a, b any) int {
	ra, rb := sortRank(a), sortRank(b)
	if ra != rb {
		return ra - rb
	}
	switch av := a.(type) {
	case float64:
		bv := b.(float64)
		switch {
		case av < bv:
			return -1
		case av > bv:
			return 1
		}
		return 0
	case string:
		return strings.Compare(av, b.(string))
	case bool:
		bv := b.(bool)
		switch {
		case av == bv:
			return 0
		case !av:
			return -1
		}
		return 1
	case time.Time:
		return av.Compare(b.(time.Time))
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return strings.Compare(string(aj), string(bj))
}

func sortRank(v any) int {
	switch v.(type) {
	case float64:
		return 0
	case string:
		return 1
	case bool:
		return 2
	case time.Time:
		return 3
	}
	return 4
}

// bestRuns names, for every metric with a goal, the non-archived run with the
// best extreme: the lowest summary_min for "min", the highest summary_max for
// "max". Ties go to the lexically smaller run name so the answer does not
// depend on the listing order. A metric no candidate has logged is absent.
func bestRuns(runs []apitypes.ExpRun, goals map[string]string) map[string]string {
	best := map[string]string{}
	for metric, goal := range goals {
		var bestName string
		var bestValue float64
		found := false
		for _, run := range runs {
			if run.Archived {
				continue
			}
			var v float64
			var ok bool
			if goal == string(apitypes.MetricGoalMax) {
				v, ok = run.SummaryMax[metric]
			} else {
				v, ok = run.SummaryMin[metric]
			}
			if !ok || math.IsNaN(v) {
				continue
			}
			better := !found ||
				(goal == string(apitypes.MetricGoalMax) && v > bestValue) ||
				(goal != string(apitypes.MetricGoalMax) && v < bestValue) ||
				(v == bestValue && run.Name < bestName)
			if better {
				bestName, bestValue, found = run.Name, v, true
			}
		}
		if found {
			best[metric] = bestName
		}
	}
	return best
}

// flattenConfig turns a nested config into dotted paths ("optimizer.lr").
// Lists and scalars are leaves. With dropMeta, the top-level bookkeeping keys
// the Python shim writes (_meta, _resume) are left out.
func flattenConfig(config map[string]any, dropMeta bool) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		// Sorted, so a literal dotted key and a nested path that collide
		// resolve the same way on every call.
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if prefix == "" && dropMeta && isConfigMetaKey(k) {
				continue
			}
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if nested, ok := m[k].(map[string]any); ok && len(nested) > 0 {
				walk(path, nested)
				continue
			}
			out[path] = m[k]
		}
	}
	walk("", config)
	return out
}

func isConfigMetaKey(k string) bool { return k == "_meta" || k == "_resume" }

// -------------------------------------------------------------- one run

// handleExperimentRun answers GET /api/v1/experiments/{ns}/{repo}/{project}/runs/{run}
// (docs/dev/agent-features.md §2.4). With `wait`, it holds the request until
// the run's updated_at is later than `since`, or its derived status differs
// from `status`, or the wait elapses -- and answers 200 with the current run
// in every case. An omitted `since` / `status` defaults to what the first
// read found, so a bare `?wait=30s` means "until anything changes". A run
// that does not exist answers 404 at once, wait or not, and so does every
// request past the maxRunWaiters-th concurrent waiter (with the run as it is).
//
// The poll reads only the run row: its models are not part of the exit
// condition, so they are loaded once, for the answer.
func (s *Server) handleExperimentRun(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	projectName, ok := expNameParam(w, r, "project", "project")
	if !ok {
		return
	}
	runName, ok := expNameParam(w, r, "run", "run")
	if !ok {
		return
	}
	q := r.URL.Query()
	wait, err := parseRunWait(q.Get("wait"))
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	var since *time.Time
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			badRequest(w, "since must be an RFC 3339 timestamp")
			return
		}
		since = &t
	}
	var status apitypes.RunStatus
	if raw := q.Get("status"); raw != "" {
		if status, err = parseDerivedStatus(raw); err != nil {
			badRequest(w, err.Error())
			return
		}
	}

	ctx := r.Context()
	project, err := s.store.GetExpProject(ctx, repo.ID, projectName)
	if err != nil {
		handleStoreError(w, "load experiment project", err)
		return
	}
	row, err := s.store.GetExpRun(ctx, project.ID, runName)
	if err != nil {
		handleStoreError(w, "load experiment run", err)
		return
	}
	run := toExpRun(row, nil)
	if wait > 0 && acquireRunWaiter() {
		defer releaseRunWaiter()
		if since == nil {
			t := run.UpdatedAt
			since = &t
		}
		if status == "" {
			status = run.Status
		}
		deadline := time.NewTimer(wait)
		defer deadline.Stop()
		ticker := time.NewTicker(runWaitPollInterval)
		defer ticker.Stop()
	poll:
		for !runChanged(run, *since, status) {
			select {
			case <-ctx.Done():
				// The client went away; there is nobody to answer.
				return
			case <-deadline.C:
				break poll
			case <-ticker.C:
				if row, err = s.store.GetExpRun(ctx, project.ID, runName); err != nil {
					if ctx.Err() != nil {
						return
					}
					handleStoreError(w, "load experiment run", err)
					return
				}
				run = toExpRun(row, nil)
			}
		}
	}
	models, err := s.store.ListRunModels(ctx, project.ID)
	if err != nil {
		handleStoreError(w, "load run models", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ExpRunResponse{Run: toExpRun(row, models)})
}

// runChanged is the long-poll's exit condition.
func runChanged(run apitypes.ExpRun, since time.Time, status apitypes.RunStatus) bool {
	return run.UpdatedAt.After(since) || run.Status != status
}

// toExpRun is toExpRuns for one row (derived status, and models when given).
func toExpRun(row *store.ExpRun, models map[string][]store.ExpRunModel) apitypes.ExpRun {
	return toExpRuns([]store.ExpRun{*row}, models)[0]
}

// parseRunWait reads `wait` as a Go duration ("30s") or a bare number of
// seconds. Longer than maxRunWait is clamped to it rather than refused, so a
// client with a longer timeout of its own simply polls again.
func parseRunWait(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		secs, serr := strconv.ParseFloat(raw, 64)
		if serr != nil || math.IsNaN(secs) || math.IsInf(secs, 0) {
			return 0, errors.New(`wait must be a duration such as "30s" (at most 60s)`)
		}
		// Clamped while still a float: a float64 -> int64 conversion that
		// overflows is implementation-defined in Go, and on amd64 "1e12"
		// seconds came out as a large negative Duration -- refused as
		// negative there, clamped to 60s on arm64.
		if secs < 0 {
			return 0, errors.New("wait must not be negative")
		}
		secs = min(secs, maxRunWait.Seconds())
		d = time.Duration(secs * float64(time.Second))
	}
	if d < 0 {
		return 0, errors.New("wait must not be negative")
	}
	return min(d, maxRunWait), nil
}

// ------------------------------------------------------------ config diff

// handleExperimentConfigDiff answers GET .../{project}/config-diff
// (docs/dev/agent-features.md §2.5): the flattened config keys whose value
// differs, or is missing, across the chosen runs.
func (s *Server) handleExperimentConfigDiff(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	projectName, ok := expNameParam(w, r, "project", "project")
	if !ok {
		return
	}
	q := r.URL.Query()
	includeMeta := false
	if raw := q.Get("include_meta"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			badRequest(w, `include_meta must be "true" or "false"`)
			return
		}
		includeMeta = b
	}
	var requested []string
	seen := map[string]bool{}
	for _, name := range q["run"] {
		if name != "" && !seen[name] {
			seen[name] = true
			requested = append(requested, name)
		}
	}
	if len(requested) > maxConfigDiffRuns {
		badRequest(w, fmt.Sprintf("a config diff compares at most %d runs", maxConfigDiffRuns))
		return
	}

	empty := apitypes.ExpConfigDiffResponse{Runs: []string{}, Keys: []apitypes.ExpConfigDiffKey{}}
	project, err := s.store.GetExpProject(r.Context(), repo.ID, projectName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && len(requested) == 0 {
			writeJSON(w, http.StatusOK, empty)
			return
		}
		handleStoreError(w, "load experiment project", err)
		return
	}
	rows, err := s.store.ListExpRuns(r.Context(), project.ID)
	if err != nil {
		internalError(w, "list experiment runs", err)
		return
	}
	byName := make(map[string]store.ExpRun, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}

	var chosen []store.ExpRun
	if len(requested) > 0 {
		for _, name := range requested {
			row, ok := byName[name]
			if !ok {
				notFound(w, fmt.Sprintf("run %q not found in project %q", name, projectName))
				return
			}
			chosen = append(chosen, row)
		}
	} else {
		for _, row := range rows {
			if !row.Archived {
				chosen = append(chosen, row)
			}
		}
		if len(chosen) > maxConfigDiffRuns {
			// The most recently updated ones, kept in listing order.
			recent := append([]store.ExpRun(nil), chosen...)
			sort.SliceStable(recent, func(i, j int) bool { return recent[i].UpdatedAt.After(recent[j].UpdatedAt) })
			keep := map[string]bool{}
			for _, row := range recent[:maxConfigDiffRuns] {
				keep[row.Name] = true
			}
			kept := chosen[:0]
			for _, row := range chosen {
				if keep[row.Name] {
					kept = append(kept, row)
				}
			}
			chosen = kept
		}
	}
	writeJSON(w, http.StatusOK, configDiff(chosen, includeMeta))
}

// configDiff compares the flattened configs of runs and keeps only the keys
// that are missing from at least one run or whose values are not all equal.
func configDiff(runs []store.ExpRun, includeMeta bool) apitypes.ExpConfigDiffResponse {
	resp := apitypes.ExpConfigDiffResponse{Runs: make([]string, 0, len(runs)), Keys: []apitypes.ExpConfigDiffKey{}}
	flat := make([]map[string]any, len(runs))
	allKeys := map[string]bool{}
	for i, run := range runs {
		resp.Runs = append(resp.Runs, run.Name)
		flat[i] = flattenConfig(run.Config, !includeMeta)
		for k := range flat[i] {
			allKeys[k] = true
		}
	}
	keys := make([]string, 0, len(allKeys))
	for k := range allKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := map[string]any{}
		differs := false
		var first any
		for i, run := range runs {
			v, ok := flat[i][key]
			if !ok {
				differs = true
				continue
			}
			if len(values) == 0 {
				first = v
			} else if !reflect.DeepEqual(first, v) {
				differs = true
			}
			values[run.Name] = v
		}
		if differs {
			resp.Keys = append(resp.Keys, apitypes.ExpConfigDiffKey{Key: key, Values: values})
		}
	}
	return resp
}
