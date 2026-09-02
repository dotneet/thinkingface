// Live metric ingestion: the two endpoints the trackio-compatible shim posts
// to while a training run is in flight (POST .../log and POST .../finish), and
// the validation both of them share.
//
// This is route B of docs/dev/thinkingface-design.md §8: the rows written here
// are a *buffer*, not the record. The source of truth stays the parquet inside
// the dataset repository, which the sync worker flushes these points into --
// so everything below is shaped by what the flush will have to write, which is
// why a project name has to be usable as a directory and a metric name may not
// collide with the parquet's own structural columns.
//
// The wire shapes are the contract: clients/python's trackio tests pin them,
// so a field renamed here is a client broken there.

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

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
	// batch's names with the ones already stored (mergeRunState), so the limit
	// cannot be walked past a batch at a time.
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

// ingestIdentity is everything both ingest endpoints must agree on before a
// single row is written: which run, in which project, at what status, and the
// optional sweep grouping.
type ingestIdentity struct {
	run     string
	project string
	status  string
	group   *string
	jobType *string
}

// parseIngestIdentity validates the run name, the sweep grouping, the project
// segment and the status of an ingest request, answering the request itself
// on the first thing that is wrong.
//
// Both /log and /finish go through it, because a check added to only one of
// them is not a check at all: the two endpoints write the same exp_runs row
// through the same upsert, so a name /log refuses would simply be created
// through /finish instead -- and /finish is *the* call that creates a run
// which logged no points. The two had drifted into twenty-four duplicated
// lines apiece, which is exactly how that gap opens without anyone deciding
// to open it.
//
// defaultStatus is the only thing they legitimately disagree about:
// "running" for a batch of points, "finished" for the call that ends a run.
func parseIngestIdentity(w http.ResponseWriter, r *http.Request,
	run, group, jobType, status, defaultStatus string,
) (ingestIdentity, bool) {
	if err := validateIngestName(run); err != nil {
		badRequest(w, "run "+err.Error())
		return ingestIdentity{}, false
	}
	groupPtr, err := optionalIngestName(group)
	if err != nil {
		badRequest(w, "group "+err.Error())
		return ingestIdentity{}, false
	}
	jobTypePtr, err := optionalIngestName(jobType)
	if err != nil {
		badRequest(w, "job_type "+err.Error())
		return ingestIdentity{}, false
	}
	project := chi.URLParam(r, "project")
	if err := validateIngestProject(project); err != nil {
		badRequest(w, "project "+err.Error())
		return ingestIdentity{}, false
	}
	if status == "" {
		status = defaultStatus
	}
	if !validRunStatus(status) {
		badRequest(w, `status must be "running", "finished" or "failed"`)
		return ingestIdentity{}, false
	}
	return ingestIdentity{run: run, project: project, status: status, group: groupPtr, jobType: jobTypePtr}, true
}

// runBatch is what one batch of points says about the run it belongs to,
// beyond the points themselves.
type runBatch struct {
	points   []store.MetricPoint
	lastStep int64
	// keys is the set of metric names this batch mentions, which is merged
	// onto the run's metric_keys rather than replacing it.
	keys map[string]bool
	// summary is the last value the batch carried for each metric, merged
	// onto the stored summary for the same reason.
	summary map[string]any
}

// buildRunPoints turns the wire points of one batch into store rows,
// validating every metric name on the way. It answers the request itself on a
// bad name and reports ok=false; nothing has been written at that point, which
// is the whole reason the validation happens here rather than at flush time.
func buildRunPoints(w http.ResponseWriter, raw []ingestPoint) (runBatch, bool) {
	batch := runBatch{
		points:  make([]store.MetricPoint, 0, len(raw)),
		keys:    map[string]bool{},
		summary: map[string]any{},
	}
	for _, p := range raw {
		ts := time.Now()
		if p.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, p.Timestamp); err == nil {
				ts = parsed
			}
		}
		if p.Step > batch.lastStep {
			batch.lastStep = p.Step
		}
		for k, v := range p.Metrics {
			if err := validateIngestMetricName(k); err != nil {
				badRequest(w, "metric name "+strconv.Quote(k)+" "+err.Error())
				return runBatch{}, false
			}
			batch.keys[k] = true
			batch.summary[k] = v
		}
		batch.points = append(batch.points, store.MetricPoint{Step: p.Step, TS: ts, Metrics: p.Metrics})
	}
	// The metric-name cap is deliberately not checked here: it is a ceiling on
	// the run rather than on the request, so it needs the names already stored
	// as well as these. mergeRunState applies it.
	return batch, true
}

// runState is what the store already holds for a run: the zero value when the
// project, or the run inside it, has never been written.
type runState struct {
	keys    []string
	summary map[string]any
	status  string
}

// loadRunState reads a run's stored state, creating nothing on the way: the
// project is looked up rather than upserted.
//
// That is what lets mergeRunState refuse a batch below without leaving an
// empty project row behind, exactly like a batch refused for a bad name or an
// unknown status.
func (s *Server) loadRunState(ctx context.Context, repoID int64, project, run string) runState {
	proj, err := s.store.GetExpProject(ctx, repoID, project)
	if err != nil {
		return runState{}
	}
	existing, err := s.store.GetExpRun(ctx, proj.ID, run)
	if err != nil {
		return runState{}
	}
	return runState{keys: existing.MetricKeys, summary: existing.Summary, status: existing.Status}
}

