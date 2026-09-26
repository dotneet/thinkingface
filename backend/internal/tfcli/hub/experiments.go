package hub

// Experiment tracking: the read and annotate half of /api/v1/experiments
// (docs/dev/agent-features.md §2.2-§2.5, §2.7; docs/dev/api-contract.md §7).
// Ingest (/log, /finish) lives in ingest.go.
//
// Every method answers the server's wire type from internal/apitypes as is:
// those types are the API contract, so a caller (the `tf experiments`
// subcommands, `tf mcp`) can print them with --json without re-shaping them,
// and a field the server adds shows up here without a change to this file.
//
// Naming: an experiment lives at repository ns/name (always a dataset
// repository), project, run. Every one of those segments is path-escaped
// individually -- project and run names are free text on the server ("sweep/
// seed-2", "exp 1", "100%", "実験") and a name that were pasted into the path
// raw would either route to a different endpoint or reach the store as a name
// no row has (see expNameParam in internal/api/experiments.go).

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// MaxRunWait is the longest long-poll the server honours on GetRun
// (agent-features.md §2.4). Longer WaitOpts.Wait values are clamped to it.
const MaxRunWait = 60 * time.Second

// RunQuery narrows and orders ListRuns. The zero value lists every run, in
// the server's default order, archived ones included.
type RunQuery struct {
	// Groups keeps runs whose sweep group is any of these (exact match).
	Groups []string
	// Statuses keeps runs whose derived status is any of these
	// ("running", "finished", "failed", "stale").
	Statuses []string
	// Tags keeps runs carrying every one of these tags.
	Tags []string
	// Archived: nil = both, true = only archived, false = only unarchived.
	Archived *bool
	// Sort is a sort key: name, started_at, updated_at, last_step,
	// last:<metric>, min:<metric>, max:<metric>, best:<metric> (needs a goal)
	// or config:<dotted.key>. "" = the server's default order.
	Sort string
	// Order is "asc" or "desc"; "" = the server's default for Sort.
	Order string
	// Limit caps the number of runs (1..1000); 0 = no limit.
	Limit int
}

// WaitOpts turns GetRun into a long poll. The zero value asks for the
// current state immediately.
type WaitOpts struct {
	// Wait is how long the server may hold the request (clamped to
	// MaxRunWait). 0 = answer immediately.
	Wait time.Duration
	// Since: answer as soon as the run's updated_at is later than this.
	// Pass the updated_at of the state the caller already has.
	Since time.Time
	// Status: answer as soon as the run's derived status differs from this.
	// Pass the status of the state the caller already has.
	Status string
}

// expRepoURL is {endpoint}/api/v1/experiments/{ns}/{name}.
func (c *Client) expRepoURL(ns, name string) string {
	return c.endpoint + "/api/v1/experiments/" + url.PathEscape(ns) + "/" + url.PathEscape(name)
}

// expProjectURL is expRepoURL + /{project}.
func (c *Client) expProjectURL(ns, name, project string) string {
	return c.expRepoURL(ns, name) + "/" + url.PathEscape(project)
}

// expRunURL is expProjectURL + /runs/{run}.
func (c *Client) expRunURL(ns, name, project, run string) string {
	return c.expProjectURL(ns, name, project) + "/runs/" + url.PathEscape(run)
}

// withQuery appends q to u when it is not empty.
func withQuery(u string, q url.Values) string {
	if len(q) == 0 {
		return u
	}
	return u + "?" + q.Encode()
}

