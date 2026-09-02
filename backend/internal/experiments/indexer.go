package experiments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// Indexer reads experiment parquet files out of a dataset repository and keeps
// the run index in Postgres up to date.
type Indexer struct {
	store   *store.Store
	git     *gitrepo.Manager
	storage storage.Storage
	viewer  *viewer.Reader
}

func NewIndexer(st *store.Store, git *gitrepo.Manager, obj storage.Storage, v *viewer.Reader) *Indexer {
	return &Indexer{store: st, git: git, storage: obj, viewer: v}
}

// runAggregate accumulates one run's statistics during a scan.
type runAggregate struct {
	lastStep   int64
	numPoints  int64
	firstTS    time.Time
	lastValues map[string]float64
	keys       map[string]bool
}

// IndexRepo rebuilds the project and run index for a repository. It is
// idempotent: the sync worker may run it after every push.
func (ix *Indexer) IndexRepo(ctx context.Context, repo *store.Repo) error {
	files, err := ix.store.ListRepoFiles(ctx, repo.ID, repo.DefaultBranch)
	if err != nil {
		return fmt.Errorf("list repo files: %w", err)
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}

	layouts := DetectLayouts(paths, repo.Name)
	if len(layouts) == 0 {
		return nil
	}

	gitRepo, err := ix.git.Open(repo.StoragePath)
	if err != nil {
		return fmt.Errorf("open git repository: %w", err)
	}

	for _, layout := range layouts {
		if err := ix.indexProject(ctx, repo, gitRepo, layout); err != nil {
			// One bad project should not stop the others from indexing.
			slog.Error("index experiment project", "repo", repo.FullName(),
				"project", layout.Project, "error", err)
		}
	}
	return nil
}

func (ix *Indexer) indexProject(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, layout Layout) error {
	aggregates := map[string]*runAggregate{}
	// Every file the project's rows live in, base first: a rotated project
	// whose newest points are in a continuation file would otherwise be
	// indexed as if it had stopped logging at the rotation point, and its
	// summary would freeze at whatever value it held then.
	for _, metricsPath := range layout.MetricsFiles() {
		err := ix.scanMetricRows(ctx, repo, gitRepo, repo.DefaultBranch, metricsPath, viewer.ScanRequest{},
			func(run string, row map[string]any, cols map[string]bool) error {
				agg, ok := aggregates[run]
				if !ok {
					agg = &runAggregate{lastValues: map[string]float64{}, keys: map[string]bool{}}
					aggregates[run] = agg
				}
				agg.numPoints++

				if stepCol := stepColumn(cols); stepCol != "" {
					if step, ok := toInt(row[stepCol]); ok && step > agg.lastStep {
						agg.lastStep = step
					}
				}
				if tsCol := timeColumn(cols); tsCol != "" {
					if ts, ok := toTime(row[tsCol]); ok {
						if agg.firstTS.IsZero() || ts.Before(agg.firstTS) {
							agg.firstTS = ts
						}
					}
				}
				forEachMetricValue(row, "", func(name string, v float64) {
					agg.keys[name] = true
					agg.lastValues[name] = v
				})
				return nil
			})
		if err != nil {
			return err
		}
	}
	if len(aggregates) == 0 {
		return nil
	}
	ix.indexSystemMetrics(ctx, gitRepo, repo, layout, aggregates)

	configs := ix.readConfigs(ctx, repo, gitRepo, layout)

	projectID, err := ix.store.UpsertExpProject(ctx, repo.ID, layout.Project)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(aggregates))
	for run, agg := range aggregates {
		names = append(names, run)

		keys := make([]string, 0, len(agg.keys))
		for k := range agg.keys {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		summary := make(map[string]any, len(agg.lastValues))
		for k, v := range agg.lastValues {
			summary[k] = v
		}

		var startedAt *time.Time
		if !agg.firstTS.IsZero() {
			ts := agg.firstTS
			startedAt = &ts
		}
		// What status this run is written with turns entirely on whether it
		// already has a row -- and on that question being answered honestly
		// even when the database is the thing that failed. See
		// indexedRunStatus.
		_, lookupErr := ix.store.GetExpRun(ctx, projectID, run)
		status, err := indexedRunStatus(run, lookupErr)
		if err != nil {
			return err
		}
		if _, err := ix.store.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
			Name:       run,
			Status:     status,
			Config:     configs[run],
			Summary:    summary,
			MetricKeys: keys,
			LastStep:   agg.lastStep,
			NumPoints:  agg.numPoints,
			StartedAt:  startedAt,
			// A trackio script that passed group= / job_type= leaves them as
			// plain columns in the configs export. nil keeps whatever the
			// ingest API already declared, so route A never clears a sweep.
			Group:   groupingFromConfig(configs[run], "group"),
			JobType: groupingFromConfig(configs[run], "job_type"),
		}); err != nil {
			return err
		}
	}
	return ix.store.DeleteProjectRunsNotIn(ctx, projectID, names)
}

