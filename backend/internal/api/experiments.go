package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// handleListExperiments answers GET /api/v1/experiments. `author` narrows to
// one namespace (case-insensitively, like every other namespace lookup) and
// `limit`/`offset` page through, which is what the namespace page's
// Experiments tab needs: it used to fetch the global list and filter it in
// the browser, silently losing everything past the first hundred
// (docs/dev/namespace-design.md §5.6). With no parameters the response is what it
// always was, plus `total`. `search` is the same full text filter every other
// Web UI listing takes (store.RepoFilter.Search) -- the global /experiments
// page needs it for the same reason it needs paging: the backend already caps
// this endpoint at 100 rows, so past that a search is the only way to reach
// an experiment repository that isn't on the first page.
func (s *Server) handleListExperiments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	isExperiment := true
	filter := store.RepoFilter{
		Kind: "dataset", IsExperiment: &isExperiment,
		Author: q.Get("author"), Search: q.Get("search"), Limit: limit, Offset: offset,
	}
	repos, total, _, err := s.store.ListRepos(r.Context(), filter)
	if err != nil {
		internalError(w, "list experiment repositories", err)
		return
	}

	items := make([]apitypes.ExpProjectListItem, 0, len(repos))
	for i := range repos {
		repo := &repos[i]
		projects, err := s.store.ListExpProjects(r.Context(), repo.ID)
		if err != nil {
			internalError(w, "list experiment projects", err)
			return
		}
		items = append(items, apitypes.ExpProjectListItem{
			Namespace:   repo.Namespace,
			Name:        repo.Name,
			FullName:    repo.FullName(),
			NumProjects: len(projects),
			UpdatedAt:   repo.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, apitypes.ExpProjectListResponse{Items: items, Total: total})
}

// loadExperimentRepo resolves the dataset repository backing an experiment URL.
func (s *Server) loadExperimentRepo(w http.ResponseWriter, r *http.Request) (*store.Repo, bool) {
	return s.loadRepoForRead(w, r, "dataset", chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "repo")), redirectUI)
}

func (s *Server) handleExperimentRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	projects, err := s.store.ListExpProjects(r.Context(), repo.ID)
	if err != nil {
		internalError(w, "list experiment projects", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ExpRepoResponse{
		Repo: toSummary(repo), Projects: toExpProjects(projects),
	})
}

func (s *Server) handleExperimentRuns(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	project, err := s.store.GetExpProject(r.Context(), repo.ID, chi.URLParam(r, "project"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, apitypes.ExpRunListResponse{Runs: []apitypes.ExpRun{}})
			return
		}
		internalError(w, "load experiment project", err)
		return
	}
	runs, err := s.store.ListExpRuns(r.Context(), project.ID)
	if err != nil {
		internalError(w, "list experiment runs", err)
		return
	}
	// One extra query for the whole project rather than one per run: the
	// produced-model list lives in its own table (store/experiments.go).
	models, err := s.store.ListRunModels(r.Context(), project.ID)
	if err != nil {
		internalError(w, "list run models", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ExpRunListResponse{Runs: toExpRuns(runs, models)})
}

