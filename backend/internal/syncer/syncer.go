// Package syncer runs the post-push work: publishing the revision's file
// contents into GCS and refreshing the metadata index.
//
// Storage is two content-addressed layers and nothing else: LFS bytes at
// storage.LFSKey(oid), plain git blobs at storage.BlobKey(sha). Both are
// immutable and deduplicated instance-wide, so a push only ever *adds* objects
// -- there is no mirror to prune, no key to vacate, and transferring, renaming
// or deleting a repository moves nothing at all. The human-readable layout
// users see is generated on the fly by the GCS endpoint, as the destination
// side of a `gcloud storage cp` script.
//
// `thinkingface gc` is what reclaims objects no repository references any more.
package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/experiments"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/repocard"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

const maxReadmeBytes = 256 << 10

const (
	// syncLease is how long a claimed job is held before the sweeper is
	// entitled to assume the worker died. It is deliberately short relative
	// to how long a large first push takes to publish, because the worker
	// extends it (see heartbeat): a short lease bounds how long a genuinely
	// dead claim blocks its ref, and the heartbeat is what keeps a slow but
	// live job from being taken away underneath itself.
	syncLease = 2 * time.Minute

	// syncHeartbeat must stay comfortably below syncLease so an ordinary
	// scheduling hiccup or a slow storage round-trip cannot let the lease
	// lapse while the worker is still running.
	syncHeartbeat = 30 * time.Second

	// syncSweep is how often expired leases are returned to the queue. The
	// same sweep runs once at startup (see cmd/thinkingface): a replica that
	// crashed mid-sync is recovered without waiting for anyone to restart
	// the survivors.
	syncSweep = time.Minute
)

// WebhookFirer records webhook deliveries for an event. Implemented by
// internal/webhooks.Dispatcher; kept as an interface here so the syncer never
// needs to import the delivery machinery.
type WebhookFirer interface {
	Fire(ctx context.Context, event, namespace string, repoID *int64, payload any) error
}

type Syncer struct {
	store    *store.Store
	git      *gitrepo.Manager
	storage  storage.Storage
	viewer   *viewer.Reader
	indexer  *experiments.Indexer
	webhooks WebhookFirer

	workers int
	wake    chan struct{}

	// Metrics flush (flush.go). Nil until EnableFlush wires it, which is what
	// keeps the flush out of tests that only exercise the push pipeline.
	flusher       *experiments.Flusher
	flushInterval time.Duration
	flushMu       sync.Mutex
	lastFlush     map[int64]time.Time // exp_projects.id -> last successful flush
}

func New(st *store.Store, git *gitrepo.Manager, obj storage.Storage, v *viewer.Reader, ix *experiments.Indexer, wh WebhookFirer, workers int) *Syncer {
	if workers < 1 {
		workers = 1
	}
	return &Syncer{
		store: st, git: git, storage: obj, viewer: v, indexer: ix, webhooks: wh,
		workers: workers,
		// Buffered so an enqueue never blocks on a busy worker.
		wake:      make(chan struct{}, 1),
		lastFlush: map[int64]time.Time{},
	}
}

// fireWebhook is best-effort: a webhook failure must never fail the sync job
// that triggered it, and a Syncer built without a Dispatcher (as in tests)
// simply fires nothing.
func (s *Syncer) fireWebhook(ctx context.Context, event, ns string, repoID *int64, payload any) {
	if s.webhooks == nil {
		return
	}
	if err := s.webhooks.Fire(ctx, event, ns, repoID, payload); err != nil {
		slog.Warn("fire webhook", "event", event, "namespace", ns, "error", err)
	}
}

// Enqueue records work and nudges an idle worker so the job does not wait for
// the next poll tick. It satisfies api.Enqueuer.
func (s *Syncer) Enqueue(ctx context.Context, repoID int64, ref, oldSHA, newSHA string) error {
	if err := s.store.EnqueueSync(ctx, repoID, ref, oldSHA, newSHA); err != nil {
		return fmt.Errorf("enqueue sync job: %w", err)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

// Run starts the worker pool and the lease sweeper, and blocks until ctx is
// cancelled.
func (s *Syncer) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.sweepLoop(ctx)
	}()
	for i := range s.workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s.loop(ctx, id)
		}(i)
	}
	wg.Wait()
}