// indexSystemMetrics folds trackio's {project}_system.parquet into the run
// summaries under SystemMetricPrefix, so route A's telemetry lands in the same
// namespace the Python shim already writes to.
//
// Only agg.keys and agg.lastValues are touched. numPoints, lastStep and
// firstTS deliberately are not: telemetry is sampled on a wall-clock timer of
// its own, so counting it would make "how many points did this run log" and
// "what step is it on" depend on how long the machine happened to be up.
//
// A run that appears only in the telemetry file is skipped rather than
// invented -- the metrics file is what defines which runs exist.
//
// A failure here is logged and swallowed: telemetry is an extra, and losing it
// must not cost the project its run index.
func (ix *Indexer) indexSystemMetrics(ctx context.Context, gitRepo *gitrepo.Repo, repo *store.Repo,
	layout Layout, aggregates map[string]*runAggregate) {

	if layout.SystemMetricsPath == "" {
		return
	}
	err := ix.scanMetricRows(ctx, repo, gitRepo, repo.DefaultBranch, layout.SystemMetricsPath, viewer.ScanRequest{},
		func(run string, row map[string]any, _ map[string]bool) error {
			agg, ok := aggregates[run]
			if !ok {
				return nil
			}
			forEachMetricValue(row, SystemMetricPrefix, func(name string, v float64) {
				agg.keys[name] = true
				agg.lastValues[name] = v
			})
			return nil
		})
	if err != nil {
		slog.Warn("index system metrics", "repo", repo.FullName(),
			"project", layout.Project, "path", layout.SystemMetricsPath, "error", err)
	}
}

// scanMetricRows resolves one metrics-shaped parquet inside the repository and
// calls fn once per row that names a run. Both the indexer and the series
// reader start from exactly this -- resolve the blob, scan it, find the run
// column, skip rows that have none -- and differ only in what they do with the
// row afterwards.
//
// scan narrows what has to be decoded: its Columns keep a single-metric chart
// from paying for every other metric's column (Series sets them from
// projectSeriesColumns), and its Predicates let whole row groups be skipped on
// their run statistics. Both are optimisations only -- rows the predicate
// would reject still reach fn, and IndexRepo passes the zero value because it
// aggregates every run and every metric a project has.
func (ix *Indexer) scanMetricRows(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, rev, filePath string,
	scan viewer.ScanRequest, fn func(run string, row map[string]any, cols map[string]bool) error) error {

	key, err := ix.objectKey(ctx, repo, gitRepo, rev, filePath)
	if err != nil {
		return fmt.Errorf("locate %s: %w", filePath, err)
	}
	err = ix.viewer.Scan(ctx, key, scan, func(row map[string]any) error {
		cols := columnSet(row)
		runCol := runColumn(cols)
		if runCol == "" {
			return nil
		}
		run := toString(row[runCol])
		if run == "" {
			return nil
		}
		return fn(run, row, cols)
	})
	if err != nil {
		return fmt.Errorf("scan %s: %w", filePath, err)
	}
	return nil
}

// forEachMetricValue calls fn for every column of a row that describes a
// measurement rather than the row itself, with prefix prepended to the name.
// Columns whose value is not numeric (a null, a string label) are skipped:
// there is nothing to chart in them.
func forEachMetricValue(row map[string]any, prefix string, fn func(name string, value float64)) {
	for name, raw := range row {
		if structuralColumns[name] {
			continue
		}
		if v, ok := toFloat(raw); ok {
			fn(prefix+name, v)
		}
	}
}

// readConfigs loads per-run hyperparameters. A missing configs file is normal.
func (ix *Indexer) readConfigs(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, layout Layout) map[string]map[string]any {
	out := map[string]map[string]any{}
	if layout.ConfigsPath == "" {
		return out
	}
	key, err := ix.objectKey(ctx, repo, gitRepo, repo.DefaultBranch, layout.ConfigsPath)
	if err != nil {
		return out
	}
	err = ix.viewer.Scan(ctx, key, viewer.ScanRequest{}, func(row map[string]any) error {
		runCol := runColumn(columnSet(row))
		if runCol == "" {
			return nil
		}
		run := toString(row[runCol])
		if run == "" {
			return nil
		}
		cfg := map[string]any{}
		for name, raw := range row {
			if structuralColumns[name] || raw == nil {
				continue
			}
			cfg[name] = raw
		}
		out[run] = cfg
		return nil
	})
	if err != nil {
		slog.Warn("read experiment configs", "path", layout.ConfigsPath, "error", err)
	}
	return out
}

