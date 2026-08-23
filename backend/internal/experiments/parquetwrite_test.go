package experiments

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// readBackRows renders columns/rows through writeMetricsParquetLaidOut and
// reads the file back, returning the rows in file order plus the number of row
// groups the writer produced.
func readBackRows(t *testing.T, columns []flushColumn, rows []map[string]any,
	layout rowGroupLayout) ([]map[string]any, int) {

	t.Helper()
	data, err := writeMetricsParquetLaidOut(columns, rows, layout)
	if err != nil {
		t.Fatalf("write metrics parquet: %v", err)
	}

	ctx := context.Background()
	obj := newMemStorage()
	const key = "lfs/ab/cd/metrics.parquet"
	if err := obj.Put(ctx, key, bytes.NewReader(data), "application/octet-stream"); err != nil {
		t.Fatalf("put: %v", err)
	}
	r := viewer.New(obj, t.TempDir(), 0)

	schema, err := r.Schema(ctx, key)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var out []map[string]any
	if err := r.Scan(ctx, key, viewer.ScanRequest{}, func(row map[string]any) error {
		out = append(out, row)
		return nil
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out, schema.NumRowGroups
}

// commitParquetData puts an already-rendered parquet on the default branch the
// way a push would, and refreshes the indexes that depend on it. It is
// commitParquet (system_metrics_test.go) for a caller that needs to control
// the file's byte-level layout rather than just its rows.
func (h *expHarness) commitParquetData(path string, data []byte) {
	h.t.Helper()
	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		h.t.Fatalf("open git repo: %v", err)
	}
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch: h.repo.DefaultBranch, Message: "sync " + path,
		Author: gitrepo.Signature{Name: "trackio", Email: "trackio@example.com"},
		Ops:    []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: data}},
	}); err != nil {
		h.t.Fatalf("commit %s: %v", path, err)
	}
	h.reindex()
}

func metricColumns() []flushColumn {
	return []flushColumn{
		stringColumn("run_name", false),
		int64Column("step"),
		doubleColumn("loss"),
	}
}

// TestWriteMetricsParquet_GroupsRunsButKeepsPerRunOrder is the correctness
// condition for the row-group layout: runs may be reordered relative to each
// other (that is the whole point -- it makes a row group's run statistics
// narrow enough to prune on), but the rows *within* one run must come out in
// exactly the order they went in, because that order is what resolves two
// values logged at one step (docs/thinkingface-design.md §8).
func TestWriteMetricsParquet_GroupsRunsButKeepsPerRunOrder(t *testing.T) {
	// Arrival order: two runs interleaved, and run "b" logs step 1 twice --
	// first 9.0, later 1.0. The later value must stay later.
	rows := []map[string]any{
		{"run_name": "b", "step": int64(1), "loss": 9.0},
		{"run_name": "a", "step": int64(1), "loss": 0.1},
		{"run_name": "b", "step": int64(2), "loss": 0.2},
		{"run_name": "a", "step": int64(2), "loss": 0.3},
		{"run_name": "b", "step": int64(1), "loss": 1.0},
		{"run_name": "a", "step": int64(3), "loss": 0.4},
	}

	got, groups := readBackRows(t, metricColumns(), rows, defaultRowGroupLayout())
	if groups != 1 {
		t.Errorf("row groups = %d, want 1 (far below minRowGroupRows)", groups)
	}
	if len(got) != len(rows) {
		t.Fatalf("read back %d rows, want %d", len(got), len(rows))
	}

	// Runs are contiguous and in name order.
	wantRuns := []string{"a", "a", "a", "b", "b", "b"}
	for i, want := range wantRuns {
		if got[i]["run_name"] != want {
			t.Fatalf("row %d run = %v, want %v (rows: %v)", i, got[i]["run_name"], want, got)
		}
	}
	// Within each run, arrival order survives -- including b's two step-1
	// values, in the order they were logged.
	wantLoss := []float64{0.1, 0.3, 0.4, 9.0, 0.2, 1.0}
	for i, want := range wantLoss {
		if got[i]["loss"] != want {
			t.Errorf("row %d loss = %v, want %v", i, got[i]["loss"], want)
		}
	}
}

// A file whose rows name no run (no run column at all) must come out
// completely untouched: series.go charts those rows against their position.
func TestWriteMetricsParquet_NoRunColumnKeepsFileOrder(t *testing.T) {
	columns := []flushColumn{int64Column("step"), doubleColumn("loss")}
	rows := []map[string]any{
		{"step": int64(3), "loss": 3.0},
		{"step": int64(1), "loss": 1.0},
		{"step": int64(2), "loss": 2.0},
	}
	got, _ := readBackRows(t, columns, rows, defaultRowGroupLayout())
	if len(got) != 3 {
		t.Fatalf("read back %d rows, want 3", len(got))
	}
	for i, want := range []float64{3.0, 1.0, 2.0} {
		if got[i]["loss"] != want {
			t.Fatalf("row %d loss = %v, want %v (file order must be preserved)", i, got[i]["loss"], want)
		}
	}
}

