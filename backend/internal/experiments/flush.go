// Flushing live ingest points into the dataset repository's parquet
// (docs/dev/thinkingface-design.md §8, route B). The native ingest API buffers
// points in exp_points so a chart can be live, but the *source of truth* for
// an experiment is the parquet inside the dataset repository -- that is what
// git versions, what `gcloud storage` fetches out of the content-addressed
// bucket, and what DuckDB reads. This file moves the buffer into that file and hands the caller the
// row ids it may then delete.

package experiments

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// MaxFlushPoints bounds how many buffered points one flush moves. The whole
// file is rebuilt in memory, so the cap is what keeps a runaway logger from
// turning a flush into an unbounded allocation; the remainder is picked up by
// the next tick.
const MaxFlushPoints = 50000

// maxExistingFlushRows bounds the *other* half of that allocation, which
// MaxFlushPoints never covered: the rows already in the file. readExisting
// materialises one map[string]any per row, so a repository carrying a
// multi-million-row trackio export (or any large parquet pushed by hand)
// turned a single live point into gigabytes of heap -- in the API process,
// since the syncer runs there (cmd/thinkingface/main.go) -- and the next tick
// did it again. A row costs roughly half a kilobyte once the map header,
// its buckets and the boxed values are counted, so a million rows is a
// several-hundred-megabyte flush: high enough that no realistic training run
// reaches it, low enough to survive.
//
// It is a var only so the tests can lower it; nothing changes it at runtime.
//
// TODO: the permanent fix is to append a row group to the existing file
// instead of rebuilding it, which would need neither cap. That is a rewrite of
// the writer (parquetwrite.go) and of the LFS blob handling around it, so it
// is deliberately not attempted here.
var maxExistingFlushRows int64 = 1_000_000

// tooManyExistingRowsError reports a metrics parquet this package cannot
// rebuild within its memory budget. Like unsupportedColumnError it describes a
// file, not a transient failure -- see abandonPoints for what happens to the
// buffered points.
type tooManyExistingRowsError struct {
	Path    string
	NumRows int64
	Max     int64
}

func (e *tooManyExistingRowsError) Error() string {
	return fmt.Sprintf("metrics parquet %s has %d rows, more than the %d a flush can rebuild in memory",
		e.Path, e.NumRows, e.Max)
}

// flushAuthor signs the machine-generated commits, so `git log` makes it
// obvious that nobody typed them.
var flushAuthor = gitrepo.Signature{Name: "thinkingface", Email: "noreply@thinkingface.local"}

// Flusher writes buffered ingest points into a dataset repository's metrics
// parquet and commits the result through the same WAL rules every other
// server-side write obeys (docs/dev/continuity-design.md §7).
type Flusher struct {
	store   *store.Store
	git     *gitrepo.Manager
	storage storage.Storage
	viewer  *viewer.Reader
	walMode string
}

func NewFlusher(st *store.Store, git *gitrepo.Manager, obj storage.Storage, v *viewer.Reader, walMode string) *Flusher {
	if walMode == "" {
		walMode = "off"
	}
	return &Flusher{store: st, git: git, storage: obj, viewer: v, walMode: walMode}
}

// FlushResult describes a completed flush. PointIDs are the exp_points rows
// now durably in git; the caller deletes them once it has re-indexed the
// repository, so the chart never loses the points in between.
type FlushResult struct {
	Path     string
	Ref      string
	OldSHA   string
	NewSHA   string
	PointIDs []int64
	// NumAppended counts the points this commit actually added. It is smaller
	// than len(PointIDs) when a previous attempt already wrote some of them
	// (see IngestIDColumn) -- those rows are still deleted, just not rewritten.
	NumAppended int
}

