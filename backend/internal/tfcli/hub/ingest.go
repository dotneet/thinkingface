package hub

// The write side of the experiment API that `tf experiments import` and
// `tf experiments sync` drive: the two live-ingest endpoints the Python shim
// posts to (POST .../log, POST .../finish), deleting a run, reading the runs a
// project already has (name collisions, --replace, resume rules), recording a
// run's produced models, and committing a run's artifacts
// (docs/dev/agent-features.md §2.9, §4; api-contract.md §7).
//
// Every method is prefixed Ingest (or names artifacts explicitly) so it never
// collides with the read-side client in experiments.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// IngestMaxPoints is the server's cap on the points one POST .../log may carry
// (maxIngestPoints in internal/api/experiments_ingest.go).
const IngestMaxPoints = 10000

// IngestMaxMetricKeys is the server's lifetime cap on distinct metric names per
// run (maxIngestKeys).
const IngestMaxMetricKeys = 1000

// IngestMaxNameBytes bounds a run, project, group or metric name
// (maxIngestNameBytes).
const IngestMaxNameBytes = 256

// ArtifactsRev is the branch run artifacts are committed to.
const ArtifactsRev = "main"

// IngestPoint is one metric point on the wire. Timestamp is RFC 3339 or ""
// (the server then stamps the time it received the point).
type IngestPoint struct {
	Step      int64              `json:"step"`
	Timestamp string             `json:"timestamp,omitempty"`
	Metrics   map[string]float64 `json:"metrics"`
}

// IngestLogRequest is the body of POST .../log. Status "" means "running" to
// the server; Group / JobType "" leave whatever the run already declared.
// Config nil leaves the stored config alone.
type IngestLogRequest struct {
	Run     string         `json:"run"`
	Status  string         `json:"status,omitempty"`
	Config  map[string]any `json:"config,omitempty"`
	Group   string         `json:"group,omitempty"`
	JobType string         `json:"job_type,omitempty"`
	Points  []IngestPoint  `json:"points"`
}

// IngestFinishRequest is the body of POST .../finish. Status "" means
// "finished" to the server.
type IngestFinishRequest struct {
	Run     string `json:"run"`
	Status  string `json:"status,omitempty"`
	Group   string `json:"group,omitempty"`
	JobType string `json:"job_type,omitempty"`
}

// ingestProjectURL is {endpoint}/api/v1/experiments/{ns}/{name}/{project},
// every segment path-escaped (a project may contain "/", spaces, "%", or
// non-ASCII; url.PathEscape turns "/" into %2F, which the server routes on).
func (c *Client) ingestProjectURL(ref Ref, project string) string {
	return c.endpoint + "/api/v1/experiments/" + url.PathEscape(ref.Namespace) + "/" +
		url.PathEscape(ref.Name) + "/" + url.PathEscape(project)
}

// ingestRunURL is ingestProjectURL + /runs/{run}.
func (c *Client) ingestRunURL(ref Ref, project, run string) string {
	return c.ingestProjectURL(ref, project) + "/runs/" + url.PathEscape(run)
}

