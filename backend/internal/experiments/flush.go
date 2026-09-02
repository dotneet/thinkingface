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
	"math"
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
// **The bound applies to what a flush writes as well as to what it reads.**
// It did not, once, and the two halves disagreeing was worse than either
// limit: a flush would happily append 50,000 rows to a file already holding a
// million, the commit would succeed, and every flush after that read the
// footer, found more rows than it would accept, and blocked the project --
// permanently, since nothing shrinks a committed file. A long run crossing the
// line wedged its own project forever. maybeRotate is what keeps the two in
// step: rather than write a file it will refuse to read, a flush starts a
// continuation file (layout.MetricsShards) and carries on.
//
// It is a var only so the tests can lower it; nothing changes it at runtime.
//
// TODO: the permanent fix is to append a row group to the existing file
// instead of rebuilding it, which would need neither cap nor rotation. That is
// a rewrite of the writer (parquetwrite.go) and of the LFS blob handling
// around it, so it is deliberately not attempted here.
var maxExistingFlushRows int64 = 1_000_000

// maxMetricsShards bounds how far this package will look for -- or create --
// continuation files. Each one holds up to maxExistingFlushRows rows, so the
// ceiling is a project of ten billion points; what it really guards is the
// probe loop in resolveMetricsTarget, which walks the numbers in order and
// must terminate even if the tree is somehow full of them.
const maxMetricsShards = 10_000

// tooManyExistingRowsError reports a metrics parquet this package cannot
// rebuild within its memory budget. Like unsupportedColumnError it describes a
// file, not a transient failure -- see blockFlush for what happens to the
// buffered points.
//
// It is only ever raised for a file this package did not write: rotation keeps
// its own output under the bound, so reaching this means an oversized parquet
// was pushed into the repository by hand (or that maxExistingFlushRows was
// lowered under a file that already existed).
type tooManyExistingRowsError struct {
	Path string
	// NumRows is what the file's footer declared, or -1 when the count came
	// from the scan overrunning the cap -- which is the case where the footer
	// was not telling the truth, so quoting it would be worse than saying
	// nothing.
	NumRows int64
	Max     int64
}