// Flush rebuilds the project's metrics parquet with its buffered points
// appended and commits it. It returns nil when there was nothing to do.
func (f *Flusher) Flush(ctx context.Context, repo *store.Repo, projectID int64, project string) (*FlushResult, error) {
	points, err := f.store.ListProjectPoints(ctx, projectID, MaxFlushPoints)
	if err != nil {
		return nil, fmt.Errorf("list buffered points: %w", err)
	}
	if len(points) == 0 {
		return nil, nil
	}

	// Authoritative mode serves reads from the WAL, so the local copy has to
	// be current before its tree is read or extended.
	if err := f.git.EnsureLocal(ctx, repo.StoragePath); err != nil {
		return nil, err
	}
	gitRepo, err := f.git.Open(repo.StoragePath)
	if err != nil {
		return nil, err
	}

	ref := repo.DefaultBranch
	if ref == "" {
		ref = "main"
	}
	path, err := f.metricsPath(ctx, repo, project)
	if err != nil {
		return nil, err
	}
	// A project whose name cannot be a path segment (".git", ".", "..") makes
	// a file Commit will always refuse, so the buffer is abandoned here rather
	// than failing at the end of every tick from now on. Ingest rejects such a
	// name today (api.validateIngestProject); this is for the rows an older
	// build already accepted.
	if perr := gitrepo.ValidatePath(path); perr != nil {
		return nil, f.abandonPoints(ctx, project, points, perr)
	}

	existing, baseOID, err := f.readExisting(ctx, repo, gitRepo, ref, path)
	if err != nil {
		var tooMany *tooManyExistingRowsError
		if errors.As(err, &tooMany) {
			return nil, f.abandonPoints(ctx, project, points, err)
		}
		return nil, err
	}

	columns, rows, appended := mergePoints(existing, points)
	data, err := writeMetricsParquet(columns, rows)
	if err != nil {
		return nil, err
	}

	blob, err := f.blobFor(ctx, gitRepo, repo, ref, path, data)
	if err != nil {
		return nil, err
	}

	newHash, oldHash, err := f.commit(ctx, repo, gitrepo.CommitRequest{
		Branch:  ref,
		Message: fmt.Sprintf("chore(trackio): flush %s metrics", project),
		Author:  gitrepo.Signature{Name: flushAuthor.Name, Email: flushAuthor.Email, When: time.Now()},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: blob}},
		// The optimistic lock is evaluated against the parent commit under the
		// mutex that picks it, so a push landing between readExisting and here
		// loses the race instead of being silently overwritten.
		Preconditions: []gitrepo.PathPrecondition{{Path: path, OID: baseOID}},
	})
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(points))
	for _, p := range points {
		ids = append(ids, p.ID)
	}
	return &FlushResult{
		Path: path, Ref: ref,
		OldSHA: oldHash.String(), NewSHA: newHash.String(),
		PointIDs: ids, NumAppended: appended,
	}, nil
}

// abandonPoints drops buffered points that can never reach the parquet, after
// saying so as loudly as this layer can. It returns the error from the delete
// (or nil), so its caller reports "there was nothing to flush" rather than a
// failure that would be retried.
//
// Deleting them is a deliberate departure from what every *transient* failure
// here does, which is to leave the buffer alone and try again next tick. The
// two conditions routed here -- a project name no path can hold, and a file
// too large to rebuild in memory -- are properties of the repository, not of
// this attempt: nothing about the next tick is different. Retrying forever
// would be tolerable if the damage stayed inside the one project, but it does
// not. exp_points grows without bound for it (and Series loads all of it to
// draw the live chart), and the poller takes its candidates from
// ListPendingFlushProjects, which takes the hundred projects whose oldest
// unflushed point has waited longest -- and a wedged project's oldest point
// only ever gets older, so a hundred of them sit at the front of that order
// forever and *no* project on the instance is flushed again. A buffer that
// can never drain is not a harmless buffer.
//
// The cost is real, which is why this is an error and carries the counts: the
// points were accepted with a 200 and are now gone from the chart as well.
// Only the batch this flush read is dropped, so the log says exactly how much
// was lost each time, and the operator's fix -- shrink or move the metrics
// file, or use a project name that can be committed -- takes effect on the
// next points ingested rather than on the ones already thrown away.
func (f *Flusher) abandonPoints(ctx context.Context, project string, points []store.PendingPoint, reason error) error {
	ids := make([]int64, 0, len(points))
	for _, p := range points {
		ids = append(ids, p.ID)
	}
	slog.Error("dropping buffered experiment points that can never be flushed",
		"project", project, "points", len(ids), "reason", reason)
	return f.store.DeletePoints(ctx, ids)
}

