package experiments

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/encoding/thrift"
	"github.com/parquet-go/parquet-go/format"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// ---------------------------------------------------------------- fixtures

// memStorage is a minimal in-memory storage.Storage. Each package that needs
// one keeps its own copy (there is no shared exported fake to import).
type memStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemStorage() *memStorage { return &memStorage{objects: map[string][]byte{}} }

func (m *memStorage) SupportsSignedURL() bool { return false }

func (m *memStorage) SignedGetURL(context.Context, string, time.Duration, string) (string, error) {
	return "", errors.New("memStorage: signed URLs not supported")
}

func (m *memStorage) SignedPutURL(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("memStorage: signed URLs not supported")
}

func (m *memStorage) Put(_ context.Context, key string, r io.Reader, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = data
	return nil
}

func (m *memStorage) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memStorage) GetWithGeneration(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	rc, err := m.Get(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	return rc, 1, nil
}

func (m *memStorage) PutIfGeneration(context.Context, string, int64, io.Reader, string) (int64, error) {
	return 0, errors.New("memStorage: conditional writes not supported")
}

func (m *memStorage) GetRange(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, storage.ErrNotFound
	}
	end := int64(len(data))
	if length >= 0 && offset+length < end {
		end = offset + length
	}
	return io.NopCloser(bytes.NewReader(data[offset:end])), nil
}

func (m *memStorage) Stat(_ context.Context, key string) (storage.ObjectInfo, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return storage.ObjectInfo{}, storage.ErrNotFound
	}
	return storage.ObjectInfo{Key: key, Size: int64(len(data)), Updated: time.Now()}, nil
}

func (m *memStorage) Copy(_ context.Context, srcKey, dstKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[srcKey]
	if !ok {
		return storage.ErrNotFound
	}
	m.objects[dstKey] = data
	return nil
}

func (m *memStorage) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memStorage) List(_ context.Context, prefix string) ([]storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []storage.ObjectInfo
	for k, data := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, storage.ObjectInfo{Key: k, Size: int64(len(data))})
		}
	}
	return out, nil
}

func (m *memStorage) PublicURI(key string) string { return "mem://" + key }

var _ storage.Storage = (*memStorage)(nil)

// ------------------------------------------------------------ full harness

type expHarness struct {
	t   *testing.T
	ctx context.Context

	st      *store.Store
	git     *gitrepo.Manager
	obj     *memStorage
	indexer *Indexer
	flusher *Flusher
	repo    *store.Repo
}

func newExpHarness(t *testing.T) *expHarness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found in PATH; skipping")
	}
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	git := gitrepo.NewManager(t.TempDir())
	obj := newMemStorage()
	// A zero cache budget disables eviction; the viewer still caches on disk.
	parquet := viewer.New(obj, 8<<20)

	if _, err := st.CreateUser(ctx, "alice", "alice@example.com", "x", false); err != nil {
		t.Fatalf("create user: %v", err)
	}
	ns, err := st.GetNamespace(ctx, "alice")
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	repo, err := st.CreateRepo(ctx, ns.ID, "trackio-metrics", "dataset", "", "main", store.NewStoragePath())
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	if err := git.Init(repo.StoragePath, "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	gitRepo, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	// Real repositories are created with the default .gitattributes, which is
	// what routes *.parquet to LFS; the flush must exercise that path.
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch: "main", Message: "initial",
		Author: gitrepo.Signature{Name: "alice", Email: "alice@example.com"},
		Ops: []gitrepo.Op{
			{Kind: gitrepo.OpAdd, Path: ".gitattributes", Data: []byte(gitrepo.DefaultGitAttributes("dataset"))},
			{Kind: gitrepo.OpAdd, Path: "README.md", Data: []byte("# metrics\n")},
		},
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	h := &expHarness{
		t: t, ctx: ctx, st: st, git: git, obj: obj, repo: repo,
		indexer: NewIndexer(st, git, obj, parquet),
		flusher: NewFlusher(st, git, obj, parquet, "off"),
	}
	h.reindex()
	return h
}

// reindex reproduces what syncer.runPushPipeline does after a commit: refresh
// the file index the layout detection reads, then re-index the experiments.
func (h *expHarness) reindex() {
	h.t.Helper()
	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		h.t.Fatalf("open git repo: %v", err)
	}
	entries, _, err := gitRepo.Tree(h.repo.DefaultBranch, "", true)
	if err != nil {
		h.t.Fatalf("read tree: %v", err)
	}
	files := make([]store.RepoFile, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		f := store.RepoFile{Path: e.Path, Size: e.TargetSize(), BlobSHA: e.Hash.String()}
		if e.LFS != nil {
			oid := e.LFS.OID
			f.LFSOID = &oid
		}
		files = append(files, f)
	}
	if err := h.st.ReplaceRepoFiles(h.ctx, h.repo.ID, h.repo.DefaultBranch, files); err != nil {
		h.t.Fatalf("replace repo files: %v", err)
	}
	if err := h.indexer.IndexRepo(h.ctx, h.repo); err != nil {
		h.t.Fatalf("index repo: %v", err)
	}
}