// ListExperimentRepos lists experiment repositories (GET /api/v1/experiments).
// author narrows to one namespace and search is a full-text filter; either
// may be "". The server caps the listing at 100 repositories.
func (c *Client) ListExperimentRepos(ctx context.Context, author, search string) (*apitypes.ExpProjectListResponse, error) {
	q := url.Values{}
	if author != "" {
		q.Set("author", author)
	}
	if search != "" {
		q.Set("search", search)
	}
	var out apitypes.ExpProjectListResponse
	if err := c.doJSON(ctx, http.MethodGet, withQuery(c.endpoint+"/api/v1/experiments", q), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetExperimentRepo returns an experiment repository and its projects, each
// with its metric goals (GET /api/v1/experiments/{ns}/{name}).
func (c *Client) GetExperimentRepo(ctx context.Context, ns, name string) (*apitypes.ExpRepoResponse, error) {
	var out apitypes.ExpRepoResponse
	if err := c.doJSON(ctx, http.MethodGet, c.expRepoURL(ns, name), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListRuns lists a project's runs with their summaries, filtered and sorted
// server-side by q (GET .../{project}/runs). The answer also carries the
// project's metric goals and, per goal, the best non-archived run. A project
// that does not exist yet answers an empty list, not an error.
func (c *Client) ListRuns(ctx context.Context, ns, name, project string, q RunQuery) (*apitypes.ExpRunListResponse, error) {
	v := url.Values{}
	for _, g := range q.Groups {
		v.Add("group", g)
	}
	for _, s := range q.Statuses {
		v.Add("status", s)
	}
	for _, t := range q.Tags {
		v.Add("tag", t)
	}
	if q.Archived != nil {
		v.Set("archived", strconv.FormatBool(*q.Archived))
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	if q.Order != "" {
		v.Set("order", q.Order)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var out apitypes.ExpRunListResponse
	if err := c.doJSON(ctx, http.MethodGet, withQuery(c.expProjectURL(ns, name, project)+"/runs", v), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRun returns one run (GET .../runs/{run}). With w.Wait > 0 it is a long
// poll: the server answers once the run's updated_at passes w.Since, its
// derived status differs from w.Status, or w.Wait elapses -- in every case
// with the run as it then stands, so the caller compares and decides. A run
// that does not exist is a 404 *Error (IsNotFound) immediately, even with a
// wait.
func (c *Client) GetRun(ctx context.Context, ns, name, project, run string, w WaitOpts) (*apitypes.ExpRun, error) {
	v := url.Values{}
	if w.Wait > 0 {
		wait := min(w.Wait, MaxRunWait)
		v.Set("wait", strconv.FormatFloat(wait.Seconds(), 'f', -1, 64)+"s")
		if !w.Since.IsZero() {
			// Nanosecond precision on purpose: the server compares
			// updated_at > since, and a since truncated to the second is
			// earlier than the updated_at it came from, so every poll would
			// return at once.
			v.Set("since", w.Since.UTC().Format(time.RFC3339Nano))
		}
		if w.Status != "" {
			v.Set("status", w.Status)
		}
	}
	var out apitypes.ExpRunResponse
	if err := c.doJSON(ctx, http.MethodGet, withQuery(c.expRunURL(ns, name, project, run), v), nil, &out); err != nil {
		return nil, err
	}
	return &out.Run, nil
}

// GetMetrics returns metric traces as [x, y] pairs (GET .../{project}/metrics).
// runs and keys select what to return (nil = all of them); each name is sent
// as its own run= / key= parameter so names containing commas survive. x is
// the x axis: "step" or "time" (unix seconds); "" = step. maxPoints
// downsamples each series (0 = the server default of 1000).
func (c *Client) GetMetrics(ctx context.Context, ns, name, project string, runs, keys []string, x string, maxPoints int) (*apitypes.ExpMetricsResponse, error) {
	v := url.Values{}
	for _, r := range runs {
		v.Add("run", r)
	}
	for _, k := range keys {
		v.Add("key", k)
	}
	if x != "" {
		v.Set("x", x)
	}
	if maxPoints > 0 {
		v.Set("max_points", strconv.Itoa(maxPoints))
	}
	var out apitypes.ExpMetricsResponse
	if err := c.doJSON(ctx, http.MethodGet, withQuery(c.expProjectURL(ns, name, project)+"/metrics", v), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ConfigDiff returns the flattened config keys whose values differ across
// runs (GET .../{project}/config-diff). runs = nil compares every
// non-archived run (at most 200). includeMeta also compares the _meta and
// _resume subtrees, which are left out by default.
func (c *Client) ConfigDiff(ctx context.Context, ns, name, project string, runs []string, includeMeta bool) (*apitypes.ExpConfigDiffResponse, error) {
	v := url.Values{}
	for _, r := range runs {
		v.Add("run", r)
	}
	if includeMeta {
		v.Set("include_meta", "true")
	}
	var out apitypes.ExpConfigDiffResponse
	if err := c.doJSON(ctx, http.MethodGet, withQuery(c.expProjectURL(ns, name, project)+"/config-diff", v), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateProject merges metric goals into a project (PATCH .../{project}):
// each metric maps to "min", "max", or "" to remove its goal; metrics not in
// goals keep theirs. The project is created if it has no runs yet. Needs
// write access. Answers the project as it now stands.
func (c *Client) UpdateProject(ctx context.Context, ns, name, project string, goals map[string]string) (*apitypes.ExpProject, error) {
	var out apitypes.ExpProject
	body := apitypes.ExpProjectUpdateRequest{MetricGoals: goals}
	if err := c.doJSON(ctx, http.MethodPatch, c.expProjectURL(ns, name, project), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetNotes reads a project's notebook, {project}/NOTES.md on the default
// branch (GET .../{project}/notes). A project without notes is not an error:
// the answer has Exists = false and BlobSHA = "".
func (c *Client) GetNotes(ctx context.Context, ns, name, project string) (*apitypes.ExpNotesResponse, error) {
	var out apitypes.ExpNotesResponse
	if err := c.doJSON(ctx, http.MethodGet, c.expProjectURL(ns, name, project)+"/notes", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutNotes replaces a project's notebook with content and commits it to the
// default branch (PUT .../{project}/notes). baseSHA is the optimistic lock:
// nil overwrites unconditionally; otherwise it must equal the current blob
// sha (the BlobSHA a GetNotes answered, "" meaning "the file must not exist
// yet") or the server refuses with a 409 *Error (IsConflict). message is the
// commit message ("" = the server's default). Content is capped at 256 KiB.
func (c *Client) PutNotes(ctx context.Context, ns, name, project, content string, baseSHA *string, message string) (*apitypes.ExpNotesResponse, error) {
	var out apitypes.ExpNotesResponse
	body := apitypes.ExpNotesUpdateRequest{Content: content, BaseSHA: baseSHA, Message: message}
	if err := c.doJSON(ctx, http.MethodPut, c.expProjectURL(ns, name, project)+"/notes", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnnotateRun partially updates a run's hand-maintained metadata (PATCH
// .../runs/{run}): only the non-nil fields of req change, and a non-nil
// Tags replaces the whole tag list (an empty slice clears it). Needs write
// access. Answers the run as it now stands.
func (c *Client) AnnotateRun(ctx context.Context, ns, name, project, run string, req apitypes.ExpRunAnnotationRequest) (*apitypes.ExpRun, error) {
	var out apitypes.ExpRunAnnotationResponse
	if err := c.doJSON(ctx, http.MethodPatch, c.expRunURL(ns, name, project, run), req, &out); err != nil {
		return nil, err
	}
	return &out.Run, nil
}