// mergeRunState folds a batch onto whatever the run already holds, and applies
// the one limit that can only be checked once the two are together. It answers
// the request itself and reports ok=false when the batch is refused.
//
// Existing metric keys *and* summary values must survive: this batch may not
// carry every metric the run logs. A batch of only "loss" must not drop the
// "accuracy" a previous batch recorded, and a batch with no points at all (a
// status ping) must not empty the summary -- the stored column is replaced
// wholesale by the upsert, so the merge has to happen here.
//
// summary comes back nil when nothing is known either way, so a status ping
// against a run this read could not see keeps whatever is stored instead of
// writing an empty object over it.
//
// maxIngestKeys is a ceiling on the run, not on the request: every key a batch
// introduces is written into metric_keys and stays there for the life of the
// run, so ten batches of 500 new names have to be refused exactly like one
// batch of 5000. Checking the batch alone let a client walk straight past the
// limit one batch at a time, growing the array without bound. What is measured
// is therefore the merged set -- the number of distinct names the run would
// carry once this batch is stored -- and what is refused is *growth* past the
// ceiling rather than every write to a run that is already past it. A run that
// grew beyond the cap while the check was still per-batch would otherwise
// become unwritable on upgrade, including the status ping that marks it
// finished, which carries no metric names at all and cannot make anything
// worse.
func mergeRunState(w http.ResponseWriter, run string, stored runState, batch runBatch) (keys []string, summary map[string]any, ok bool) {
	keySet := batch.keys
	for _, k := range stored.keys {
		keySet[k] = true
	}
	// Names already on the run are not "new", so report the two halves
	// separately: a client that sees only the total cannot tell whether it
	// sent too much or the run was already full.
	added := len(keySet) - len(stored.keys)
	if added > 0 && len(keySet) > maxIngestKeys {
		badRequest(w, fmt.Sprintf(
			"a run may carry at most %d distinct metrics; run %q already has %d and this batch adds %d more (%d in total)",
			maxIngestKeys, run, len(stored.keys), added, len(keySet)))
		return nil, nil, false
	}
	for k := range keySet {
		keys = append(keys, k)
	}
	merged := map[string]any{}
	for k, v := range stored.summary {
		merged[k] = v
	}
	for k, v := range batch.summary {
		merged[k] = v
	}
	if len(merged) == 0 {
		return keys, nil, true
	}
	return keys, merged, true
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
	id, ok := parseIngestIdentity(w, r, req.Run, req.Group, req.JobType, req.Status, "running")
	if !ok {
		return
	}
	if len(req.Points) > maxIngestPoints {
		badRequest(w, fmt.Sprintf("a batch may carry at most %d points", maxIngestPoints))
		return
	}
	batch, ok := buildRunPoints(w, req.Points)
	if !ok {
		return
	}

	ctx := r.Context()
	// The run is read before the project is upserted, and deliberately: the
	// merged key set is what maxIngestKeys is checked against, and a batch
	// refused there must leave no empty project row behind. The status it had
	// beforehand comes from the same read, so a webhook fires only on the
	// transition into finished/failed rather than on every batch a
	// long-running run logs at that terminal status. A run this read cannot
	// find has prevStatus "", which is not a valid status, so the first write
	// is always a transition.
	stored := s.loadRunState(ctx, repo.ID, id.project, id.run)
	prevStatus := stored.status
	keys, summary, ok := mergeRunState(w, id.run, stored, batch)
	if !ok {
		return
	}

	// Nothing is written until every name, status and limit has been checked,
	// so a rejected batch never leaves an empty project row behind.
	projectID, err := s.store.UpsertExpProject(ctx, repo.ID, id.project)
	if err != nil {
		internalError(w, "upsert experiment project", err)
		return
	}

	now := time.Now()
	runID, err := s.store.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
		Name: id.run, Status: id.status, Config: req.Config, Summary: summary,
		MetricKeys: keys, LastStep: batch.lastStep, StartedAt: &now,
		Group: id.group, JobType: id.jobType,
	})
	if err != nil {
		internalError(w, "upsert experiment run", err)
		return
	}
	if err := s.store.InsertPoints(ctx, runID, batch.points); err != nil {
		internalError(w, "insert metric points", err)
		return
	}
	// A second write of the same row, and it cannot be folded into the first:
	// num_points is a count of what is in exp_points, which needs the run id
	// the first upsert is what produces, and the points inserted against it.
	// So the order is fixed -- upsert to learn the id, insert, count, write
	// the count back.
	//
	// Only the counter columns are set. Everything else is left at its zero
	// value and the upsert keeps what the first write stored, which is why
	// config/summary/metric_keys are absent here rather than repeated: passing
	// them again would be a second chance to write a *different* value for
	// them.
	total, _ := s.store.CountPoints(ctx, runID)
	if _, err := s.store.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
		Name: id.run, Status: id.status, LastStep: batch.lastStep, NumPoints: total,
	}); err != nil {
		internalError(w, "update run counters", err)
		return
	}
	s.fireRunStatusWebhook(ctx, repo, id.project, id.run, prevStatus, id.status)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": id.run, "accepted": len(batch.points)})
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
	id, ok := parseIngestIdentity(w, r, req.Run, req.Group, req.JobType, req.Status, "finished")
	if !ok {
		return
	}
	ctx := r.Context()
	projectID, err := s.store.UpsertExpProject(ctx, repo.ID, id.project)
	if err != nil {
		internalError(w, "upsert experiment project", err)
		return
	}
	prevStatus := ""
	if existing, err := s.store.GetExpRun(ctx, projectID, id.run); err == nil {
		prevStatus = existing.Status
	}
	if _, err := s.store.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
		Name: id.run, Status: id.status, Group: id.group, JobType: id.jobType,
	}); err != nil {
		internalError(w, "update run status", err)
		return
	}
	s.fireRunStatusWebhook(ctx, repo, id.project, id.run, prevStatus, id.status)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run": id.run, "status": id.status})
}