// ingest writes one batch through the same store calls the ingest handler
// uses, so the buffered state under test is the real one.
func (h *expHarness) ingest(project, run, status string, steps []int64, key string) int64 {
	h.t.Helper()
	projectID, err := h.st.UpsertExpProject(h.ctx, h.repo.ID, project)
	if err != nil {
		h.t.Fatalf("upsert project: %v", err)
	}
	started := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	runID, err := h.st.UpsertExpRun(h.ctx, projectID, run, status, nil, nil, []string{key},
		steps[len(steps)-1], 0, &started)
	if err != nil {
		h.t.Fatalf("upsert run: %v", err)
	}
	points := make([]store.MetricPoint, 0, len(steps))
	for _, step := range steps {
		points = append(points, store.MetricPoint{
			Step:    step,
			TS:      started.Add(time.Duration(step) * time.Second),
			Metrics: map[string]float64{key: float64(step) / 10},
		})
	}
	if err := h.st.InsertPoints(h.ctx, runID, points); err != nil {
		h.t.Fatalf("insert points: %v", err)
	}
	return projectID
}

func (h *expHarness) series(project string) []Series {
	h.t.Helper()
	got, err := h.indexer.Series(h.ctx, h.repo, SeriesRequest{Project: project})
	if err != nil {
		h.t.Fatalf("series: %v", err)
	}
	return got
}

func (h *expHarness) flush(projectID int64, project string) *FlushResult {
	h.t.Helper()
	res, err := h.flusher.Flush(h.ctx, h.repo, projectID, project)
	if err != nil {
		h.t.Fatalf("flush: %v", err)
	}
	if res == nil {
		h.t.Fatal("flush: no result, want a commit")
	}
	return res
}

func (h *expHarness) pointCount(projectID int64) int {
	h.t.Helper()
	points, err := h.st.ListProjectPoints(h.ctx, projectID, 0)
	if err != nil {
		h.t.Fatalf("list points: %v", err)
	}
	return len(points)
}

// --------------------------------------------------------------- the tests

// TestFlush_SeriesIdenticalBeforeDuringAndAfter is the invariant the whole
// feature hangs on: moving points from the ingest buffer into the repository's
// parquet must not change a single pixel of the chart. It checks all three
// states a flush passes through -- buffered only, committed *and* still
// buffered (the window between the commit and the delete), and parquet only --
// because each one exercises a different half of the merge in series.go.
func TestFlush_SeriesIdenticalBeforeDuringAndAfter(t *testing.T) {
	h := newExpHarness(t)
	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3, 4, 5}, "loss")

	before := h.series("demo")
	if len(before) != 1 || len(before[0].Points) != 5 {
		t.Fatalf("buffered series = %#v, want 1 series of 5 points", before)
	}

	result := h.flush(projectID, "demo")
	if result.Path != "demo/metrics.parquet" {
		t.Errorf("flush path = %q, want demo/metrics.parquet", result.Path)
	}
	if result.NumAppended != 5 {
		t.Errorf("appended = %d, want 5", result.NumAppended)
	}
	h.reindex()

	// The dangerous window: the points are in git and still in exp_points.
	if got := h.series("demo"); !reflect.DeepEqual(got, before) {
		t.Errorf("series while both copies exist = %#v, want %#v (no duplicates)", got, before)
	}

	if err := h.st.DeletePoints(h.ctx, result.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}
	if n := h.pointCount(projectID); n != 0 {
		t.Errorf("buffered points after flush = %d, want 0", n)
	}
	if got := h.series("demo"); !reflect.DeepEqual(got, before) {
		t.Errorf("series after flush = %#v, want %#v (nothing lost)", got, before)
	}
}

// TestFlush_AppendsToTheSameFileAcrossFlushes covers the steady state of a
// long run: every interval appends to the one metrics.parquet, and the chart
// grows monotonically instead of being replaced.
func TestFlush_AppendsToTheSameFileAcrossFlushes(t *testing.T) {
	h := newExpHarness(t)

	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3}, "loss")
	first := h.flush(projectID, "demo")
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, first.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	h.ingest("demo", "run-1", "running", []int64{4, 5}, "loss")
	// A second run joins the same project, which must land in the same file.
	h.ingest("demo", "run-2", "finished", []int64{1, 2}, "loss")
	second := h.flush(projectID, "demo")
	if second.Path != first.Path {
		t.Errorf("second flush wrote %q, want the same file as the first (%q)", second.Path, first.Path)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, second.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	got := h.series("demo")
	if len(got) != 2 {
		t.Fatalf("series = %d, want one per run", len(got))
	}
	counts := map[string]int{}
	for _, s := range got {
		counts[s.Run] = len(s.Points)
	}
	if counts["run-1"] != 5 || counts["run-2"] != 2 {
		t.Errorf("points per run = %v, want run-1:5 run-2:2", counts)
	}

	// A run that was still going must not be reported as finished just
	// because the indexer found it in a parquet.
	project, err := h.st.GetExpProject(h.ctx, h.repo.ID, "demo")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	run, err := h.st.GetExpRun(h.ctx, project.ID, "run-1")
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("run-1 status after flush = %q, want running", run.Status)
	}
}

