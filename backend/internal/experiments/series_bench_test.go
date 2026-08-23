// Benchmarks for the two read paths behind GET .../metrics
// (docs/dev/thinkingface-design.md §8, todo/exp-metrics-scale.md): the parquet
// export (scanParquetSeries) and the live ingest buffer (scanLiveSeries).
// Run with, e.g.:
//
//	go test ./internal/experiments/ -run '^$' -bench . -benchtime=1x -benchmem
//
// -benchtime=1x is important for the larger cases: the default adaptive
// benchtime would otherwise re-scan a multi-million-row file repeatedly to
// get a stable per-op average, which is not what a single chart load costs.
package experiments

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// benchRows builds `runs` runs of `steps` sequential steps each, `metrics`
// numeric columns per row -- the shape trackio's own log() produces (several
// metrics logged together at one step). Runs are laid out one after another
// (all of run-000's steps, then all of run-001's, ...), the order a batch
// export produces; benchRowsInterleaved below models a live flush's arrival
// order instead. Under the default layout the writer regroups either one by
// run, so the two differ only in what the legacy (single row group) variants
// measure.
func benchRows(runs, steps, metrics int) []map[string]any {
	rows := make([]map[string]any, 0, runs*steps)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for r := 0; r < runs; r++ {
		run := fmt.Sprintf("run-%03d", r)
		for s := 0; s < steps; s++ {
			row := map[string]any{
				"run_name":  run,
				"step":      int64(s),
				"timestamp": start.Add(time.Duration(s) * time.Second),
			}
			for m := 0; m < metrics; m++ {
				row[fmt.Sprintf("metric_%02d", m)] = float64(s) * float64(m+1) * 0.001
			}
			rows = append(rows, row)
		}
		start = start.Add(time.Hour)
	}
	return rows
}

