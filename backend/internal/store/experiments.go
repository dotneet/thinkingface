package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type ExpProject struct {
	ID        int64     `json:"-"`
	RepoID    int64     `json:"-"`
	Name      string    `json:"name"`
	NumRuns   int       `json:"num_runs"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ExpRun struct {
	ID         int64          `json:"-"`
	ProjectID  int64          `json:"-"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Config     map[string]any `json:"config"`
	Summary    map[string]any `json:"summary"`
	MetricKeys []string       `json:"metric_keys"`
	LastStep   int64          `json:"last_step"`
	NumPoints  int64          `json:"num_points"`
	StartedAt  *time.Time     `json:"started_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	// Group is the sweep this run belongs to and JobType the role it played in
	// it, as `trackio.init(group=..., job_type=...)` declared them. Unlike the
	// annotations below they are written by ingest -- but with the same "an
	// omitted value is kept" rule, so a batch that does not repeat them (or a
	// re-index of the parquet, which does not know them) leaves them alone.
	// "" means "not in a group", which is every run logged before this
	// existed.
	Group   string `json:"group"`
	JobType string `json:"job_type"`
	// Tags, Archived, IsBaseline and Note are annotations a user attaches by
	// hand while comparing runs. They are never written by ingest or by the
	// parquet indexer, so an upsert must leave them alone.
	Tags       []string `json:"tags"`
	Archived   bool     `json:"archived"`
	IsBaseline bool     `json:"is_baseline"`
	// Note is the free-form Markdown a person wrote about this run.
	Note string `json:"note"`
}

// ExpRunModel is one model repository a run declared it produced
// (`trackio.log_model`). It is an annotation like Note: written only through
// UpdateExpRunAnnotation, never by ingest or the parquet indexer.
//
// The declaration deliberately does not become a repo_lineage row. That index
// is rebuilt from the repository card on every default-branch push, so an edge
// written from the run side would survive only until the next push to the
// model; the model page reads this table instead (ListModelProducers).
type ExpRunModel struct {
	// Raw is the reference as the script wrote it, e.g. "team/bert-ja@a1b2c3d".
	Raw string `json:"raw"`
	// Namespace and Name are the parsed target. A reference that does not
	// parse is rejected at the API boundary, so these are always set.
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Revision is the commit, branch or tag the run pinned, "" when it could
	// not resolve one.
	Revision string `json:"revision"`
	Ordinal  int    `json:"ordinal"`
	// Exists reports that the target resolves to a model repository. Like a
	// dangling lineage reference, a false value is kept and shown as text
	// rather than dropped: a typo and an unpushed model look the same from
	// here.
	Exists bool `json:"exists"`
}

// FullName is the "ns/name" the API reports as repo_id.
func (m ExpRunModel) FullName() string { return m.Namespace + "/" + m.Name }

// RunAnnotation is a partial update of a run's hand-maintained metadata: a nil
// field means "leave this one as it is", which is what a PATCH body decodes to
// when the key is absent.
type RunAnnotation struct {
	Tags       *[]string
	Archived   *bool
	IsBaseline *bool
	Note       *string
	// Models replaces the run's produced-model list wholesale (an empty slice
	// clears it), the same way Tags does.
	Models *[]ExpRunModel
}

// IsEmpty reports whether the update would change nothing.
func (a RunAnnotation) IsEmpty() bool {
	return a.Tags == nil && a.Archived == nil && a.IsBaseline == nil &&
		a.Note == nil && a.Models == nil
}

type MetricPoint struct {
	Step    int64              `json:"step"`
	TS      time.Time          `json:"timestamp"`
	Metrics map[string]float64 `json:"metrics"`
}

func (s *Store) UpsertExpProject(ctx context.Context, repoID int64, name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO exp_projects (repo_id, name) VALUES ($1, $2)
		 ON CONFLICT (repo_id, name) DO UPDATE SET updated_at = now()
		 RETURNING id`, repoID, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert experiment project: %w", err)
	}
	return id, nil
}

