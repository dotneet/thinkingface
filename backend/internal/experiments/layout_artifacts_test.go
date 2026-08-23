package experiments

import "testing"

// A run's artifacts live at "{project}/artifacts/{run}/..." (api-contract §7).
// Nothing under that prefix is project data, so DetectLayouts must not read it
// -- an artifact that happens to be called metrics.parquet would otherwise
// invent a project named after the artifact directory.
func TestDetectLayouts_IgnoresRunArtifacts(t *testing.T) {
	layouts := DetectLayouts([]string{
		"mnist/metrics.parquet",
		"mnist/artifacts/run-1/metrics.parquet",
		"mnist/artifacts/run-1/confusion.parquet",
		"mnist/Artifacts/run-2/metrics.parquet",
	}, "repo")

	if len(layouts) != 1 {
		names := make([]string, 0, len(layouts))
		for _, l := range layouts {
			names = append(names, l.Project)
		}
		t.Fatalf("got projects %v, want only [mnist]", names)
	}
	if layouts[0].Project != "mnist" {
		t.Fatalf("got project %q, want %q", layouts[0].Project, "mnist")
	}
	if layouts[0].MetricsPath != "mnist/metrics.parquet" {
		t.Fatalf("got metrics path %q, want the project's own file", layouts[0].MetricsPath)
	}
}

// The guard keys on a path segment, not a substring: a project legitimately
// named "artifacts-v2" is not an artifact directory.
func TestDetectLayouts_ArtifactGuardMatchesWholeSegments(t *testing.T) {
	layouts := DetectLayouts([]string{"artifacts-v2/metrics.parquet"}, "repo")
	if len(layouts) != 1 || layouts[0].Project != "artifacts-v2" {
		t.Fatalf("got %+v, want the artifacts-v2 project", layouts)
	}
}
