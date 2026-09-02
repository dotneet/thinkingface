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

// ------------------------------------------------------ continuation files

// TestDetectLayouts_ContinuationFilesAttachToTheirProject covers the files a
// flush rotates into once appending to the base parquet would produce one it
// could not read back (flush.go's maxExistingFlushRows).
//
// Both halves of this matter. A shard that is not attached is a shard nobody
// reads, so the chart and the run index simply stop at the rotation point; and
// a shard mistaken for a project of its own puts a second, half-empty
// "demo.part0001" in the project list.
func TestDetectLayouts_ContinuationFilesAttachToTheirProject(t *testing.T) {
	// Deliberately out of order: reading order is the part number, not the
	// order the repository listed the files in.
	paths := []string{
		"demo/metrics.part0002.parquet",
		"demo/metrics.parquet",
		"demo/metrics.part0001.parquet",
	}
	got := DetectLayouts(paths, "repo-name-unused")
	want := []Layout{{
		Project:     "demo",
		MetricsPath: "demo/metrics.parquet",
		MetricsShards: []string{
			"demo/metrics.part0001.parquet",
			"demo/metrics.part0002.parquet",
		},
	}}
	if !reflect.DeepEqual(sortedLayouts(got), want) {
		t.Errorf("DetectLayouts(sharded project) = %+v, want %+v", got, want)
	}
}

func TestDetectLayouts_ContinuationFilesOfALocalExport(t *testing.T) {
	paths := []string{"myproj.parquet", "myproj.part0001.parquet", "myproj_configs.parquet"}
	got := DetectLayouts(paths, "repo-name-unused")
	want := []Layout{{
		Project:       "myproj",
		MetricsPath:   "myproj.parquet",
		MetricsShards: []string{"myproj.part0001.parquet"},
		ConfigsPath:   "myproj_configs.parquet",
	}}
	if !reflect.DeepEqual(sortedLayouts(got), want) {
		t.Errorf("DetectLayouts(sharded local export) = %+v, want %+v", got, want)
	}
}

// TestDetectLayouts_RootMetricsShardsUseTheRepoName mirrors the base file's
// rule: a root-level metrics.parquet is named after the repository, so its
// continuation files must be too rather than inventing a project called
// "metrics.part0001".
func TestDetectLayouts_RootMetricsShardsUseTheRepoName(t *testing.T) {
	got := DetectLayouts([]string{"metrics.parquet", "metrics.part0001.parquet"}, "my-repo")
	want := []Layout{{
		Project:       "my-repo",
		MetricsPath:   "metrics.parquet",
		MetricsShards: []string{"metrics.part0001.parquet"},
	}}
	if !reflect.DeepEqual(sortedLayouts(got), want) {
		t.Errorf("DetectLayouts(root shard) = %+v, want %+v", got, want)
	}
}

// TestDetectLayouts_ShardWithoutItsBaseIsNotAProject: readers walk the shards
// after the base file, so a shard with nothing to be read relative to is
// dropped the same way a stray configs file is.
func TestDetectLayouts_ShardWithoutItsBaseIsNotAProject(t *testing.T) {
	if got := DetectLayouts([]string{"demo/metrics.part0001.parquet"}, "repo"); len(got) != 0 {
		t.Errorf("DetectLayouts(orphan shard) = %+v, want none", got)
	}
	if got := DetectLayouts([]string{"myproj.part0001.parquet"}, "repo"); len(got) != 0 {
		t.Errorf("DetectLayouts(orphan root shard) = %+v, want none", got)
	}
}

// TestDetectLayouts_NearMissesAreOrdinaryFiles pins how narrow the marker is.
// "part" followed by fewer than four digits, or by none, is somebody's own
// filename and keeps behaving exactly as it did before shards existed.
func TestDetectLayouts_NearMissesAreOrdinaryFiles(t *testing.T) {
	got := DetectLayouts([]string{"demo.parquet", "demo.part1.parquet", "demo.partial.parquet"}, "repo")
	byProject := map[string]Layout{}
	for _, l := range got {
		byProject[l.Project] = l
	}
	for _, name := range []string{"demo", "demo.part1", "demo.partial"} {
		if _, ok := byProject[name]; !ok {
			t.Errorf("project %q missing from %+v; a near miss must stay an ordinary project file", name, got)
		}
	}
	if shards := byProject["demo"].MetricsShards; len(shards) != 0 {
		t.Errorf("demo picked up %v as continuation files", shards)
	}
}

func TestMetricsShardPath(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"demo/metrics.parquet", "demo/metrics.part0001.parquet"},
		{"myproj.parquet", "myproj.part0001.parquet"},
		{"demo/metrics.part0001.parquet", "demo/metrics.part0001.part0001.parquet"},
	} {
		if got := MetricsShardPath(tc.base, 1); got != tc.want {
			t.Errorf("MetricsShardPath(%q, 1) = %q, want %q", tc.base, got, tc.want)
		}
	}
	// Five digits once four are not enough: the numbering never has to stop,
	// and parseMetricsShard accepts four *or more*.
	if got := MetricsShardPath("m.parquet", 12345); got != "m.part12345.parquet" {
		t.Errorf("MetricsShardPath(_, 12345) = %q", got)
	}
	if _, n, ok := parseMetricsShard("m.part12345"); !ok || n != 12345 {
		t.Errorf("parseMetricsShard(five digits) = %d, %v", n, ok)
	}
}

func TestLayout_MetricsFilesIsBaseThenShards(t *testing.T) {
	l := Layout{MetricsPath: "a.parquet", MetricsShards: []string{"a.part0001.parquet"}}
	want := []string{"a.parquet", "a.part0001.parquet"}
	if got := l.MetricsFiles(); !reflect.DeepEqual(got, want) {
		t.Errorf("MetricsFiles() = %v, want %v", got, want)
	}
	if got := (Layout{}).MetricsFiles(); got != nil {
		t.Errorf("MetricsFiles() of a project with no metrics = %v, want nil", got)
	}
}
