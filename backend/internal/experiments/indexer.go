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
	// minValues / maxValues are the extremes over every point scanned, which
	// become the run's summary_min / summary_max.
	minValues map[string]float64
	maxValues map[string]float64
	keys      map[string]bool
	// lastValueSteps is the step each lastValues entry was logged at, so a
	// row with a lower step cannot displace it (observeAt).
	lastValueSteps map[string]int64
}

func newRunAggregate() *runAggregate {
	return &runAggregate{
		lastValues: map[string]float64{},
		minValues:  map[string]float64{},
		maxValues:  map[string]float64{},
		keys:       map[string]bool{},

		lastValueSteps: map[string]int64{},
	}
}

// observe records one metric value that carries no step: it becomes the last
// value seen (scan order is chronological, see indexProject) and widens the
// run's min / max.
func (agg *runAggregate) observe(name string, v float64) {
	agg.keys[name] = true
	agg.lastValues[name] = v
	delete(agg.lastValueSteps, name)
	agg.widen(name, v)
}

// observeAt is observe for a value logged at step: it only becomes the last
// value if no value at a higher step has been seen. Rows are chronological by
// *write*, not by step -- `tf experiments sync` replaying points a training
// run could not deliver at the time appends old steps after newer ones, and in
// row order the summary would fall back to that stale value on every re-index.
// A tie goes to the later row, which is how a resumed run overwriting a step
// reads.
func (agg *runAggregate) observeAt(step int64, name string, v float64) {
	agg.keys[name] = true
	if prev, ok := agg.lastValueSteps[name]; !ok || step >= prev {
		agg.lastValues[name] = v
		agg.lastValueSteps[name] = step
	}
	agg.widen(name, v)
}

func (agg *runAggregate) widen(name string, v float64) {
	if cur, ok := agg.minValues[name]; !ok || v < cur {
		agg.minValues[name] = v
	}
	if cur, ok := agg.maxValues[name]; !ok || v > cur {
		agg.maxValues[name] = v
	}
}

