package experiments

import (
	"math"
	"testing"
	"time"
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