// benchRowsInterleaved models a live flush's arrival order: every run advances
// one step per "tick", so the rows reach the writer with every run's history
// interleaved. Written through the legacy layout that becomes one row group
// holding every run -- the shape nothing can prune -- while the default layout
// regroups them by run first.
func benchRowsInterleaved(runs, steps, metrics int) []map[string]any {
	rows := make([]map[string]any, 0, runs*steps)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for s := 0; s < steps; s++ {
		ts := start.Add(time.Duration(s) * time.Second)
		for r := 0; r < runs; r++ {
			run := fmt.Sprintf("run-%03d", r)
			row := map[string]any{
				"run_name":  run,
				"step":      int64(s),
				"timestamp": ts,
			}
			for m := 0; m < metrics; m++ {
				row[fmt.Sprintf("metric_%02d", m)] = float64(s) * float64(m+1) * 0.001
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func benchColumns(metrics int) []flushColumn {
	cols := []flushColumn{
		stringColumn("run_name", false),
		int64Column("step"),
		timestampColumn("timestamp"),
	}
	for m := 0; m < metrics; m++ {
		cols = append(cols, doubleColumn(fmt.Sprintf("metric_%02d", m)))
	}
	return cols
}

// benchIndexer wires an Indexer to a real (temp-dir) git repository holding
// one committed metrics.parquet. IndexRepo's own project/run aggregation is
// deliberately skipped: Series() only needs the file listing (for layout
// detection), not the project/run rows, so running the full indexer would
// only add setup noise to a benchmark of Series() itself.
type benchIndexer struct {
	indexer *Indexer
	repo    *store.Repo
}

func setupBenchIndexer(b *testing.B, rows []map[string]any, metrics int, layout rowGroupLayout) *benchIndexer {
	b.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git binary not found in PATH; skipping")
	}
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(b.TempDir(), "store.db"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		b.Fatalf("migrate: %v", err)
	}

	git := gitrepo.NewManager(b.TempDir())
	obj := newMemStorage()
	parquet := viewer.New(obj, b.TempDir(), 0)

	if _, err := st.CreateUser(ctx, "bench", "bench@example.com", "x", false); err != nil {
		b.Fatalf("create user: %v", err)
	}
	ns, err := st.GetNamespace(ctx, "bench")
	if err != nil {
		b.Fatalf("get namespace: %v", err)
	}
	repo, err := st.CreateRepo(ctx, ns.ID, "trackio-metrics", "dataset", "", "main", store.NewStoragePath())
	if err != nil {
		b.Fatalf("create repo: %v", err)
	}
	if err := git.Init(repo.StoragePath, "main"); err != nil {
		b.Fatalf("git init: %v", err)
	}
	gitRepo, err := git.Open(repo.StoragePath)
	if err != nil {
		b.Fatalf("open git repo: %v", err)
	}

	columns := benchColumns(metrics)
	data, err := writeMetricsParquetLaidOut(columns, rows, layout)
	if err != nil {
		b.Fatalf("write metrics parquet: %v", err)
	}

	// The commit carries an LFS pointer with the payload in the bucket, which
	// is what both upload paths actually produce (.gitattributes routes
	// *.parquet to LFS). Committing the raw bytes instead would make every
	// Series() call decompress a multi-hundred-megabyte blob out of git before
	// it could read a single row -- harness cost that has nothing to do with
	// what a chart request costs in production.
	oid, size, err := gitrepo.HashSHA256(bytes.NewReader(data))
	if err != nil {
		b.Fatalf("hash metrics parquet: %v", err)
	}
	if err := obj.Put(ctx, storage.LFSKey(oid), bytes.NewReader(data), "application/vnd.apache.parquet"); err != nil {
		b.Fatalf("upload metrics parquet: %v", err)
	}
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch: "main", Message: "seed",
		Author: gitrepo.Signature{Name: "bench", Email: "bench@example.com"},
		Ops: []gitrepo.Op{
			{Kind: gitrepo.OpAdd, Path: ".gitattributes", Data: []byte(gitrepo.DefaultGitAttributes("dataset"))},
			{Kind: gitrepo.OpAdd, Path: "demo/metrics.parquet", Data: gitrepo.FormatLFSPointer(oid, size)},
		},
	}); err != nil {
		b.Fatalf("seed commit: %v", err)
	}

	entries, _, err := gitRepo.Tree("main", "", true)
	if err != nil {
		b.Fatalf("read tree: %v", err)
	}
	files := make([]store.RepoFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		f := store.RepoFile{Path: e.Path, Size: e.TargetSize(), BlobSHA: e.Hash.String()}
		if e.LFS != nil {
			oid := e.LFS.OID
			f.LFSOID = &oid
		}
		files = append(files, f)
	}
	if err := st.ReplaceRepoFiles(ctx, repo.ID, repo.DefaultBranch, files); err != nil {
		b.Fatalf("replace repo files: %v", err)
	}

	return &benchIndexer{indexer: NewIndexer(st, git, obj, parquet), repo: repo}
}

func benchmarkSeries(b *testing.B, rows []map[string]any, metrics int, keys []string) {
	benchmarkSeriesReq(b, rows, metrics, defaultRowGroupLayout(),
		SeriesRequest{Project: "demo", Keys: keys, Max: 1000})
}

// benchmarkSeriesReq measures one Series() call against a file written with
// the given row-group layout. legacyRowGroupLayout (the zero value) is the
// single-row-group shape route A produces and this package used to write, so
// pairing a benchmark with both layouts shows exactly what the run grouping
// and the row-group pruning are worth.
func benchmarkSeriesReq(b *testing.B, rows []map[string]any, metrics int, layout rowGroupLayout, req SeriesRequest) {
	bi := setupBenchIndexer(b, rows, metrics, layout)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		got, err := bi.indexer.Series(ctx, bi.repo, req)
		if err != nil {
			b.Fatalf("series: %v", err)
		}
		if len(got) == 0 {
			b.Fatal("series: no data returned, benchmark is measuring nothing")
		}
	}
}

