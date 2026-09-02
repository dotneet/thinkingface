package experiments

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// Series is one metric's trace for one run, as [x, y] pairs. A series is sent
// to clients verbatim, so it is declared once in apitypes -- the package the
// TypeScript types are generated from -- and aliased here.
type Series = apitypes.ExpMetricSeries

// SeriesRequest selects what to chart.
type SeriesRequest struct {
	Project string
	Runs    []string // empty means every run
	Keys    []string // empty means every metric
	XAxis   string   // "step" (default) or "time"
	Max     int      // maximum points per series after downsampling
}

// Series reads metric traces for a project, merging the parquet export with
// live points from the native ingest API.
func (ix *Indexer) Series(ctx context.Context, repo *store.Repo, req SeriesRequest) ([]Series, error) {
	if req.Max <= 0 {
		req.Max = 1000
	}
	collect := newSeriesCollector(toSet(req.Runs), toSet(req.Keys))
	appendPoint := collect.add

	// Two different things can put two values on one (run, key, step), and
	// they are resolved by two different mechanisms -- do not conflate them:
	//
	//   * The same measurement, seen twice. The flush commits the parquet
	//     before it deletes the rows it wrote, so mid-flush a point exists in
	//     git *and* in exp_points. The ingest ids below remove those, keeping
	//     the chart neither doubled nor gapped while a flush is in flight.
	//
	//   * Two different measurements, at one step. A resumed run re-logs
	//     steps its previous attempt already reached (see the resume contract
	//     in docs/dev/thinkingface-design.md §8), and a caller may simply log the
	//     same step twice. Both values are real and both are kept on disk;
	//     the chart shows the later one, which dedupeLastWins does after the
	//     sort below.
	//
	// The parquet half is scanned first and the live buffer second, which is
	// also chronological -- git holds what has already been flushed -- so
	// "later in collection order" means "logged later".
	flushed := map[int64]bool{}
	if err := ix.scanParquetSeries(ctx, repo, req, flushed, appendPoint); err != nil {
		return nil, err
	}
	if err := ix.scanLiveSeries(ctx, repo, req, flushed, appendPoint); err != nil {
		return nil, err
	}

	if dropped := collect.droppedSeries; dropped > 0 {
		// Nothing in the response says "truncated" -- ExpMetricsResponse is a
		// list of series and adding a field to it is a wire change -- so the
		// only place this can be reported is the log. It takes a repository
		// with more than maxSeriesCount distinct (run, metric) pairs to reach,
		// which no chart the UI draws comes near.
		slog.Warn("metric series truncated: too many distinct traces",
			"repo", repo.FullName(), "project", req.Project,
			"kept", maxSeriesCount, "dropped", dropped)
	}

	out := []Series{}
	for run, byKey := range collect.series {
		for key, buf := range byKey {
			points := buf.finish()
			// SliceStable, not Slice: points that share an x must keep the
			// order they were collected in, because that order is what
			// dedupeLastWins resolves the tie with.
			sort.SliceStable(points, func(i, j int) bool { return points[i][0] < points[j][0] })
			// Only on the step axis. Two values at one step contradict each
			// other; two values at one timestamp are just two samples a fast
			// loop logged inside the same millisecond, and dropping half of
			// them would be data loss rather than deduplication.
			if req.XAxis != "time" {
				points = dedupeLastWins(points)
			}
			out = append(out, Series{Run: run, Key: key, Points: downsample(points, req.Max)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Run < out[j].Run
	})
	return out, nil
}

// maxSeriesScanPoints bounds how many [x, y] pairs one Series() call holds at
// once, across every trace it is building.
//
// The write path has capped its half of this allocation since the beginning
// (MaxFlushPoints, maxExistingFlushRows, maxLiveSeriesPoints); the read path
// had no equivalent, and it is the half that is reachable without
// authentication. `GET .../{project}/metrics` used to materialise one
// [2]float64 per numeric cell of the whole parquet before max_points was so
// much as looked at, so a repository carrying a large metrics file turned an
// anonymous GET into a several-hundred-megabyte allocation in the API process
// -- repeatable, and concurrently.
//
// A million pairs is 16 MiB of payload (plus append's headroom). It is two
// hundred times the largest response this endpoint will emit
// (api.maxMetricPoints), so no honest chart ever reaches it and the thinning
// below is invisible in practice.
//
// A var only so the tests can lower it; nothing changes it at runtime.
var maxSeriesScanPoints = 1_000_000

// maxSeriesCount bounds the *other* dimension: the number of distinct
// (run, metric) traces. Thinning the points of ten million one-point traces
// would still leave ten million map entries and headers, so the trace count
// needs its own ceiling. Well above any real project -- a sweep of a thousand
// runs logging twenty metrics is 20,000 traces and the UI asks for a handful
// of keys at a time -- and low enough that the map cannot become the leak the
// points cap just closed.
//
// A var only so the tests can lower it; nothing changes it at runtime.
var maxSeriesCount = 20_000

// seriesPoints accumulates one trace under the collector's global stride.
type seriesPoints struct {
	points [][2]float64
	// seen counts every point offered for this trace, retained or not: it is
	// what the stride is applied to, so that thinning stays uniform along the
	// trace rather than dropping its tail.
	seen int64
	// last is the most recent point offered, kept whether or not the stride
	// retained it. downsample() promises the chart's endpoints are truthful,
	// and a stride that happens to skip the final point would quietly break
	// that promise -- the chart would stop short of where the run actually is.
	last    [2]float64
	hasLast bool
}

// finish returns the trace's points with its true final point restored.
func (s *seriesPoints) finish() [][2]float64 {
	if !s.hasLast {
		return s.points
	}
	if n := len(s.points); n > 0 && s.points[n-1] == s.last {
		return s.points
	}
	return append(s.points, s.last)
}

// seriesCollector accumulates the traces one Series() call is building, inside
// a fixed memory budget.
//
// The bound cannot be "stop at N points": a chart that stopped scanning
// halfway through the file would show the run ending halfway through, which is
// worse than a coarse chart because it is wrong rather than approximate. So
// the scan always runs to the end and it is the *resolution* that gives way:
// once the budget is full every trace is halved in place and the stride
// doubles, so what is held stays a uniform sample of everything seen so far.
// That is the same trade downsample() makes at the end of the call, applied
// early enough to bound the heap.
type seriesCollector struct {
	runFilter map[string]bool
	keyFilter map[string]bool

	series map[string]map[string]*seriesPoints
	// keepEvery is the current stride: a point is retained when its position
	// within its trace is a multiple of it. Doubling it on every halving is
	// what keeps the retained set exactly "every keepEvery-th point", since
	// halving drops precisely the odd-indexed survivors of the previous
	// stride.
	keepEvery int64
	held      int
	numSeries int
	// droppedSeries counts traces refused once maxSeriesCount was reached.
	// Reported by the caller; silently returning fewer traces than the data
	// holds is the kind of thing that has to be visible somewhere.
	droppedSeries int
}

func newSeriesCollector(runFilter, keyFilter map[string]bool) *seriesCollector {
	return &seriesCollector{
		runFilter: runFilter, keyFilter: keyFilter,
		series: map[string]map[string]*seriesPoints{}, keepEvery: 1,
	}
}

func (c *seriesCollector) add(run, key string, x, y float64) {
	if len(c.runFilter) > 0 && !c.runFilter[run] {
		return
	}
	if len(c.keyFilter) > 0 && !c.keyFilter[key] {
		return
	}
	byKey, ok := c.series[run]
	if !ok {
		if c.numSeries >= maxSeriesCount {
			c.droppedSeries++
			return
		}
		byKey = map[string]*seriesPoints{}
		c.series[run] = byKey
	}
	buf, ok := byKey[key]
	if !ok {
		if c.numSeries >= maxSeriesCount {
			c.droppedSeries++
			return
		}
		buf = &seriesPoints{}
		byKey[key] = buf
		c.numSeries++
	}

	p := [2]float64{x, y}
	if buf.seen%c.keepEvery == 0 {
		buf.points = append(buf.points, p)
		c.held++
	}
	buf.seen++
	buf.last, buf.hasLast = p, true

	if c.held >= maxSeriesScanPoints {
		c.halve()
	}
}

// halve drops every second retained point of every trace and doubles the
// stride, freeing half the budget without disturbing the order of what is
// left -- which matters, because that order is what dedupeLastWins resolves
// ties with.
func (c *seriesCollector) halve() {
	held := 0
	for _, byKey := range c.series {
		for _, buf := range byKey {
			kept := buf.points[:0]
			for i := 0; i < len(buf.points); i += 2 {
				kept = append(kept, buf.points[i])
			}
			buf.points = kept
			held += len(kept)
		}
	}
	c.held = held
	c.keepEvery *= 2
}

func (ix *Indexer) scanParquetSeries(ctx context.Context, repo *store.Repo, req SeriesRequest,
	flushed map[int64]bool, appendPoint func(run, key string, x, y float64)) error {

	files, err := ix.store.ListRepoFiles(ctx, repo.ID, repo.DefaultBranch)
	if err != nil {
		return fmt.Errorf("list repo files: %w", err)
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}

	var layout *Layout
	for _, l := range DetectLayouts(paths, repo.Name) {
		if l.Project == req.Project {
			found := l
			layout = &found
			break
		}
	}
	if layout == nil {
		return nil
	}

	gitRepo, err := ix.git.Open(repo.StoragePath)
	if err != nil {
		return fmt.Errorf("open git repository: %w", err)
	}

	// Base file first, then its continuation files in part order: that is
	// chronological, which is what the "later value wins" tie-break in
	// Series() depends on.
	for _, metricsPath := range layout.MetricsFiles() {
		if err := ix.scanSeriesFile(ctx, gitRepo, repo, metricsPath, "", req, flushed, appendPoint); err != nil {
			return err
		}
	}
	if layout.SystemMetricsPath == "" {
		return nil
	}
	// Telemetry is an extra: a project whose _system.parquet cannot be read
	// still charts its own metrics.
	if err := ix.scanSeriesFile(ctx, gitRepo, repo, layout.SystemMetricsPath,
		SystemMetricPrefix, req, flushed, appendPoint); err != nil {
		slog.Warn("read system metric series", "repo", repo.FullName(),
			"project", req.Project, "path", layout.SystemMetricsPath, "error", err)
	}
	return nil
}

// scanSeriesFile turns one metrics-shaped parquet into points, naming every
// metric it finds with keyPrefix prepended (empty for the project's own
// metrics, SystemMetricPrefix for its telemetry).
//
// Two narrowings keep a chart from paying for the whole file. When the request
// names specific keys, only those metric columns (plus the handful of
// structural ones every row needs) are decoded -- see projectSeriesColumns.
// When it names specific runs, a row-group predicate on the run column lets
// the viewer skip the row groups whose statistics prove none of those runs is
// in them -- see seriesScanRequest. Both degrade to reading everything, which
// is what an unfiltered chart needs anyway.
func (ix *Indexer) scanSeriesFile(ctx context.Context, gitRepo *gitrepo.Repo, repo *store.Repo,
	filePath, keyPrefix string, req SeriesRequest, flushed map[int64]bool,
	appendPoint func(run, key string, x, y float64)) error {

	var scan viewer.ScanRequest
	if len(req.Keys) > 0 || len(req.Runs) > 0 {
		key, err := ix.objectKey(ctx, repo, gitRepo, repo.DefaultBranch, filePath)
		if err != nil {
			return fmt.Errorf("locate %s: %w", filePath, err)
		}
		schema, err := ix.viewer.Schema(ctx, key)
		if err != nil {
			return fmt.Errorf("read %s schema: %w", filePath, err)
		}
		var skip bool
		scan, skip = seriesScanRequest(schema, keyPrefix, req)
		if skip {
			// None of the requested keys live in this file -- nothing to gain
			// by decoding it at all.
			return nil
		}
	}

	// Rows without a step column fall back to their position in the file. A
	// pruned row group cannot disturb that counter: the counter is kept per
	// run, and a group is only skipped when its statistics prove none of the
	// requested runs is in it.
	counters := map[string]int64{}
	return ix.scanMetricRows(ctx, repo, gitRepo, repo.DefaultBranch, filePath, scan,
		func(run string, row map[string]any, cols map[string]bool) error {
			if id, ok := toInt(row[IngestIDColumn]); ok {
				flushed[id] = true
			}

			x, ok := xValue(row, cols, req.XAxis)
			if !ok {
				x = float64(counters[run])
			}
			counters[run]++

			forEachMetricValue(row, keyPrefix, func(name string, y float64) {
				appendPoint(run, name, x, y)
			})
			return nil
		})
}

// seriesScanRequest builds the viewer scan one Series() call needs against one
// file: the column projection from projectSeriesColumns, plus a row-group
// predicate restricting the file's run column to the requested runs. It
// reports skip when the file holds none of the requested keys.
//
// Pruning row groups is safe next to the ingest-id bookkeeping in
// scanSeriesFile even though it means some of the file's ingest ids are never
// seen: an id that goes unrecorded belongs to a run the caller did not ask
// for, and its live twin is dropped by the same run filter before it could
// reach the chart.
func seriesScanRequest(schema *viewer.Schema, keyPrefix string, req SeriesRequest) (scan viewer.ScanRequest, skip bool) {
	present := make(map[string]bool, len(schema.Columns))
	for _, c := range schema.Columns {
		present[c.Name] = true
	}

	if len(req.Keys) > 0 {
		scan.Columns = projectSeriesColumns(present, keyPrefix, req.Keys)
		if scan.Columns == nil {
			return scan, true
		}
	}
	if len(req.Runs) > 0 {
		// A file with no run column at all charts nothing (scanMetricRows
		// drops every one of its rows), so there is nothing to prune either.
		if runCol := runColumn(present); runCol != "" {
			scan.Predicates = []viewer.Predicate{{Column: runCol, AnyOf: req.Runs}}
		}
	}
	return scan, false
}

// projectSeriesColumns narrows a metrics-shaped parquet's columns to what one
// Series() call actually needs: whichever structural columns identify a row
// (run, step, timestamp, the flush's ingest id) plus the requested metric
// columns that exist in this file. It returns nil when none of the requested
// keys live here, letting the caller skip the file outright rather than
// decode it for nothing.
func projectSeriesColumns(present map[string]bool, keyPrefix string, keys []string) []string {
	var out []string
	for name := range structuralColumns {
		if present[name] {
			out = append(out, name)
		}
	}

	matched := false
	for _, k := range keys {
		name := k
		if keyPrefix != "" {
			if !strings.HasPrefix(k, keyPrefix) {
				continue
			}
			name = k[len(keyPrefix):]
		}
		if present[name] {
			out = append(out, name)
			matched = true
		}
	}
	if !matched {
		return nil
	}
	return out
}

// dedupeLastWins collapses points that share an x, keeping the last one. The
// input must already be sorted by x with equal x values in collection order
// (see Series), so "last" is the most recently logged value for that step.
func dedupeLastWins(points [][2]float64) [][2]float64 {
	out := points[:0]
	for i, p := range points {
		if i+1 < len(points) && points[i+1][0] == p[0] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// maxLiveSeriesPoints bounds one chart read of the ingest buffer. The buffer
// is drained every flush interval, so reaching this is a sign the flush is
// failing (or disabled) rather than normal operation; the cap is there so a
// runaway buffer degrades the chart instead of the process.
const maxLiveSeriesPoints = 500000

func (ix *Indexer) scanLiveSeries(ctx context.Context, repo *store.Repo, req SeriesRequest,
	flushed map[int64]bool, appendPoint func(run, key string, x, y float64)) error {

	project, err := ix.store.GetExpProject(ctx, repo.ID, req.Project)
	if err != nil {
		// No project row yet simply means nothing has been ingested live.
		return nil
	}
	points, err := ix.store.ListProjectPoints(ctx, project.ID, maxLiveSeriesPoints)
	if err != nil {
		return err
	}
	for _, p := range points {
		if flushed[p.ID] {
			continue
		}
		x := float64(p.Step)
		if req.XAxis == "time" {
			x = float64(p.TS.UnixMilli()) / 1000
		}
		for key, value := range p.Metrics {
			appendPoint(p.RunName, key, x, value)
		}
	}
	return nil
}

func xValue(row map[string]any, cols map[string]bool, axis string) (float64, bool) {
	if axis == "time" {
		if tsCol := timeColumn(cols); tsCol != "" {
			if ts, ok := toTime(row[tsCol]); ok {
				return float64(ts.UnixMilli()) / 1000, true
			}
		}
		return 0, false
	}
	if stepCol := stepColumn(cols); stepCol != "" {
		if step, ok := toInt(row[stepCol]); ok {
			return float64(step), true
		}
	}
	return 0, false
}

// downsample thins a series to at most max points while always keeping the
// first and last, so the chart's endpoints stay truthful.
func downsample(points [][2]float64, max int) [][2]float64 {
	if len(points) <= max || max < 2 {
		return points
	}
	out := make([][2]float64, 0, max)
	stride := float64(len(points)-1) / float64(max-1)
	for i := range max {
		idx := int(float64(i) * stride)
		if idx >= len(points) {
			idx = len(points) - 1
		}
		out = append(out, points[idx])
	}
	out[len(out)-1] = points[len(points)-1]
	return out
}

func toSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, v := range values {
		if v != "" {
			set[v] = true
		}
	}
	return set
}