func (s *Server) handleExperimentMetrics(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	maxPoints, _ := strconv.Atoi(q.Get("max_points"))
	// Clamped, like every other caller-supplied limit here. Downsampling
	// (experiments.downsample) is the only thing keeping a long run's series
	// out of the response body, and this endpoint is readable without
	// authentication, so an unbounded max_points turns one request into
	// "serialise every point you have". Zero or less still means "unset" and
	// is left to the package default of 1000.
	if maxPoints > maxMetricPoints {
		maxPoints = maxMetricPoints
	}

	series, err := s.experiments().Series(r.Context(), repo, experiments.SeriesRequest{
		Project: chi.URLParam(r, "project"),
		Runs:    selectedNames(q, "run", "runs"),
		Keys:    selectedNames(q, "key", "keys"),
		XAxis:   q.Get("x"),
		Max:     maxPoints,
	})
	if err != nil {
		internalError(w, "read metric series", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ExpMetricsResponse{Series: series})
}

const (
	// maxMetricPoints is the ceiling on `max_points` for the metrics
	// endpoint. It is well above the default of 1000
	// (experiments.SeriesRequest) so no chart the UI draws ever meets it,
	// and far below the point count a long training run accumulates, which
	// is what it exists to keep out of a single response.
	maxMetricPoints = 5000
	// maxRunTags bounds how many labels one run may carry. The column is an
	// unconstrained text[], so the ceiling lives here.
	maxRunTags = 32
	// maxRunTagBytes bounds one tag, matching the other free-text ingest names.
	maxRunTagBytes = 64
	// maxRunNoteBytes bounds a run's note. The column is unconstrained TEXT and
	// the note is rendered as Markdown on the run page, so the ceiling lives
	// here; a few pages of prose fit comfortably.
	maxRunNoteBytes = 16384
)

// runStaleAfter is how long a run stored as "running" may go without an update
// before this API reports it as apitypes.RunStatusStale.
//
// 30 minutes. The number has to sit above the longest gap a *live* run leaves
// between updates and below the point where "still running" stops being a
// useful thing to read. The Python shim flushes its buffer on a timer and
// pings even when a step logged nothing, so a healthy job checks in on the
// order of seconds; the slowest realistic case is a large-model training loop
// that only logs once per evaluation, which is minutes, not half an hour.
// Below ten minutes such a job would flicker into "stale" and back; much above
// half an hour a crashed job would sit in the list looking alive for most of a
// working session. Deliberately not configurable: it is a presentation
// threshold, and a knob here would only make two deployments disagree about
// what the same row means.
const runStaleAfter = 30 * time.Minute

// deriveRunStatus is the whole of the stale-run feature: a status is computed
// on read from the row's own updated_at and never written back, so there is no
// column, no migration and no sweeper to keep honest, and a run that comes
// back to life clears the flag simply by logging again (its updated_at moves).
//
// Only a "running" row can change. finished and failed are terminal states
// whose age says nothing -- a job that finished last year is still finished --
// and an unknown status is passed through rather than reinterpreted.
//
// The comparison is strictly greater, so a run whose last update is exactly
// runStaleAfter old still reads as running: the boundary belongs to the
// optimistic side, where a job that is merely slow to check in lives.
func deriveRunStatus(stored string, updatedAt, now time.Time) apitypes.RunStatus {
	status := apitypes.RunStatus(stored)
	if status != apitypes.RunStatusRunning {
		return status
	}
	// A row with no timestamp at all (a run recovered from an export that
	// carried none) cannot be judged, so it keeps what it claims.
	if updatedAt.IsZero() {
		return status
	}
	if now.Sub(updatedAt) > runStaleAfter {
		return apitypes.RunStatusStale
	}
	return status
}

// normalizeNote validates a run note. Unlike a tag it may span lines, so only
// the control characters that are not whitespace are rejected; trailing
// whitespace is trimmed so "clear the note" is expressible as a blank string.
func normalizeNote(raw string) (string, error) {
	if len(raw) > maxRunNoteBytes {
		return "", fmt.Errorf("a note must be at most %d bytes", maxRunNoteBytes)
	}
	if !utf8.ValidString(raw) {
		return "", errors.New("a note must be valid UTF-8")
	}
	for _, r := range raw {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return "", errors.New("a note must not contain control characters")
		}
	}
	return strings.TrimRight(raw, " \t\r\n"), nil
}

const (
	// maxRunModels bounds how many produced models one run may declare. A
	// training script logging more than this is not describing provenance any
	// more, and the column behind it is an unbounded child table.
	maxRunModels = 32
	// maxRevisionBytes bounds a pinned revision. A commit SHA is 40 bytes; a
	// branch or tag name may be longer, but not by much.
	maxRevisionBytes = 256
)

