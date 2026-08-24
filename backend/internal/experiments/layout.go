// Package experiments turns trackio's parquet exports into the run and metric
// index the dashboard reads, and serves metric series merged with live points
// from the native ingest API.
package experiments

import (
	"path"
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
	ConfigsPath string
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
		// its own tells us nothing to chart.
		if l.MetricsPath != "" {
			out = append(out, *l)
		}
	}
	return out
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