// TestFlush_RetryAfterCrashDoesNotDuplicate simulates the process dying
// between the commit and the delete: the same buffered points are flushed a
// second time. The ingest-id column must make that a no-op rather than
// doubling every measurement.
func TestFlush_RetryAfterCrashDoesNotDuplicate(t *testing.T) {
	h := newExpHarness(t)
	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3}, "loss")
	before := h.series("demo")

	first := h.flush(projectID, "demo")
	h.reindex()
	if first.NumAppended != 3 {
		t.Fatalf("first flush appended %d, want 3", first.NumAppended)
	}

	// No DeletePoints: the "crash" happened here.
	retry := h.flush(projectID, "demo")
	if retry.NumAppended != 0 {
		t.Errorf("retry appended %d rows, want 0 (already in the file)", retry.NumAppended)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, retry.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	if got := h.series("demo"); !reflect.DeepEqual(got, before) {
		t.Errorf("series after a retried flush = %#v, want %#v", got, before)
	}
}

// TestFlush_WritesAnLFSTrackedParquet checks the storage side of the contract:
// *.parquet is LFS-tracked by default, so the commit must carry a pointer and
// the bytes must be in the bucket (which is what makes the file reachable
// through resolve and `gcloud storage`).
func TestFlush_WritesAnLFSTrackedParquet(t *testing.T) {
	h := newExpHarness(t)
	projectID := h.ingest("demo", "run-1", "finished", []int64{1, 2}, "loss")
	result := h.flush(projectID, "demo")

	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	entry, _, err := gitRepo.Stat(h.repo.DefaultBranch, result.Path)
	if err != nil {
		t.Fatalf("stat %s: %v", result.Path, err)
	}
	if entry.LFS == nil {
		t.Fatalf("%s was committed inline, want an LFS pointer", result.Path)
	}
	if _, err := h.obj.Stat(h.ctx, storage.LFSKey(entry.LFS.OID)); err != nil {
		t.Errorf("lfs object %s missing from storage: %v", entry.LFS.OID, err)
	}
	if size, ok, err := h.st.HasLFSObject(h.ctx, entry.LFS.OID); err != nil || !ok || size == 0 {
		t.Errorf("lfs object not registered in the database (size=%d ok=%v err=%v)", size, ok, err)
	}
}

// TestFlush_UnreadableExistingFileLeavesPointsBuffered guards the failure
// mode that matters most: when the metrics file cannot be parsed (someone
// committed something else at that path, or the schema uses a shape this
// package will not rewrite), the flush must fail loudly and leave every point
// in the buffer. Dropping data because a file was surprising is not an option.
func TestFlush_UnreadableExistingFileLeavesPointsBuffered(t *testing.T) {
	h := newExpHarness(t)
	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2}, "loss")

	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch: h.repo.DefaultBranch, Message: "someone else's push",
		Author: gitrepo.Signature{Name: "bob", Email: "bob@example.com"},
		Ops:    []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "demo/metrics.parquet", Data: []byte("not a parquet")}},
	}); err != nil {
		t.Fatalf("racing commit: %v", err)
	}
	h.reindex()

	if _, err := h.flusher.Flush(h.ctx, h.repo, projectID, "demo"); err == nil {
		t.Fatal("flush over an unreadable metrics file: want an error, got nil")
	}
	if n := h.pointCount(projectID); n != 2 {
		t.Errorf("buffered points after a failed flush = %d, want 2 (nothing dropped)", n)
	}
}

