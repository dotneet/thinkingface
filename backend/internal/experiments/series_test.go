package experiments

import "testing"

func genPoints(n int) [][2]float64 {
	pts := make([][2]float64, n)
	for i := range pts {
		pts[i] = [2]float64{float64(i), float64(i) * 10}
	}
	return pts
}

func TestDownsample_UnderOrAtMaxReturnsUnchanged(t *testing.T) {
	pts := genPoints(5)
	got := downsample(pts, 10)
	if len(got) != len(pts) {
		t.Fatalf("downsample under max: len = %d, want %d (unchanged)", len(got), len(pts))
	}
	for i := range pts {
		if got[i] != pts[i] {
			t.Errorf("point %d = %v, want unchanged %v", i, got[i], pts[i])
		}
	}
}

func TestDownsample_AtExactlyMax(t *testing.T) {
	pts := genPoints(10)
	got := downsample(pts, 10)
	if len(got) != 10 {
		t.Fatalf("downsample at exactly max: len = %d, want 10 (unchanged, <=max short-circuits)", len(got))
	}
}

func TestDownsample_OverMaxReturnsExactlyMax(t *testing.T) {
	pts := genPoints(1000)
	max := 37
	got := downsample(pts, max)
	if len(got) != max {
		t.Fatalf("downsample over max: len = %d, want exactly %d", len(got), max)
	}
}

func TestDownsample_PreservesFirstAndLastPoint(t *testing.T) {
	pts := genPoints(997) // an awkward, non-round count
	max := 50
	got := downsample(pts, max)
	if len(got) != max {
		t.Fatalf("len = %d, want %d", len(got), max)
	}
	if got[0] != pts[0] {
		t.Errorf("first point = %v, want original first point %v", got[0], pts[0])
	}
	if got[len(got)-1] != pts[len(pts)-1] {
		t.Errorf("last point = %v, want original last point %v", got[len(got)-1], pts[len(pts)-1])
	}
}

func TestDownsample_MonotonicXAfterThinning(t *testing.T) {
	pts := genPoints(500)
	got := downsample(pts, 33)
	for i := 1; i < len(got); i++ {
		if got[i][0] < got[i-1][0] {
			t.Fatalf("downsampled series is not monotonic at index %d: %v then %v", i, got[i-1], got[i])
		}
	}
}

func TestDownsample_MaxBelowTwoReturnsInputUnchanged(t *testing.T) {
	pts := genPoints(100)
	got := downsample(pts, 1)
	if len(got) != len(pts) {
		t.Fatalf("downsample with max<2: len = %d, want %d (function bails out rather than producing a single point)", len(got), len(pts))
	}
}

func TestDownsample_EmptyInput(t *testing.T) {
	got := downsample(nil, 10)
	if len(got) != 0 {
		t.Errorf("downsample(nil) = %v, want empty", got)
	}
}

// ------------------------------------------------- the read path's memory bound

// TestSeriesCollector_ThinsRatherThanGrowingWithoutBound is the read half of
// the bound the write path has always had.
//
// Series() used to accumulate every numeric cell of the parquet into
// collected[run][key] and only then apply max_points, so `GET
// .../{project}/metrics` -- which needs no authentication -- allocated one
// [2]float64 per cell of the whole file before it looked at the caller's
// limit. The fix is not to stop scanning (a chart that stops halfway shows a
// run that stopped halfway, which is wrong rather than coarse) but to give up
// resolution as the budget fills.
func TestSeriesCollector_ThinsRatherThanGrowingWithoutBound(t *testing.T) {
	restore := maxSeriesScanPoints
	maxSeriesScanPoints = 64
	t.Cleanup(func() { maxSeriesScanPoints = restore })

	c := newSeriesCollector(nil, nil)
	const logged = 5000
	for i := range logged {
		c.add("run-1", "loss", float64(i), float64(i)*2)
	}
	if c.held > maxSeriesScanPoints {
		t.Fatalf("held %d pairs, want at most the budget %d", c.held, maxSeriesScanPoints)
	}

	points := c.series["run-1"]["loss"].finish()
	// finish() may restore one point past the budget: the true final one.
	if len(points) > maxSeriesScanPoints+1 {
		t.Fatalf("kept %d points, want at most %d", len(points), maxSeriesScanPoints+1)
	}
	if len(points) < 2 {
		t.Fatalf("kept %d points, want a usable trace", len(points))
	}
	// The endpoints are what downsample() promises the chart, so the thinning
	// underneath it must not move them.
	if points[0] != [2]float64{0, 0} {
		t.Errorf("first point = %v, want the first point logged", points[0])
	}
	if want := ([2]float64{logged - 1, (logged - 1) * 2}); points[len(points)-1] != want {
		t.Errorf("last point = %v, want the last point logged %v", points[len(points)-1], want)
	}
	// Order is preserved, because it is what dedupeLastWins resolves ties with.
	for i := 1; i < len(points); i++ {
		if points[i][0] <= points[i-1][0] {
			t.Fatalf("thinning disturbed the collection order at %d: %v then %v", i, points[i-1], points[i])
		}
	}
}

