// Package experiments turns trackio's parquet exports into the run and metric
// index the dashboard reads, and serves metric series merged with live points
// from the native ingest API.
package experiments

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SystemMetricPrefix namespaces machine telemetry (GPU / CPU / memory) so it
// never collides with -- or crowds out -- the metrics the training script
// itself logs. Both ingest routes converge on it: the Python shim already
// emits keys like "system/gpu.0.util" (thinkingface._system_metrics), and the
// columns of trackio's own {project}_system.parquet are given the same prefix
// as they are indexed here.
const SystemMetricPrefix = "system/"

// Layout locates the parquet files that make up one project inside a dataset
// repository.
type Layout struct {
	Project string
	// MetricsPath is the file every reader requires; a project without one is
	// not a project (see the filter at the end of DetectLayouts).
	MetricsPath string
	// MetricsShards are the continuation files a flush created when appending
	// to MetricsPath would have produced a file too large to read back
	// (flush.go's maxExistingFlushRows), in part-number order. They have
	// exactly the shape of MetricsPath -- one row per point, the same
	// structural columns -- so every reader that scans MetricsPath must scan
	// these straight after it, in this order, or a long project's chart simply
	// stops at the rotation point.
	//
	// Nil for the overwhelmingly common project that never crossed the bound.
	MetricsShards []string
	ConfigsPath   string
	// SystemMetricsPath is trackio's {project}_system.parquet, holding the
	// machine telemetry it samples in the background. It is optional, and its
	// columns are read under SystemMetricPrefix.
	SystemMetricsPath string
}

// auxSuffixes are the non-metric tables trackio's local export writes next to
// {project}.parquet. They must never be mistaken for a project of their own.
// _system and _configs are also matched by dedicated cases above this fallback
// -- they are read rather than merely skipped -- and stay listed here so the
// set of "not a project" suffixes remains readable in one place.
var auxSuffixes = []string{
	"_system", "_configs", "_traces", "_artifacts", "_artifact_versions",
	"_artifact_aliases", "_run_artifact_links",
}

// metricsShardPattern matches the stem of a continuation file this package
// writes: "metrics.part0001" -> stem "metrics", part 1. Four digits is a
// minimum, not a maximum, so the numbering never has to stop.
//
// The marker is deliberately specific. A shard has to be recognised from its
// name alone (nothing else in the repository says which file continues which),
// and the generic `{project}.parquet` case at the bottom of DetectLayouts
// would otherwise turn "myproj.part0001.parquet" into a project of its own
// called "myproj.part0001" -- a second, half-empty entry in the project list
// for every rotated project.
var metricsShardPattern = regexp.MustCompile(`^(.+)\.part(\d{4,})$`)

// MetricsShardPath is the name of the n-th continuation file of a metrics
// parquet: "demo/metrics.parquet" with n=1 becomes
// "demo/metrics.part0001.parquet". The extension is taken from base so an
// uppercase ".PARQUET" keeps its casing; only the stem is extended.
func MetricsShardPath(base string, n int) string {
	ext := path.Ext(base)
	return fmt.Sprintf("%s.part%04d%s", strings.TrimSuffix(base, ext), n, ext)
}

// parseMetricsShard splits a file stem (the name with its extension already
// removed) into the stem of the file it continues and its part number.
func parseMetricsShard(stem string) (base string, n int, ok bool) {
	m := metricsShardPattern.FindStringSubmatch(stem)
	if m == nil {
		return "", 0, false
	}
	// A number too large for an int is not a shard this package wrote; the
	// generic project case can have it.
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return m[1], n, true
}

// shardProject resolves which project a continuation file belongs to, by
// applying to its base stem exactly the rules DetectLayouts applies to the
// base file itself. Anything those rules would not have accepted as a metrics
// file (an aux table, a configs export, a nested non-"metrics" name) is not a
// shard of anything and is reported as such.
func shardProject(stem, dir, repoName string) (string, bool) {
	switch {
	case strings.EqualFold(stem, "metrics"):
		if dir != "" {
			return dir, true
		}
		return repoName, true
	case dir == "" && !hasAuxSuffix(strings.ToLower(stem)):
		return stem, true
	}
	return "", false
}

// metricsShard is one continuation file waiting to be attached to its project.
type metricsShard struct {
	path string
	n    int
}