// TestFlush_AppendsToARouteAExport is the §8 promise itself: a project that
// trackio already exported as a batch (route A) and that is now also being
// logged live (route B) must end up with one file holding both. The seeded
// file deliberately uses trackio's own shape -- a text timestamp and an `id`
// column -- so the rewrite has to preserve column types it did not choose.
func TestFlush_AppendsToARouteAExport(t *testing.T) {
	h := newExpHarness(t)

	routeA, err := writeMetricsParquet(
		[]flushColumn{
			stringColumn("run_name", false),
			int64Column("id"),
			int64Column("step"),
			stringColumn("timestamp", true),
			doubleColumn("loss"),
		},
		[]map[string]any{
			{"run_name": "batch-run", "id": int64(1), "step": int64(1), "timestamp": "2026-08-21T00:00:00Z", "loss": 1.0},
			{"run_name": "batch-run", "id": int64(2), "step": int64(2), "timestamp": "2026-08-21T00:00:01Z", "loss": 0.5},
		})
	if err != nil {
		t.Fatalf("build route A export: %v", err)
	}
	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch: h.repo.DefaultBranch, Message: "trackio batch sync",
		Author: gitrepo.Signature{Name: "trackio", Email: "trackio@example.com"},
		// Small enough to stay inline, which also proves the flush can read a
		// metrics file that is not an LFS object.
		Ops: []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "metrics.parquet", Data: routeA}},
	}); err != nil {
		t.Fatalf("commit route A export: %v", err)
	}
	h.reindex()

	// A root-level metrics.parquet is the repository's own project name.
	const project = "trackio-metrics"
	projectID := h.ingest(project, "live-run", "running", []int64{1, 2, 3}, "loss")

	result := h.flush(projectID, project)
	if result.Path != "metrics.parquet" {
		t.Fatalf("flush path = %q, want the existing metrics.parquet", result.Path)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, result.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	got := h.series(project)
	counts := map[string]int{}
	for _, s := range got {
		counts[s.Run] = len(s.Points)
	}
	if counts["batch-run"] != 2 || counts["live-run"] != 3 {
		t.Errorf("points per run = %v, want batch-run:2 live-run:3", counts)
	}
	// The `id` column trackio wrote is structural, so it must not have turned
	// into a second series.
	for _, s := range got {
		if s.Key != "loss" {
			t.Errorf("unexpected series key %q; only metrics belong on the chart", s.Key)
		}
	}
}

// TestMergePoints_MetricsCannotOverwriteStructuralColumns is the unit-level
// half of the reserved-name fix. The ingest API refuses these names now, but
// exp_points still holds whatever earlier builds accepted, and one such point
// is enough to rename a run in the committed parquet.
func TestMergePoints_MetricsCannotOverwriteStructuralColumns(t *testing.T) {
	existing := &existingTable{ingestIDs: map[int64]bool{}}
	ts := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	points := []store.PendingPoint{{
		ID: 7, RunName: "run-1", Step: 3, TS: ts,
		Metrics: map[string]float64{
			"loss":         0.25,
			"run_name":     0.5,
			"step":         99,
			"timestamp":    1,
			IngestIDColumn: 42,
		},
	}}

	columns, rows, appended := mergePoints(existing, points)
	if appended != 1 || len(rows) != 1 {
		t.Fatalf("appended = %d, rows = %d, want 1 and 1", appended, len(rows))
	}
	row := rows[0]
	if row["run_name"] != "run-1" {
		t.Errorf("run_name = %#v, want the point's own run name", row["run_name"])
	}
	if row["step"] != int64(3) {
		t.Errorf("step = %#v, want the point's own step", row["step"])
	}
	if row["timestamp"] != ts {
		t.Errorf("timestamp = %#v, want the point's own timestamp", row["timestamp"])
	}
	if row[IngestIDColumn] != int64(7) {
		t.Errorf("%s = %#v, want the point id (the idempotency key)", IngestIDColumn, row[IngestIDColumn])
	}
	if row["loss"] != 0.25 {
		t.Errorf("loss = %#v, want the metric to still be written", row["loss"])
	}
	// The structural columns keep the shape this package gives them; a
	// doubleColumn("run_name") would have made the file unreadable as well.
	for _, c := range columns {
		if c.name == "run_name" && (c.kind != colString || c.optional) {
			t.Errorf("run_name column = %+v, want a required string", c)
		}
		if c.name == "step" && c.kind != colInt64 {
			t.Errorf("step column = %+v, want an int64", c)
		}
	}
}

// TestFlush_BlocksAProjectThatCanNeverBeCommitted covers the rows an older
// build accepted before the ingest API rejected the name: `.git/metrics.parquet`
// is a path Commit always refuses, so without this the project would sit in
// the flush queue forever, warning every ten seconds and holding one of the
// hundred slots the poller has.
//
// The answer is to mark the project, not to throw its points away: they were
// accepted with a 200 and deleting them was silent data loss.
func TestFlush_BlocksAProjectThatCanNeverBeCommitted(t *testing.T) {
	h := newExpHarness(t)
	projectID := h.ingest(".git", "run-1", "running", []int64{1, 2, 3}, "loss")

	result, err := h.flusher.Flush(h.ctx, h.repo, projectID, ".git")
	if !errors.Is(err, ErrFlushBlocked) {
		t.Fatalf("flush error = %v, want ErrFlushBlocked", err)
	}
	if result != nil {
		t.Fatalf("flush committed %+v, want nothing (the path is not committable)", result)
	}
	if n := h.pointCount(projectID); n != 3 {
		t.Errorf("buffered points after a blocked flush = %d, want all 3 kept", n)
	}
	assertFlushBlocked(t, h, projectID, "refusing to write inside .git")
}