// The layout only pays off if the writer actually cuts row groups at run
// boundaries once a run is big enough to fill one.
func TestWriteMetricsParquet_CutsRowGroupsAtRunBoundaries(t *testing.T) {
	const runs, steps = 3, minRowGroupRows + 100
	rows := make([]map[string]any, 0, runs*steps)
	for s := range steps {
		for r := range runs {
			rows = append(rows, map[string]any{
				"run_name": string(rune('a' + r)),
				"step":     int64(s),
				"loss":     float64(s),
			})
		}
	}

	got, groups := readBackRows(t, metricColumns(), rows, defaultRowGroupLayout())
	if groups != runs {
		t.Fatalf("row groups = %d, want %d (one per run)", groups, runs)
	}
	if len(got) != runs*steps {
		t.Fatalf("read back %d rows, want %d", len(got), runs*steps)
	}
	for i, row := range got {
		want := string(rune('a' + i/steps))
		if row["run_name"] != want {
			t.Fatalf("row %d run = %v, want %v", i, row["run_name"], want)
		}
	}
}

// One very long run must still be cut, or it becomes a single row group that
// nothing can skip -- and a row group that big is also a memory spike for the
// writer.
func TestWriteMetricsParquet_CapsRowGroupSizeForOneRun(t *testing.T) {
	const steps = maxRowGroupRows + 10
	rows := make([]map[string]any, 0, steps)
	for s := range steps {
		rows = append(rows, map[string]any{"run_name": "solo", "step": int64(s), "loss": float64(s)})
	}

	got, groups := readBackRows(t, metricColumns(), rows, defaultRowGroupLayout())
	if groups != 2 {
		t.Fatalf("row groups = %d, want 2 (%d rows capped at %d)", groups, steps, maxRowGroupRows)
	}
	if len(got) != steps {
		t.Fatalf("read back %d rows, want %d", len(got), steps)
	}
	for i, row := range got {
		if row["step"] != int64(i) {
			t.Fatalf("row %d step = %v, want %d", i, row["step"], i)
		}
	}
}

// The legacy layout is what the tests and benchmarks compare against; it must
// really produce the single row group a route-A export has.
func TestWriteMetricsParquet_LegacyLayoutIsOneRowGroupInArrivalOrder(t *testing.T) {
	rows := []map[string]any{
		{"run_name": "b", "step": int64(1), "loss": 1.0},
		{"run_name": "a", "step": int64(1), "loss": 2.0},
		{"run_name": "b", "step": int64(2), "loss": 3.0},
	}
	got, groups := readBackRows(t, metricColumns(), rows, rowGroupLayout{})
	if groups != 1 {
		t.Fatalf("row groups = %d, want 1", groups)
	}
	want := []string{"b", "a", "b"}
	for i, w := range want {
		if got[i]["run_name"] != w {
			t.Fatalf("row %d run = %v, want %v (arrival order)", i, got[i]["run_name"], w)
		}
	}
}

// TestSeries_RunFilterAgreesAcrossLayouts is the end-to-end guarantee behind
// the pruning: whether the file was written by this package (grouped, several
// row groups, prunable) or arrived from trackio (one row group, arrival order,
// unprunable), Series() must return the same chart.
func TestSeries_RunFilterAgreesAcrossLayouts(t *testing.T) {
	rows := []map[string]any{}
	for s := 1; s <= 40; s++ {
		for _, run := range []string{"r1", "r2", "r3"} {
			rows = append(rows, map[string]any{
				"run_name": run, "step": int64(s), "loss": float64(s) * 0.1,
			})
		}
	}
	// r2 re-logs step 40 with a different value; the later one must win in
	// both layouts.
	rows = append(rows, map[string]any{"run_name": "r2", "step": int64(40), "loss": 99.0})

	req := SeriesRequest{Project: "demo", Runs: []string{"r1", "r2"}, Max: 1000}

	seriesFor := func(layout rowGroupLayout) []Series {
		h := newExpHarness(t)
		data, err := writeMetricsParquetLaidOut(metricColumns(), rows, layout)
		if err != nil {
			t.Fatalf("write metrics parquet: %v", err)
		}
		h.commitParquetData("demo/metrics.parquet", data)
		got, err := h.indexer.Series(h.ctx, h.repo, req)
		if err != nil {
			t.Fatalf("series: %v", err)
		}
		return got
	}

	grouped := seriesFor(defaultRowGroupLayout())
	legacy := seriesFor(rowGroupLayout{})

	if !reflect.DeepEqual(grouped, legacy) {
		t.Fatalf("grouped layout series = %#v,\nlegacy layout series = %#v", grouped, legacy)
	}
	if len(grouped) != 2 {
		t.Fatalf("series = %#v, want exactly the two requested runs", grouped)
	}
	for _, s := range grouped {
		if s.Run != "r1" && s.Run != "r2" {
			t.Errorf("series for unrequested run %q leaked through", s.Run)
		}
		if len(s.Points) != 40 {
			t.Errorf("run %s: %d points, want 40 (step 40 collapsed, not duplicated)", s.Run, len(s.Points))
		}
		last := s.Points[len(s.Points)-1]
		want := 4.0
		if s.Run == "r2" {
			want = 99.0 // the re-logged value, not the one it replaced
		}
		if last[1] != want {
			t.Errorf("run %s: last value = %v, want %v", s.Run, last[1], want)
		}
	}
}