// sweepLoop returns jobs whose lease lapsed to the queue. Without it a replica
// that dies mid-sync leaves its ref blocked until somebody restarts a process,
// because ClaimSyncJob refuses a ref that has any 'running' sibling.
func (s *Syncer) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(syncSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		n, err := s.store.RequeueExpiredSyncJobs(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				slog.Error("requeue expired sync jobs", "error", err)
			}
			continue
		}
		if n > 0 {
			// Worth a log line: every row here is a job whose worker
			// vanished, which is the visible symptom of a crash elsewhere.
			slog.Warn("requeued expired sync jobs", "count", n)
			s.nudge()
		}
	}
}

// nudge wakes one idle worker without blocking if none is waiting.
func (s *Syncer) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Syncer) loop(ctx context.Context, id int) {
	// The wake channel covers the common case; the ticker is a safety net for
	// jobs enqueued by another replica or left behind by a restart.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		worked, err := s.step(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("sync worker", "worker", id, "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Syncer) step(ctx context.Context) (bool, error) {
	job, err := s.store.ClaimSyncJob(ctx, syncLease)
	if err != nil || job == nil {
		return false, err
	}

	// Hold the lease for as long as the job actually runs. A first push of a
	// large repository publishes for longer than one lease, and without this
	// the sweeper would hand the ref to a second worker while this one is
	// still walking the diff -- the exact double-publish ClaimSyncJob's
	// NOT EXISTS clause exists to prevent.
	stopHeartbeat := s.heartbeat(ctx, job.ID)
	jobErr := s.process(ctx, job)
	stopHeartbeat()

	if jobErr != nil {
		slog.Error("sync job failed", "job", job.ID, "repo", job.RepoID, "ref", job.Ref,
			"attempt", job.Attempts, "max_attempts", store.SyncMaxAttempts, "error", jobErr)
	}
	// FinishSyncJob decides retry-with-backoff vs park-as-failed from the
	// attempt count on the row, so a cancelled context must not skip it --
	// the lease would then have to lapse before anything happened. It runs
	// on a background context for exactly that reason.
	finishCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		finishCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	if err := s.store.FinishSyncJob(finishCtx, job.ID, jobErr); err != nil {
		return true, fmt.Errorf("finish sync job %d: %w", job.ID, err)
	}
	return true, nil
}

// heartbeat keeps a claimed job's lease alive until the returned function is
// called. The returned stop is idempotent and waits for the goroutine, so the
// caller can be sure no heartbeat lands after FinishSyncJob.
func (s *Syncer) heartbeat(ctx context.Context, jobID int64) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(syncHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if err := s.store.HeartbeatSyncJob(ctx, jobID, syncLease); err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.Warn("heartbeat sync job", "job", jobID, "error", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}

// process dispatches a claimed job by kind. "push" -- and "" for rows that
// predate the kind column -- is the only kind there is; anything else is a
// row from a version of this binary that knew a pipeline this one does not,
// and failing loudly beats silently running the wrong one.
func (s *Syncer) process(ctx context.Context, job *store.SyncJob) error {
	repo, err := s.store.GetRepoByID(ctx, job.RepoID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The repository was deleted while the job waited; nothing to do.
			return nil
		}
		return err
	}

	switch job.Kind {
	case "", "push":
		return s.processPush(ctx, repo, job)
	default:
		return fmt.Errorf("sync job %d: unknown kind %q", job.ID, job.Kind)
	}
}

// processPush runs the normal post-push pipeline for one ref and announces it
// as repo.push. The pipeline itself is shared with the metrics flush
// (flush.go), which re-indexes its own commit inline but must not masquerade
// as somebody's push.
func (s *Syncer) processPush(ctx context.Context, repo *store.Repo, job *store.SyncJob) error {
	changed, err := s.runPushPipeline(ctx, repo, job)
	if err != nil || changed == nil {
		return err
	}
	s.fireWebhook(ctx, "repo.push", changed.repo.Namespace, &changed.repo.ID, map[string]any{
		"namespace": changed.repo.Namespace, "repo": changed.repo.Name,
		"full_name": changed.repo.FullName(), "kind": changed.repo.Kind,
		"ref": job.Ref, "old_sha": job.OldSHA, "new_sha": job.NewSHA,
		"changed_files": changed.numChangedFiles,
	})
	return nil
}