// objectKey resolves a file of repo to the key holding its bytes.
func (ix *Indexer) objectKey(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, rev, filePath string) (string, error) {
	entry, _, err := gitRepo.Stat(rev, filePath)
	if err != nil {
		return "", err
	}
	return objectKeyFor(ctx, ix.store, ix.storage, repo, gitRepo, entry)
}

// lfsOwnership is the one store method objectKeyFor needs.
type lfsOwnership interface {
	RepoHasLFSObject(ctx context.Context, repoID int64, oid string) (bool, error)
}

// objectKeyFor is objectKey for a tree entry the caller already stat'ed. The
// flusher needs the entry itself (its hash is the commit's precondition), so
// the resolution lives here rather than being repeated against a second stat.
//
// Both storage layers are content-addressed. An LFS object is at its oid's
// key -- but only if repo links it: a pointer is just text anyone can commit,
// and the key has no repository in it, so an unlinked oid reads as absent
// (store.ErrNotFound) rather than as another repository's bytes, the same
// gate the API applies. A plain blob is at its sha's key; the sync worker
// publishes it on every push, and a revision it has not reached yet is
// repaired here through the same function.
func objectKeyFor(ctx context.Context, db lfsOwnership, obj storage.Storage, repo *store.Repo, gitRepo *gitrepo.Repo, entry gitrepo.Entry) (string, error) {
	if entry.LFS != nil {
		owned, err := db.RepoHasLFSObject(ctx, repo.ID, entry.LFS.OID)
		if err != nil {
			return "", err
		}
		if !owned {
			return "", store.ErrNotFound
		}
		return storage.LFSKey(entry.LFS.OID), nil
	}
	return gitRepo.PublishBlob(ctx, obj, entry.Hash)
}

// ------------------------------------------------------------ value coercion

func columnSet(row map[string]any) map[string]bool {
	cols := make(map[string]bool, len(row))
	for k := range row {
		cols[k] = true
	}
	return cols
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func toInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return int64(t), true
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

// toFloat accepts the numeric shapes the parquet reader emits. Booleans count
// as metrics too, since trackio logs flags alongside losses.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	case float32:
		return float64(t), true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func toTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05"} {
			if ts, err := time.Parse(layout, strings.TrimSpace(t)); err == nil {
				return ts, true
			}
		}
	case int64:
		// Heuristic on magnitude: trackio writes seconds, other tools ms.
		if t > 1e12 {
			return time.UnixMilli(t), true
		}
		if t > 1e9 {
			return time.Unix(t, 0), true
		}
	case float64:
		if t > 1e9 {
			return time.Unix(int64(t), 0), true
		}
	}
	return time.Time{}, false
}

// maxGroupingBytes mirrors api.maxIngestNameBytes: route A must not be able to
// store a group_name the ingest API would have rejected.
const maxGroupingBytes = 256

// groupingFromConfig lifts a sweep grouping column out of a run's flattened
// config. trackio has no notion of grouping, so this only fires when the
// training script happened to log "group" / "job_type" itself.
//
// It returns nil -- "keep whatever is stored" -- for anything it cannot vouch
// for, which is what stops a re-index from clearing a grouping the ingest API
// declared. The value stays in config as well; this only mirrors it onto the
// column the run table groups by.
func groupingFromConfig(config map[string]any, key string) *string {
	s, ok := config[key].(string)
	if !ok || s == "" || len(s) > maxGroupingBytes || !utf8.ValidString(s) {
		return nil
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return nil
		}
	}
	return &s
}

// indexedRunStatus decides what status a re-indexed run is written with, from
// the result of looking the run up: "" to keep whatever it already has, and
// "finished" for a run this index is meeting for the first time.
//
// The distinction it draws is the point. The lookup used to be tested with
// `err == nil`, which folded every failure -- a dropped connection, a
// cancelled context, a statement timeout -- into "there is no such run", and
// that answer is destructive rather than merely wrong: the run is written as
// finished, the ingest API is no longer authoritative for it, and nothing
// ever marks it back. A live training run ended, permanently, because the
// database blinked. Only ErrNotFound means new; anything else fails the
// index, which is a job that already retries.
func indexedRunStatus(run string, lookupErr error) (string, error) {
	switch {
	case lookupErr == nil:
		// The run has a row and owns its own status: the ingest API is
		// authoritative for it, and a flush re-indexes the repository
		// mid-run, so forcing "finished" here would flip every live run to
		// finished once a minute.
		return "", nil
	case errors.Is(lookupErr, store.ErrNotFound):
		// A batch export (route A) only ever appears once the run is over.
		return "finished", nil
	default:
		return "", fmt.Errorf("read experiment run %q: %w", run, lookupErr)
	}
}