// TestFlush_BlocksAFileTooLargeToRebuild pins the memory bound on its cheap
// path: a flush rebuilds the whole file, so a metrics parquet past
// maxExistingFlushRows is refused from its footer -- before a single row is
// decoded -- instead of loading every row into a map and taking the API
// process down with it. The buffered points survive the refusal.
//
// The footer is not a guarantee, only an optimisation; the guard that does not
// trust it is TestFlush_BlocksAFileWhoseFooterUnderstatesItsRows below. The two
// are told apart by the wording of flush_error, which is why the assertions
// here quote the row count the footer declared.
func TestFlush_BlocksAFileTooLargeToRebuild(t *testing.T) {
	h := newExpHarness(t)

	// A first, ordinary flush is what puts rows in the file at all.
	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3}, "loss")
	first := h.flush(projectID, "demo")
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, first.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	// Rather than build a million-row fixture, shrink the bound to below what
	// the file already holds; the code path is identical.
	restore := maxExistingFlushRows
	maxExistingFlushRows = 2
	t.Cleanup(func() { maxExistingFlushRows = restore })

	h.ingest("demo", "run-1", "running", []int64{4, 5}, "loss")
	result, err := h.flusher.Flush(h.ctx, h.repo, projectID, "demo")
	if !errors.Is(err, ErrFlushBlocked) {
		t.Fatalf("flush error = %v, want ErrFlushBlocked", err)
	}
	if result != nil {
		t.Fatalf("flush committed %+v, want nothing (the file is too large to rebuild)", result)
	}
	if n := h.pointCount(projectID); n != 2 {
		t.Errorf("buffered points after a blocked flush = %d, want both kept", n)
	}
	// "has 3 rows" is the footer path's wording and only the footer path's:
	// the scan path cannot quote a count, because the count it would quote is
	// the one that lied.
	assertFlushBlocked(t, h, projectID, "has 3 rows, more than the 2 a flush can rebuild")

	// What was already committed must be untouched: blocking a flush leaves
	// everything alone, it does not rewrite the file. The chart still shows
	// all five points, because the two that could not be committed are still
	// in the buffer Series reads -- which is the visible difference from the
	// behaviour this replaced, where they were simply gone.
	h.reindex()
	got := h.series("demo")
	if len(got) != 1 || len(got[0].Points) != 5 {
		t.Fatalf("series = %#v, want 3 committed points plus the 2 still buffered", got)
	}

	// Raising the bound again is the operator's fix, and nothing has to be
	// told about it: the block simply stops mattering on the next attempt,
	// and a successful flush clears the mark.
	maxExistingFlushRows = restore
	if _, err := h.flusher.Flush(h.ctx, h.repo, projectID, "demo"); err != nil {
		t.Fatalf("flush after the cause was fixed: %v", err)
	}
	blocked, err := h.st.ListBlockedFlushProjects(h.ctx)
	if err != nil {
		t.Fatalf("list blocked: %v", err)
	}
	if len(blocked) != 0 {
		t.Errorf("blocked projects after a successful flush = %+v, want none", blocked)
	}
}

// TestFlush_BlocksAFileWhoseFooterUnderstatesItsRows covers the guard the
// footer check cannot provide. A parquet's file-level num_rows and its
// per-row-group num_rows are separate thrift fields and no reader is obliged to
// check that one is the sum of the others, so a file can declare three rows and
// hand the scan a million. Nothing between the object store and readExisting
// re-derives the count, so if only the footer were trusted, such a file would
// walk straight into the row-by-row rebuild this bound exists to prevent -- and
// the project would go back to retrying it at full rate for ever, which is the
// starvation blockFlush was written to stop.
//
// The fixture is a real flushed metrics parquet whose footer is rewritten to
// claim one row while its row groups still hold three; the bound is then set
// between the two, so only the scan can catch it.
func TestFlush_BlocksAFileWhoseFooterUnderstatesItsRows(t *testing.T) {
	h := newExpHarness(t)

	// An ordinary flush is what puts rows in the file at all. No reindex
	// afterwards, so nothing has read (and cached) the object before it is
	// rewritten below.
	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3}, "loss")
	first := h.flush(projectID, "demo")
	if err := h.st.DeletePoints(h.ctx, first.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}
	understateFooterRows(t, h, "demo/metrics.parquet", 1)

	// One is under the bound, three is over it: the footer check passes and
	// only the scan can refuse the file.
	restore := maxExistingFlushRows
	maxExistingFlushRows = 2
	t.Cleanup(func() { maxExistingFlushRows = restore })

	h.ingest("demo", "run-1", "running", []int64{4, 5}, "loss")
	result, err := h.flusher.Flush(h.ctx, h.repo, projectID, "demo")
	if !errors.Is(err, ErrFlushBlocked) {
		t.Fatalf("flush error = %v, want ErrFlushBlocked -- the scan trusted the footer", err)
	}
	if result != nil {
		t.Fatalf("flush committed %+v, want nothing (the file is too large to rebuild)", result)
	}
	if n := h.pointCount(projectID); n != 2 {
		t.Errorf("buffered points after a blocked flush = %d, want both kept", n)
	}
	// The scan path's wording, and only the scan path's: it does not quote a
	// row count, because the count it could quote is the one that lied.
	assertFlushBlocked(t, h, projectID, "its footer declares fewer than it contains")
}

