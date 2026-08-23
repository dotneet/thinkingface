package viewer

import (
	"bytes"
	"context"
	"testing"

	"github.com/parquet-go/parquet-go"
)

// pruneRow is a metrics-shaped row: a string column whose row groups can be
// told apart by their statistics, plus an integer column to range-test.
type pruneRow struct {
	RunName string  `parquet:"run_name"`
	Step    int64   `parquet:"step"`
	Loss    float64 `parquet:"loss"`
}

const pruneStepsPerRun = 20

// buildRunGroupedParquet writes one row group per run (the layout
// experiments.writeMetricsParquet produces), so a run predicate can skip
// whole groups.
func buildRunGroupedParquet(t *testing.T, runs []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[pruneRow](&buf)
	for _, run := range runs {
		batch := make([]pruneRow, pruneStepsPerRun)
		for i := range batch {
			batch[i] = pruneRow{RunName: run, Step: int64(i), Loss: float64(i) * 0.5}
		}
		if _, err := w.Write(batch); err != nil {
			t.Fatalf("write %s: %v", run, err)
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("flush %s: %v", run, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

// buildInterleavedParquet writes every run into a single row group, the shape
// a trackio export (or this package's readers before run grouping existed)
// produces. No predicate can prune it.
func buildInterleavedParquet(t *testing.T, runs []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[pruneRow](&buf)
	rows := make([]pruneRow, 0, len(runs)*pruneStepsPerRun)
	for i := 0; i < pruneStepsPerRun; i++ {
		for _, run := range runs {
			rows = append(rows, pruneRow{RunName: run, Step: int64(i), Loss: float64(i) * 0.5})
		}
	}
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes()
}

func scanRuns(t *testing.T, data []byte, key string, req ScanRequest) map[string]int {
	t.Helper()
	st := newMemStorage()
	putParquet(t, st, key, data)
	r := newTestReader(t, st)

	seen := map[string]int{}
	err := r.Scan(context.Background(), key, req, func(row map[string]any) error {
		run, _ := row["run_name"].(string)
		seen[run]++
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return seen
}

var pruneRuns = []string{"run-000", "run-001", "run-002", "run-003", "run-004"}

func TestScan_PredicateSkipsRowGroups(t *testing.T) {
	data := buildRunGroupedParquet(t, pruneRuns)
	seen := scanRuns(t, data, "lfs/pr/un/grouped.parquet", ScanRequest{
		Predicates: []Predicate{{Column: "run_name", AnyOf: []string{"run-001", "run-003"}}},
	})

	want := map[string]int{"run-001": pruneStepsPerRun, "run-003": pruneStepsPerRun}
	if len(seen) != len(want) {
		t.Fatalf("scanned runs = %v, want only %v", seen, want)
	}
	for run, n := range want {
		if seen[run] != n {
			t.Errorf("run %s: scanned %d rows, want %d", run, seen[run], n)
		}
	}
}

// A predicate matching nothing must skip every row group rather than fall
// back to reading the file.
func TestScan_PredicateMatchingNothingReadsNothing(t *testing.T) {
	data := buildRunGroupedParquet(t, pruneRuns)
	seen := scanRuns(t, data, "lfs/pr/un/none.parquet", ScanRequest{
		Predicates: []Predicate{{Column: "run_name", AnyOf: []string{"run-999"}}},
	})
	if len(seen) != 0 {
		t.Fatalf("scanned %v, want nothing", seen)
	}
}

// The predicate is a row-group hint, not a row filter: a file whose single
// row group holds every run is read in full, and the caller still sees rows
// it did not ask for. This is the fallback every pre-existing metrics.parquet
// takes.
func TestScan_PredicateFallsBackOnUnprunableFile(t *testing.T) {
	data := buildInterleavedParquet(t, pruneRuns)
	seen := scanRuns(t, data, "lfs/pr/un/interleaved.parquet", ScanRequest{
		Predicates: []Predicate{{Column: "run_name", AnyOf: []string{"run-001"}}},
	})
	if len(seen) != len(pruneRuns) {
		t.Fatalf("scanned runs = %v, want all %d of them", seen, len(pruneRuns))
	}
	for _, run := range pruneRuns {
		if seen[run] != pruneStepsPerRun {
			t.Errorf("run %s: scanned %d rows, want %d", run, seen[run], pruneStepsPerRun)
		}
	}
}

// A predicate naming a column the file does not have (or one whose type the
// restriction does not fit) prunes nothing rather than everything.
func TestScan_PredicateOnAbsentOrMismatchedColumn(t *testing.T) {
	data := buildRunGroupedParquet(t, pruneRuns)
	total := len(pruneRuns) * pruneStepsPerRun

	cases := map[string]Predicate{
		"unknown column":            {Column: "no_such_column", AnyOf: []string{"run-001"}},
		"string predicate on int":   {Column: "step", AnyOf: []string{"run-001"}},
		"range predicate on string": {Column: "run_name", Min: ptr(int64(0)), Max: ptr(int64(1))},
		"unsupported column type":   {Column: "loss", Min: ptr(int64(0)), Max: ptr(int64(1))},
	}
	for name, pred := range cases {
		t.Run(name, func(t *testing.T) {
			seen := scanRuns(t, data, "lfs/pr/un/grouped.parquet", ScanRequest{Predicates: []Predicate{pred}})
			got := 0
			for _, n := range seen {
				got += n
			}
			if got != total {
				t.Fatalf("scanned %d rows, want all %d", got, total)
			}
		})
	}
}

// An empty predicate set, and a predicate with no restriction in it, both
// leave the scan alone.
func TestScan_EmptyPredicateReadsEverything(t *testing.T) {
	data := buildRunGroupedParquet(t, pruneRuns)
	total := len(pruneRuns) * pruneStepsPerRun

	for name, req := range map[string]ScanRequest{
		"no predicates": {},
		"predicate with no restriction": {
			Predicates: []Predicate{{Column: "run_name"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			seen := scanRuns(t, data, "lfs/pr/un/grouped.parquet", req)
			got := 0
			for _, n := range seen {
				got += n
			}
			if got != total {
				t.Fatalf("scanned %d rows, want all %d", got, total)
			}
		})
	}
}

// Every run's rows carry steps 0..pruneStepsPerRun-1, so a step range predicate
// prunes nothing here; what it must not do is drop rows that are in range.
func TestScan_IntRangePredicate(t *testing.T) {
	data := buildRunGroupedParquet(t, pruneRuns)

	t.Run("overlapping range keeps every group", func(t *testing.T) {
		seen := scanRuns(t, data, "lfs/pr/un/grouped.parquet", ScanRequest{
			Predicates: []Predicate{{Column: "step", Min: ptr(int64(5)), Max: ptr(int64(10))}},
		})
		if len(seen) != len(pruneRuns) {
			t.Fatalf("scanned runs = %v, want all %d", seen, len(pruneRuns))
		}
	})

	t.Run("range past the end prunes everything", func(t *testing.T) {
		seen := scanRuns(t, data, "lfs/pr/un/grouped.parquet", ScanRequest{
			Predicates: []Predicate{{Column: "step", Min: ptr(int64(pruneStepsPerRun + 1))}},
		})
		if len(seen) != 0 {
			t.Fatalf("scanned %v, want nothing", seen)
		}
	})
}

// Predicates are ANDed: a row group survives only if every one of them could
// be satisfied by it.
func TestScan_PredicatesAreConjunctive(t *testing.T) {
	data := buildRunGroupedParquet(t, pruneRuns)
	seen := scanRuns(t, data, "lfs/pr/un/grouped.parquet", ScanRequest{
		Predicates: []Predicate{
			{Column: "run_name", AnyOf: []string{"run-002"}},
			{Column: "step", Max: ptr(int64(-1))},
		},
	})
	if len(seen) != 0 {
		t.Fatalf("scanned %v, want nothing", seen)
	}
}

// Pruning must compose with the column projection: the predicate column does
// not have to be one of the decoded columns.
func TestScan_PredicateWithColumnProjection(t *testing.T) {
	data := buildRunGroupedParquet(t, pruneRuns)
	st := newMemStorage()
	const key = "lfs/pr/un/proj.parquet"
	putParquet(t, st, key, data)
	r := newTestReader(t, st)

	rows := 0
	err := r.Scan(context.Background(), key, ScanRequest{
		Columns:    []string{"loss"},
		Predicates: []Predicate{{Column: "run_name", AnyOf: []string{"run-004"}}},
	}, func(row map[string]any) error {
		rows++
		if _, ok := row["run_name"]; ok {
			t.Fatalf("run_name should not have been decoded: %#v", row)
		}
		if _, ok := row["loss"].(float64); !ok {
			t.Fatalf("loss missing or not float64: %#v", row)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rows != pruneStepsPerRun {
		t.Fatalf("scanned %d rows, want %d", rows, pruneStepsPerRun)
	}
}

func ptr[T any](v T) *T { return &v }
