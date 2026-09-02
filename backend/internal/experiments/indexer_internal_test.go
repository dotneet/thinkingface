package experiments

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

func TestToFloat_RejectsNaNAndInf(t *testing.T) {
	tests := []struct {
		name string
		v    any
	}{
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := toFloat(tt.v)
			if ok {
				t.Errorf("toFloat(%v) ok = true, want false (must not leak a value that breaks JSON encoding)", tt.v)
			}
		})
	}
}

func TestToFloat_AcceptsOrdinaryNumericShapes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want float64
	}{
		{"float64", float64(3.5), 3.5},
		{"float32", float32(2.5), 2.5},
		{"int64", int64(7), 7},
		{"int", int(9), 9},
		{"bool true", true, 1},
		{"bool false", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat(tt.v)
			if !ok {
				t.Fatalf("toFloat(%v) ok = false, want true", tt.v)
			}
			if got != tt.want {
				t.Errorf("toFloat(%v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

func TestToFloat_RejectsUnsupportedTypes(t *testing.T) {
	tests := []any{"3.5", nil, []byte("x"), map[string]any{}}
	for _, v := range tests {
		if _, ok := toFloat(v); ok {
			t.Errorf("toFloat(%#v) ok = true, want false", v)
		}
	}
}

func TestToInt_RejectsNaNAndInf(t *testing.T) {
	tests := []float64{math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, v := range tests {
		if _, ok := toInt(v); ok {
			t.Errorf("toInt(%v) ok = true, want false", v)
		}
	}
}

func TestToInt_VariousShapes(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int64
		ok   bool
	}{
		{"int64", int64(42), 42, true},
		{"int", int(7), 7, true},
		{"float64", float64(3.9), 3, true}, // truncates, matching (int64) cast semantics
		{"string valid", "123", 123, true},
		{"string invalid", "abc", 0, false},
		{"unsupported", []byte("x"), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toInt(tt.v)
			if ok != tt.ok {
				t.Fatalf("toInt(%v) ok = %v, want %v", tt.v, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("toInt(%v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

func TestToString(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"string", "hello", "hello"},
		{"nil", nil, ""},
		{"int", 42, "42"},
		{"float", 3.5, "3.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toString(tt.v); got != tt.want {
				t.Errorf("toString(%v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}

func TestToTime_RFC3339(t *testing.T) {
	ts, ok := toTime("2024-03-15T10:30:00Z")
	if !ok {
		t.Fatalf("toTime(RFC3339 string) ok = false")
	}
	want := time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)
	if !ts.Equal(want) {
		t.Errorf("toTime(RFC3339) = %v, want %v", ts, want)
	}
}

func TestToTime_SecondsEpoch(t *testing.T) {
	// A plausible "seconds since epoch" value, e.g. 2024-01-01ish, well above
	// 1e9 but well below 1e12 so it's not confused with milliseconds.
	var epochSeconds int64 = 1_700_000_000
	ts, ok := toTime(epochSeconds)
	if !ok {
		t.Fatalf("toTime(seconds epoch int64) ok = false")
	}
	want := time.Unix(epochSeconds, 0)
	if !ts.Equal(want) {
		t.Errorf("toTime(%d) = %v, want %v", epochSeconds, ts, want)
	}
}

func TestToTime_MillisecondsEpoch(t *testing.T) {
	// Above 1e12: interpreted as milliseconds.
	var epochMillis int64 = 1_700_000_000_000
	ts, ok := toTime(epochMillis)
	if !ok {
		t.Fatalf("toTime(millis epoch int64) ok = false")
	}
	want := time.UnixMilli(epochMillis)
	if !ts.Equal(want) {
		t.Errorf("toTime(%d) = %v, want %v", epochMillis, ts, want)
	}
}

func TestToTime_RejectsSmallNumbers(t *testing.T) {
	// Below the 1e9 heuristic threshold: not treated as a timestamp at all
	// (e.g. this could be a step count, not a unix time).
	if _, ok := toTime(int64(42)); ok {
		t.Errorf("toTime(42) ok = true, want false (too small to be a plausible timestamp)")
	}
}

func TestToTime_UnsupportedType(t *testing.T) {
	if _, ok := toTime([]byte("x")); ok {
		t.Errorf("toTime(unsupported type) ok = true, want false")
	}
}

// A run that already exists keeps its own status, a run that does not is
// "finished", and a database failure is neither. The last case is the
// regression: `err == nil` used to decide this, so any transient error made
// the indexer declare a live run finished -- a state nothing moves it back
// out of.
func TestIndexedRunStatus(t *testing.T) {
	if got, err := indexedRunStatus("r", nil); err != nil || got != "" {
		t.Errorf(`indexedRunStatus(nil) = (%q, %v), want ("", nil)`, got, err)
	}
	if got, err := indexedRunStatus("r", store.ErrNotFound); err != nil || got != "finished" {
		t.Errorf(`indexedRunStatus(ErrNotFound) = (%q, %v), want ("finished", nil)`, got, err)
	}
	boom := errors.New("connection reset by peer")
	got, err := indexedRunStatus("r", boom)
	if !errors.Is(err, boom) {
		t.Fatalf("indexedRunStatus(%v) error = %v, want it to wrap the cause", boom, err)
	}
	if got == "finished" {
		t.Error("a database failure was read as a new run and would have finished a live one")
	}
	if !strings.Contains(err.Error(), `"r"`) {
		t.Errorf("error %q does not name the run", err)
	}
}

// TestIndexRepo_KeepsARunThatLoggedNoPoints covers the run shape the prune
// used to destroy.
//
// `trackio.init(...)` followed by `finish()` with no `log()` in between -- a
// job that crashed before its first metric, or one that only records artifacts
// -- creates a run through the API alone. It is in no parquet and holds no
// buffered points, so DeleteProjectRunsNotIn saw exactly a ghost: it listed
// fine, and then vanished on the next index, which happens after any push and
// after any flush. Its tags, note, baseline flag and produced-model rows went
// with it through the foreign key's cascade.
//
// num_points is what tells the two apart: the indexer builds a run from rows
// it scanned, so a run it created always has some, and the upsert only ever
// raises the count.
func TestIndexRepo_KeepsARunThatLoggedNoPoints(t *testing.T) {
	h := newExpHarness(t)
	h.commitParquet("demo.parquet",
		[]flushColumn{
			stringColumn("run_name", false),
			int64Column("step"),
			doubleColumn("loss"),
		},
		[]map[string]any{{"run_name": "logged", "step": int64(1), "loss": 0.5}})

	project, err := h.st.GetExpProject(h.ctx, h.repo.ID, "demo")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	// What POST .../finish writes for a run that never logged anything.
	if _, err := h.st.UpsertExpRunWith(h.ctx, project.ID, store.ExpRunUpsert{
		Name: "crashed-early", Status: "failed",
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if _, err := h.st.UpdateExpRunAnnotation(h.ctx, project.ID, "crashed-early",
		store.RunAnnotation{Note: ptrTo("OOM before the first step")}); err != nil {
		t.Fatalf("annotate run: %v", err)
	}
	// A genuine ghost for contrast: a run the indexer once wrote (hence a
	// point count) that a rewritten export no longer lists.
	if _, err := h.st.UpsertExpRunWith(h.ctx, project.ID, store.ExpRunUpsert{
		Name: "dropped-from-export", Status: "finished", NumPoints: 4,
	}); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	h.reindex()

	runs, err := h.st.ListExpRuns(h.ctx, project.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	names := map[string]store.ExpRun{}
	for _, r := range runs {
		names[r.Name] = r
	}
	if _, ok := names["logged"]; !ok {
		t.Errorf("runs = %v, want the parquet's own run", names)
	}
	kept, ok := names["crashed-early"]
	if !ok {
		t.Fatalf("runs = %v, want the API-created run to survive the index", names)
	}
	if kept.Note != "OOM before the first step" {
		t.Errorf("note = %q, want the annotation to survive with it", kept.Note)
	}
	if _, ok := names["dropped-from-export"]; ok {
		t.Errorf("runs = %v, want the run that vanished from the export to be pruned", names)
	}
}

func ptrTo[T any](v T) *T { return &v }