// DetectLayouts maps repository file paths onto projects. It understands the
// two shapes trackio produces:
//
//	static/dataset export:  metrics.parquet         + aux/configs.parquet
//	                        {project}/metrics.parquet + {project}/aux/configs.parquet
//	local export:           {project}.parquet       + {project}_configs.parquet
//
// repoName is used as the project name for a root-level metrics.parquet, which
// carries no name of its own.
func DetectLayouts(paths []string, repoName string) []Layout {
	byProject := map[string]*Layout{}
	shards := map[string][]metricsShard{}
	get := func(project string) *Layout {
		if l, ok := byProject[project]; ok {
			return l
		}
		l := &Layout{Project: project}
		byProject[project] = l
		return l
	}

	for _, p := range paths {
		if !strings.HasSuffix(strings.ToLower(p), ".parquet") {
			continue
		}
		if hasArtifactSegment(p) {
			// "{project}/artifacts/{run}/..." is a run's own output
			// (docs/dev/api-contract.md §7), never project data. Without this an
			// artifact named metrics.parquet would invent a project called
			// "{project}/artifacts/{run}".
			continue
		}
		dir, file := path.Split(p)
		dir = strings.Trim(dir, "/")
		base := strings.TrimSuffix(file, path.Ext(file))
		// Filenames are matched case-insensitively so an uppercase .PARQUET does
		// not slip past the well-known names into the generic {project}.parquet
		// case, but project names keep the casing the author chose.
		lowerFile := strings.ToLower(file)
		lowerBase := strings.ToLower(base)

		// Continuation files are resolved before anything else, because every
		// case below would misread one: at the root the generic project case
		// would invent "{project}.part0001", and in a subdirectory the file
		// would match nothing at all and its rows would silently vanish from
		// the chart.
		if stem, n, ok := parseMetricsShard(base); ok {
			if project, ok := shardProject(stem, dir, repoName); ok {
				shards[project] = append(shards[project], metricsShard{path: p, n: n})
				continue
			}
		}

		switch {
		case lowerFile == "metrics.parquet":
			project := repoName
			if dir != "" {
				project = dir
			}
			get(project).MetricsPath = p

		case lowerFile == "configs.parquet" && (dir == "aux" || strings.HasSuffix(dir, "/aux")):
			project := repoName
			if trimmed := strings.TrimSuffix(strings.TrimSuffix(dir, "aux"), "/"); trimmed != "" {
				project = trimmed
			}
			get(project).ConfigsPath = p

		case dir == "" && strings.HasSuffix(lowerBase, "_configs"):
			get(base[:len(base)-len("_configs")]).ConfigsPath = p

		// Telemetry is attached to the project it belongs to rather than
		// becoming one. get() may create the project entry here, but a
		// _system file on its own still yields nothing: the filter below
		// drops any project that never got a metrics file.
		case dir == "" && strings.HasSuffix(lowerBase, "_system"):
			get(base[:len(base)-len("_system")]).SystemMetricsPath = p

		case dir == "" && !hasAuxSuffix(lowerBase):
			get(base).MetricsPath = p
		}
	}

	out := make([]Layout, 0, len(byProject))
	for _, l := range byProject {
		// A project without metrics is not a project; a stray configs file on
		// its own tells us nothing to chart. A shard without its base file is
		// the same thing one step further along, and is dropped with it:
		// readers walk the shards *after* the base, so a shard on its own has
		// no anchor to be read relative to.
		if l.MetricsPath == "" {
			continue
		}
		if found := shards[l.Project]; len(found) > 0 {
			// Part-number order is the reading order, and the reading order is
			// chronological: a shard only ever exists because the file before
			// it was full. Series() resolves two values at one step by taking
			// the later one in scan order, so getting this backwards would
			// chart a resumed run's *older* value.
			sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
			l.MetricsShards = make([]string, 0, len(found))
			for _, s := range found {
				l.MetricsShards = append(l.MetricsShards, s.path)
			}
		}
		out = append(out, *l)
	}
	return out
}

// MetricsFiles is every file holding this project's metric rows, in reading
// order: the base parquet followed by its continuation files. Readers that
// scan the whole project use this rather than MetricsPath, so adding a shard
// never means remembering to update a second loop.
func (l Layout) MetricsFiles() []string {
	if l.MetricsPath == "" {
		return nil
	}
	out := make([]string, 0, 1+len(l.MetricsShards))
	out = append(out, l.MetricsPath)
	return append(out, l.MetricsShards...)
}

func hasAuxSuffix(base string) bool {
	for _, suffix := range auxSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// Column names trackio writes that describe the row rather than a metric.
var structuralColumns = map[string]bool{
	"id": true, "log_id": true, "space_id": true, "run_id": true,
	"run_name": true, "run": true, "step": true, "_step": true,
	"timestamp": true, "_timestamp": true, "created_at": true,
	"project": true, "global_step": true,
	// Written by the ingest flush to make it idempotent (flush.go); it is
	// bookkeeping, never something to chart.
	IngestIDColumn: true,
}

// IsStructuralColumn reports whether name describes the row rather than a
// measurement. The ingest API refuses it as a metric name (api/experiments.go)
// and mergePoints refuses to write one, because a point's metrics and its
// structural fields end up in the same parquet row: a metric named "run_name"
// would otherwise overwrite the run's own name with a number.
func IsStructuralColumn(name string) bool { return structuralColumns[name] }

func runColumn(columns map[string]bool) string {
	for _, c := range []string{"run_name", "run", "run_id"} {
		if columns[c] {
			return c
		}
	}
	return ""
}

func stepColumn(columns map[string]bool) string {
	for _, c := range []string{"step", "_step", "global_step"} {
		if columns[c] {
			return c
		}
	}
	return ""
}

func timeColumn(columns map[string]bool) string {
	for _, c := range []string{"timestamp", "_timestamp", "created_at"} {
		if columns[c] {
			return c
		}
	}
	return ""
}

// hasArtifactSegment reports whether p has an "artifacts" path segment, which
// marks everything below it as a run's logged output rather than project data.
// Matched case-insensitively for the same reason filenames are.
func hasArtifactSegment(p string) bool {
	for seg := range strings.SplitSeq(strings.Trim(p, "/"), "/") {
		if strings.EqualFold(seg, "artifacts") {
			return true
		}
	}
	return false
}