func (s *Store) ListExpProjects(ctx context.Context, repoID int64) ([]ExpProject, error) {
	rows, err := s.db.Query(ctx,
		`SELECT p.id, p.repo_id, p.name, p.updated_at,
		        (SELECT count(*) FROM exp_runs r WHERE r.project_id = p.id)
		 FROM exp_projects p WHERE p.repo_id = $1 ORDER BY p.updated_at DESC, p.name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ExpProject{}
	for rows.Next() {
		var p ExpProject
		if err := rows.Scan(&p.ID, &p.RepoID, &p.Name, &p.UpdatedAt, &p.NumRuns); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetExpProject(ctx context.Context, repoID int64, name string) (*ExpProject, error) {
	p := &ExpProject{}
	err := s.db.QueryRow(ctx,
		`SELECT id, repo_id, name, updated_at FROM exp_projects WHERE repo_id = $1 AND name = $2`,
		repoID, name).Scan(&p.ID, &p.RepoID, &p.Name, &p.UpdatedAt)
	return p, norm(err)
}

// ExpRunUpsert is everything a writer of runs -- the live ingest API or the
// parquet indexer -- knows about one run. Every field follows the same rule:
// the zero value means "leave whatever is stored alone", so an incremental
// batch never wipes information another write already found.
type ExpRunUpsert struct {
	Name string
	// Status is "" to keep the stored one (the indexer relies on this: a run
	// the ingest API owns must not be flipped to "finished" mid-flight).
	Status string
	// Config, Summary and MetricKeys are nil to keep the stored value.
	Config     map[string]any
	Summary    map[string]any
	MetricKeys []string
	// LastStep and NumPoints only ever grow (GREATEST / MAX in the statement),
	// so 0 is "no news".
	LastStep  int64
	NumPoints int64
	// StartedAt is only written once: the first start time wins.
	StartedAt *time.Time
	// Group and JobType are the sweep grouping from `init(group=...,
	// job_type=...)`; nil keeps the stored value, which is what makes them
	// survive both a batch that does not repeat them and a re-index of the
	// project's parquet.
	Group   *string
	JobType *string
}

// UpsertExpRun writes the run summary the UI lists. Passing nil for config,
// summary, or metricKeys leaves the stored value alone, so an incremental
// ingest never wipes information a batch index already found.
//
// It is the positional form of UpsertExpRunWith. The ingest path now builds
// an ExpRunUpsert directly -- a call site that wants three of ten parameters
// reads better naming them than counting nils -- so this remains only for the
// tests that predate the struct form.
func (s *Store) UpsertExpRun(ctx context.Context, projectID int64, name, status string,
	config, summary map[string]any, metricKeys []string, lastStep, numPoints int64, startedAt *time.Time) (int64, error) {

	return s.UpsertExpRunWith(ctx, projectID, ExpRunUpsert{
		Name: name, Status: status, Config: config, Summary: summary, MetricKeys: metricKeys,
		LastStep: lastStep, NumPoints: numPoints, StartedAt: startedAt,
	})
}

// UpsertExpRunWith is UpsertExpRun with every field named, which is what the
// grouping columns need: a positional signature with two more optional
// strings on the end would be unreadable at the call sites that pass neither.
func (s *Store) UpsertExpRunWith(ctx context.Context, projectID int64, u ExpRunUpsert) (int64, error) {
	var configRaw, summaryRaw, keysRaw []byte
	var err error
	if u.Config != nil {
		if configRaw, err = json.Marshal(u.Config); err != nil {
			return 0, err
		}
	}
	if u.Summary != nil {
		if summaryRaw, err = json.Marshal(u.Summary); err != nil {
			return 0, err
		}
	}
	if u.MetricKeys != nil {
		if keysRaw, err = json.Marshal(u.MetricKeys); err != nil {
			return 0, err
		}
	}

	// The statement is per engine (jsonb casts and GREATEST on Postgres,
	// MAX() on SQLite); see dialectQueries.upsertExpRun for the placeholders.
	var id int64
	err = s.db.QueryRow(ctx, s.d.queries().upsertExpRun,
		projectID, u.Name, u.Status, configRaw, summaryRaw, keysRaw,
		u.LastStep, u.NumPoints, u.StartedAt, u.Group, u.JobType).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert experiment run: %w", err)
	}
	_, _ = s.db.Exec(ctx, `UPDATE exp_projects SET updated_at = now() WHERE id = $1`, projectID)
	return id, nil
}

// runColumns is the shared projection behind every run read, so a column added
// to ExpRun cannot be picked up by one query and missed by the next.
const runColumns = `id, project_id, name, status, config, summary, metric_keys,
	last_step, num_points, started_at, updated_at, group_name, job_type,
	tags, archived, is_baseline, note`

func (s *Store) ListExpRuns(ctx context.Context, projectID int64) ([]ExpRun, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+runColumns+`
		 FROM exp_runs WHERE project_id = $1 ORDER BY started_at NULLS LAST, name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ExpRun{}
	for rows.Next() {
		r, err := s.scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) GetExpRun(ctx context.Context, projectID int64, name string) (*ExpRun, error) {
	return s.scanRun(s.db.QueryRow(ctx,
		`SELECT `+runColumns+`
		 FROM exp_runs WHERE project_id = $1 AND name = $2`, projectID, name))
}

// UpdateExpRunAnnotation applies a partial annotation update to one run and
// returns the row as it now stands. Marking a run as the baseline clears the
// flag from its siblings. A partial unique index on (project_id) WHERE
// is_baseline is the invariant; on Postgres the transaction alone is not
// enough, because two concurrent PATCHes of previously-unset runs lock
// distinct rows and can both commit. An advisory lock serializes the switch
// so the second waiter sees the first commit, clears it, and sets its own
// row. (SQLite serialises write transactions outright, so the lock is a
// no-op there.)
func (s *Store) UpdateExpRunAnnotation(ctx context.Context, projectID int64, name string, upd RunAnnotation) (*ExpRun, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if upd.IsBaseline != nil && *upd.IsBaseline {
		if err := s.d.advisoryXactLock(ctx, tx, "exp-run-baseline", projectID); err != nil {
			return nil, fmt.Errorf("lock project baseline: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE exp_runs SET is_baseline = FALSE
			 WHERE project_id = $1 AND name <> $2 AND is_baseline`, projectID, name); err != nil {
			return nil, fmt.Errorf("clear previous baseline: %w", err)
		}
	}

	// COALESCE on a typed NULL keeps the "absent means unchanged" rule in the
	// statement itself, so no branch of this function builds SQL by hand.
	var tags []string
	if upd.Tags != nil {
		tags = *upd.Tags
	}
	run, err := s.scanRun(tx.QueryRow(ctx, s.d.queries().updateExpRunAnnotation,
		projectID, name, s.d.stringArrayArg(tags), upd.Archived, upd.IsBaseline, upd.Note))
	if err != nil {
		return nil, err
	}
	// The produced-model list lives in its own table, so it is replaced here
	// rather than in the statement above -- inside the same transaction, so a
	// PATCH carrying both a note and a model list is still all-or-nothing.
	if upd.Models != nil {
		if err := replaceRunModels(ctx, tx, run.ID, *upd.Models); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return run, nil
}

