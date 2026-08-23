package experiments

import (
	"strings"
	"testing"
)

// Route A has no grouping of its own, so the indexer only mirrors a "group" /
// "job_type" the training script happened to log. Anything it cannot vouch for
// must come back nil, because nil is what keeps a grouping the ingest API
// declared from being cleared by the next re-index.
func TestGroupingFromConfig(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		key    string
		want   string // "" means nil
	}{
		{"plain string", map[string]any{"group": "sweep-1"}, "group", "sweep-1"},
		{"job_type", map[string]any{"job_type": "train"}, "job_type", "train"},
		{"absent key", map[string]any{"lr": 0.1}, "group", ""},
		{"nil config", nil, "group", ""},
		{"empty string", map[string]any{"group": ""}, "group", ""},
		{"not a string", map[string]any{"group": 3}, "group", ""},
		{"control character", map[string]any{"group": "a\x00b"}, "group", ""},
		{"newline", map[string]any{"group": "a\nb"}, "group", ""},
		{"too long", map[string]any{"group": strings.Repeat("a", maxGroupingBytes+1)}, "group", ""},
		{"at the limit", map[string]any{"group": strings.Repeat("a", maxGroupingBytes)}, "group", strings.Repeat("a", maxGroupingBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := groupingFromConfig(tt.config, tt.key)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("got %q, want nil (nil is what preserves the stored grouping)", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %q", tt.want)
			}
			if *got != tt.want {
				t.Fatalf("got %q, want %q", *got, tt.want)
			}
		})
	}
}