// understateFooterRows rewrites the footer of the LFS-stored parquet at path so
// its file-level num_rows reads numRows, leaving every row group -- and so
// every row a scan produces -- exactly as it was. Only the trailing metadata is
// replaced, so the row groups' recorded offsets stay valid.
func understateFooterRows(t *testing.T, h *expHarness, path string, numRows int64) {
	t.Helper()
	gitRepo, err := h.git.Open(h.repo.StoragePath)
	if err != nil {
		t.Fatalf("open git repo: %v", err)
	}
	entry, _, err := gitRepo.Stat(h.repo.DefaultBranch, path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if entry.LFS == nil {
		t.Fatalf("%s is not an LFS file; the fixture assumes .gitattributes routes parquet to LFS", path)
	}
	key := storage.LFSKey(entry.LFS.OID)
	rc, err := h.obj.Get(h.ctx, key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}

	// [row groups][FileMetaData thrift][uint32 footer length][PAR1]
	if len(data) < 8 || string(data[len(data)-4:]) != "PAR1" {
		t.Fatalf("%s is not a parquet file", path)
	}
	size := int64(binary.LittleEndian.Uint32(data[len(data)-8 : len(data)-4]))
	start := int64(len(data)) - 8 - size
	if start < 0 {
		t.Fatalf("footer length %d does not fit in %d bytes", size, len(data))
	}
	var proto thrift.CompactProtocol
	var meta format.FileMetaData
	if err := thrift.NewDecoder(proto.NewReader(bytes.NewReader(data[start : start+size]))).Decode(&meta); err != nil {
		t.Fatalf("decode footer: %v", err)
	}
	if meta.NumRows <= numRows {
		t.Fatalf("footer already declares %d rows, want more than %d", meta.NumRows, numRows)
	}
	meta.NumRows = numRows

	var footer bytes.Buffer
	if err := thrift.NewEncoder(proto.NewWriter(&footer)).Encode(&meta); err != nil {
		t.Fatalf("encode footer: %v", err)
	}
	out := make([]byte, 0, int(start)+footer.Len()+8)
	out = append(out, data[:start]...)
	out = append(out, footer.Bytes()...)
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(footer.Len()))
	out = append(out, n[:]...)
	out = append(out, "PAR1"...)

	if err := h.obj.Put(h.ctx, key, bytes.NewReader(out), "application/octet-stream"); err != nil {
		t.Fatalf("put forged %s: %v", key, err)
	}
}

// assertFlushBlocked checks that the project was marked rather than emptied,
// and that the reason travelled with the mark -- an operator has to be able to
// find out why a buffer is not draining.
func assertFlushBlocked(t *testing.T, h *expHarness, projectID int64, wantReason string) {
	t.Helper()
	blocked, err := h.st.ListBlockedFlushProjects(h.ctx)
	if err != nil {
		t.Fatalf("list blocked flush projects: %v", err)
	}
	for _, b := range blocked {
		if b.ProjectID != projectID {
			continue
		}
		if !strings.Contains(b.Error, wantReason) {
			t.Errorf("flush_error = %q, want it to mention %q", b.Error, wantReason)
		}
		if b.NumPoints == 0 {
			t.Errorf("blocked project reports %d buffered points, want the buffer kept", b.NumPoints)
		}
		return
	}
	t.Fatalf("project %d is not listed as blocked (%+v)", projectID, blocked)
}

// ------------------------------------------------- rotation and column types