// pushOutcome is what runPushPipeline learned while re-indexing a revision:
// the repository row as it stands afterwards (the is_experiment flag may have
// been set by this very run) and how many files the revision touched.
type pushOutcome struct {
	repo            *store.Repo
	numChangedFiles int
}

// runPushPipeline publishes one ref's blobs and refreshes the
// file/parquet/lineage/experiment indexes. A nil outcome with a nil error
// means there was nothing to index (missing or empty repository).
func (s *Syncer) runPushPipeline(ctx context.Context, repo *store.Repo, job *store.SyncJob) (*pushOutcome, error) {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		if errors.Is(err, gitrepo.ErrRepoNotFound) {
			return nil, nil
		}
		return nil, err
	}

	entries, commit, err := gitRepo.Tree(job.Ref, "", true)
	if err != nil {
		if errors.Is(err, gitrepo.ErrEmptyRepo) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tree: %w", err)
	}

	files := make([]store.RepoFile, 0, len(entries))
	var totalSize int64
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		f := store.RepoFile{Path: e.Path, Size: e.TargetSize(), BlobSHA: e.Hash.String()}
		if e.LFS != nil {
			oid := e.LFS.OID
			f.LFSOID = &oid
		}
		totalSize += f.Size
		files = append(files, f)
	}

	// Publish before indexing, so "repo_files has the row" always implies
	// "blobs/ has the object": a job that dies mid-publish leaves the index
	// as it was and the next job for the ref publishes the rest. Every ref,
	// not just the default branch -- a blobs/ key is the content's hash, so
	// publishing a branch or a tag adds nothing a reader of another ref could
	// be confused by, and costs nothing for content already there.
	indexed, err := s.store.ListIndexedBlobSHAs(ctx, repo.ID, job.Ref)
	if err != nil {
		return nil, fmt.Errorf("list indexed blobs: %w", err)
	}
	if err := s.publishBlobs(ctx, gitRepo, entries, indexed); err != nil {
		return nil, fmt.Errorf("publish blobs: %w", err)
	}
	if err := s.store.ReplaceRepoFiles(ctx, repo.ID, job.Ref, files); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Deleted between the GetRepoByID in process and now -- the same
			// "nothing left to index" as a missing git directory above.
			return nil, nil
		}
		return nil, fmt.Errorf("update file index: %w", err)
	}

	card := repocard.Card{Data: map[string]any{}}
	if readme, err := gitRepo.ReadFile(job.Ref, "README.md", maxReadmeBytes); err == nil {
		card = repocard.Parse(readme)
	}

	if job.Ref == repo.DefaultBranch {
		if err := s.store.UpdateRepoIndex(ctx, repo.ID, commit.String(), totalSize,
			card.Data, card.Description(), card.IsExperiment() || looksLikeExperiment(files)); err != nil {
			return nil, fmt.Errorf("update repository index: %w", err)
		}
		// Lineage follows the card, so it is rebuilt from the same parse: an
		// edge removed from the README disappears from the index on this push.
		if err := s.store.ReplaceRepoLineage(ctx, repo.ID, lineageEdges(repo.Kind, card, files)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, nil
			}
			return nil, fmt.Errorf("update lineage index: %w", err)
		}
	}

	if err := s.indexParquet(ctx, repo, gitRepo, job.Ref, files); err != nil {
		return nil, fmt.Errorf("index parquet files: %w", err)
	}

	// Re-read the repository so the experiment indexer sees the flag we just
	// wrote rather than the pre-sync value. Adopt the fresh row only on
	// success: the repository can vanish between enqueue and here (a delete
	// racing a push), and the old `repo, err =` form nilled repo out on that
	// path — the webhook below then dereferenced nil and took the whole
	// process down. The remaining steps only attribute the event, so the
	// copy loaded at the start of this job is the right fallback.
	if fresh, err := s.store.GetRepoByID(ctx, repo.ID); err == nil {
		repo = fresh
		if repo.IsExperiment && job.Ref == repo.DefaultBranch {
			if err := s.indexer.IndexRepo(ctx, repo); err != nil {
				return nil, fmt.Errorf("index experiments: %w", err)
			}
		}
	}

	return &pushOutcome{repo: repo, numChangedFiles: len(s.changedPaths(gitRepo, job, entries))}, nil
}