// normalizeRunModels validates a produced-model list: every entry must name a
// model repository as "ns/name", optionally pinned to a revision.
//
// A reference that does not parse is rejected outright rather than kept as a
// dangling raw string. Unlike a lineage card -- which a human writes by hand
// and whose typos are worth surfacing -- this list is written by a program
// through an API that can answer it, so a malformed reference is a bug to
// report at the call site. A *well-formed* reference to a repository that does
// not exist is still kept: that is the dangling case the UI warns about.
func normalizeRunModels(raw []apitypes.ExpRunModelInput) ([]store.ExpRunModel, error) {
	if len(raw) > maxRunModels {
		return nil, fmt.Errorf("a run may declare at most %d produced models", maxRunModels)
	}
	out := make([]store.ExpRunModel, 0, len(raw))
	seen := map[string]bool{}
	for _, m := range raw {
		repoID := strings.TrimSpace(m.RepoID)
		ns, name, ok := strings.Cut(repoID, "/")
		if !ok {
			return nil, fmt.Errorf("model %q must look like \"namespace/name\"", m.RepoID)
		}
		if err := validateName(ns); err != nil {
			return nil, fmt.Errorf("model %q: namespace %s", m.RepoID, err.Error())
		}
		if err := validateName(name); err != nil {
			return nil, fmt.Errorf("model %q: name %s", m.RepoID, err.Error())
		}
		rev := strings.TrimSpace(m.Revision)
		if len(rev) > maxRevisionBytes {
			return nil, fmt.Errorf("model %q: revision must be at most %d bytes", m.RepoID, maxRevisionBytes)
		}
		if rev != "" {
			if err := validateIngestName(rev); err != nil {
				return nil, fmt.Errorf("model %q: revision %s", m.RepoID, err.Error())
			}
		}
		entry := store.ExpRunModel{Namespace: ns, Name: name, Revision: rev, Raw: ns + "/" + name}
		if rev != "" {
			entry.Raw += "@" + rev
		}
		if seen[entry.Raw] {
			continue
		}
		seen[entry.Raw] = true
		out = append(out, entry)
	}
	return out, nil
}

// normalizeTags validates and canonicalises a tag list: whitespace is trimmed,
// empties are dropped, and duplicates collapse so the stored array is the set
// the UI renders. Order is preserved to keep the user's own arrangement.
func normalizeTags(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if len(tag) > maxRunTagBytes {
			return nil, fmt.Errorf("a tag must be at most %d bytes", maxRunTagBytes)
		}
		if !utf8.ValidString(tag) {
			return nil, errors.New("a tag must be valid UTF-8")
		}
		for _, r := range tag {
			if r < 0x20 || r == 0x7f {
				return nil, errors.New("a tag must not contain control characters")
			}
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	if len(out) > maxRunTags {
		return nil, fmt.Errorf("a run may carry at most %d tags", maxRunTags)
	}
	return out, nil
}

// decodeRunSegment undoes the one escape chi leaves in a path parameter. chi
// routes on the raw path, so an encoded slash arrives as %2F while every other
// escape (%20 and friends) has already been decoded for us — and run names may
// contain slashes, since ingest only forbids control characters.
func decodeRunSegment(raw string) string {
	return strings.ReplaceAll(strings.ReplaceAll(raw, "%2F", "/"), "%2f", "/")
}

// handleExperimentRunAnnotation updates the hand-maintained metadata on one
// run (tags, archived, baseline, note). Write access to the backing dataset
// repository is required: annotations are shared state, not a per-viewer
// preference.
func (s *Server) handleExperimentRunAnnotation(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, "dataset", chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "repo")), redirectUI)
	if !ok {
		return
	}
	var req apitypes.ExpRunAnnotationRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with tags, archived, is_baseline or note") {
		return
	}

	upd := store.RunAnnotation{Archived: req.Archived, IsBaseline: req.IsBaseline}
	if req.Tags != nil {
		tags, err := normalizeTags(*req.Tags)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		upd.Tags = &tags
	}
	if req.Note != nil {
		note, err := normalizeNote(*req.Note)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		upd.Note = &note
	}
	if req.Models != nil {
		models, err := normalizeRunModels(*req.Models)
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		upd.Models = &models
	}
	if upd.IsEmpty() {
		badRequest(w, "nothing to update: send at least one of tags, archived, is_baseline, note or models")
		return
	}

	project, err := s.store.GetExpProject(r.Context(), repo.ID, chi.URLParam(r, "project"))
	if err != nil {
		handleStoreError(w, "load experiment project", err)
		return
	}
	runName := decodeRunSegment(chi.URLParam(r, "run"))
	run, err := s.store.UpdateExpRunAnnotation(r.Context(), project.ID, runName, upd)
	if err != nil {
		handleStoreError(w, "update run annotations", err)
		return
	}
	// Re-read the produced models rather than echoing what was sent: only the
	// store knows whether each target resolves.
	models, err := s.store.ListRunModels(r.Context(), project.ID)
	if err != nil {
		internalError(w, "list run models", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ExpRunAnnotationResponse{
		Run: toExpRuns([]store.ExpRun{*run}, models)[0],
	})
}