// floatsToAny copies a metric map into the map[string]any the store writes.
func floatsToAny(in map[string]float64) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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
	// Every file the project's rows live in, oldest first: a rotated project
	// whose newest points are in a continuation file would otherwise be
	// indexed as if it had stopped logging at the rotation point, and its
	// summary would freeze at whatever value it held then. The order is
	// load-bearing beyond that -- lastValues below keeps the last value it
	// sees -- and layout.MetricsFiles is what guarantees it is chronological.
	for _, metricsPath := range layout.MetricsFiles() {
		err := ix.scanMetricRows(ctx, repo, gitRepo, repo.DefaultBranch, metricsPath, viewer.ScanRequest{},
			func(run string, row map[string]any) error {
				agg, ok := aggregates[run]
				if !ok {
					agg = newRunAggregate()
					aggregates[run] = agg
				}
				agg.numPoints++

				step, hasStep := rowStep(row)
				if hasStep && step > agg.lastStep {
					agg.lastStep = step
				}
				if ts, ok := rowTime(row); ok {
					if agg.firstTS.IsZero() || ts.Before(agg.firstTS) {
						agg.firstTS = ts
					}
				}
				observe := agg.observe
				if hasStep {
					observe = func(name string, v float64) { agg.observeAt(step, name, v) }
				}
				forEachMetricValue(row, "", observe)
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

		summary := floatsToAny(agg.lastValues)
		summaryMin := floatsToAny(agg.minValues)
		summaryMax := floatsToAny(agg.maxValues)

		var startedAt *time.Time
		if !agg.firstTS.IsZero() {
			ts := agg.firstTS
			startedAt = &ts
		}
		// What status this run is written with turns entirely on whether it
		// already has a row -- and on that question being answered honestly
		// even when the database is the thing that failed. See
		// indexedRunStatus.
		existing, lookupErr := ix.store.GetExpRun(ctx, projectID, run)
		status, err := indexedRunStatus(run, lookupErr)
		if err != nil {
			return err
		}
		if lookupErr == nil {
			buffered, err := ix.store.CountPoints(ctx, existing.ID)
			if err != nil {
				return fmt.Errorf("count buffered points of run %q: %w", run, err)
			}
			if buffered > 0 {
				summary, summaryMin, summaryMax, keys = mergeBufferedSummaries(existing,
					summary, summaryMin, summaryMax, keys)
			}
		}
		if _, err := ix.store.UpsertExpRunWith(ctx, projectID, store.ExpRunUpsert{
			Name:       run,
			Status:     status,
			Config:     configs[run],
			Summary:    summary,
			SummaryMin: summaryMin,
			SummaryMax: summaryMax,
			MetricKeys: keys,
			LastStep:   agg.lastStep,
			NumPoints:  agg.numPoints,
			StartedAt:  startedAt,
			// A trackio script that passed group= / job_type= leaves them as
			// plain columns in the configs export. nil keeps whatever the
			// ingest API already declared, so route A never clears a sweep.
			Group:   groupingFromConfig(configs[run], "group"),
			JobType: groupingFromConfig(configs[run], "job_type"),
			// A re-index is not a sign of life: it runs for every run of the
			// repository whenever any one of them flushes or anything is
			// pushed, so touching updated_at here would keep a crashed run
			// looking alive (store.ExpRunUpsert.Touch). A parquet that grew
			// past the stored step still moves it.
			Touch: false,
		}); err != nil {
			return err
		}
	}
	return ix.store.DeleteProjectRunsNotIn(ctx, projectID, names)
}

// mergeBufferedSummaries folds what the store already holds for a run into
// the summaries a re-index computed from the parquet, for a run that still has
// points buffered in exp_points.
//
// "The parquet is the truth" only holds once everything has been flushed into
// it. A flush of one project re-indexes the whole repository, and a run
// that logged a batch after the flush read exp_points -- or one in another
// project, whose flush has not come round yet -- has points the file does not
// contain. Its stored summary already reflects them (ingest merged each batch
// in), so writing the parquet's figures over it would roll the "last value"
// back, narrow min / max to the flushed subset, and drop metric names only the
// buffered points carry -- until the next flush put them back.
//
// So: min of the mins and max of the maxes, the stored last value wins for a
// metric both sides know (the buffered points are the newer ones), and the
// metric names are the union. Stored values that are not numbers (a
// hand-edited row) are ignored for min / max.
func mergeBufferedSummaries(stored *store.ExpRun, summary, summaryMin, summaryMax map[string]any,
	keys []string) (map[string]any, map[string]any, map[string]any, []string) {

	for k, v := range stored.Summary {
		summary[k] = v
	}
	mergeExtremes(summaryMin, stored.SummaryMin, func(a, b float64) bool { return a < b })
	mergeExtremes(summaryMax, stored.SummaryMax, func(a, b float64) bool { return a > b })

	seen := make(map[string]bool, len(keys)+len(stored.MetricKeys))
	for _, k := range keys {
		seen[k] = true
	}
	for _, k := range stored.MetricKeys {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return summary, summaryMin, summaryMax, keys
}

// mergeExtremes folds other into dst in place: better(a, b) reports that a
// should replace b.
func mergeExtremes(dst, other map[string]any, better func(a, b float64) bool) {
	for k, raw := range other {
		v, ok := raw.(float64)
		if !ok {
			continue
		}
		if cur, ok := dst[k].(float64); ok && !better(v, cur) {
			continue
		}
		dst[k] = v
	}
}

// indexSystemMetrics folds trackio's {project}_system.parquet into the run
// summaries under SystemMetricPrefix, so route A's telemetry lands in the same
// namespace the Python shim already writes to.
//
// Only the per-metric values (agg.observe) are touched. numPoints, lastStep and
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
		func(run string, row map[string]any) error {
			agg, ok := aggregates[run]
			if !ok {
				return nil
			}
			forEachMetricValue(row, SystemMetricPrefix, agg.observe)
			return nil
		})
	if err != nil {
		slog.Warn("index system metrics", "repo", repo.FullName(),
			"project", layout.Project, "path", layout.SystemMetricsPath, "error", err)
	}
}