// legacyRowGroupLayout is the single-row-group, arrival-order shape both a
// route-A trackio export and this package's pre-grouping flush produce. It is
// the "before" column of every comparison below, and the shape the pruning
// must degrade gracefully on.
func legacyRowGroupLayout() rowGroupLayout { return rowGroupLayout{} }

// ---- parquet path, sorted-by-run write order (what option 1 produces) ----

func BenchmarkSeries_Parquet_1run_1kstep_10metric_AllKeys(b *testing.B) {
	benchmarkSeries(b, benchRows(1, 1_000, 10), 10, nil)
}

func BenchmarkSeries_Parquet_10run_10kstep_10metric_AllKeys(b *testing.B) {
	benchmarkSeries(b, benchRows(10, 10_000, 10), 10, nil)
}

func BenchmarkSeries_Parquet_50run_100kstep_10metric_AllKeys(b *testing.B) {
	benchmarkSeries(b, benchRows(50, 100_000, 10), 10, nil)
}

func BenchmarkSeries_Parquet_50run_100kstep_10metric_OneKey(b *testing.B) {
	benchmarkSeries(b, benchRows(50, 100_000, 10), 10, []string{"metric_00"})
}

// BenchmarkSeries_Parquet_50run_100kstep_10metric_FiveRunsAllKeys is the
// closest proxy to what the dashboard actually requests today
// (frontend/components/experiments/experiment-dashboard.tsx preselects the
// first 5 runs and never sends `keys=`): a run filter narrows what gets
// charted, but every column still has to be decoded to find out which run a
// row belongs to, so this is expected to cost about the same as AllKeys.
func benchFiveRunsRequest() SeriesRequest {
	return SeriesRequest{
		Project: "demo",
		Runs:    []string{"run-000", "run-001", "run-002", "run-003", "run-004"},
		Max:     1000,
	}
}

func benchOneRunOneKeyRequest() SeriesRequest {
	return SeriesRequest{Project: "demo", Runs: []string{"run-000"}, Keys: []string{"metric_00"}, Max: 1000}
}

// BenchmarkSeries_Parquet_50run_100kstep_10metric_FiveRunsAllKeys is what the
// dashboard actually requests today
// (frontend/components/experiments/experiment-dashboard.tsx preselects the
// first 5 runs and never sends `keys=`). With rows grouped by run, the run
// column's row-group statistics let the viewer decode only the row groups
// those five runs live in; the _Legacy_ variant below is the same request
// against the old single-row-group file, where nothing can be skipped.
func BenchmarkSeries_Parquet_50run_100kstep_10metric_FiveRunsAllKeys(b *testing.B) {
	benchmarkSeriesReq(b, benchRows(50, 100_000, 10), 10, defaultRowGroupLayout(), benchFiveRunsRequest())
}

func BenchmarkSeries_ParquetLegacy_50run_100kstep_10metric_FiveRunsAllKeys(b *testing.B) {
	benchmarkSeriesReq(b, benchRows(50, 100_000, 10), 10, legacyRowGroupLayout(), benchFiveRunsRequest())
}

func BenchmarkSeries_Parquet_50run_100kstep_10metric_OneRunOneKey(b *testing.B) {
	benchmarkSeriesReq(b, benchRows(50, 100_000, 10), 10, defaultRowGroupLayout(), benchOneRunOneKeyRequest())
}

func BenchmarkSeries_ParquetLegacy_50run_100kstep_10metric_OneRunOneKey(b *testing.B) {
	benchmarkSeriesReq(b, benchRows(50, 100_000, 10), 10, legacyRowGroupLayout(), benchOneRunOneKeyRequest())
}