// replaceRunModels swaps a run's whole produced-model set. A full replace is
// what "the script declared these outputs" means: a re-run that no longer
// pushes a model should stop claiming it.
func replaceRunModels(ctx context.Context, tx tx, runID int64, models []ExpRunModel) error {
	if _, err := tx.Exec(ctx, `DELETE FROM exp_run_models WHERE run_id = $1`, runID); err != nil {
		return fmt.Errorf("clear run models: %w", err)
	}
	for i, m := range models {
		// ON CONFLICT rather than a plain insert: (run_id, raw) is the primary
		// key and a caller may well repeat the same reference twice.
		if _, err := tx.Exec(ctx,
			`INSERT INTO exp_run_models (run_id, raw, repo_namespace, repo_name, revision, ordinal, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, now())
			 ON CONFLICT (run_id, raw) DO UPDATE SET
			   repo_namespace = EXCLUDED.repo_namespace,
			   repo_name      = EXCLUDED.repo_name,
			   revision       = EXCLUDED.revision,
			   ordinal        = EXCLUDED.ordinal,
			   updated_at     = now()`,
			runID, m.Raw, m.Namespace, m.Name, m.Revision, i); err != nil {
			return fmt.Errorf("insert run model: %w", err)
		}
	}
	return nil
}

// ListRunModels returns the models every run of one project declared it
// produced, keyed by run name. A model that does not resolve is reported as
// dangling, exactly as ListRepoLineage does it. repo_namespace is matched
// case-insensitively, same as any other namespace lookup (see GetNamespace).
func (s *Store) ListRunModels(ctx context.Context, projectID int64) (map[string][]ExpRunModel, error) {
	rows, err := s.db.Query(ctx,
		`SELECT rn.name, m.raw, m.repo_namespace, m.repo_name, m.revision, m.ordinal,
		        EXISTS (
		          SELECT 1 FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
		          WHERE LOWER(n.name) = LOWER(m.repo_namespace) AND r.name = m.repo_name
		            AND r.kind = 'model'
		        )
		 FROM exp_run_models m
		 JOIN exp_runs rn ON rn.id = m.run_id
		 WHERE rn.project_id = $1
		 ORDER BY rn.name, m.ordinal, m.raw`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list run models: %w", err)
	}
	defer rows.Close()

	out := map[string][]ExpRunModel{}
	for rows.Next() {
		var runName string
		var m ExpRunModel
		if err := rows.Scan(&runName, &m.Raw, &m.Namespace, &m.Name, &m.Revision, &m.Ordinal, &m.Exists); err != nil {
			return nil, err
		}
		out[runName] = append(out[runName], m)
	}
	return out, rows.Err()
}