// scanMetricRows resolves one metrics-shaped parquet inside the repository and
// calls fn once per row that names a run. Both the indexer and the series
// reader start from exactly this -- resolve the blob, scan it, find the row's
// run (rowRun), skip rows that have none -- and differ only in what they do
// with the row afterwards.
//
// scan narrows what has to be decoded: its Columns keep a single-metric chart
// from paying for every other metric's column (Series sets them from
// projectSeriesColumns), and its Predicates let whole row groups be skipped on
// their run statistics. Both are optimisations only -- rows the predicate
// would reject still reach fn, and IndexRepo passes the zero value because it
// aggregates every run and every metric a project has.
func (ix *Indexer) scanMetricRows(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, rev, filePath string,
	scan viewer.ScanRequest, fn func(run string, row map[string]any) error) error {

	key, err := ix.objectKey(ctx, repo, gitRepo, rev, filePath)
	if err != nil {
		return fmt.Errorf("locate %s: %w", filePath, err)
	}
	err = ix.viewer.Scan(ctx, key, scan, func(row map[string]any) error {
		run := rowRun(row)
		if run == "" {
			return nil
		}
		return fn(run, row)
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
		run := rowRun(row)
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
	case uint64:
		// The viewer returns every cell of an INT(64,false) column as a Go
		// uint64 (viewer/convert.go's unsignedIntValue and normalizeGeneric
		// both widen to this one type, never uint32/uint -- see their
		// comments), since a uint64 can exceed math.MaxInt64. Anything within
		// int64's range converts losslessly; anything past it is rejected
		// rather than silently wrapping negative, the same as every other
		// unconvertible value below.
		if t > math.MaxInt64 {
			return 0, false
		}
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

// toUint64 accepts the value shapes a UINT64 metrics-parquet column can hold:
// the viewer's own uint64 for a cell read back out of the file, or the
// int64/int/float64 a new point supplies before it is written. Nothing in
// this package produces a negative value for such a column on purpose, so one
// found here is rejected rather than reinterpreted; the caller (flushColumn.
// encode) turns that into a null cell like every other unconvertible value.
func toUint64(v any) (uint64, bool) {
	switch t := v.(type) {
	case uint64:
		return t, true
	case int64:
		if t < 0 {
			return 0, false
		}
		return uint64(t), true
	case int:
		if t < 0 {
			return 0, false
		}
		return uint64(t), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t > math.MaxUint64 {
			return 0, false
		}
		return uint64(t), true
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
		// The same guard as float64: a NaN or Inf logged at float32 precision
		// would otherwise reach the summary, which JSON cannot encode.
		if math.IsNaN(float64(t)) || math.IsInf(float64(t), 0) {
			return 0, false
		}
		return float64(t), true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case uint64:
		// Mirrors the int64 case above: float64 cannot represent every value
		// past 2^53 exactly, which is the same precision limit an int64 this
		// large already has here. See toInt for why uint64 is the only
		// unsigned Go type this package ever sees.
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
		return epochToTime(t)
	case uint64:
		// The viewer returns every cell of an INT(64,false) column as a Go
		// uint64 (see toInt's comment on the same shape), so a UINT64
		// started_at/timestamp column -- one written by an exporter other
		// than this package's own flush -- must be accepted here too, or the
		// run's start time silently goes missing and any time-based chart
		// renders empty. Values past int64's range are rejected rather than
		// wrapped negative, the same as toInt does for the identical case.
		if t <= math.MaxInt64 {
			return epochToTime(int64(t))
		}
	case float64:
		if t > 1e9 {
			return time.Unix(int64(t), 0), true
		}
	}
	return time.Time{}, false
}

// epochToTime is the int64 arm of toTime's heuristic on magnitude: trackio
// writes seconds, other tools milliseconds. Factored out so toTime's uint64
// case (a UINT64 epoch column) can share it after its own range check,
// instead of duplicating the two thresholds.
func epochToTime(t int64) (time.Time, bool) {
	if t > 1e12 {
		return time.UnixMilli(t), true
	}
	if t > 1e9 {
		return time.Unix(t, 0), true
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
	if !ok || s == "" || len(s) > maxGroupingBytes || hasInvalidIngestChars(s) {
		return nil
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
