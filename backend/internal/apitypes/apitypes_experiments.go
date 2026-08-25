package apitypes

import "time"

// ------------------------------------------------------------- experiments

// ExpProjectListItem is one experiment repository in the global listing.
type ExpProjectListItem struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	FullName    string    `json:"full_name"`
	NumProjects int       `json:"num_projects"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ExpProjectListResponse is the body of GET /api/v1/experiments.
type ExpProjectListResponse struct {
	Items []ExpProjectListItem `json:"items"`
	// Total is the number of matching repositories regardless of limit /
	// offset (docs/dev/namespace-design.md §5.6).
	Total int64 `json:"total"`
}

// ExpProject is one project (a group of runs) inside an experiment repository.
type ExpProject struct {
	Name      string    `json:"name"`
	NumRuns   int       `json:"num_runs"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExpRepoResponse is an experiment repository together with its projects.
type ExpRepoResponse struct {
	Repo     RepoSummary  `json:"repo"`
	Projects []ExpProject `json:"projects"`
}

// ExpRun is one training run's summary row.
type ExpRun struct {
	Name   string    `json:"name"`
	Status RunStatus `json:"status"`
	// LastStep is the highest step seen for this run.
	LastStep int64 `json:"last_step"`
	// NumPoints is how many metric points have been recorded.
	NumPoints int64 `json:"num_points"`
	// StartedAt is null for a run recovered from an export that carried no
	// start time.
	StartedAt *time.Time `json:"started_at" tstype:"string | null,required"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Config is the run's hyperparameters, as logged.
	Config     map[string]any `json:"config"`
	MetricKeys []string       `json:"metric_keys"`
	// Summary holds the last value seen for each metric.
	Summary map[string]float64 `json:"summary"`
	// Group is the sweep this run belongs to, as `trackio.init(group=...)`
	// declared it, and JobType the role it played in that sweep
	// (`job_type=...`). Both are "" for a run that declared neither, which is
	// how the run table tells a sweep member from a standalone run.
	Group   string `json:"group"`
	JobType string `json:"job_type"`
	// Tags are free-form labels a user attached to the run.
	Tags []string `json:"tags"`
	// Archived hides the run from the default listing without deleting it.
	Archived bool `json:"archived"`
	// IsBaseline marks the run every other run is compared against. At most one
	// run per project carries it.
	IsBaseline bool `json:"is_baseline"`
	// Note is free-form Markdown a user wrote about the run. Like the other
	// annotations it is never written by ingest or by the parquet indexer, so
	// re-indexing the project leaves it in place.
	Note string `json:"note"`
	// Models are the model repositories this run declared it produced
	// (`trackio.log_model`). Another annotation: ingest and the indexer leave
	// it alone.
	Models []ExpRunModelRef `json:"models"`
}

// ExpRunModelRef is one model a run recorded as its output.
type ExpRunModelRef struct {
	// RepoID is the model repository as "ns/name".
	RepoID string `json:"repo_id"`
	// Revision is the commit, branch or tag the run pinned, "" when the shim
	// could not resolve one. It is recorded verbatim and never verified: only
	// the repository's existence is checked (see Exists), so a link to a
	// revision that has since been rewritten fails in the file browser rather
	// than being hidden here.
	Revision string `json:"revision"`
	// Exists reports that RepoID resolves to a model repository this viewer
	// may read. A false value means the UI shows text and a warning instead of
	// a link -- the same treatment a dangling lineage reference gets.
	Exists bool `json:"exists"`
}

// ExpRunModelInput is one entry of a produced-model list being written. Unlike
// ExpRunModelRef it carries no Exists: that is the server's answer, not the
// client's claim.
type ExpRunModelInput struct {
	RepoID   string `json:"repo_id"`
	Revision string `json:"revision,omitempty" tstype:"string"`
}

// ExpRunListResponse is the body of the run listing endpoint.
type ExpRunListResponse struct {
	Runs []ExpRun `json:"runs"`
}

// ExpArtifact is one file a run stored under its artifact directory.
type ExpArtifact struct {
	// Name is the path relative to the run's artifact directory -- the name
	// `log_artifact` was given, possibly with subdirectories.
	Name string `json:"name"`
	// Path is the full path inside the repository, which is what the file
	// browser and `resolve` need.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// LFS reports that the file is stored as a Git LFS pointer.
	LFS bool `json:"lfs"`
	// Preview is how the file browser would render this file, so the run page
	// can pick a matching icon and link.
	Preview PreviewKind `json:"preview"`
}

// ExpArtifactListResponse lists one run's artifacts.
type ExpArtifactListResponse struct {
	// Path is the directory the listing came from,
	// "{project}/artifacts/{run}" (docs/dev/api-contract.md §7).
	Path string `json:"path"`
	// Rev is the revision listed, always the repository's default branch.
	Rev       string        `json:"rev"`
	Artifacts []ExpArtifact `json:"artifacts"`
}

// ExpRunAnnotationRequest is a partial update of a run's annotations: an
// omitted field is left as it is, so a client can toggle one flag without
// having to send the rest.
//
// For the two list fields, Tags and Models, "omitted" and "empty" are
// different requests and JSON spells them differently: a missing key or an
// explicit null leaves the list unchanged, while [] replaces it with nothing
// -- which is the only way to clear one. Sending null to clear a list is the
// mistake this note exists to prevent.
type ExpRunAnnotationRequest struct {
	// Tags replaces the run's tag list wholesale; an empty array clears it.
	Tags       *[]string `json:"tags,omitempty" tstype:"string[]"`
	Archived   *bool     `json:"archived,omitempty" tstype:"boolean"`
	IsBaseline *bool     `json:"is_baseline,omitempty" tstype:"boolean"`
	Note       *string   `json:"note,omitempty" tstype:"string"`
	// Models replaces the run's produced-model list wholesale; an empty array
	// clears it. This is the write path behind `trackio.log_model`.
	Models *[]ExpRunModelInput `json:"models,omitempty" tstype:"ExpRunModelInput[]"`
}

// ExpRunAnnotationResponse returns the run as it stands after the update.
type ExpRunAnnotationResponse struct {
	Run ExpRun `json:"run"`
}

// ExpMetricSeries is one metric's trace for one run, as [x, y] pairs.
type ExpMetricSeries struct {
	Run    string       `json:"run"`
	Key    string       `json:"key"`
	Points [][2]float64 `json:"points" tstype:"MetricPoint[]"`
}

// ExpMetricsResponse carries the traces a chart asked for.
type ExpMetricsResponse struct {
	Series []ExpMetricSeries `json:"series"`
}