// IngestLog posts one batch of points (POST .../log). The caller keeps
// len(req.Points) <= IngestMaxPoints. It returns how many points the server
// accepted.
func (c *Client) IngestLog(ctx context.Context, ref Ref, project string, req IngestLogRequest) (int, error) {
	if len(req.Points) > IngestMaxPoints {
		return 0, fmt.Errorf("ingest: a batch may carry at most %d points, got %d", IngestMaxPoints, len(req.Points))
	}
	if req.Points == nil {
		// The server decodes a missing "points" as none, but say so explicitly:
		// an empty batch is a legitimate config-only or liveness call.
		req.Points = []IngestPoint{}
	}
	var resp struct {
		Accepted int `json:"accepted"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.ingestProjectURL(ref, project)+"/log", req, &resp); err != nil {
		return 0, err
	}
	return resp.Accepted, nil
}

// IngestFinish marks a run finished or failed (POST .../finish). It creates
// the run when it logged no points at all.
func (c *Client) IngestFinish(ctx context.Context, ref Ref, project string, req IngestFinishRequest) error {
	return c.doJSON(ctx, http.MethodPost, c.ingestProjectURL(ref, project)+"/finish", req, nil)
}

// IngestDeleteRun deletes a run and its points (DELETE .../runs/{run}). A 404
// (no such project or run) is returned as-is; see IsNotFound.
func (c *Client) IngestDeleteRun(ctx context.Context, ref Ref, project, run string) error {
	return c.doJSON(ctx, http.MethodDelete, c.ingestRunURL(ref, project, run), nil, nil)
}

// IngestGetRun reads one run (GET .../runs/{run}, no long-poll). A run (or
// repository) that does not exist yields (nil, nil).
func (c *Client) IngestGetRun(ctx context.Context, ref Ref, project, run string) (*apitypes.ExpRun, error) {
	var resp apitypes.ExpRunResponse
	if err := c.doJSON(ctx, http.MethodGet, c.ingestRunURL(ref, project, run), nil, &resp); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &resp.Run, nil
}

// IngestListRuns lists every run of a project (GET .../runs with no filter).
// A project that does not exist yet answers an empty list.
func (c *Client) IngestListRuns(ctx context.Context, ref Ref, project string) ([]apitypes.ExpRun, error) {
	var resp apitypes.ExpRunListResponse
	if err := c.doJSON(ctx, http.MethodGet, c.ingestProjectURL(ref, project)+"/runs", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Runs, nil
}

// IngestListRunNames is IngestListRuns reduced to the set of names in use.
func (c *Client) IngestListRunNames(ctx context.Context, ref Ref, project string) (map[string]bool, error) {
	runs, err := c.IngestListRuns(ctx, ref, project)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(runs))
	for _, r := range runs {
		names[r.Name] = true
	}
	return names, nil
}

// IngestSetRunModels replaces a run's produced-model list (PATCH
// .../runs/{run} with {"models": [...]}), the write path behind
// trackio.log_model. The run must already exist (post IngestFinish first).
func (c *Client) IngestSetRunModels(ctx context.Context, ref Ref, project, run string, models []apitypes.ExpRunModelInput) error {
	if models == nil {
		models = []apitypes.ExpRunModelInput{}
	}
	body := apitypes.ExpRunAnnotationRequest{Models: &models}
	return c.doJSON(ctx, http.MethodPatch, c.ingestRunURL(ref, project, run), body, nil)
}

// RunArtifactPath is where one artifact of a run lands inside the experiment
// dataset repository: {project}/artifacts/{run}/{name} (the same layout the
// Python shim's _artifacts.artifact_path uses).
func RunArtifactPath(project, run, name string) string {
	return project + "/artifacts/" + run + "/" + name
}

// RunArtifact is one file to commit as a run artifact. Name is the in-run
// artifact name (forward slashes, already validated by the caller).
type RunArtifact struct {
	Name string
	File LocalFile // RepoPath is ignored; it is derived from project/run/Name
}

// RunArtifactsResult reports what UploadRunArtifacts did.
type RunArtifactsResult struct {
	Commit   *CommitResult // nil when nothing had to be committed
	Uploaded []string      // repository paths committed
	Skipped  []string      // repository paths already present with identical content
}

// UploadRunArtifacts commits a run's artifacts to the default branch in one
// commit, through the same preupload / LFS / commit machinery as `tf up`.
//
// Whether a path is already up to date is decided by content, never by size:
// Upload hashes each file (git blob sha1, or the LFS sha256 for a path routed
// through LFS) and drops the ones whose hash matches the remote tree entry.
// Two checkpoints of the same model are routinely the same size, so a size
// match says nothing about whether the new version is on the server. The
// content check is also what keeps a retry idempotent: a crash between the
// commit and the caller recording it finds every file unchanged and commits
// nothing. Nothing to commit is not an error.
func (c *Client) UploadRunArtifacts(ctx context.Context, ref Ref, project, run string, artifacts []RunArtifact, report func(Event)) (*RunArtifactsResult, error) {
	res := &RunArtifactsResult{}
	var files []LocalFile
	seen := make(map[string]bool, len(artifacts))
	for _, a := range artifacts {
		p := RunArtifactPath(project, run, a.Name)
		if seen[p] {
			continue // a later record re-staging the same name: commit it once
		}
		seen[p] = true
		f := a.File
		f.RepoPath = p
		files = append(files, f)
	}
	if len(files) == 0 {
		return res, nil
	}

	plan := Plan{
		Ref:     ref,
		Rev:     ArtifactsRev,
		Files:   files,
		Summary: fmt.Sprintf("chore(trackio): artifacts for %s/%s", project, run),
	}
	result, err := Upload(ctx, c, plan, report)
	if errors.Is(err, ErrNothingToDo) {
		if result != nil {
			res.Skipped = append(res.Skipped, result.Unchanged...)
		}
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	res.Commit = result.Commit
	res.Uploaded = append(res.Uploaded, result.Regular...)
	res.Uploaded = append(res.Uploaded, result.LFS...)
	res.Skipped = append(res.Skipped, result.Unchanged...)
	return res, nil
}