// TestSeriesCollector_ThinsEveryTraceTogether checks that the budget is global
// rather than per trace: a project with many traces must not multiply the
// bound by the number of them, and no trace may be starved by the others.
func TestSeriesCollector_ThinsEveryTraceTogether(t *testing.T) {
	restore := maxSeriesScanPoints
	maxSeriesScanPoints = 100
	t.Cleanup(func() { maxSeriesScanPoints = restore })

	c := newSeriesCollector(nil, nil)
	for i := range 2000 {
		c.add("run-1", "loss", float64(i), float64(i))
		c.add("run-2", "loss", float64(i), float64(i))
	}
	if c.held > maxSeriesScanPoints {
		t.Fatalf("held %d pairs across both traces, want at most %d", c.held, maxSeriesScanPoints)
	}
	for _, run := range []string{"run-1", "run-2"} {
		points := c.series[run]["loss"].finish()
		if len(points) < 2 {
			t.Errorf("%s kept %d points; the budget must be shared, not handed to whichever trace got there first", run, len(points))
		}
		if points[len(points)-1][0] != 1999 {
			t.Errorf("%s last point = %v, want the last one logged", run, points[len(points)-1])
		}
	}
}

// TestSeriesCollector_BoundsTheNumberOfTraces covers the other dimension:
// thinning the points of a million one-point traces would still leave a
// million map entries.
func TestSeriesCollector_BoundsTheNumberOfTraces(t *testing.T) {
	restore := maxSeriesCount
	maxSeriesCount = 3
	t.Cleanup(func() { maxSeriesCount = restore })

	c := newSeriesCollector(nil, nil)
	for i := range 10 {
		c.add("run-1", string(rune('a'+i)), float64(i), 1)
	}
	if c.numSeries != 3 {
		t.Errorf("traces = %d, want the ceiling %d", c.numSeries, maxSeriesCount)
	}
	if c.droppedSeries != 7 {
		t.Errorf("droppedSeries = %d, want 7 -- a truncated answer has to be visible somewhere", c.droppedSeries)
	}
}

// TestSeriesCollector_FiltersBeforeAllocating pins that the run and key
// filters are applied on the way in. Charting one key of one run must not pay
// for -- or be thinned by -- every other trace in the file.
func TestSeriesCollector_FiltersBeforeAllocating(t *testing.T) {
	c := newSeriesCollector(map[string]bool{"run-1": true}, map[string]bool{"loss": true})
	c.add("run-1", "loss", 1, 1)
	c.add("run-1", "accuracy", 1, 1)
	c.add("run-2", "loss", 1, 1)
	if c.numSeries != 1 || c.held != 1 {
		t.Fatalf("traces = %d, held = %d, want 1 and 1", c.numSeries, c.held)
	}
	if c.droppedSeries != 0 {
		t.Errorf("droppedSeries = %d, want 0: a filtered trace was not asked for, so it is not truncation", c.droppedSeries)
	}
}
