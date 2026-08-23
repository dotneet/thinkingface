package experiments

import (
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// metricPoint is one logged point, spelled out so a test can control the value
// at a given step (the shared ingest helper derives it from the step).
type metricPoint struct {
	step   int64
	values map[string]float64
}

// ingestPoints writes a batch the way the ingest handler does, in the order
// given: exp_points ids are assigned in insertion order, and that order is
// what decides which of two values at one step the chart shows.
func (h *expHarness) ingestPoints(project, run string, points []metricPoint) int64 {
	h.t.Helper()
	projectID, err := h.st.UpsertExpProject(h.ctx, h.repo.ID, project)
	if err != nil {
		h.t.Fatalf("upsert project: %v", err)
	}
	started := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	var lastStep int64
	keys := map[string]bool{}
	for _, p := range points {
		if p.step > lastStep {
			lastStep = p.step
		}
		for k := range p.values {
			keys[k] = true
		}
	}
	keyList := make([]string, 0, len(keys))
	for k := range keys {
		keyList = append(keyList, k)
	}
	runID, err := h.st.UpsertExpRun(h.ctx, projectID, run, "running", nil, nil, keyList, lastStep, 0, &started)
	if err != nil {
		h.t.Fatalf("upsert run: %v", err)
	}
	rows := make([]store.MetricPoint, 0, len(points))
	for i, p := range points {
		rows = append(rows, store.MetricPoint{
			Step: p.step,
			// Distinct timestamps, so the time axis can tell the points apart
			// even when two of them share a step.
			TS:      started.Add(time.Duration(i) * time.Second),
			Metrics: p.values,
		})
	}
	if err := h.st.InsertPoints(h.ctx, runID, rows); err != nil {
		h.t.Fatalf("insert points: %v", err)
	}
	return projectID
}

// seriesFor returns one run's trace for one metric as a step -> value map plus
// the raw point count, which is what "drawn twice" is measured against.
func (h *expHarness) seriesFor(project, run, key string) (map[float64]float64, int) {
	h.t.Helper()
	for _, s := range h.series(project) {
		if s.Run == run && s.Key == key {
			byStep := make(map[float64]float64, len(s.Points))
			for _, p := range s.Points {
				byStep[p[0]] = p[1]
			}
			return byStep, len(s.Points)
		}
	}
	h.t.Fatalf("no series for run %q key %q", run, key)
	return nil, 0
}

// TestSeries_ResumedRunIsOneContinuousLine is the resume completion condition
// (todo/exp-run-resume.md): a run that is interrupted after its points were
// flushed to parquet, and then re-logs the step it died on, must chart as a
// single line -- no duplicated step, and the re-logged value winning over the
// one the dead attempt left behind.
func TestSeries_ResumedRunIsOneContinuousLine(t *testing.T) {
	h := newExpHarness(t)

	// First attempt: steps 1..3, then the machine is preempted.
	projectID := h.ingestPoints("demo", "r1", []metricPoint{
		{step: 1, values: map[string]float64{"loss": 1.0}},
		{step: 2, values: map[string]float64{"loss": 0.9}},
		{step: 3, values: map[string]float64{"loss": 0.8}},
	})
	first := h.flush(projectID, "demo")
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, first.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	// Resumed: the checkpoint was taken before step 3 committed, so step 3 is
	// recomputed and logged again with a different value.
	h.ingestPoints("demo", "r1", []metricPoint{
		{step: 3, values: map[string]float64{"loss": 0.75}},
		{step: 4, values: map[string]float64{"loss": 0.7}},
		{step: 5, values: map[string]float64{"loss": 0.6}},
	})

	byStep, n := h.seriesFor("demo", "r1", "loss")
	if n != 5 {
		t.Errorf("point count = %d, want 5 (steps 1..5, step 3 not drawn twice)", n)
	}
	if byStep[3] != 0.75 {
		t.Errorf("value at step 3 = %v, want the re-logged 0.75 (last write wins)", byStep[3])
	}

	// The same must hold once the resumed points have also been flushed: the
	// rule cannot depend on which side of the flush a point happens to be on.
	second := h.flush(projectID, "demo")
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, second.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}
	byStep, n = h.seriesFor("demo", "r1", "loss")
	if n != 5 {
		t.Errorf("point count after flush = %d, want 5", n)
	}
	if byStep[3] != 0.75 {
		t.Errorf("value at step 3 after flush = %v, want 0.75 (parquet keeps both rows; the chart shows the later one)", byStep[3])
	}
}

// TestSeries_DuplicateStepInOneBufferTakesTheLastValue covers the plain case:
// a caller logging the same step twice without any resume involved.
func TestSeries_DuplicateStepInOneBufferTakesTheLastValue(t *testing.T) {
	h := newExpHarness(t)
	h.ingestPoints("demo", "r1", []metricPoint{
		{step: 1, values: map[string]float64{"loss": 1.0}},
		{step: 1, values: map[string]float64{"loss": 2.0}},
		{step: 2, values: map[string]float64{"loss": 0.5}},
	})

	byStep, n := h.seriesFor("demo", "r1", "loss")
	if n != 2 {
		t.Errorf("point count = %d, want 2 (the duplicate step collapses)", n)
	}
	if byStep[1] != 2.0 {
		t.Errorf("value at step 1 = %v, want 2 (the later of the two)", byStep[1])
	}
}

// TestSeries_TimeAxisKeepsEverySample is the deliberate exception. On the step
// axis two values at one step are a contradiction to resolve; on the time axis
// they are simply two samples, and a loop fast enough to log twice inside one
// millisecond must not have half its points silently discarded.
func TestSeries_TimeAxisKeepsEverySample(t *testing.T) {
	h := newExpHarness(t)
	h.ingestPoints("demo", "r1", []metricPoint{
		{step: 1, values: map[string]float64{"loss": 1.0}},
		{step: 1, values: map[string]float64{"loss": 2.0}},
	})

	got, err := h.indexer.Series(h.ctx, h.repo, SeriesRequest{Project: "demo", XAxis: "time"})
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(got) != 1 || len(got[0].Points) != 2 {
		t.Errorf("time-axis series = %#v, want both samples kept", got)
	}
}

func TestDedupeLastWins(t *testing.T) {
	tests := []struct {
		name string
		in   [][2]float64
		want [][2]float64
	}{
		{"empty", nil, [][2]float64{}},
		{"no duplicates", [][2]float64{{1, 1}, {2, 2}}, [][2]float64{{1, 1}, {2, 2}}},
		{"pair collapses to the last", [][2]float64{{1, 1}, {1, 9}}, [][2]float64{{1, 9}}},
		{
			"a run of three collapses to the last",
			[][2]float64{{1, 1}, {1, 2}, {1, 3}, {2, 4}},
			[][2]float64{{1, 3}, {2, 4}},
		},
		{
			"duplicates at the end",
			[][2]float64{{1, 1}, {2, 2}, {2, 7}},
			[][2]float64{{1, 1}, {2, 7}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupeLastWins(append([][2]float64(nil), tt.in...))
			if len(got) != len(tt.want) {
				t.Fatalf("dedupeLastWins(%v) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("dedupeLastWins(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}