// metricsPath picks the file this project's points belong in: the metrics
// parquet the repository already carries for it (so route A and route B share
// one file, which is the whole point of §8), or `{project}/metrics.parquet`
// for a project that has never been exported.
func (f *Flusher) metricsPath(ctx context.Context, repo *store.Repo, project string) (string, error) {
	files, err := f.store.ListRepoFiles(ctx, repo.ID, repo.DefaultBranch)
	if err != nil {
		return "", fmt.Errorf("list repo files: %w", err)
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	for _, layout := range DetectLayouts(paths, repo.Name) {
		if layout.Project == project && layout.MetricsPath != "" {
			return layout.MetricsPath, nil
		}
	}
	return project + "/metrics.parquet", nil
}

// existingTable is what a flush read out of the current metrics parquet.
type existingTable struct {
	columns   []flushColumn
	rows      []map[string]any
	ingestIDs map[int64]bool
}

// readExisting loads the metrics parquet as it stands, plus the blob OID the
// commit's precondition is checked against ("" when the file is absent).
func (f *Flusher) readExisting(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo, ref, path string) (*existingTable, string, error) {
	out := &existingTable{ingestIDs: map[int64]bool{}}

	entry, _, err := gitRepo.Stat(ref, path)
	switch {
	case errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, gitrepo.ErrEmptyRepo):
		return out, "", nil
	case err != nil:
		return nil, "", fmt.Errorf("stat %s: %w", path, err)
	case entry.IsDir:
		return nil, "", fmt.Errorf("%s is a directory", path)
	}
	baseOID := entry.Hash.String()

	key, err := objectKeyFor(ctx, f.store, f.storage, repo, gitRepo, entry)
	if err != nil {
		return nil, "", fmt.Errorf("locate %s: %w", path, err)
	}
	schema, err := f.viewer.Schema(ctx, key)
	if err != nil {
		return nil, "", fmt.Errorf("read %s schema: %w", path, err)
	}
	// The row count comes out of the footer, so this costs one ranged read and
	// no decoding: the file is refused *before* the scan below turns every one
	// of its rows into a map. Checking it afterwards would be no check at all,
	// since the scan is the allocation being guarded against.
	if schema.NumRows > maxExistingFlushRows {
		return nil, "", &tooManyExistingRowsError{Path: path, NumRows: schema.NumRows, Max: maxExistingFlushRows}
	}
	for _, c := range schema.Columns {
		col, err := columnFromSchema(c)
		if err != nil {
			return nil, "", err
		}
		out.columns = append(out.columns, col)
	}

	err = f.viewer.Scan(ctx, key, viewer.ScanRequest{}, func(row map[string]any) error {
		copied := make(map[string]any, len(row))
		for k, v := range row {
			copied[k] = v
		}
		if id, ok := toInt(copied[IngestIDColumn]); ok {
			out.ingestIDs[id] = true
		}
		out.rows = append(out.rows, copied)
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("scan %s: %w", path, err)
	}
	return out, baseOID, nil
}

// mergePoints appends the buffered points to the rows already in the file and
// returns the column set the result needs. Points whose id is already recorded
// in the file are skipped: they were written by an attempt that committed but
// died before deleting them.
func mergePoints(existing *existingTable, points []store.PendingPoint) ([]flushColumn, []map[string]any, int) {
	byName := make(map[string]flushColumn, len(existing.columns)+8)
	order := make([]string, 0, len(existing.columns)+8)
	add := func(c flushColumn) {
		if _, ok := byName[c.name]; ok {
			return
		}
		byName[c.name] = c
		order = append(order, c.name)
	}
	for _, c := range existing.columns {
		add(c)
	}
	// run_name is the only column every reader requires (layout.go), so it is
	// the only one written as required when this package creates the file.
	add(stringColumn("run_name", false))
	add(int64Column("step"))
	add(timestampColumn("timestamp"))
	add(int64Column(IngestIDColumn))

	rows := existing.rows
	appended := 0
	for _, p := range points {
		if existing.ingestIDs[p.ID] {
			continue
		}
		row := map[string]any{
			"run_name":     p.RunName,
			"step":         p.Step,
			"timestamp":    p.TS.UTC(),
			IngestIDColumn: p.ID,
		}
		for key, value := range p.Metrics {
			// A metric may not be named after one of the row's structural
			// columns: the two share this row, so "run_name" would replace the
			// run's name with a number -- and after the next re-index its
			// points would belong to a run called "0.5" that never ran. The
			// ingest API rejects such a name with a 400
			// (api.validateIngestMetricName), but points accepted before that
			// check existed are still in exp_points, and nothing else between
			// there and here would notice. Dropping the value loses nothing
			// chartable: the indexer skips structural columns anyway.
			if IsStructuralColumn(key) {
				continue
			}
			add(doubleColumn(key))
			row[key] = value
		}
		rows = append(rows, row)
		appended++
	}

	columns := make([]flushColumn, 0, len(order))
	for _, name := range order {
		columns = append(columns, byName[name])
	}
	return columns, rows, appended
}

// blobFor turns the rendered parquet into the bytes the commit carries: an LFS
// pointer (with the payload uploaded to the bucket and registered first) when
// .gitattributes routes the path to LFS, which is the default for *.parquet.
// This mirrors what a huggingface_hub upload does, so the file behaves
// identically no matter which side produced it.
func (f *Flusher) blobFor(ctx context.Context, gitRepo *gitrepo.Repo, repo *store.Repo, ref, path string, data []byte) ([]byte, error) {
	rules := gitrepo.ParseGitAttributes([]byte(gitrepo.DefaultGitAttributes(repo.Kind)))
	if content, err := gitRepo.ReadFile(ref, ".gitattributes", 1<<20); err == nil {
		rules = gitrepo.ParseGitAttributes(content)
	}
	size := int64(len(data))
	if !rules.ShouldUseLFS(path, size) {
		return data, nil
	}

	oid, _, err := gitrepo.HashSHA256(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	// An oid these metrics produced before may already be stored -- the key is
	// the content's own hash, so a re-flush of identical bytes writes nothing.
	key := storage.LFSKey(oid)
	if _, err := f.storage.Stat(ctx, key); err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("stat lfs object: %w", err)
		}
		if err := f.storage.Put(ctx, key, bytes.NewReader(data), "application/vnd.apache.parquet"); err != nil {
			return nil, fmt.Errorf("upload metrics parquet: %w", err)
		}
	}
	err = f.store.RecordLFSObject(ctx, repo.ID, oid, size, func(k string) (bool, error) {
		_, sErr := f.storage.Stat(ctx, k)
		if errors.Is(sErr, storage.ErrNotFound) {
			return false, nil
		}
		return sErr == nil, sErr
	})
	if err != nil {
		return nil, fmt.Errorf("register lfs object: %w", err)
	}
	return gitrepo.FormatLFSPointer(oid, size), nil
}