// looksLikeExperiment recognises a trackio export even when the card carries no
// tag, which is the common case for a dataset trackio created itself.
func looksLikeExperiment(files []store.RepoFile) bool {
	for _, f := range files {
		if f.Path == "metrics.parquet" || strings.HasSuffix(f.Path, "/metrics.parquet") {
			return true
		}
	}
	return false
}

// publishBlobs puts every plain (non-LFS) file of the revision into the
// blobs/ layer, skipping the shas the ref's previous index already covers --
// those were published before that index was written. The decision is made
// against the index, not against the push's own old..new diff, so two jobs
// for one ref running side by side, or a job that failed after its push
// moved the ref, cannot leave a file out: whatever the last successful index
// did not cover gets published now. LFS files need nothing: their bytes were
// uploaded to lfs/ before the commit existed. Nothing is ever deleted here --
// a blobs/ key is immutable and may be shared by any number of repositories.
func (s *Syncer) publishBlobs(ctx context.Context, gitRepo *gitrepo.Repo, entries []gitrepo.Entry, indexed map[string]bool) error {
	published := make(map[plumbing.Hash]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir || entry.LFS != nil || published[entry.Hash] || indexed[entry.Hash.String()] {
			continue
		}
		published[entry.Hash] = true
		if _, err := gitRepo.PublishBlob(ctx, s.storage, entry.Hash); err != nil {
			return fmt.Errorf("publish %s: %w", entry.Path, err)
		}
	}
	return nil
}

// changedPaths returns the files this push touched. It falls back to the whole
// tree when the previous commit is unknown or unreadable, which is what happens
// on the first sync and after a force-push.
func (s *Syncer) changedPaths(gitRepo *gitrepo.Repo, job *store.SyncJob, entries []gitrepo.Entry) []gitrepo.Change {
	if job.OldSHA != "" && job.NewSHA != "" {
		oldHash, newHash := plumbingHash(job.OldSHA), plumbingHash(job.NewSHA)
		if !newHash.IsZero() {
			if changes, err := gitRepo.Diff(oldHash, newHash); err == nil {
				return changes
			}
		}
	}
	all := make([]gitrepo.Change, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			all = append(all, gitrepo.Change{Kind: gitrepo.ChangeAdd, Path: e.Path})
		}
	}
	return all
}

// indexParquet records schema and row counts so listings never have to open a
// parquet file.
func (s *Syncer) indexParquet(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, ref string, files []store.RepoFile) error {
	seen := map[string]bool{}
	for _, f := range files {
		if !strings.HasSuffix(strings.ToLower(f.Path), ".parquet") {
			continue
		}
		seen[f.Path] = true

		var key string
		if f.LFSOID != nil {
			key = storage.LFSKey(*f.LFSOID)
		} else {
			// publishBlobs has normally put this there already; a file the
			// push did not touch on a ref indexed for the first time has not
			// been, so make sure before handing the key to the viewer.
			published, err := gitRepo.PublishBlob(ctx, s.storage, plumbing.NewHash(f.BlobSHA))
			if err != nil {
				slog.Warn("parquet index skipped", "repo", repo.FullName(), "path", f.Path, "error", err)
				continue
			}
			key = published
		}

		schema, err := s.viewer.Schema(ctx, key)
		if err != nil {
			slog.Warn("parquet index skipped", "repo", repo.FullName(), "path", f.Path, "error", err)
			continue
		}
		raw, err := json.Marshal(schema.Columns)
		if err != nil {
			return err
		}
		if err := s.store.UpsertParquetFile(ctx, repo.ID, ref, f.Path,
			schema.NumRows, schema.NumRowGroups, raw); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Repository deleted mid-index: nothing left to record.
				return nil
			}
			return err
		}
	}

	// Drop index rows for parquet files that no longer exist.
	existing, err := s.store.ListParquetFiles(ctx, repo.ID, ref)
	if err != nil {
		return err
	}
	var stale []string
	for _, p := range existing {
		if !seen[p.Path] {
			stale = append(stale, p.Path)
		}
	}
	return s.store.DeleteParquetFiles(ctx, repo.ID, ref, stale)
}

// plumbingHash parses a stored SHA, yielding the zero hash for the empty or
// malformed values that mark "no previous commit".
func plumbingHash(s string) plumbing.Hash {
	if len(s) != 40 {
		return plumbing.ZeroHash
	}
	return plumbing.NewHash(s)
}
