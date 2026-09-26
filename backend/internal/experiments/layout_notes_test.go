package experiments

import (
	"reflect"
	"testing"
)

// The project notes endpoint (PUT /api/v1/experiments/{ns}/{repo}/{project}/notes)
// commits {project}/NOTES.md next to the metrics parquet. Layout detection
// must treat it as the plain file it is: no extra project, no change to the
// project it sits in, in every layout the flusher and trackio produce.
func TestDetectLayouts_IgnoresProjectNotes(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  []Layout
	}{
		{
			"project subdirectory",
			[]string{"p1/metrics.parquet", "p1/NOTES.md", "p1/aux/configs.parquet"},
			[]Layout{{Project: "p1", MetricsPath: "p1/metrics.parquet", ConfigsPath: "p1/aux/configs.parquet"}},
		},
		{
			"continuation shards",
			[]string{"p1/metrics.parquet", "p1/metrics.part0001.parquet", "p1/NOTES.md"},
			DetectLayouts([]string{"p1/metrics.parquet", "p1/metrics.part0001.parquet"}, "repo"),
		},
		{
			"local export shape at the root",
			[]string{"p1.parquet", "p1_configs.parquet", "p1/NOTES.md"},
			[]Layout{{Project: "p1", MetricsPath: "p1.parquet", ConfigsPath: "p1_configs.parquet"}},
		},
		{
			// Notes written before the project's first flush.
			"notes only",
			[]string{"p1/NOTES.md", "README.md"},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectLayouts(tc.paths, "repo")
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(sortedLayouts(got), sortedLayouts(tc.want)) {
				t.Errorf("DetectLayouts(%v) = %+v, want %+v", tc.paths, got, tc.want)
			}
		})
	}
}