// ExpRunProducer is one experiment run that declared it produced a given model
// repository -- the reverse of ListRunModels, and what the model page shows
// next to the lineage its own card declares.
type ExpRunProducer struct {
	// Repo is the *experiment* dataset repository holding the run.
	Repo     Repo
	Project  string
	Run      string
	Revision string
	Raw      string
}

// maxRunProducers bounds the reverse lookup for the same reason
// maxLineageDependents does: nothing stops a thousand runs from naming one
// model, and a repository page shows a handful.
const maxRunProducers = 100

// ListModelProducers answers "which runs claim to have produced this model?".
func (s *Store) ListModelProducers(ctx context.Context, ns, name string) ([]ExpRunProducer, error) {
	if ns == "" || name == "" {
		return []ExpRunProducer{}, nil
	}
	args := []any{ns, name}

	rows, err := s.db.Query(ctx,
		`SELECT `+repoColumns+`, p.name, rn.name, m.revision, m.raw
		 FROM exp_run_models m
		 JOIN exp_runs rn ON rn.id = m.run_id
		 JOIN exp_projects p ON p.id = rn.project_id
		 JOIN repositories r ON r.id = p.repo_id
		 JOIN namespaces n ON n.id = r.namespace_id
		 WHERE LOWER(m.repo_namespace) = LOWER($1) AND m.repo_name = $2
		 ORDER BY r.updated_at DESC, p.name, rn.name, rn.id, m.raw
		 LIMIT `+strconv.Itoa(maxRunProducers), args...)
	if err != nil {
		return nil, fmt.Errorf("list model producers: %w", err)
	}
	defer rows.Close()

	out := []ExpRunProducer{}
	for rows.Next() {
		var pr ExpRunProducer
		repo, err := scanRepoWith(rows, &pr.Project, &pr.Run, &pr.Revision, &pr.Raw)
		if err != nil {
			return nil, err
		}
		pr.Repo = *repo
		out = append(out, pr)
	}
	return out, rows.Err()
}

func (s *Store) scanRun(row rowScanner) (*ExpRun, error) {
	r := &ExpRun{}
	var configRaw, summaryRaw, keysRaw []byte
	if err := row.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Status, &configRaw, &summaryRaw, &keysRaw,
		&r.LastStep, &r.NumPoints, &r.StartedAt, &r.UpdatedAt, &r.Group, &r.JobType,
		s.d.stringArrayDest(&r.Tags), &r.Archived, &r.IsBaseline, &r.Note); err != nil {
		return nil, norm(err)
	}
	r.Config = map[string]any{}
	r.Summary = map[string]any{}
	r.MetricKeys = []string{}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	_ = json.Unmarshal(configRaw, &r.Config)
	_ = json.Unmarshal(summaryRaw, &r.Summary)
	_ = json.Unmarshal(keysRaw, &r.MetricKeys)
	return r, nil
}

