package syncer

import (
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// flushHarness extends the push harness with the pieces the metrics flush
// needs: a dataset repository with the default .gitattributes, and a Syncer
// with the flusher wired the way cmd/thinkingface does.
type flushHarness struct {
	*harness
	repo    *store.Repo
	indexer *experiments.Indexer
}

func newFlushHarness(t *testing.T) *flushHarness {
	t.Helper()
	h := newHarness(t)

	// newHarness builds its own viewer/indexer internally; rebuild the same
	// wiring here so the test can query Series through it.
	parquet := viewer.New(h.obj, t.TempDir(), 0)
	indexer := experiments.NewIndexer(h.st, h.git, h.obj, parquet)
	h.syn.indexer = indexer
	h.syn.EnableFlush(experiments.NewFlusher(h.st, h.git, h.obj, parquet, "off"), time.Minute)

	h.user("alice")
	ns := h.namespace("alice")
	repo, err := h.st.CreateRepo(h.ctx, ns.ID, "trackio-metrics", "dataset", "", "main", store.NewStoragePath())
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	if err := h.git.Init(repo.StoragePath, "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	gitRepo, err := h.git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	newHash, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch: "main", Message: "initial",
		Author: gitrepo.Signature{Name: "alice", Email: "alice@example.com"},
		Ops: []gitrepo.Op{
			{Kind: gitrepo.OpAdd, Path: ".gitattributes", Data: []byte(gitrepo.DefaultGitAttributes("dataset"))},
			addOp("README.md", "# metrics\n"),
		},
	})
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if err := h.st.EnqueueSync(h.ctx, repo.ID, "main", "", newHash.String()); err != nil {
		t.Fatalf("enqueue push: %v", err)
	}
	h.step()

	return &flushHarness{harness: h, repo: repo, indexer: indexer}
}

func (h *flushHarness) ingest(project, run, status string, steps []int64) int64 {
	h.t.Helper()
	projectID, err := h.st.UpsertExpProject(h.ctx, h.repo.ID, project)
	if err != nil {
		h.t.Fatalf("upsert project: %v", err)
	}
	started := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	runID, err := h.st.UpsertExpRun(h.ctx, projectID, run, status, nil, nil, []string{"loss"},
		steps[len(steps)-1], 0, &started)
	if err != nil {
		h.t.Fatalf("upsert run: %v", err)
	}
	points := make([]store.MetricPoint, 0, len(steps))
	for _, step := range steps {
		points = append(points, store.MetricPoint{
			Step: step, TS: started.Add(time.Duration(step) * time.Second),
			Metrics: map[string]float64{"loss": float64(step) / 10},
		})
	}
	if err := h.st.InsertPoints(h.ctx, runID, points); err != nil {
		h.t.Fatalf("insert points: %v", err)
	}
	return projectID
}

// TestFlushProject_CommitsPublishesAndDrainsTheBuffer walks the full promise
// of docs/dev/thinkingface-design.md §8 for route B: the buffered points become a
// commit in the dataset repository, the committed parquet is readable from
// object storage (so `gcloud storage` and DuckDB can reach it), the repository
// is recognised as an experiment, and exp_points is emptied.
func TestFlushProject_CommitsPublishesAndDrainsTheBuffer(t *testing.T) {
	h := newFlushHarness(t)
	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3})

	before, err := h.indexer.Series(h.ctx, h.repo, experiments.SeriesRequest{Project: "demo"})
	if err != nil {
		t.Fatalf("series before: %v", err)
	}

	if err := h.syn.FlushProject(h.ctx, h.repo.ID, projectID, "demo"); err != nil {
		t.Fatalf("flush project: %v", err)
	}

	points, err := h.st.ListProjectPoints(h.ctx, projectID, 0)
	if err != nil {
		t.Fatalf("list points: %v", err)
	}
	if len(points) != 0 {
		t.Errorf("buffered points after flush = %d, want 0", len(points))
	}

	// The flusher's own .gitattributes routes metrics.parquet to LFS, so the
	// bytes are at the object's content-addressed key.
	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	entry, _, err := gitRepo.Stat("main", "demo/metrics.parquet")
	if err != nil {
		t.Fatalf("stat flushed parquet: %v", err)
	}
	key := storage.BlobKey(entry.Hash.String())
	if entry.LFS != nil {
		key = storage.LFSKey(entry.LFS.OID)
	}
	if _, err := h.obj.Stat(h.ctx, key); err != nil {
		t.Errorf("flushed parquet missing from %s: %v", key, err)
	}

	fresh, err := h.st.GetRepoByID(h.ctx, h.repo.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if !fresh.IsExperiment {
		t.Error("repository was not marked as an experiment after the flush")
	}

	after, err := h.indexer.Series(h.ctx, fresh, experiments.SeriesRequest{Project: "demo"})
	if err != nil {
		t.Fatalf("series after: %v", err)
	}
	if len(after) != len(before) || len(after) != 1 || len(after[0].Points) != len(before[0].Points) {
		t.Fatalf("series after flush = %#v, want the same shape as %#v", after, before)
	}

	// The parquet index the file listing reads must know about it too.
	files, err := h.st.ListParquetFiles(h.ctx, h.repo.ID, "main")
	if err != nil {
		t.Fatalf("list parquet files: %v", err)
	}
	found := false
	for _, f := range files {
		if f.Path == "demo/metrics.parquet" {
			found = true
			if f.NumRows != 3 {
				t.Errorf("indexed row count = %d, want 3", f.NumRows)
			}
		}
	}
	if !found {
		t.Error("demo/metrics.parquet was not added to the parquet index")
	}
}

// TestFlushDue_OnlyTerminalRunsJumpTheInterval pins the scheduling rule: a
// project that was just flushed waits out its interval, unless one of its runs
// has finished or failed, in which case its remaining points go out at once.
func TestFlushDue_OnlyTerminalRunsJumpTheInterval(t *testing.T) {
	h := newFlushHarness(t)
	projectID := h.ingest("demo", "run-1", "running", []int64{1})

	if err := h.syn.flushDue(h.ctx); err != nil {
		t.Fatalf("first flushDue: %v", err)
	}
	if n := h.bufferedPoints(projectID); n != 0 {
		t.Fatalf("points after the first flush = %d, want 0", n)
	}

	// Still running, and the interval has not elapsed: the new points stay put.
	h.ingest("demo", "run-1", "running", []int64{2})
	if err := h.syn.flushDue(h.ctx); err != nil {
		t.Fatalf("second flushDue: %v", err)
	}
	if n := h.bufferedPoints(projectID); n != 1 {
		t.Errorf("points while the interval is open = %d, want 1 (still buffered)", n)
	}

	// The run finishes: the next poll must not wait for the interval.
	if _, err := h.st.UpsertExpRun(h.ctx, projectID, "run-1", "finished", nil, nil, nil, 0, 0, nil); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if err := h.syn.flushDue(h.ctx); err != nil {
		t.Fatalf("third flushDue: %v", err)
	}
	if n := h.bufferedPoints(projectID); n != 0 {
		t.Errorf("points after the run finished = %d, want 0 (flushed immediately)", n)
	}
}

func (h *flushHarness) bufferedPoints(projectID int64) int {
	h.t.Helper()
	points, err := h.st.ListProjectPoints(h.ctx, projectID, 0)
	if err != nil {
		h.t.Fatalf("list points: %v", err)
	}
	return len(points)
}
