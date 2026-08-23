package experiments

import (
	"reflect"
	"sort"
	"testing"
)

func sortedLayouts(l []Layout) []Layout {
	out := append([]Layout(nil), l...)
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

func TestDetectLayouts_RootMetricsUsesRepoName(t *testing.T) {
	paths := []string{"metrics.parquet", "aux/configs.parquet"}
	got := DetectLayouts(paths, "my-repo")
	want := []Layout{{Project: "my-repo", MetricsPath: "metrics.parquet", ConfigsPath: "aux/configs.parquet"}}
	if !reflect.DeepEqual(sortedLayouts(got), want) {
		t.Errorf("DetectLayouts(root metrics) = %+v, want %+v", got, want)
	}
}

func TestDetectLayouts_ProjectSubdirectory(t *testing.T) {
	paths := []string{"myproj/metrics.parquet", "myproj/aux/configs.parquet"}
	got := DetectLayouts(paths, "repo-name-unused")
	want := []Layout{{Project: "myproj", MetricsPath: "myproj/metrics.parquet", ConfigsPath: "myproj/aux/configs.parquet"}}
	if !reflect.DeepEqual(sortedLayouts(got), want) {
		t.Errorf("DetectLayouts(project subdir) = %+v, want %+v", got, want)
	}
}

func TestDetectLayouts_LocalExportShape(t *testing.T) {
	paths := []string{"myproj.parquet", "myproj_configs.parquet"}
	got := DetectLayouts(paths, "repo-name-unused")
	want := []Layout{{Project: "myproj", MetricsPath: "myproj.parquet", ConfigsPath: "myproj_configs.parquet"}}
	if !reflect.DeepEqual(sortedLayouts(got), want) {
		t.Errorf("DetectLayouts(local export) = %+v, want %+v", got, want)
	}
}

func TestDetectLayouts_AuxSuffixesNotMistakenForProjects(t *testing.T) {
	paths := []string{
		"myproj.parquet",
		"myproj_system.parquet",
		"myproj_artifacts.parquet",
		"myproj_artifact_versions.parquet",
		"myproj_artifact_aliases.parquet",
		"myproj_run_artifact_links.parquet",
		"myproj_traces.parquet",
	}
	got := DetectLayouts(paths, "repo-name-unused")
	if len(got) != 1 {
		t.Fatalf("DetectLayouts returned %d layouts, want 1 (aux suffix files should not become their own projects): %+v", len(got), got)
	}
	if got[0].Project != "myproj" {
		t.Errorf("project = %q, want %q", got[0].Project, "myproj")
	}
	if got[0].MetricsPath != "myproj.parquet" {
		t.Errorf("MetricsPath = %q, want %q", got[0].MetricsPath, "myproj.parquet")
	}
	// _system is the one aux table that is read rather than merely skipped:
	// it is attached to its project, never turned into one of its own.
	if got[0].SystemMetricsPath != "myproj_system.parquet" {
		t.Errorf("SystemMetricsPath = %q, want %q", got[0].SystemMetricsPath, "myproj_system.parquet")
	}
}

func TestDetectLayouts_SystemParquetAloneIsNotAProject(t *testing.T) {
	got := DetectLayouts([]string{"myproj_system.parquet"}, "repo-name")
	if len(got) != 0 {
		t.Errorf("DetectLayouts with only a _system file = %+v, want empty (a project needs metrics)", got)
	}
}

func TestDetectLayouts_SystemParquetAttachesToItsProject(t *testing.T) {
	paths := []string{"myproj.parquet", "myproj_configs.parquet", "myproj_system.parquet"}
	got := DetectLayouts(paths, "repo-name-unused")
	want := []Layout{{
		Project:           "myproj",
		MetricsPath:       "myproj.parquet",
		ConfigsPath:       "myproj_configs.parquet",
		SystemMetricsPath: "myproj_system.parquet",
	}}
	if !reflect.DeepEqual(sortedLayouts(got), want) {
		t.Errorf("DetectLayouts(with system) = %+v, want %+v", got, want)
	}
}

func TestDetectLayouts_ConfigsOnlyIsNotReturned(t *testing.T) {
	paths := []string{"aux/configs.parquet", "onlyconfigs_configs.parquet"}
	got := DetectLayouts(paths, "repo-name")
	if len(got) != 0 {
		t.Errorf("DetectLayouts with only configs files = %+v, want empty (a project needs metrics)", got)
	}
}

func TestDetectLayouts_MultipleProjectsMixed(t *testing.T) {
	paths := []string{
		"metrics.parquet", "aux/configs.parquet", // repo-level project
		"projA/metrics.parquet", "projA/aux/configs.parquet",
		"projB.parquet", "projB_configs.parquet",
		"projB_system.parquet", // attaches to projB, never a project of its own
	}
	got := DetectLayouts(paths, "reponame")
	sortedGot := sortedLayouts(got)

	want := []Layout{
		{Project: "projA", MetricsPath: "projA/metrics.parquet", ConfigsPath: "projA/aux/configs.parquet"},
		{Project: "projB", MetricsPath: "projB.parquet", ConfigsPath: "projB_configs.parquet",
			SystemMetricsPath: "projB_system.parquet"},
		{Project: "reponame", MetricsPath: "metrics.parquet", ConfigsPath: "aux/configs.parquet"},
	}
	if !reflect.DeepEqual(sortedGot, want) {
		t.Errorf("DetectLayouts(mixed) = %+v, want %+v", sortedGot, want)
	}
}

func TestDetectLayouts_IgnoresNonParquetFiles(t *testing.T) {
	paths := []string{"README.md", "metrics.csv", "metrics.parquet", "aux/configs.parquet"}
	got := DetectLayouts(paths, "reponame")
	if len(got) != 1 {
		t.Fatalf("DetectLayouts = %+v, want exactly 1 layout ignoring non-.parquet files", got)
	}
}

func TestDetectLayouts_CaseInsensitiveExtension(t *testing.T) {
	paths := []string{"metrics.PARQUET"}
	got := DetectLayouts(paths, "reponame")
	if len(got) != 1 || got[0].MetricsPath != "metrics.PARQUET" {
		t.Errorf("DetectLayouts with uppercase extension = %+v, want a single reponame layout", got)
	}
}