// TestFlush_RotatesRatherThanWritingAFileItCannotReadBack is the fix for a
// wedge this package inflicted on itself.
//
// maxExistingFlushRows bounded what a flush would *read* and nothing bounded
// what it *wrote*, so a flush would cheerfully append its 50,000 points to a
// file already holding a million. The commit succeeded; every flush after it
// read the footer, found more rows than it would accept, and blocked the
// project -- for ever, because nothing shrinks a committed file. The buffered
// points were kept (blockFlush's deliberate trade), so exp_points for that
// project then grew with no ceiling and no eviction. A long enough training
// run wedged its own project simply by continuing to log.
//
// Rotation keeps the two halves in step: the flush starts a continuation file
// and carries on, and every reader follows it (layout.MetricsFiles).
func TestFlush_RotatesRatherThanWritingAFileItCannotReadBack(t *testing.T) {
	h := newExpHarness(t)

	// Rather than build a million-row fixture, shrink the bound. Four rows is
	// above the first batch and below the first batch plus the second, so the
	// boundary is crossed by an ordinary append.
	restore := maxExistingFlushRows
	maxExistingFlushRows = 4
	t.Cleanup(func() { maxExistingFlushRows = restore })

	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3}, "loss")
	first := h.flush(projectID, "demo")
	if first.Path != "demo/metrics.parquet" {
		t.Fatalf("first flush wrote %q, want the base file", first.Path)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, first.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	// 3 rows + 3 points is over the bound, so this one rotates.
	h.ingest("demo", "run-1", "running", []int64{4, 5, 6}, "loss")
	second := h.flush(projectID, "demo")
	if second.Path != "demo/metrics.part0001.parquet" {
		t.Fatalf("second flush wrote %q, want a continuation file", second.Path)
	}
	if second.NumAppended != 3 {
		t.Errorf("appended = %d, want all 3 points in the new file", second.NumAppended)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, second.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	// Nothing was blocked, and nothing was lost: the chart still spans both
	// files, which is the only reason rotating beats refusing.
	blocked, err := h.st.ListBlockedFlushProjects(h.ctx)
	if err != nil {
		t.Fatalf("list blocked: %v", err)
	}
	if len(blocked) != 0 {
		t.Fatalf("blocked projects after a rotation = %+v, want none", blocked)
	}
	got := h.series("demo")
	if len(got) != 1 || len(got[0].Points) != 6 {
		t.Fatalf("series after rotation = %#v, want one trace of 6 points", got)
	}
	for i, p := range got[0].Points {
		if want := float64(i+1) / 10; p[1] != want {
			t.Errorf("point %d = %v, want y=%v", i, p, want)
		}
	}
	// The run index reads the same files, so its counters must agree.
	if run := h.run("demo", "run-1"); run.NumPoints != 6 || run.LastStep != 6 {
		t.Errorf("indexed run = num_points %d last_step %d, want 6 and 6", run.NumPoints, run.LastStep)
	}

	// The continuation file is now the active one: it fills before another is
	// started, so a rotation costs one extra file and not one per flush.
	h.ingest("demo", "run-1", "running", []int64{7}, "loss")
	third := h.flush(projectID, "demo")
	if third.Path != "demo/metrics.part0001.parquet" {
		t.Fatalf("third flush wrote %q, want the same continuation file", third.Path)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, third.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	// And it rotates again on the same rule.
	h.ingest("demo", "run-1", "running", []int64{8, 9}, "loss")
	fourth := h.flush(projectID, "demo")
	if fourth.Path != "demo/metrics.part0002.parquet" {
		t.Fatalf("fourth flush wrote %q, want a second continuation file", fourth.Path)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, fourth.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}
	if got := h.series("demo"); len(got) != 1 || len(got[0].Points) != 9 {
		t.Fatalf("series across three files = %#v, want one trace of 9 points", got)
	}
}

// TestFlush_RotationDoesNotDuplicateAlreadyWrittenPoints covers the crash
// window across a rotation: the commit lands and the process dies before the
// exp_points rows are deleted, so the same points are offered again.
//
// The rotation carries the ingest ids of the file it read forward for exactly
// this reason. Without them the retry would count those points as new, rotate
// again, and write every one of them a second time -- doubling the chart with
// no way back, since both copies are then committed history.
func TestFlush_RotationDoesNotDuplicateAlreadyWrittenPoints(t *testing.T) {
	h := newExpHarness(t)
	restore := maxExistingFlushRows
	maxExistingFlushRows = 4
	t.Cleanup(func() { maxExistingFlushRows = restore })

	projectID := h.ingest("demo", "run-1", "running", []int64{1, 2, 3}, "loss")
	first := h.flush(projectID, "demo")
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, first.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	h.ingest("demo", "run-1", "running", []int64{4, 5, 6}, "loss")
	second := h.flush(projectID, "demo")
	if second.Path != "demo/metrics.part0001.parquet" {
		t.Fatalf("second flush wrote %q, want a continuation file", second.Path)
	}
	// The crash: reindex, but never delete the rows.
	h.reindex()

	retry := h.flush(projectID, "demo")
	if retry.NumAppended != 0 {
		t.Errorf("retry appended %d points, want 0 (they are already in the file)", retry.NumAppended)
	}
	if retry.Path != "demo/metrics.part0001.parquet" {
		t.Errorf("retry wrote %q, want the continuation file it already wrote", retry.Path)
	}
	h.reindex()
	if err := h.st.DeletePoints(h.ctx, retry.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}
	if got := h.series("demo"); len(got) != 1 || len(got[0].Points) != 6 {
		t.Fatalf("series after the retry = %#v, want one trace of 6 points, not 9", got)
	}
}

// TestFlush_BlocksAFileWithAColumnItCannotRewrite covers the third condition
// routed to blockFlush, and the reason it had to be.
//
// A pushed metrics.parquet holding a repeated column, a nested group, an
// unannotated BYTE_ARRAY or an unknown logical type makes columnFromSchema
// refuse the file. That error used to fall through to the plain error return,
// which leaves the project unmarked -- so the poller picked it up again ten
// seconds later, for ever, *and* it held a permanent slot at the front of
// ListPendingFlushProjects' oldest-unflushed-point ordering, starving the
// other ninety-nine projects in the window. That starvation is the exact thing
// blockFlush exists to stop.
//
// It is not rotated past, unlike the row bound: the file may hold ingest ids
// that could not be read, so writing its points into a sibling could double
// them, and an operator pushed this file by hand and should be told.
func TestFlush_BlocksAFileWithAColumnItCannotRewrite(t *testing.T) {
	h := newExpHarness(t)
	// A BYTE_ARRAY with no STRING annotation: the viewer hands those back
	// base64-encoded, so rewriting one would change its bytes.
	h.commitParquet("demo.parquet",
		[]flushColumn{
			stringColumn("run_name", false),
			int64Column("step"),
			{name: "blob", kind: colString, node: parquet.Leaf(parquet.ByteArrayType), optional: true},
		},
		[]map[string]any{{"run_name": "run-1", "step": int64(1), "blob": "x"}})

	projectID := h.ingest("demo", "run-1", "running", []int64{2, 3}, "loss")
	result, err := h.flusher.Flush(h.ctx, h.repo, projectID, "demo")
	if !errors.Is(err, ErrFlushBlocked) {
		t.Fatalf("flush error = %v, want ErrFlushBlocked", err)
	}
	if result != nil {
		t.Fatalf("flush committed %+v, want nothing", result)
	}
	if n := h.pointCount(projectID); n != 2 {
		t.Errorf("buffered points after a blocked flush = %d, want both kept", n)
	}
	assertFlushBlocked(t, h, projectID, "unannotated BYTE_ARRAY is not supported")
}

// TestMergePoints_WidensAnIntegerColumnForAFractionalValue pins the type
// promotion. The existing file decides a column's type, which is right until
// the values change shape: a trackio export that wrote `epoch` as an integer
// typed the column INT64, and a later trackio.log({"epoch": 3.5}) went through
// toInt and was stored as 3 -- charted as 3, summarised as 3, with nothing
// anywhere recording that a value had been altered.
func TestMergePoints_WidensAnIntegerColumnForAFractionalValue(t *testing.T) {
	existing := &existingTable{
		columns: []flushColumn{
			stringColumn("run_name", false),
			int64Column("step"),
			int64Column("epoch"),
			int64Column("tokens"),
		},
		rows:      []map[string]any{{"run_name": "run-1", "step": int64(1), "epoch": int64(1), "tokens": int64(10)}},
		ingestIDs: map[int64]bool{},
	}
	points := []store.PendingPoint{{
		ID: 7, RunName: "run-1", Step: 2, TS: time.Now(),
		Metrics: map[string]float64{"epoch": 3.5, "tokens": 20},
	}}

	columns, rows, appended := mergePoints(existing, points)
	if appended != 1 || len(rows) != 2 {
		t.Fatalf("appended = %d, rows = %d, want 1 and 2", appended, len(rows))
	}
	byName := map[string]flushColumn{}
	for _, c := range columns {
		byName[c.name] = c
	}
	if got := byName["epoch"].kind; got != colDouble {
		t.Errorf("epoch column kind = %v, want colDouble: 3.5 does not fit an integer column", got)
	}
	// A metric whose values still fit keeps the type the file gave it.
	if got := byName["tokens"].kind; got != colInt64 {
		t.Errorf("tokens column kind = %v, want colInt64: 20 loses nothing as an integer", got)
	}
	// Structural columns are not metrics and are never widened; readers use
	// step as an int64.
	if got := byName["step"].kind; got != colInt64 {
		t.Errorf("step column kind = %v, want colInt64", got)
	}
}

// TestFlush_FractionalValueSurvivesAnIntegerTypedColumn is the same promotion
// end to end: a route-A export types the column, route B logs a fraction into
// it, and the value that comes back out of the parquet is the one that was
// logged.
func TestFlush_FractionalValueSurvivesAnIntegerTypedColumn(t *testing.T) {
	h := newExpHarness(t)
	h.commitParquet("demo.parquet",
		[]flushColumn{
			stringColumn("run_name", false),
			int64Column("step"),
			stringColumn("timestamp", true),
			int64Column("epoch"),
		},
		[]map[string]any{
			{"run_name": "run-1", "step": int64(1), "timestamp": "2026-08-22T00:00:00Z", "epoch": int64(1)},
		})

	// The harness logs {key: step/10}, so step 3 is an epoch of 0.3.
	projectID := h.ingest("demo", "run-1", "running", []int64{3}, "epoch")
	result := h.flush(projectID, "demo")
	if result.Path != "demo.parquet" {
		t.Fatalf("flush wrote %q, want the file route A created", result.Path)
	}
	h.reindex()
	// Deleted, so what the chart shows can only have come out of the parquet.
	if err := h.st.DeletePoints(h.ctx, result.PointIDs); err != nil {
		t.Fatalf("delete points: %v", err)
	}

	got := h.series("demo")
	if len(got) != 1 || len(got[0].Points) != 2 {
		t.Fatalf("series = %#v, want one trace of 2 points", got)
	}
	if got[0].Points[1][1] != 0.3 {
		t.Errorf("epoch at step 3 = %v, want 0.3 (an integer column truncated it to 0)", got[0].Points[1][1])
	}
	if run := h.run("demo", "run-1"); run.Summary["epoch"] != 0.3 {
		t.Errorf("summary epoch = %v, want 0.3", run.Summary["epoch"])
	}
}