// RunArtifactDir is where a run's artifacts live inside its experiment dataset
// repository: "{project}/artifacts/{run}" (docs/dev/api-contract.md §7).
//
// The "artifacts" segment is what keeps the convention clear of the parquet
// layout the indexer detects (internal/experiments.DetectLayouts): a project
// is found from "{project}/metrics.parquet" or "{project}.parquet", and
// neither shape can appear under a directory named artifacts -- with the one
// exception of an artifact literally called metrics.parquet, which the Python
// shim refuses for exactly that reason.
func RunArtifactDir(project, run string) string {
	return path.Join(project, "artifacts", run)
}

// safeRunArtifactDir builds the artifact directory and reports whether it is
// still inside the project. Ingest only forbids control characters in a
// project or run name, so ".." is a name a client could pick; path.Join would
// then quietly resolve it into a sibling directory.
func safeRunArtifactDir(project, run string) (string, bool) {
	dir := RunArtifactDir(project, run)
	// path.Join cleans, so a traversal shows up either as a leading ".." (the
	// project climbed out of the repository) or as a directory that no longer
	// sits below "{project}/artifacts/" (the run did).
	if dir == ".." || strings.HasPrefix(dir, "../") || strings.HasPrefix(dir, "/") {
		return "", false
	}
	prefix := path.Join(project, "artifacts") + "/"
	if !strings.HasPrefix(dir, prefix) || dir == prefix {
		return "", false
	}
	return dir, true
}

