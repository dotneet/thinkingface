package experiments

import (
	"sort"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// commitParquet puts a parquet file on the default branch the way a trackio
// batch sync (route A) would, and refreshes the indexes that depend on it.
func (h *expHarness) commitParquet(path string, columns []flushColumn, rows []map[string]any) {
	h.t.Helper()
	data, err := writeMetricsParquet(columns, rows)
	if err != nil {
		h.t.Fatalf("build %s: %v", path, err)
	}
	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		h.t.Fatalf("open git repo: %v", err)
	}
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch: h.repo.DefaultBranch, Message: "trackio batch sync " + path,
		Author: gitrepo.Signature{Name: "trackio", Email: "trackio@example.com"},
		Ops:    []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: data}},
	}); err != nil {
		h.t.Fatalf("commit %s: %v", path, err)
	}
	h.reindex()
}

func (h *expHarness) run(project, name string) store.ExpRun {
	h.t.Helper()
	p, err := h.st.GetExpProject(h.ctx, h.repo.ID, project)
	if err != nil {
		h.t.Fatalf("get project %q: %v", project, err)
	}
	r, err := h.st.GetExpRun(h.ctx, p.ID, name)
	if err != nil {
		h.t.Fatalf("get run %q: %v", name, err)
	}
	return *r
}

// seedRouteAWithSystem writes the pair trackio's local export produces: the
// project's metrics next to the telemetry it sampled for the same run.
func seedRouteAWithSystem(t *testing.T) *expHarness {
	t.Helper()
	h := newExpHarness(t)
	h.commitParquet("demo.parquet",
		[]flushColumn{
			stringColumn("run_name", false),
			int64Column("step"),
			stringColumn("timestamp", true),
			doubleColumn("loss"),
		},
		[]map[string]any{
			{"run_name": "r1", "step": int64(1), "timestamp": "2026-08-21T00:00:00Z", "loss": 1.0},
			{"run_name": "r1", "step": int64(2), "timestamp": "2026-08-21T00:00:01Z", "loss": 0.5},
		})
	h.commitParquet("demo_system.parquet",
		[]flushColumn{
			stringColumn("run_name", false),
			int64Column("step"),
			stringColumn("timestamp", true),
			doubleColumn("cpu_percent"),
			doubleColumn("gpu_0_memory_allocated"),
		},
		[]map[string]any{
			{"run_name": "r1", "step": int64(1), "timestamp": "2026-08-21T00:00:00Z",
				"cpu_percent": 12.0, "gpu_0_memory_allocated": 1000.0},
			{"run_name": "r1", "step": int64(2), "timestamp": "2026-08-21T00:00:01Z",
				"cpu_percent": 34.0, "gpu_0_memory_allocated": 2000.0},
		})
	return h
}

func TestSystemMetrics_RouteAColumnsAreNamespaced(t *testing.T) {
	h := seedRouteAWithSystem(t)

	got := map[string]bool{}
	for _, s := range h.series("demo") {
		got[s.Key] = true
	}
	for _, want := range []string{"loss", "system/cpu_percent", "system/gpu_0_memory_allocated"} {
		if !got[want] {
			t.Errorf("series keys = %v, want one named %q", keysOf(got), want)
		}
	}
	// The bare column names must not leak alongside the namespaced ones, or
	// the default (non-system) chart would fill up with telemetry.
	for _, unwanted := range []string{"cpu_percent", "gpu_0_memory_allocated"} {
		if got[unwanted] {
			t.Errorf("series contains un-prefixed telemetry key %q; every _system column must be namespaced under %q",
				unwanted, SystemMetricPrefix)
		}
	}
}

func TestSystemMetrics_RouteAIsIndexedIntoTheRunSummary(t *testing.T) {
	h := seedRouteAWithSystem(t)
	run := h.run("demo", "r1")

	keys := map[string]bool{}
	for _, k := range run.MetricKeys {
		keys[k] = true
	}
	if !keys["system/cpu_percent"] {
		t.Errorf("run metric keys = %v, want system/cpu_percent among them", run.MetricKeys)
	}
	if v, ok := run.Summary["system/cpu_percent"]; !ok || v.(float64) != 34.0 {
		t.Errorf("summary[system/cpu_percent] = %v (present=%v), want the last sampled value 34", v, ok)
	}
}

// TestSystemMetrics_DoNotDistortRunCounters is why the telemetry pass touches
// only keys and last values: it is sampled on a wall-clock timer, so counting
// it would make "how many points did this run log" depend on how long the
// machine was up rather than on what the training script did.
func TestSystemMetrics_DoNotDistortRunCounters(t *testing.T) {
	h := seedRouteAWithSystem(t)
	run := h.run("demo", "r1")

	if run.NumPoints != 2 {
		t.Errorf("num_points = %d, want 2 (the two rows of demo.parquet only)", run.NumPoints)
	}
	if run.LastStep != 2 {
		t.Errorf("last_step = %d, want 2", run.LastStep)
	}
}

// TestSystemMetrics_BothRoutesShareOneNamespace is the convergence the design
// asks for: route A's _system columns are prefixed as they are read, and route
// B's shim already sends "system/..." keys into the ordinary metrics file. A
// chart filtered on the system namespace must see both, and an already
// prefixed key must not be prefixed twice.
func TestSystemMetrics_BothRoutesShareOneNamespace(t *testing.T) {
	h := seedRouteAWithSystem(t)

	// Route B: the shim's own telemetry arrives through the ingest buffer,
	// named exactly as thinkingface._system_metrics emits it.
	h.ingestPoints("demo", "r1", []metricPoint{
		{step: 3, values: map[string]float64{"system/cpu.percent": 55.0}},
	})

	keys := map[string]bool{}
	for _, s := range h.series("demo") {
		keys[s.Key] = true
	}
	if !keys["system/cpu_percent"] || !keys["system/cpu.percent"] {
		t.Errorf("series keys = %v, want both routes' telemetry under %q", keysOf(keys), SystemMetricPrefix)
	}
	if keys["system/system/cpu.percent"] {
		t.Error("a key the shim already namespaced was prefixed a second time")
	}
}

func TestForEachMetricValue_SkipsStructuralAndNonNumericColumns(t *testing.T) {
	row := map[string]any{
		"run_name":     "r1",
		"step":         int64(3),
		"timestamp":    "2026-08-21T00:00:00Z",
		IngestIDColumn: int64(9),
		"note":         "a string is not a metric",
		"loss":         0.25,
	}
	got := map[string]float64{}
	forEachMetricValue(row, SystemMetricPrefix, func(name string, v float64) { got[name] = v })

	want := map[string]float64{"system/loss": 0.25}
	if len(got) != len(want) || got["system/loss"] != want["system/loss"] {
		t.Errorf("forEachMetricValue = %v, want %v", got, want)
	}
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
