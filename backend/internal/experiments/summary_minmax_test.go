package experiments

import (
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// TestRunAggregate_Observe pins the three per-metric summaries the indexer
// keeps: last value in scan order, and the extremes over every point.
func TestRunAggregate_Observe(t *testing.T) {
	agg := newRunAggregate()
	for _, v := range []float64{0.5, 0.1, 0.9, 0.3} {
		agg.observe("loss", v)
	}
	agg.observe("acc", -2)

	if agg.lastValues["loss"] != 0.3 || agg.minValues["loss"] != 0.1 || agg.maxValues["loss"] != 0.9 {
		t.Fatalf("loss last/min/max = %v/%v/%v, want 0.3/0.1/0.9",
			agg.lastValues["loss"], agg.minValues["loss"], agg.maxValues["loss"])
	}
	// A single (negative) value is its own min and max: the zero value of an
	// unset map entry must not win.
	if agg.minValues["acc"] != -2 || agg.maxValues["acc"] != -2 {
		t.Fatalf("acc min/max = %v/%v, want -2/-2", agg.minValues["acc"], agg.maxValues["acc"])
	}
	if !agg.keys["loss"] || !agg.keys["acc"] {
		t.Fatalf("keys = %v", agg.keys)
	}
}

// TestIndexer_WritesSummaryMinMax runs the real path: points are buffered,
// flushed into the parquet, and the re-index computes summary_min /
// summary_max from the file. The stored summaries are wiped once the points
// are no longer buffered, so every value checked here came from the indexer.
func TestIndexer_WritesSummaryMinMax(t *testing.T) {
	h := newExpHarness(t)
	// ingest logs step/10 for each step: 0.5, 0.1, 0.3 in that order.
	projectID := h.ingest("demo", "run-1", "running", []int64{5, 1, 3}, "loss")
	h.flushLikeTheSyncer(projectID, "demo")
	if _, err := h.st.UpsertExpRunWith(h.ctx, projectID, store.ExpRunUpsert{
		Name: "run-1", Summary: map[string]any{}, SummaryMin: map[string]any{}, SummaryMax: map[string]any{},
	}); err != nil {
		t.Fatalf("wipe summaries: %v", err)
	}
	h.reindex()

	run := h.run("demo", "run-1")
	if run.SummaryMin["loss"] != 0.1 || run.SummaryMax["loss"] != 0.5 {
		t.Fatalf("min/max = %v/%v, want loss 0.1/0.5", run.SummaryMin, run.SummaryMax)
	}
	// The last value is the one at the highest step (5), not the last row
	// written (step 3): see runAggregate.observeAt.
	if run.Summary["loss"] != 0.5 {
		t.Fatalf("summary = %v, want loss 0.5 (the highest step)", run.Summary)
	}
}

// A replayed spill appends old steps after newer ones; the last value must be
// the one at the highest step, not the last row written.
func TestRunAggregateLastValueFollowsTheHighestStep(t *testing.T) {
	agg := newRunAggregate()
	agg.observeAt(10, "loss", 0.2)
	agg.observeAt(3, "loss", 0.9) // replayed late
	if got := agg.lastValues["loss"]; got != 0.2 {
		t.Fatalf("last loss = %v; want 0.2 (step 10), not the replayed step 3", got)
	}
	if agg.maxValues["loss"] != 0.9 || agg.minValues["loss"] != 0.2 {
		t.Fatalf("min/max = %v/%v; the replayed value still widens the extremes",
			agg.minValues["loss"], agg.maxValues["loss"])
	}
	agg.observeAt(10, "loss", 0.25) // same step rewritten later: the later row wins
	if got := agg.lastValues["loss"]; got != 0.25 {
		t.Fatalf("last loss = %v; want 0.25 (a later row at the same step)", got)
	}
}