// commit mirrors api.(*Server).commitThroughWAL for the one caller that lives
// outside the HTTP layer. retryOnStale is deliberately absent: the flush holds
// its own optimistic lock on the metrics path, so a branch that moved under it
// must be re-read from scratch on the next tick rather than rebuilt blindly on
// the new head.
func (f *Flusher) commit(ctx context.Context, repo *store.Repo, req gitrepo.CommitRequest) (newHash, oldHash plumbing.Hash, err error) {
	gitRepo, err := f.git.Open(repo.StoragePath)
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, err
	}
	newHash, oldHash, err = gitRepo.Commit(req)
	if err != nil {
		return newHash, oldHash, err
	}

	update := []wal.RefUpdate{{Ref: "refs/heads/" + req.Branch, Old: oldHash.String(), New: newHash.String()}}
	dir := f.git.Dir(repo.StoragePath)

	switch f.walMode {
	case "off":
		return newHash, oldHash, nil
	case "shadow":
		if werr := wal.ShadowPush(ctx, f.storage, dir, repo.StoragePath, update); werr != nil {
			slog.Warn("wal shadow write failed for metrics flush",
				"repo", repo.FullName(), "branch", req.Branch, "error", werr)
		}
		return newHash, oldHash, nil
	default: // authoritative
		werr := wal.AuthoritativePush(ctx, f.storage, dir, repo.StoragePath, update)
		if werr == nil {
			if aerr := f.git.AdoptLocal(ctx, repo.StoragePath); aerr != nil {
				slog.Warn("wal adopt after metrics flush", "repo", repo.FullName(), "error", aerr)
			}
			return newHash, oldHash, nil
		}
		// A ref the WAL never accepted must not survive locally, or readers
		// would be served a commit the index does not know about.
		if rerr := gitRepo.ResetBranch(req.Branch, oldHash); rerr != nil {
			slog.Error("roll back local ref after failed WAL write",
				"repo", repo.FullName(), "branch", req.Branch, "error", rerr)
		}
		return newHash, oldHash, fmt.Errorf("record metrics flush in WAL: %w", werr)
	}
}
