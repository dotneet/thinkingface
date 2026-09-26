package experiments

import (
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// backdateRun rewinds one run's updated_at through a second connection to the
// harness's SQLite file: no store method writes the column directly, and
// sleeping through a staleness window is not an option.
func (h *expHarness) backdateRun(run string, to time.Time) {
	h.t.Helper()
	db, err := sql.Open("sqlite", "file:"+h.dbPath+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		h.t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	// The format the sqlite dialect writes, so the store reads it back as UTC.
	res, err := db.Exec(`UPDATE exp_runs SET updated_at = ? WHERE name = ?`,
		to.UTC().Format("2006-01-02 15:04:05.000"), run)
	if err != nil {
		h.t.Fatalf("backdate run: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		h.t.Fatalf("backdate run %q touched %d rows, want 1", run, n)
	}
}

// flushLikeTheSyncer is the syncer's order of operations: commit the buffered
// points, re-index while they are still in exp_points (so the chart never
// loses them), then delete them.
func (h *expHarness) flushLikeTheSyncer(projectID int64, project string) {
	h.t.Helper()
	res := h.flush(projectID, project)
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, res.PointIDs); err != nil {
		h.t.Fatalf("delete points: %v", err)
	}
}

// TestIndexer_ReindexIsNotASignOfLife: every flush re-indexes every run of the
// repository, so a re-index must not move updated_at -- otherwise a crashed
// run is kept "alive" by its siblings' logging and never goes stale. A parquet
// that grew past the stored step (a batch-path run still being written) is a
// sign of life, and does move it.
func TestIndexer_ReindexIsNotASignOfLife(t *testing.T) {
	h := newExpHarness(t)
	projectID := h.ingest("demo", "dead", "running", []int64{1, 2}, "loss")
	h.ingest("demo", "alive", "running", []int64{1, 2}, "loss")
	h.flushLikeTheSyncer(projectID, "demo")

	longAgo := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	h.backdateRun("dead", longAgo)

	// The sibling keeps logging and flushing; "dead" is re-indexed each time.
	h.ingest("demo", "alive", "running", []int64{3}, "loss")
	h.flushLikeTheSyncer(projectID, "demo")
	if got := h.run("demo", "dead").UpdatedAt; !got.Equal(longAgo) {
		t.Fatalf("dead run's updated_at = %v after a sibling's flush, want it left at %v", got, longAgo)
	}

	// New rows for "dead" reach the parquet without an ingest touch (route A
	// appending to its export): the step grows, and that is life.
	dead := h.run("demo", "dead")
	if err := h.st.InsertPoints(h.ctx, dead.ID, []store.MetricPoint{
		{Step: 7, TS: time.Now(), Metrics: map[string]float64{"loss": 0.01}},
	}); err != nil {
		t.Fatalf("insert points: %v", err)
	}
	h.flushLikeTheSyncer(projectID, "demo")
	got := h.run("demo", "dead")
	if got.LastStep != 7 || time.Since(got.UpdatedAt) > time.Minute {
		t.Fatalf("after the parquet grew: last_step = %d, updated_at = %v; want 7 and now", got.LastStep, got.UpdatedAt)
	}
}

// TestIndexer_KeepsBufferedPointsInTheSummary: a run with points still in
// exp_points has a stored summary that is newer than its parquet. A re-index
// (triggered by anything -- another project's flush, a notes commit) must not
// roll its last value back, narrow its min / max to the flushed subset, or
// drop a metric only the buffered points carry. Once nothing is buffered, the
// parquet is the truth again.
func TestIndexer_KeepsBufferedPointsInTheSummary(t *testing.T) {
	h := newExpHarness(t)
	// ingest logs step/10: loss 0.1, 0.2, 0.3.
	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3}, "loss")
	h.flushLikeTheSyncer(projectID, "demo")

	// One more batch arrives, exactly as the ingest handler stores it: the
	// points go to exp_points and the run's summaries are merged.
	run := h.run("demo", "run-1")
	if err := h.st.InsertPoints(h.ctx, run.ID, []store.MetricPoint{
		{Step: 4, TS: time.Now(), Metrics: map[string]float64{"loss": 0.05, "acc": 0.9}},
	}); err != nil {
		t.Fatalf("insert points: %v", err)
	}
	if _, err := h.st.UpsertExpRunWith(h.ctx, projectID, store.ExpRunUpsert{
		Name:       "run-1",
		Summary:    map[string]any{"loss": 0.05, "acc": 0.9},
		SummaryMin: map[string]any{"loss": 0.05, "acc": 0.9},
		SummaryMax: map[string]any{"loss": 0.3, "acc": 0.9},
		MetricKeys: []string{"acc", "loss"},
		LastStep:   4, Touch: true,
	}); err != nil {
		t.Fatalf("upsert run: %v", err)
	}

	// A re-index before the flush sees only steps 1-3 in the parquet.
	h.reindex()
	run = h.run("demo", "run-1")
	if run.Summary["loss"] != 0.05 || run.Summary["acc"] != 0.9 {
		t.Fatalf("summary = %v, want the buffered values (loss 0.05, acc 0.9)", run.Summary)
	}
	if run.SummaryMin["loss"] != 0.05 || run.SummaryMax["loss"] != 0.3 ||
		run.SummaryMin["acc"] != 0.9 || run.SummaryMax["acc"] != 0.9 {
		t.Fatalf("min/max = %v/%v, want loss 0.05/0.3 and acc 0.9/0.9", run.SummaryMin, run.SummaryMax)
	}
	if !reflect.DeepEqual(run.MetricKeys, []string{"acc", "loss"}) {
		t.Fatalf("metric_keys = %v, want [acc loss]", run.MetricKeys)
	}

	// With nothing buffered, the parquet wins, even over a stored value it
	// disagrees with.
	h.flushLikeTheSyncer(projectID, "demo")
	if _, err := h.st.UpsertExpRunWith(h.ctx, projectID, store.ExpRunUpsert{
		Name: "run-1", Summary: map[string]any{"loss": 99.0, "acc": 0.9},
		SummaryMin: map[string]any{"loss": -1.0, "acc": 0.9},
	}); err != nil {
		t.Fatalf("upsert run: %v", err)
	}
	h.reindex()
	run = h.run("demo", "run-1")
	if run.Summary["loss"] != 0.05 || run.SummaryMin["loss"] != 0.05 {
		t.Fatalf("after the flush: summary/min = %v/%v, want the parquet's loss 0.05/0.05", run.Summary, run.SummaryMin)
	}
}