// InsertPoints appends live metrics from the native ingest API.
func (s *Store) InsertPoints(ctx context.Context, runID int64, points []MetricPoint) error {
	if len(points) == 0 {
		return nil
	}
	rows := make([][]any, 0, len(points))
	for _, p := range points {
		raw, err := json.Marshal(p.Metrics)
		if err != nil {
			return err
		}
		ts := p.TS
		if ts.IsZero() {
			ts = time.Now()
		}
		rows = append(rows, []any{runID, p.Step, ts, raw})
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := tx.BulkInsert(ctx, "exp_points", []string{"run_id", "step", "ts", "metrics"}, rows); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CountPoints(ctx context.Context, runID int64) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM exp_points WHERE run_id = $1`, runID).Scan(&n)
	return n, err
}

// ------------------------------------------------------- parquet flush queue

// PendingFlush is one project holding live points that have not yet been
// written into the dataset repository's parquet (docs/dev/thinkingface-design.md
// §8: route B buffers in exp_points, the source of truth stays the parquet).
type PendingFlush struct {
	RepoID    int64
	ProjectID int64
	Project   string
	NumPoints int64
	// Terminal is true when at least one run still holding unflushed points
	// has already reached finished/failed. Such a project is flushed on the
	// next poll instead of waiting out the whole flush interval, so a run's
	// data lands in git as soon as the run is over.
	Terminal bool
}

// ListPendingFlushProjects groups the unflushed points by project. Archived
// repositories are excluded: every other write path refuses them, and a
// machine-generated commit must not be the one exception. So is a project
// blocked within the last flushBlockRetryAfter (see SetProjectFlushBlock),
// which is what stops one unflushable project from occupying the front of the
// order below for ever.
//
// The order is oldest-unflushed-point first, not project id first, and the
// difference is starvation. exp_points holds only what has not been written to
// parquet yet, so ordering by the project's own id ranks projects by when they
// were *created*: the hundred lowest ids that are still ingesting fill the
// window on every poll, and a project created after them never comes up at
// all. Its buffer then grows without bound -- nothing else drains exp_points
// -- and experiments.Series reads every live point to draw the chart, so the
// dashboard degrades along with it. MIN(pt.id) instead ranks by how long the
// project has been waiting, which a project that is flushed resets (its points
// are deleted) and a project that is skipped only improves. Point ids are
// unique across projects, so the order is total on both engines without a
// tiebreaker; p.id is appended anyway to keep the plan's sort deterministic if
// that ever stops being true.
func (s *Store) ListPendingFlushProjects(ctx context.Context, limit int) ([]PendingFlush, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx,
		`SELECT p.repo_id, p.id, p.name, count(*),
		        MAX(CASE WHEN r.status IN ('finished', 'failed') THEN 1 ELSE 0 END)
		 FROM exp_points pt
		 JOIN exp_runs r ON r.id = pt.run_id
		 JOIN exp_projects p ON p.id = r.project_id
		 JOIN repositories repo ON repo.id = p.repo_id
		 WHERE repo.archived_at IS NULL
		   AND (p.flush_blocked_at IS NULL OR p.flush_blocked_at < $2)
		 GROUP BY p.repo_id, p.id, p.name
		 ORDER BY MIN(pt.id), p.id
		 LIMIT $1`, limit, time.Now().Add(-flushBlockRetryAfter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PendingFlush{}
	for rows.Next() {
		var f PendingFlush
		var terminal int
		if err := rows.Scan(&f.RepoID, &f.ProjectID, &f.Project, &f.NumPoints, &terminal); err != nil {
			return nil, err
		}
		f.Terminal = terminal != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

// PendingPoint is one unflushed point plus the identity the flusher needs: the
// run it belongs to, and the row id it is deleted by (and recorded under in
// the parquet, so a retried flush recognises what it already wrote).
type PendingPoint struct {
	ID      int64
	RunName string
	Step    int64
	TS      time.Time
	Metrics map[string]float64
}

// ListProjectPoints returns a project's unflushed points in insertion order,
// capped at limit so one flush can never build an unbounded parquet in memory.
func (s *Store) ListProjectPoints(ctx context.Context, projectID int64, limit int) ([]PendingPoint, error) {
	if limit <= 0 {
		limit = 100000
	}
	rows, err := s.db.Query(ctx,
		`SELECT pt.id, r.name, pt.step, pt.ts, pt.metrics
		 FROM exp_points pt
		 JOIN exp_runs r ON r.id = pt.run_id
		 WHERE r.project_id = $1
		 ORDER BY pt.id
		 LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PendingPoint{}
	for rows.Next() {
		var p PendingPoint
		var raw []byte
		if err := rows.Scan(&p.ID, &p.RunName, &p.Step, &p.TS, &raw); err != nil {
			return nil, err
		}
		p.Metrics = map[string]float64{}
		_ = json.Unmarshal(raw, &p.Metrics)
		out = append(out, p)
	}
	return out, rows.Err()
}

// deletePointsBatch bounds one DELETE's placeholder count. Both drivers bind
// $N positionally, but SQLite's compiled-statement variable limit is finite,
// so the ids are deleted in chunks rather than in one enormous statement.
const deletePointsBatch = 500

// DeletePoints removes the rows a flush has written to parquet. Deleting by
// explicit id (rather than by an id watermark) is what makes a flush that
// raced a concurrent ingest safe: only the points actually written are
// dropped, and anything logged while the parquet was being built survives to
// the next flush.
func (s *Store) DeletePoints(ctx context.Context, ids []int64) error {
	for start := 0; start < len(ids); start += deletePointsBatch {
		end := min(start+deletePointsBatch, len(ids))
		chunk := ids[start:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			placeholders[i] = "$" + strconv.Itoa(i+1)
			args[i] = id
		}
		if _, err := s.db.Exec(ctx,
			`DELETE FROM exp_points WHERE id IN (`+strings.Join(placeholders, ", ")+`)`, args...); err != nil {
			return fmt.Errorf("delete flushed points: %w", err)
		}
	}
	return nil
}

// DeleteExpRun removes one run and, by cascade, its live metric points.
// Unlike DeleteProjectRunsNotIn this is a deliberate user action, so a run
// that still holds points goes too. The parquet export is the batch path's
// source of truth: a run that is still present there reappears on the next
// index, which is why the API contract calls this a delete of the *indexed*
// run. ErrNotFound when the project has no such run.
func (s *Store) DeleteExpRun(ctx context.Context, projectID int64, name string) error {
	n, err := s.db.Exec(ctx, `DELETE FROM exp_runs WHERE project_id = $1 AND name = $2`, projectID, name)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	_, _ = s.db.Exec(ctx, `UPDATE exp_projects SET updated_at = now() WHERE id = $1`, projectID)
	return nil
}

// DeleteProjectRunsNotIn prunes runs that vanished from the parquet export, so
// a rewritten history does not leave ghosts in the run list.
//
// It deletes only what the indexer owns, and the three conditions below are
// what "owns" means:
//
//   - not named by this index (the caller passes every run the parquet holds);
//   - holding no buffered ingest points, so a run whose points have not been
//     flushed yet is not deleted out from under a live training job;
//   - and num_points > 0, which is the one that says the indexer put it there.
//
// That last condition is not a heuristic. num_points only ever counts rows the
// indexer read out of a parquet or rows sitting in exp_points, and the upsert
// raises it monotonically (GREATEST / MAX), so zero means "this run has never
// had a single point anywhere" -- which the indexer cannot produce: it creates
// a run only from rows it scanned. Zero therefore means the run was created
// through the API, by `POST .../finish` or by a `POST .../log` carrying no
// points. That is a real run: a job that crashed before its first metric, or
// one that logs only artifacts.
//
// Without it those runs were listed and then deleted on the next index --
// which happens after any push and after any flush, so within seconds --
// taking the user's tags, note, baseline flag and exp_run_models rows with
// them through the foreign key's cascade. The run had never been in the
// parquet and had no buffered points, so both of the older conditions passed
// and it looked exactly like a ghost.
//
// A nil keep set is a no-op. Both dialects bind nil as SQL NULL, and the two
// engines then disagree by exactly the worst amount: `NOT (name = ANY(NULL))`
// is NULL on Postgres and deletes nothing, while
// `NOT (name IN (SELECT value FROM json_each(NULL)))` is true for every row
// on SQLite and would delete the project's whole run list. An *empty* slice
// is a different statement -- "the export really does list no runs" -- and
// still prunes on both engines, since it binds as an empty array.
func (s *Store) DeleteProjectRunsNotIn(ctx context.Context, projectID int64, keep []string) error {
	if keep == nil {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`DELETE FROM exp_runs WHERE project_id = $1 AND NOT (name `+s.d.inArray("$2")+`)
		   AND num_points > 0
		   AND NOT EXISTS (SELECT 1 FROM exp_points p WHERE p.run_id = exp_runs.id)`,
		projectID, s.d.stringArrayArg(keep))
	return err
}

// flushBlockRetryAfter is how long a project stays out of the flush poller's
// candidate list after a flush found a condition it cannot get past. It is
// long enough that a wedged project costs one attempt an hour instead of one
// every ten seconds, and short enough that an operator who fixes the cause
// does not have to tell anything about it: the next poll after the window
// simply succeeds and clears the mark.
//
// A var only so tests can shorten it; nothing changes it at runtime.
var flushBlockRetryAfter = time.Hour

// SetProjectFlushBlock records why a project's buffered points could not be
// written to its parquet, or clears the record when reason is "".
//
// It exists because the alternative was deleting the points. Two conditions
// -- a project name no path can hold, and a metrics file with more rows than
// a flush can rebuild in memory -- are properties of the repository, so every
// retry fails identically, and the poller's oldest-point-first order means a
// project that never drains climbs to the front and starves every other one.
// The old answer was to drop the batch, log it, and move on: silent loss of
// points the ingest API had already answered 200 for, against a limit no
// user-facing document mentions. Marking the project keeps the data and gets
// the same starvation guarantee, at the cost of a buffer that grows without a
// ceiling until somebody acts on flush_error -- and, as
// ListBlockedFlushProjects below records, nothing yet puts flush_error in
// front of anybody.
func (s *Store) SetProjectFlushBlock(ctx context.Context, projectID int64, reason string) error {
	if reason == "" {
		_, err := s.db.Exec(ctx,
			`UPDATE exp_projects SET flush_blocked_at = NULL, flush_error = ''
			 WHERE id = $1 AND (flush_blocked_at IS NOT NULL OR flush_error <> '')`, projectID)
		return err
	}
	_, err := s.db.Exec(ctx,
		`UPDATE exp_projects SET flush_blocked_at = now(), flush_error = $2 WHERE id = $1`,
		projectID, sanitizeText(reason))
	return err
}

// BlockedFlushProject is one project the flusher has given up on for now,
// with the repository it belongs to.
type BlockedFlushProject struct {
	RepoID    int64
	ProjectID int64
	Project   string
	Error     string
	BlockedAt time.Time
	NumPoints int64
}

// ListBlockedFlushProjects reports the projects whose buffer is not being
// written, with the reason and how many points are waiting.
//
// **Nothing calls it yet outside its own test.** There is no API endpoint, no
// admin screen and no `thinkingface` subcommand behind it, so as things stand
// an operator finds out about a blocked project from one of two places: the
// ERROR line experiments.blockFlush writes at the moment of blocking
// ("experiment project cannot be flushed; its buffered points are being
// kept"), or a query against exp_projects.flush_blocked_at / flush_error by
// hand. Neither is a monitor, and the first scrolls away.
//
// It is written and kept because the exposure is the small half of the job:
// the query is the part that has to agree with SetProjectFlushBlock and with
// ListPendingFlushProjects' skip, and it belongs next to them. Where it should
// surface is the operator settings area that already lists parked sync jobs
// (ListFailedSyncJobs -> /settings/admin), since a blocked flush is the same
// kind of fault: work that has stopped and will not restart on its own.
//
// The points are still there -- NumPoints says how many -- which is the whole
// difference from the behaviour this replaced.
func (s *Store) ListBlockedFlushProjects(ctx context.Context) ([]BlockedFlushProject, error) {
	rows, err := s.db.Query(ctx,
		`SELECT p.repo_id, p.id, p.name, p.flush_error, p.flush_blocked_at,
		        (SELECT count(*) FROM exp_points pt
		         JOIN exp_runs r ON r.id = pt.run_id
		         WHERE r.project_id = p.id)
		 FROM exp_projects p
		 WHERE p.flush_blocked_at IS NOT NULL
		 ORDER BY p.flush_blocked_at DESC, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []BlockedFlushProject{}
	for rows.Next() {
		var b BlockedFlushProject
		if err := rows.Scan(&b.RepoID, &b.ProjectID, &b.Project, &b.Error, &b.BlockedAt, &b.NumPoints); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