// The interleaved rows below are today's flush arrival order. Written through
// the default layout they are regrouped by run, so the run filter prunes just
// as well as it does for an already-sorted export; the legacy pairing shows
// the same file with neither the grouping nor the row groups.
func BenchmarkSeries_ParquetInterleaved_50run_100kstep_10metric_FiveRunsAllKeys(b *testing.B) {
	benchmarkSeriesReq(b, benchRowsInterleaved(50, 100_000, 10), 10, defaultRowGroupLayout(), benchFiveRunsRequest())
}

func BenchmarkSeries_ParquetInterleavedLegacy_50run_100kstep_10metric_FiveRunsAllKeys(b *testing.B) {
	benchmarkSeriesReq(b, benchRowsInterleaved(50, 100_000, 10), 10, legacyRowGroupLayout(), benchFiveRunsRequest())
}

// ---- parquet path, interleaved write order (today's actual flush order) ----

func BenchmarkSeries_ParquetInterleaved_50run_100kstep_10metric_AllKeys(b *testing.B) {
	benchmarkSeries(b, benchRowsInterleaved(50, 100_000, 10), 10, nil)
}

// ---- live (exp_points) path ----

func setupBenchLive(b *testing.B, points int) (*Indexer, *store.Repo) {
	b.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git binary not found in PATH; skipping")
	}
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(b.TempDir(), "store.db"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		b.Fatalf("migrate: %v", err)
	}

	git := gitrepo.NewManager(b.TempDir())
	obj := newMemStorage()
	parquet := viewer.New(obj, b.TempDir(), 0)

	if _, err := st.CreateUser(ctx, "bench", "bench@example.com", "x", false); err != nil {
		b.Fatalf("create user: %v", err)
	}
	ns, err := st.GetNamespace(ctx, "bench")
	if err != nil {
		b.Fatalf("get namespace: %v", err)
	}
	repo, err := st.CreateRepo(ctx, ns.ID, "trackio-metrics", "dataset", "", "main", store.NewStoragePath())
	if err != nil {
		b.Fatalf("create repo: %v", err)
	}
	if err := git.Init(repo.StoragePath, "main"); err != nil {
		b.Fatalf("git init: %v", err)
	}

	projectID, err := st.UpsertExpProject(ctx, repo.ID, "demo")
	if err != nil {
		b.Fatalf("upsert project: %v", err)
	}
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runID, err := st.UpsertExpRun(ctx, projectID, "run-live", "running", nil, nil, []string{"loss"}, int64(points), 0, &started)
	if err != nil {
		b.Fatalf("upsert run: %v", err)
	}

	const batch = 5000
	buf := make([]store.MetricPoint, 0, batch)
	for i := 0; i < points; i++ {
		buf = append(buf, store.MetricPoint{
			Step: int64(i),
			TS:   started.Add(time.Duration(i) * time.Second),
			Metrics: map[string]float64{
				"loss": float64(i) * 0.001,
			},
		})
		if len(buf) == batch {
			if err := st.InsertPoints(ctx, runID, buf); err != nil {
				b.Fatalf("insert points: %v", err)
			}
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		if err := st.InsertPoints(ctx, runID, buf); err != nil {
			b.Fatalf("insert points: %v", err)
		}
	}

	return NewIndexer(st, git, obj, parquet), repo
}

func benchmarkSeriesLive(b *testing.B, points int) {
	indexer, repo := setupBenchLive(b, points)
	ctx := context.Background()
	req := SeriesRequest{Project: "demo", Max: 1000}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		got, err := indexer.Series(ctx, repo, req)
		if err != nil {
			b.Fatalf("series: %v", err)
		}
		if len(got) == 0 {
			b.Fatal("series: no data returned, benchmark is measuring nothing")
		}
	}
}

func BenchmarkSeries_Live_1kpoints(b *testing.B)   { benchmarkSeriesLive(b, 1_000) }
func BenchmarkSeries_Live_10kpoints(b *testing.B)  { benchmarkSeriesLive(b, 10_000) }
func BenchmarkSeries_Live_100kpoints(b *testing.B) { benchmarkSeriesLive(b, 100_000) }