func (e *tooManyExistingRowsError) Error() string {
	if e.NumRows < 0 {
		return fmt.Sprintf("metrics parquet %s holds more rows than the %d a flush can rebuild in memory "+
			"(its footer declares fewer than it contains)", e.Path, e.Max)
	}
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

// FlushRef is the ref a flush commits its parquet to.
//
// It is exported because the syncer has to know the answer *before* the flush
// runs: it takes that ref's lock first, so that a sync job holding the ref is
// answered by deferring rather than by committing into a pipeline that is
// about to overwrite the index (syncer.FlushProject). Two copies of the
// derivation would be worse than none -- if they ever disagreed, the lock
// would be taken under one key and the commit made under another, which
// un-serialises the two silently and leaves the flush unable to finish at all.
//
// The fallback covers a repository row whose default_branch is empty. The
// column is NOT NULL DEFAULT 'main' on both backends and nothing writes ""
// today, so this is belt and braces rather than a live path.
func FlushRef(repo *store.Repo) string {
	if repo.DefaultBranch == "" {
		return "main"
	}
	return repo.DefaultBranch
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
	if err := f.git.EnsureLocalWithDefaultBranch(ctx, repo.StoragePath, repo.DefaultBranch); err != nil {
		return nil, err
	}
	gitRepo, err := f.git.Open(repo.StoragePath)
	if err != nil {
		return nil, err
	}

	ref := FlushRef(repo)
	target, err := f.resolveMetricsTarget(ctx, repo, gitRepo, ref, project)
	if err != nil {
		return nil, err
	}
	// A project whose name cannot be a path segment (".git", ".", "..") makes
	// a file Commit will always refuse, so the project is blocked here rather
	// than failing at the end of every tick from now on. Ingest rejects such a
	// name today (api.validateIngestProject); this is for the rows an older
	// build already accepted.
	if perr := gitrepo.ValidatePath(target.active); perr != nil {
		return nil, f.blockFlush(ctx, projectID, project, points, perr)
	}

	path := target.active
	existing, baseOID, err := f.readExisting(ctx, repo, gitRepo, ref, path)
	if err != nil {
		// Two shapes of file this package cannot rebuild, and neither gets
		// better by being retried: one holds more rows than a rebuild can
		// hold, the other a column the writer cannot reproduce (a list, a
		// nested group, an unannotated BYTE_ARRAY). Both were pushed by hand
		// -- rotation keeps this package's own output clear of the first, and
		// it never writes the second -- so the answer is to mark the project
		// and tell the operator, not to append somewhere else: the file may
		// well hold ingest ids we could not read, and writing its points again
		// into a sibling would double them on the chart.
		var tooMany *tooManyExistingRowsError
		var unsupported *unsupportedColumnError
		if errors.As(err, &tooMany) || errors.As(err, &unsupported) {
			return nil, f.blockFlush(ctx, projectID, project, points, err)
		}
		return nil, err
	}

	// Rotating *before* the merge is the whole point: the file this flush is
	// about to write has to be one the next flush can read back.
	if rotated, ok := maybeRotate(existing, points, target); ok {
		slog.Info("rotating an experiment project's metrics parquet: appending would exceed the rebuild bound",
			"project", project, "project_id", projectID, "from", path, "to", rotated,
			"existing_rows", len(existing.rows), "max_rows", maxExistingFlushRows)
		path = rotated
		// The ingest ids survive the rotation and the rows do not. Keeping the
		// ids is what stops a flush that committed and then died from writing
		// its points a second time into the new file; dropping the rows (and
		// with them the old file's column descriptions) is what makes the new
		// file small, which is the reason to rotate at all.
		existing = &existingTable{ingestIDs: existing.ingestIDs}
		// A file that does not exist yet: the commit's precondition becomes
		// "absent", so a concurrent flush that got there first loses the race
		// instead of overwriting it.
		baseOID = ""
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

	ops := []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: blob}}
	// The optimistic lock is evaluated against the parent commit under the
	// mutex that picks it, so a push landing between readExisting and here
	// loses the race instead of being silently overwritten.
	preconditions := []gitrepo.PathPrecondition{{Path: path, OID: baseOID}}
	if !target.baseExists && path != target.base {
		// The points went into a continuation file, and the base file the
		// chain is named after is gone -- deleted by whoever owns the
		// repository. The rows do not need it back (DetectLayouts anchors on
		// the oldest surviving file), but two name-based signals do:
		// syncer.looksLikeExperiment reads "metrics.parquet is in the tree" as
		// "this dataset is an experiment", and repo.is_experiment is
		// recomputed from it on every push -- so without the file a project
		// whose card carries no trackio tag stops being indexed at all, while
		// its flushes go on succeeding. Restoring it *empty* is what keeps the
		// chain append-only: putting the points here instead would make the
		// file both readers scan first the one holding the newest rows.
		anchor, err := f.emptyMetricsBlob(ctx, gitRepo, repo, ref, target.base)
		if err != nil {
			return nil, err
		}
		ops = append(ops, gitrepo.Op{Kind: gitrepo.OpAdd, Path: target.base, Data: anchor})
		preconditions = append(preconditions, gitrepo.PathPrecondition{Path: target.base, OID: ""})
	}

	newHash, oldHash, err := f.commit(ctx, repo, gitrepo.CommitRequest{
		Branch:        ref,
		Message:       fmt.Sprintf("chore(trackio): flush %s metrics", project),
		Author:        gitrepo.Signature{Name: flushAuthor.Name, Email: flushAuthor.Email, When: time.Now()},
		Ops:           ops,
		Preconditions: preconditions,
	})
	if err != nil {
		return nil, err
	}

	// Whatever blocked this project before is evidently gone: the commit is
	// in. Clearing the mark is what makes the block self-healing -- an
	// operator who shrinks the metrics file or renames the project never has
	// to tell anything about it. The statement matches nothing for a project
	// that was never blocked, which is every ordinary flush.
	if err := f.store.SetProjectFlushBlock(ctx, projectID, ""); err != nil {
		slog.Warn("could not clear an experiment project's flush block",
			"project", project, "project_id", projectID, "error", err)
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

// ErrFlushBlocked reports a project whose buffered points cannot be written
// to its parquet for a reason the next attempt would hit identically. The
// points are still there; what has changed is that the project is marked, so
// the poller stops picking it up until the block expires.
var ErrFlushBlocked = errors.New("experiments: this project's metrics cannot be flushed")

// blockFlush marks the project as unflushable and returns why, wrapped in
// ErrFlushBlocked.
//
// The three conditions routed here -- a project name no path can hold, a
// metrics file too large to rebuild in memory, and one holding a column this
// package's writer cannot reproduce (unsupportedColumnError: a repeated
// column, a nested group, an unannotated BYTE_ARRAY, an unsupported logical
// type) -- are properties of the repository, not of this attempt: nothing
// about the next tick is different.
//
// The third of those used to fall through to the plain error return, and the
// difference mattered: an unmarked project is picked up again on the next
// tick, so a single pushed metrics.parquet with a list column made one project
// fail every ten seconds for ever *and* hold a permanent slot at the front of
// ListPendingFlushProjects' oldest-first window -- starving the other
// ninety-nine, which is precisely what this function exists to prevent.
// What made retrying forever intolerable was that the damage did not stay
// inside the one project: the poller takes its candidates from
// ListPendingFlushProjects, which takes the hundred projects whose oldest
// unflushed point has waited longest, and a wedged project's oldest point only
// ever gets older -- so a hundred of them sit at the front of that order
// forever and *no* project on the instance is flushed again.
//
// **The mark solves that one problem and deliberately accepts the other.**
// This used to be answered by deleting the buffered points, which stopped the
// starvation and lost the data: the ingest API had already returned 200 for
// every one of those points, no user-facing document mentions
// maxExistingFlushRows, and a run that crossed it simply found its metrics
// missing from both the chart and the parquet. The trade made here is that
// risk for an unbounded one: exp_points for a blocked project now grows for as
// long as the client keeps logging and nobody intervenes, with no ceiling and
// no eviction. That is the intended outcome, not an oversight -- see
// store.SetProjectFlushBlock, which names the same cost -- but it is a real
// one, and on an instance nobody is watching it is a table that grows until
// the disk says no.
//
// The chart is not the pressure point it once was: scanLiveSeries reads the
// buffer through ListProjectPoints with maxLiveSeriesPoints, so a runaway
// buffer degrades the chart rather than the process. The database is.
//
// What the mark buys is time: ListPendingFlushProjects skips a project blocked
// within flushBlockRetryAfter, so the cost of a wedged project is one attempt
// an hour instead of one every ten seconds, the rest of the instance keeps
// draining, and the buffer survives until somebody acts on it.
//
// Marking is best effort: if the mark itself cannot be written the flush
// still fails with the same error, so nothing is silently retried at full
// rate without anybody being told why.
func (f *Flusher) blockFlush(ctx context.Context, projectID int64, project string, points []store.PendingPoint, reason error) error {
	slog.Error("experiment project cannot be flushed; its buffered points are being kept",
		"project", project, "project_id", projectID, "points", len(points), "reason", reason)
	if err := f.store.SetProjectFlushBlock(ctx, projectID, reason.Error()); err != nil {
		slog.Error("could not record why an experiment project cannot be flushed",
			"project", project, "project_id", projectID, "error", err)
	}
	return fmt.Errorf("%w: %w", ErrFlushBlocked, reason)
}

// metricsTarget is where a flush may write: the file it appends to, and the
// name it would rotate into if appending would make that file unreadable.
type metricsTarget struct {
	// base is the chain's anchor -- the file its continuations are numbered
	// from ("{project}/metrics.parquet" and "{project}/metrics.partNNNN.parquet").
	// It is a name, not necessarily a file: baseExists says whether the ref
	// still carries it.
	base       string
	baseExists bool
	// active is the newest of the project's metrics files -- the base parquet
	// when nothing has rotated yet, otherwise its highest-numbered
	// continuation file.
	active string
	// next is the name the rotation would claim. It is guaranteed not to exist
	// in the ref this was resolved against.
	next string
}

// resolveMetricsTarget picks the file this project's points belong in: the
// metrics parquet the repository already carries for it (so route A and route
// B share one file, which is the whole point of §8), or
// `{project}/metrics.parquet` for a project that has never been exported --
// then walks forward over the continuation files a previous rotation created.
//
// The walk goes to the git tree rather than stopping at what DetectLayouts
// found, and that is not belt and braces. The file index it reads is refreshed
// by the sync worker *after* the commit lands, so a crash in between leaves a
// continuation file that is in the tree and not in the index. Appending to the
// base file in that window would write points the new shard already holds --
// with ingest ids this flush never saw, so nothing would deduplicate them --
// and the chart would show every one of them twice, forever.
//
// **The chain is append-only: a flush writes its highest-numbered surviving
// file, or a higher-numbered one, and never anything below that.** That is
// what makes part-number order chronological, which is the property both
// readers rely on -- Series() resolves two values at one step by taking the
// later one in scan order, and indexProject overwrites a run's summary as it
// goes, so a file written out of order reports measurements from before the
// rotation and does it silently and permanently.
//
// The rule has to hold even though the files are not this package's to keep:
// the Web UI's delete button, huggingface_hub's delete_file, `tf up --delete`
// and a history rewrite can remove any of them, the base file included. So the
// walk starts at the highest number the chain still has rather than at the
// base, and a deleted base is simply a chain that starts later -- DetectLayouts
// anchors on the oldest *surviving* member for exactly this reason, which is
// the reader's half of the same invariant. Re-anchoring onto the base instead
// would put the newest points in the file both readers scan first.
//
// The one thing the base file's absence does cost is the name: syncer's
// looksLikeExperiment recognises a trackio dataset by "metrics.parquet" being
// in the tree, and repo.is_experiment -- which decides whether the project is
// indexed at all -- is recomputed from it on every push. So Flush restores the
// anchor as an empty table alongside the points it writes. Empty is what keeps
// it honest: a file with no rows cannot be read out of order.
func (f *Flusher) resolveMetricsTarget(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo,
	ref, project string) (metricsTarget, error) {

	files, err := f.store.ListRepoFiles(ctx, repo.ID, repo.DefaultBranch)
	if err != nil {
		return metricsTarget{}, fmt.Errorf("list repo files: %w", err)
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}

	// The index gives the chain's shape in one query: its anchor name (which
	// outlives the base file, since the shards are named after it) and the
	// highest part it has reached. The walk below starts from there instead of
	// from zero, so a project that rotated a hundred times does not cost a
	// hundred stats per flush.
	base := project + "/metrics.parquet"
	highest := 0
	for _, layout := range DetectLayouts(paths, repo.Name) {
		if layout.Project != project {
			continue
		}
		chain := layout.MetricsFiles()
		if len(chain) == 0 {
			continue
		}
		base, _ = MetricsChainFile(chain[0])
		_, highest = MetricsChainFile(chain[len(chain)-1])
		break
	}

	baseExists, err := f.pathExists(gitRepo, ref, base)
	if err != nil {
		return metricsTarget{}, err
	}

	active := base
	if highest > 0 {
		active = MetricsShardPath(base, highest)
	}
	// The walk goes past what the index knows because the index is refreshed
	// after the commit lands: a crash in between leaves a continuation file
	// that is in the tree and not in the index. It normally probes once and
	// stops.
	for n := highest; n < maxMetricsShards; n++ {
		candidate := MetricsShardPath(base, n+1)
		exists, err := f.pathExists(gitRepo, ref, candidate)
		if err != nil {
			return metricsTarget{}, err
		}
		if !exists {
			return metricsTarget{base: base, baseExists: baseExists, active: active, next: candidate}, nil
		}
		active = candidate
	}
	return metricsTarget{}, fmt.Errorf("metrics parquet %s already has %d continuation files", base, maxMetricsShards)
}

func (f *Flusher) pathExists(gitRepo *gitrepo.Repo, ref, path string) (bool, error) {
	_, _, err := gitRepo.Stat(ref, path)
	switch {
	case errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, gitrepo.ErrEmptyRepo):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

// maybeRotate reports the file this flush should write instead of the one it
// read, when appending to that one would push it past maxExistingFlushRows.
//
// The count that matters is of the points that will actually be added: a retry
// after a crash re-offers points the file already holds, and those cost no
// rows (mergePoints skips them by ingest id). Counting them would rotate a
// project that was merely being retried.
//
// It declines to rotate when the batch alone would not fit either, because
// then the new file would be born over the bound and the next flush would
// block on it -- exactly the state rotation exists to avoid. That cannot
// happen in production, where MaxFlushPoints (50,000) is a twentieth of the
// bound; it is reachable only with the bound lowered, which is a thing tests
// do.
func maybeRotate(existing *existingTable, points []store.PendingPoint, target metricsTarget) (string, bool) {
	newRows := 0
	for _, p := range points {
		if !existing.ingestIDs[p.ID] {
			newRows++
		}
	}
	if newRows == 0 {
		return "", false
	}
	if int64(len(existing.rows)+newRows) <= maxExistingFlushRows {
		return "", false
	}
	if int64(newRows) > maxExistingFlushRows {
		return "", false
	}
	return target.next, true
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
	//
	// It is not, on its own, a guarantee. The footer's file-level num_rows and
	// the per-row-group num_rows are separate thrift fields and nothing in the
	// format makes a reader verify that one is the sum of the others, so a
	// hand-written (or deliberately crafted) parquet can declare a small file
	// and hand the scan millions of rows -- which is exactly the allocation
	// this bounds. So the same cap is enforced again below, against rows
	// actually produced. The footer check stays because it is the cheap one:
	// an honest oversized file never reaches the scan at all.
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
		// The real guard: whatever the footer claimed, stop as soon as the
		// scan has handed over more rows than a rebuild can hold. Returning
		// an error aborts the scan, so the rows past the cap are never
		// materialised.
		if int64(len(out.rows)) >= maxExistingFlushRows {
			return &tooManyExistingRowsError{Path: path, NumRows: -1, Max: maxExistingFlushRows}
		}
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
	// widenFor promotes a metric column to DOUBLE when the value about to be
	// written could not survive the column's current type.
	//
	// The existing file decides a column's type, which is right for a metric
	// whose values keep the shape the file already gives them -- and silently
	// wrong for one whose shape changes. A trackio export that wrote `epoch`
	// as an integer types the column INT64, and a later
	// `trackio.log({"epoch": 3.5})` went through toInt: stored as 3, charted
	// as 3, with nothing anywhere saying a value had been altered. A boolean
	// column does the same with `!= 0`, turning 0.5 into 1.
	//
	// Widening rather than dropping the value, because the alternatives are
	// both worse: keeping the truncated number is a fabricated measurement,
	// and writing a null loses a point the ingest API accepted with a 200.
	// A DOUBLE holds every value the narrower column could -- an int32 or an
	// int64 up to 2^53 exactly, a bool as 0/1 -- so the rows already in the
	// file re-encode without loss, and every reader here coerces through
	// toFloat and cannot tell the difference. Widening is one-way: the column
	// stays DOUBLE afterwards, which is the correct record of "this metric is
	// not an integer".
	//
	// Structural columns are deliberately exempt. `step` is an int64 by this
	// package's own construction and readers use it as one; a metric may not
	// be named after one anyway (IsStructuralColumn, checked by the caller
	// below).
	widenFor := func(name string, value float64) {
		c, ok := byName[name]
		if !ok || IsStructuralColumn(name) {
			return
		}
		switch c.kind {
		case colInt32, colInt64:
			// NaN and +-Inf are left alone: encode() cannot represent them in
			// an integer column *or* a double one and writes a null either
			// way, and converting one to an int64 to compare is undefined in
			// Go rather than merely lossy.
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return
			}
			// Outside the int64 range the round-trip below is undefined too,
			// and the value certainly does not fit: widen.
			if value >= math.MinInt64 && value <= math.MaxInt64 && float64(int64(value)) == value {
				return
			}
		case colBool:
			if value == 0 || value == 1 {
				return
			}
		default:
			return
		}
		byName[name] = doubleColumn(name)
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
			widenFor(key, value)
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

// emptyMetricsBlob renders a metrics parquet with this package's structural
// columns and no rows, ready to be committed at path. It is only ever used to
// put a deleted chain anchor back (see Flush): every reader opens it like any
// other metrics file and gets nothing out of it, which is the point -- a file
// with no rows carries no measurement whose position in the chain could
// contradict when it was logged.
func (f *Flusher) emptyMetricsBlob(ctx context.Context, gitRepo *gitrepo.Repo, repo *store.Repo,
	ref, path string) ([]byte, error) {

	columns, rows, _ := mergePoints(&existingTable{ingestIDs: map[int64]bool{}}, nil)
	data, err := writeMetricsParquet(columns, rows)
	if err != nil {
		return nil, fmt.Errorf("render empty metrics parquet: %w", err)
	}
	return f.blobFor(ctx, gitRepo, repo, ref, path, data)
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
