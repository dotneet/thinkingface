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