// handleExperimentRunArtifacts lists the files `trackio.log_artifact` wrote
// for one run. There is no artifact store of its own: the files are ordinary
// repository content committed through the HuggingFace-compatible commit
// endpoint, so this is a directory listing of the repository's default branch
// and everything else -- `git clone`, `resolve`, the content-addressed
// objects behind `/gcs/{rev}` -- already carries them.
//
// A run that logged nothing has no directory, which is an empty list rather
// than a 404: "this run produced no artifacts" is an answer.
func (s *Server) handleExperimentRunArtifacts(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	project := chi.URLParam(r, "project")
	runName := decodeRunSegment(chi.URLParam(r, "run"))
	dir, safe := safeRunArtifactDir(project, runName)
	if !safe {
		badRequest(w, "project and run must not contain path traversal segments")
		return
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	resp := apitypes.ExpArtifactListResponse{
		Path: dir, Rev: repo.DefaultBranch, Artifacts: []apitypes.ExpArtifact{},
	}
	entries, _, err := gitRepo.Tree(repo.DefaultBranch, dir, true)
	if err != nil {
		if errors.Is(err, gitrepo.ErrPathNotFound) {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		handleStoreError(w, "read tree", err)
		return
	}

	for _, e := range entries {
		if e.IsDir {
			continue
		}
		resp.Artifacts = append(resp.Artifacts, apitypes.ExpArtifact{
			Name:    strings.TrimPrefix(e.Path, dir+"/"),
			Path:    e.Path,
			Size:    e.TargetSize(),
			LFS:     e.LFS != nil,
			Preview: previewKind(e.Path),
		})
	}
	sort.Slice(resp.Artifacts, func(i, j int) bool { return resp.Artifacts[i].Name < resp.Artifacts[j].Name })
	writeJSON(w, http.StatusOK, resp)
}

// handleDeleteExperimentRun answers
// DELETE /api/v1/experiments/{ns}/{repo}/{project}/runs/{run}. It removes the
// indexed run and its live metric points; unlike PATCH .../runs/{run} with
// {"archived": true}, which merely hides the run, this is irreversible for
// anything that was only ever ingested live.
//
// A run whose points came from a trackio parquet export reappears the next
// time that export is indexed -- the export is that path's source of truth,
// and deleting the run here does not rewrite git history. Deleting the whole
// experiment repository is the way to get rid of those for good.
func (s *Server) handleDeleteExperimentRun(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, "dataset", chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "repo")), redirectUI)
	if !ok {
		return
	}
	project, err := s.store.GetExpProject(r.Context(), repo.ID, chi.URLParam(r, "project"))
	if err != nil {
		handleStoreError(w, "load experiment project", err)
		return
	}
	runName := decodeRunSegment(chi.URLParam(r, "run"))
	if err := s.store.DeleteExpRun(r.Context(), project.ID, runName); err != nil {
		handleStoreError(w, "delete run", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// toExpProjects drops the database identifiers from the stored project rows.
func toExpProjects(rows []store.ExpProject) []apitypes.ExpProject {
	out := make([]apitypes.ExpProject, 0, len(rows))
	for _, p := range rows {
		out = append(out, apitypes.ExpProject{Name: p.Name, NumRuns: p.NumRuns, UpdatedAt: p.UpdatedAt})
	}
	return out
}

// toExpRuns drops the database identifiers and narrows the stored summary to
// the numbers a chart can plot: every value logged as a metric is a float, and
// anything else in there could not be drawn anyway.
//
// modelsByRun comes from store.ListRunModels and may be nil, which simply
// leaves every run's Models empty.
//
// This is also the one place a run's reported status is decided
// (deriveRunStatus), and it is the single funnel every endpoint that returns
// an apitypes.ExpRun goes through -- the listing and the annotation response
// alike -- so "running" cannot mean one thing on one route and another on the
// next.
func toExpRuns(rows []store.ExpRun, modelsByRun map[string][]store.ExpRunModel) []apitypes.ExpRun {
	// One clock reading for the whole batch, so two runs that were updated at
	// the same instant cannot land on opposite sides of the threshold.
	now := time.Now()
	out := make([]apitypes.ExpRun, 0, len(rows))
	for _, r := range rows {
		models := make([]apitypes.ExpRunModelRef, 0, len(modelsByRun[r.Name]))
		for _, m := range modelsByRun[r.Name] {
			models = append(models, apitypes.ExpRunModelRef{
				RepoID: m.FullName(), Revision: m.Revision, Exists: m.Exists,
			})
		}
		summary := make(map[string]float64, len(r.Summary))
		for k, v := range r.Summary {
			if f, ok := numeric(v); ok {
				summary[k] = f
			}
		}
		tags := r.Tags
		if tags == nil {
			tags = []string{}
		}
		out = append(out, apitypes.ExpRun{
			Name: r.Name, Status: deriveRunStatus(r.Status, r.UpdatedAt, now),
			LastStep: r.LastStep, NumPoints: r.NumPoints,
			StartedAt: r.StartedAt, UpdatedAt: r.UpdatedAt,
			Config: r.Config, MetricKeys: r.MetricKeys, Summary: summary,
			Group: r.Group, JobType: r.JobType,
			Tags: tags, Archived: r.Archived, IsBaseline: r.IsBaseline,
			Note: r.Note, Models: models,
		})
	}
	return out
}

// numeric converts a value decoded from the summary JSON column to a float.
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// selectedNames reads a list of run or metric names off the query string, in
// either of the two spellings the metrics endpoint accepts: repeated keys
// (`run=lr=0.1,bs=32&run=baseline`), where each value is one whole name taken
// exactly as sent, or the original comma-joined single value
// (`runs=a,b`), which is split and trimmed.
//
// A single repeated key wins outright and the comma-joined one is then
// ignored; merging them would let a stale `runs=` in the same URL silently
// widen the selection a caller narrowed with `run=`.
//
// The comma-joined spelling cannot be made correct, only kept for links that
// already exist. Nothing stops a comma in a run name -- ingest validation
// (validateIngestName) rejects control characters and anything over 256 bytes,
// and a sweep names its runs after the parameters it varies, so "lr=0.1,bs=32"
// is the normal case rather than a contrived one. Split, it selects two runs
// that do not exist, and the chart comes back empty; worse, if a fragment
// happens to match a *different* real run, the chart quietly draws a line for
// a run nobody selected.
func selectedNames(q url.Values, repeatedKey, csvKey string) []string {
	if repeated := q[repeatedKey]; len(repeated) > 0 {
		out := make([]string, 0, len(repeated))
		for _, v := range repeated {
			// Only an empty value is dropped: every other string is a name
			// the caller means literally, commas and spaces included.
			if v != "" {
				out = append(out, v)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return splitCSV(q.Get(csvKey))
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ------------------------------------------------------------ live ingestion

type ingestPoint struct {
	Step      int64              `json:"step"`
	Timestamp string             `json:"timestamp"`
	Metrics   map[string]float64 `json:"metrics"`
}

const (
	// maxIngestPoints bounds one live metric batch. A client logging more than
	// this in a single call should split it; the cap stops one request from
	// becoming an unbounded COPY into exp_points.
	maxIngestPoints = 10000
	// maxIngestNameBytes bounds a run, project or metric name. The columns
	// behind them are unconstrained TEXT, so the ceiling lives here.
	maxIngestNameBytes = 256
	// maxIngestKeys bounds how many distinct metric names one run may carry,
	// since every key is kept in the run's metric_keys array forever. It is a
	// lifetime cap on the run, not a per-request one: the check merges the
	// batch's names with the ones already stored, so the limit cannot be
	// walked past a batch at a time.
	maxIngestKeys = 1000
)

// validateIngestName rejects names that would otherwise go straight into an
// unbounded TEXT column: empty, oversized, or carrying control characters that
// render badly everywhere the value is later displayed.
func validateIngestName(name string) error {
	switch {
	case name == "":
		return errors.New("is required")
	case len(name) > maxIngestNameBytes:
		return fmt.Errorf("must be at most %d bytes", maxIngestNameBytes)
	case !utf8.ValidString(name):
		return errors.New("must be valid UTF-8")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// validateIngestMetricName is validateIngestName plus the one rule only a
// metric has to obey: it must not be named after a column the metrics parquet
// uses to describe the row rather than to hold a measurement.
//
// A point's structural columns and its metrics are written into the same row
// at flush time (experiments.mergePoints), so a metric called "run_name" would
// land in the file's own required-string run_name column: the run would come
// back from the next re-index named "0.5", losing every point it logged in
// that window to a run that never existed. "step" would do the same to a
// chart's x axis, and "_ingest_id" would break the key that keeps a flush
// retried after a crash from writing the same points twice.
func validateIngestMetricName(name string) error {
	if err := validateIngestName(name); err != nil {
		return err
	}
	if experiments.IsStructuralColumn(name) {
		return errors.New("is reserved: it names a structural column of the metrics parquet")
	}
	return nil
}

// validateIngestProject is validateIngestName plus the check that the project
// can actually become a directory in the repository. The flush writes
// "{project}/metrics.parquet" (experiments.Flusher.metricsPath), and Commit
// refuses a ".", ".." or ".git" segment -- so a project named ".git" would be
// accepted here, buffered in exp_points, and then fail every flush forever.
// This is the same guard safeRunArtifactDir applies to an artifact path, moved
// to the point where the name first enters the system.
func validateIngestProject(project string) error {
	if err := validateIngestName(project); err != nil {
		return err
	}
	if err := gitrepo.ValidatePath(project + "/metrics.parquet"); err != nil {
		return errors.New("must be usable as a directory name in the repository")
	}
	return nil
}

// optionalIngestName validates a name the client may omit and reports it as
// "set this" (non-nil) or "leave it alone" (nil).
//
// An empty string is "leave it alone" rather than "clear it": the grouping is
// declared once at init() and repeated on every batch by the shim, so a
// client that stops sending it -- an older shim, a hand-rolled poster -- must
// not silently pull a run out of its group. Clearing a group is a re-run
// without one, which means a new run.
func optionalIngestName(raw string) (*string, error) {
	if raw == "" {
		return nil, nil
	}
	if err := validateIngestName(raw); err != nil {
		return nil, err
	}
	return &raw, nil
}

// validRunStatus reports whether s is a run lifecycle state the API contract
// allows (apitypes.RunStatus). Ingest must not store anything else: the UI and
// the generated TS types treat the column as this closed union.
func validRunStatus(s string) bool {
	switch apitypes.RunStatus(s) {
	case apitypes.RunStatusRunning, apitypes.RunStatusFinished, apitypes.RunStatusFailed:
		return true
	}
	return false
}

func (s *Server) handleExperimentLog(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, "dataset", chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "repo")), redirectUI)
	if !ok {
		return
	}
	var req struct {
		Run    string         `json:"run"`
		Status string         `json:"status"`
		Config map[string]any `json:"config"`
		Points []ingestPoint  `json:"points"`
		// Group and JobType are the sweep grouping (`init(group=...,
		// job_type=...)`). Both are optional and an omitted -- or empty --
		// value keeps whatever the run already declared, so a client that
		// only sends them on the first batch loses nothing.
		Group   string `json:"group"`
		JobType string `json:"job_type"`
	}
	if !decodeJSON(w, r, maxIngestBody, &req, "request body must be JSON with run and points") {
		return
	}
	if err := validateIngestName(req.Run); err != nil {
		badRequest(w, "run "+err.Error())
		return
	}
	group, err := optionalIngestName(req.Group)
	if err != nil {
		badRequest(w, "group "+err.Error())
		return
	}
	jobType, err := optionalIngestName(req.JobType)
	if err != nil {
		badRequest(w, "job_type "+err.Error())
		return
	}
	if len(req.Points) > maxIngestPoints {
		badRequest(w, fmt.Sprintf("a batch may carry at most %d points", maxIngestPoints))
		return
	}

	project := chi.URLParam(r, "project")
	if err := validateIngestProject(project); err != nil {
		badRequest(w, "project "+err.Error())
		return
	}

	var lastStep int64
	keySet := map[string]bool{}
	// batchSummary is the last value this batch carried for each metric. It is
	// merged onto the stored summary below rather than replacing it.
	batchSummary := map[string]any{}
	points := make([]store.MetricPoint, 0, len(req.Points))
	for _, p := range req.Points {
		ts := time.Now()
		if p.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, p.Timestamp); err == nil {
				ts = parsed
			}
		}
		if p.Step > lastStep {
			lastStep = p.Step
		}
		for k, v := range p.Metrics {
			if err := validateIngestMetricName(k); err != nil {
				badRequest(w, "metric name "+strconv.Quote(k)+" "+err.Error())
				return
			}
			keySet[k] = true
			batchSummary[k] = v
		}
		points = append(points, store.MetricPoint{Step: p.Step, TS: ts, Metrics: p.Metrics})
	}

	status := req.Status
	if status == "" {
		status = "running"
	}
	if !validRunStatus(status) {
		badRequest(w, `status must be "running", "finished" or "failed"`)
		return
	}
	ctx := r.Context()

	// Existing metric keys *and* summary values must survive: this batch may
	// not carry every metric the run logs. A batch of only "loss" must not
	// drop the "accuracy" the previous batch recorded, and a batch with no
	// points at all (a status ping) must not empty the summary -- the stored
	// column is replaced wholesale by the statement, so the merge happens
	// here, exactly as it does for metric_keys.
	// The previous status is also captured here so a webhook fires only on
	// the transition into finished/failed, not on every batch a long-running
	// run logs at that terminal status.
	//
	// This read happens *before* the project is upserted, and deliberately:
	// the merged key set is what maxIngestKeys is checked against below, and
	// a batch rejected there must leave no empty project row behind, exactly
	// like a batch rejected for a bad name or status.
	prevStatus := ""
	var storedKeys []string
	summary := map[string]any{}
	if proj, err := s.store.GetExpProject(ctx, repo.ID, project); err == nil {
		if existing, err := s.store.GetExpRun(ctx, proj.ID, req.Run); err == nil {
			prevStatus = existing.Status
			storedKeys = existing.MetricKeys
			for k, v := range existing.Summary {
				summary[k] = v
			}
		}
	}
	for k, v := range batchSummary {
		summary[k] = v
	}

	// maxIngestKeys is a ceiling on the run, not on the request: every key
	// this batch introduces is written into metric_keys and stays there for
	// the life of the run, so ten batches of 500 new names have to be refused
	// exactly like one batch of 5000. Checking the batch alone let a client
	// walk straight past the limit one batch at a time, growing the array
	// without bound. The merged set is therefore what is measured -- the
	// number of distinct names the run would carry once this batch is
	// stored.
	for _, k := range storedKeys {
		keySet[k] = true
	}
	// Names already on the run are not "new", so report the two halves
	// separately: a client that sees only the total cannot tell whether it
	// sent too much or the run was already full.
	added := len(keySet) - len(storedKeys)
	// What is refused is *growth* past the ceiling, not every write to a run
	// that is already past it. A run that grew beyond the cap while the check
	// was still per-batch would otherwise become unwritable on upgrade --
	// including the status ping that marks it finished, which carries no
	// metric names at all and cannot make anything worse.
	if added > 0 && len(keySet) > maxIngestKeys {
		badRequest(w, fmt.Sprintf(
			"a run may carry at most %d distinct metrics; run %q already has %d and this batch adds %d more (%d in total)",
			maxIngestKeys, req.Run, len(storedKeys), added, len(keySet)))
		return
	}

	var keys []string
	for k := range keySet {
		keys = append(keys, k)
	}

	// Nothing is written until every name, status and limit has been checked,
	// so a rejected batch never leaves an empty project row behind.
	projectID, err := s.store.UpsertExpProject(ctx, repo.ID, project)
	if err != nil {
		internalError(w, "upsert experiment project", err)
		return
	}
	// Nothing known either way: keep whatever is stored rather than writing an
	// empty object, so a status ping against a run this read could not see
	// still cannot clear it.
	var summaryArg map[string]any
	if len(summary) > 0 {
		summaryArg = summary
	}

	now := time.Now()
	runID, err := s.store.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
		Name: req.Run, Status: status, Config: req.Config, Summary: summaryArg,
		MetricKeys: keys, LastStep: lastStep, StartedAt: &now,
		Group: group, JobType: jobType,
	})
	if err != nil {
		internalError(w, "upsert experiment run", err)
		return
	}
	if err := s.store.InsertPoints(ctx, runID, points); err != nil {
		internalError(w, "insert metric points", err)
		return
	}
	total, _ := s.store.CountPoints(ctx, runID)
	if _, err := s.store.UpsertExpRun(ctx, projectID, req.Run, status, nil, nil, nil, lastStep, total, nil); err != nil {
		internalError(w, "update run counters", err)
		return
	}
	s.fireRunStatusWebhook(ctx, repo, project, req.Run, prevStatus, status)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": req.Run, "accepted": len(points)})
}

// fireRunStatusWebhook notifies run.finished/run.failed exactly on the
// transition into that terminal status, so a run that keeps logging at
// "finished" (or a retried finish call) does not spam a new delivery every
// time.
func (s *Server) fireRunStatusWebhook(ctx context.Context, repo *store.Repo, project, run, prevStatus, status string) {
	if status == prevStatus {
		return
	}
	var event apitypes.WebhookEvent
	switch apitypes.RunStatus(status) {
	case apitypes.RunStatusFinished:
		event = apitypes.WebhookEventRunFinished
	case apitypes.RunStatusFailed:
		event = apitypes.WebhookEventRunFailed
	default:
		return
	}
	s.fireWebhook(ctx, string(event), repo.Namespace, &repo.ID, map[string]any{
		"namespace": repo.Namespace, "repo": repo.Name, "full_name": repo.FullName(),
		"project": project, "run": run, "status": status,
	})
}

func (s *Server) handleExperimentFinish(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, "dataset", chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "repo")), redirectUI)
	if !ok {
		return
	}
	var req struct {
		Run    string `json:"run"`
		Status string `json:"status"`
		// The same optional sweep grouping the log endpoint takes. It is
		// accepted here too because a run that logged no points at all is
		// created by this call, and it should still land in its group.
		Group   string `json:"group"`
		JobType string `json:"job_type"`
	}
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with a run name") {
		return
	}
	if err := validateIngestName(req.Run); err != nil {
		badRequest(w, "run "+err.Error())
		return
	}
	group, err := optionalIngestName(req.Group)
	if err != nil {
		badRequest(w, "group "+err.Error())
		return
	}
	jobType, err := optionalIngestName(req.JobType)
	if err != nil {
		badRequest(w, "job_type "+err.Error())
		return
	}
	project := chi.URLParam(r, "project")
	if err := validateIngestProject(project); err != nil {
		badRequest(w, "project "+err.Error())
		return
	}
	status := req.Status
	if status == "" {
		status = "finished"
	}
	if !validRunStatus(status) {
		badRequest(w, `status must be "running", "finished" or "failed"`)
		return
	}
	ctx := r.Context()
	projectID, err := s.store.UpsertExpProject(ctx, repo.ID, project)
	if err != nil {
		internalError(w, "upsert experiment project", err)
		return
	}
	prevStatus := ""
	if existing, err := s.store.GetExpRun(ctx, projectID, req.Run); err == nil {
		prevStatus = existing.Status
	}
	if _, err := s.store.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
		Name: req.Run, Status: status, Group: group, JobType: jobType,
	}); err != nil {
		internalError(w, "update run status", err)
		return
	}
	s.fireRunStatusWebhook(ctx, repo, project, req.Run, prevStatus, status)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": req.Run, "status": status})
}
