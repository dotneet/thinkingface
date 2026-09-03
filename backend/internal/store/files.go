package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

type RepoFile struct {
	Path    string  `json:"path"`
	Size    int64   `json:"size"`
	BlobSHA string  `json:"blob_sha"`
	LFSOID  *string `json:"lfs_oid"`
}

// lockRepoRow takes the repositories row for repoID FOR UPDATE inside tx,
// or returns ErrNotFound when the repository is gone.
//
// The index rebuilders (ReplaceRepoFiles, ReplaceRepoLineage) delete and
// re-insert child rows, which on PostgreSQL means: row locks on the child
// table first, then a KEY SHARE lock on the parent row when the inserts run
// their foreign-key check. DeleteRepo takes the same locks in the opposite
// order -- the parent row first, then the children through ON DELETE
// CASCADE -- so a push being indexed while its repository is deleted could
// deadlock (SQLSTATE 40P01, seen in e2e/test_security.py). Taking the parent
// row lock up front makes every writer touch repositories before its
// children, so one of the two simply waits for the other instead. On SQLite
// forUpdate is a no-op: there is a single writer.
func (s *Store) lockRepoRow(ctx context.Context, tx tx, repoID int64) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM repositories WHERE id = $1`+s.d.forUpdate(""), repoID).Scan(&id)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock repository row: %w", err)
	}
	return nil
}

// ReplaceRepoFiles swaps the whole cached listing for one ref. The sync worker
// rebuilds from git, so a full replace is both simplest and always correct.
// It returns ErrNotFound when the repository has been deleted in the meantime.
func (s *Store) ReplaceRepoFiles(ctx context.Context, repoID int64, ref string, files []RepoFile) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := s.lockRepoRow(ctx, tx, repoID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_files WHERE repo_id = $1 AND ref = $2`, repoID, ref); err != nil {
		return fmt.Errorf("clear repo_files: %w", err)
	}
	if len(files) > 0 {
		rows := make([][]any, 0, len(files))
		for _, f := range files {
			// A git path is any byte string without NUL or '/', so it can be
			// Latin-1, Shift_JIS or anything else an old workstation wrote.
			// PostgreSQL's COPY refuses those bytes outright (SQLSTATE
			// 22021) and the refusal parks the sync job, freezing the whole
			// repository's index -- see text.go. Blob shas and LFS oids are
			// hex by construction and need nothing.
			rows = append(rows, []any{repoID, ref, sanitizeText(f.Path), f.Size, f.BlobSHA, f.LFSOID})
		}
		if err := tx.BulkInsert(ctx, "repo_files",
			[]string{"repo_id", "ref", "path", "size", "blob_sha", "lfs_oid"}, rows); err != nil {
			return fmt.Errorf("insert repo_files: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// DeleteRefIndex drops every cached row for one ref: the file listing and the
// parquet metadata built from it. It is what a ref *disappearing* needs, and
// until it existed there was no such path at all -- ReplaceRepoFiles was the
// only writer, and it is only ever reached from a sync job, which is only ever
// enqueued for a ref that still exists.
//
// So `git push --delete feature` and DELETE /api/{type}s/{ns}/{name}/branch/
// {feature} both left the branch's rows behind for the life of the
// repository. They are not inert: ListRepoFiles(repo, "feature") kept
// answering with the files of a branch that is gone, and -- the expensive
// half -- ListReferencedBlobSHAs counted their blobs as referenced, so
// `thinkingface gc` could never reclaim content whose last ref had been
// deleted. `thinkingface resync` does not find them either: it walks the
// refs git still has.
//
// Only branches, and that is not an oversight. repo_files.ref holds a branch
// short name -- HeadsAfterPush lists branches, the HF commit API refuses a
// {rev} that is a tag (api.ensureBranchRev), and creating a tag schedules no
// sync job -- so a tag has no rows to remove, and one short name can be both
// a branch and a tag at once. Deleting by a tag's name would take the
// identically named branch's index with it.
//
// A repository deleted in the meantime is not an error: the rows went with
// it through ON DELETE CASCADE, which is the outcome asked for.
func (s *Store) DeleteRefIndex(ctx context.Context, repoID int64, ref string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Same parent-row-first ordering as ReplaceRepoFiles (see lockRepoRow):
	// this deletes rows of the same two child tables the rebuilders write, so
	// taking the repositories row up front is what keeps it from deadlocking
	// against a concurrent DeleteRepo cascading the other way.
	if err := s.lockRepoRow(ctx, tx, repoID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_files WHERE repo_id = $1 AND ref = $2`, repoID, ref); err != nil {
		return fmt.Errorf("clear repo_files: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM parquet_files WHERE repo_id = $1 AND ref = $2`, repoID, ref); err != nil {
		return fmt.Errorf("clear parquet_files: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Store) ListRepoFiles(ctx context.Context, repoID int64, ref string) ([]RepoFile, error) {
	rows, err := s.db.Query(ctx,
		`SELECT path, size, blob_sha, lfs_oid FROM repo_files
		 WHERE repo_id = $1 AND ref = $2 ORDER BY path`, repoID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RepoFile{}
	for rows.Next() {
		var f RepoFile
		if err := rows.Scan(&f.Path, &f.Size, &f.BlobSHA, &f.LFSOID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListIndexedBlobSHAs returns the git blob shas of the plain (non-LFS) files
// the sync worker has indexed for ref. The worker writes repo_files only after
// publishing every one of those blobs to blobs/, so this set is also the
// record of which of the ref's blobs are promised to be in the bucket.
func (s *Store) ListIndexedBlobSHAs(ctx context.Context, repoID int64, ref string) (map[string]bool, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT blob_sha FROM repo_files
		 WHERE repo_id = $1 AND ref = $2 AND lfs_oid IS NULL`, repoID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out[sha] = true
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------- LFS

func (s *Store) HasLFSObject(ctx context.Context, oid string) (int64, bool, error) {
	var size int64
	err := s.db.QueryRow(ctx, `SELECT size FROM lfs_objects WHERE oid = $1`, oid).Scan(&size)
	if isNoRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return size, true, nil
}

// RecordLFSObject registers an uploaded object and links it to the repository
// that uploaded it, which is what later authorises downloads.
//
// The bytes always live at storage.LFSKey(oid) -- content-addressed, immutable
// and shared across every repository -- so there is no key to record: the
// ledger only says "this oid exists, at this size, and this repository may
// read it".
//
// confirmPresent is called while the lfs_objects row is locked and must
// re-check object storage for that key. A concurrent GC's
// DeleteOrphanedLFSObject holds the same lock while it removes the bytes, then
// deletes the row. After that commit this INSERT recreates the row; without
// the re-check we would publish a phantom object, and an upload batch that
// already Stat'ed a hit would return no upload action -- the client never
// re-uploads and the content is gone. If confirmPresent reports missing, the
// transaction rolls back and ErrLFSObjectGone is returned.
//
// That call is a round trip to object storage made with a write transaction
// open, which on SQLite means holding the process's single writer connection
// (sqlite.go): every other write queues behind it, and another process gets
// SQLITE_BUSY once sqliteBusyTimeout runs out. It cannot simply move out of
// the transaction -- what makes it worth anything is that it runs after the
// row is locked, so a GC deleting the bytes has either already finished (and
// the re-check sees the hole) or has to wait (and then finds the link).
// Checking first and inserting afterwards would only answer about a moment
// that has passed. What the fast path below does instead is skip the whole
// transaction for the calls that never needed it.
func (s *Store) RecordLFSObject(ctx context.Context, repoID int64, oid string, size int64, confirmPresent func(key string) (bool, error)) error {
	if confirmPresent == nil {
		return errors.New("confirmPresent is required")
	}

	// An object this repository already links to is not a candidate for
	// either collector: DeleteOrphanedLFSObject requires that no
	// repo_lfs_objects row names the oid, and DeleteUntrackedLFSObject only
	// claims an oid with no lfs_objects row at all -- which the foreign key
	// under the link guarantees it has. So there is no race to hold a lock
	// against, both statements below would be no-ops, and the storage round
	// trip would establish nothing the caller has not established already
	// (every one of them stats or writes the bytes before it gets here).
	// Answering this from the read pool keeps the repeated case -- a re-push
	// of objects this repository already holds -- off the writer entirely.
	linked, err := s.RepoHasLFSObject(ctx, repoID, oid)
	if err != nil {
		return err
	}
	if linked {
		return nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// ON CONFLICT DO UPDATE (a no-op on size) keeps the lfs_objects row locked
	// until we commit, so a concurrent GC's SELECT FOR UPDATE waits and then
	// sees the repo_lfs_objects row. DO NOTHING would release the row after
	// the insert and leave a gap GC could claim. (On SQLite every write
	// transaction already runs on the single writer connection, so the same
	// statement is harmless and the ordering guarantee holds trivially.)
	var storedOID string
	if err := tx.QueryRow(ctx,
		`INSERT INTO lfs_objects (oid, size) VALUES ($1, $2)
		 ON CONFLICT (oid) DO UPDATE SET size = lfs_objects.size
		 RETURNING oid`,
		oid, size).Scan(&storedOID); err != nil {
		return fmt.Errorf("insert lfs object: %w", err)
	}

	// Re-checked under the row lock: a GC that deleted the bytes between the
	// caller's own check and this transaction must not leave a phantom row.
	ok, err := confirmPresent(storage.LFSKey(oid))
	if err != nil {
		return err
	}
	if !ok {
		return ErrLFSObjectGone
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO repo_lfs_objects (repo_id, oid, created_at) VALUES ($1, $2, now())
		 ON CONFLICT DO NOTHING`,
		repoID, oid); err != nil {
		return fmt.Errorf("link lfs object: %w", err)
	}
	return tx.Commit(ctx)
}

// LinkLFSObjects entitles a repository to objects it did not upload itself:
// the HF-compatible commit handler calls it for the pointers one commit
// introduces, and the syncer's post-push pipeline calls it for the whole
// revision, which is what covers a pointer pushed as an ordinary blob by a
// client that never spoke the LFS protocol.
//
// **The declared size is the entitlement, and it is checked here.** An oid is
// public -- every LFS pointer in every readable repository is one -- so naming
// it proves nothing; what a holder of the content can also say is how many
// bytes it is. That is the same rule the LFS batch's dedup enforces
// (lfs.storedAt) and the same one the commit handler leans on, and it has to
// be enforced on this path too: a pointer is just text, so without the check a
// writer could push `size 1` against somebody else's oid and have resolve hand
// the bytes over through their own repository. A pointer written by git-lfs
// always carries the real size, so nothing legitimate is turned away.
//
// An oid lfs_objects has never heard of links to nothing at all, rather than
// producing a row promising bytes that are not there. Unlike RecordLFSObject
// there is no confirmPresent round trip: the caller is not the one putting the
// bytes in the bucket, and the row lock below is what keeps a concurrent
// collector from taking them away.
//
// **A link this method makes is permanent**, and that is the whole of how
// history stays readable: it stamps committed_at, and PruneRepoLFSLinks only
// ever releases links that carry no such stamp (an upload whose commit never
// arrived). A file deleted from the tip therefore keeps its entitlement, so
// `git checkout` of the commit that added it still resolves. This method only
// ever adds or promotes, which is also what lets it run before
// ReplaceRepoFiles without a window in which an object is named by a file but
// linked by nothing.
func (s *Store) LinkLFSObjects(ctx context.Context, repoID int64, objects []LFSObjectRef) error {
	// Keyed by the whole (oid, size) pair, not by oid. One revision can name
	// the same object from two paths, and if one of those pointers is wrong
	// about the size, keeping only the first declaration would let it decide
	// for the other -- a bad pointer arriving earlier in tree order would
	// suppress the link a good one has earned, which is the 404-plus-gc state
	// this whole path exists to avoid. Entitlement is "some pointer got it
	// right", so every declaration gets to be checked.
	declared := make(map[string]map[int64]bool, len(objects))
	oids := make([]string, 0, len(objects))
	for _, o := range objects {
		sizes, seen := declared[o.OID]
		if !seen {
			sizes = map[int64]bool{}
			declared[o.OID] = sizes
			// One row to lock per object, however many paths name it.
			oids = append(oids, o.OID)
		}
		sizes[o.Size] = true
	}
	if len(oids) == 0 {
		return nil
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock parent rows before inserting the reference so GC cannot treat
	// them as orphaned and delete storage while this transaction is open.
	// Reading the size under the same lock is what makes the check above
	// meaningful rather than advisory.
	//
	// ORDER BY oid so two callers holding overlapping sets take the rows in
	// the same sequence. That matters more than it used to: the syncer links
	// a whole revision's oids on every push (syncer.runPushPipeline), so two
	// pushes to different repositories that share content now routinely lock
	// the same rows. PostgreSQL locks in the order the scan returns, which an
	// ordered index scan makes deterministic -- it is not a guarantee the
	// planner owes us, so this narrows the window rather than closing it, and
	// a deadlock that does happen costs one aborted sync job that the queue
	// retries. SQLite runs every write alone on its single writer, so the
	// question does not arise there.
	rows, err := tx.Query(ctx,
		`SELECT oid, size FROM lfs_objects WHERE oid `+s.d.inArray("$1")+` ORDER BY oid`+s.d.forUpdate(""),
		s.d.stringArrayArg(oids))
	if err != nil {
		return err
	}
	entitled := make([]string, 0, len(oids))
	for rows.Next() {
		var oid string
		var size int64
		if err := rows.Scan(&oid, &size); err != nil {
			rows.Close()
			return err
		}
		if declared[oid][size] {
			entitled = append(entitled, oid)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(entitled) == 0 {
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, s.d.queries().linkLFSObjectsInsert, repoID, s.d.stringArrayArg(entitled)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PruneRepoLFSLinks removes this repository's links to objects no commit of it
// has ever named, and returns how many it removed. In practice that is one
// thing: an LFS transfer that completed and whose commit never arrived -- a
// `tf up` or a huggingface_hub upload interrupted between the upload and the
// commit, which used to leave tens of gigabytes charged to a repository
// holding no files at all.
//
// **What it deliberately does not touch is history.** repo_lfs_objects is not
// a cache: store.RepoHasLFSObject reads it as the entitlement that authorises
// the LFS batch's download branch (lfs.Batch) and `resolve` at any revision
// (api.lfsObjectOwned). An object whose link is gone is a 404 even while its
// bytes sit untouched in the bucket, and `thinkingface gc` will then delete
// those bytes for real. So an object any commit named keeps its link for as
// long as the repository exists, and `git checkout <old sha>`, `git lfs fetch
// --all` and resolve at a historical revision keep working. That is what git
// means and what the HuggingFace Hub does.
//
// committed_at is the marker, and LinkLFSObjects is what sets it -- the
// HF-compatible commit handler calls it for one commit's pointers, the
// syncer's post-push pipeline for the whole indexed revision. RecordLFSObject,
// the upload path, leaves it NULL: an upload is not yet a commit.
//
// **The consequence, stated plainly: deleting a file does not reduce the
// namespace's usage.** UsageByRepo sums these rows, NamespaceQuotaForRepo
// divides by that sum, and lfs.withinQuota refuses uploads against it -- and
// none of those numbers move when a 10 GiB checkpoint is deleted from the tip,
// because the commit that added it is still in the repository's history and
// cloning that revision still has to work. This is not the leak an earlier
// revision of this method tried to fix; it is the correct accounting for a
// versioned store, the same one `git clone` costs you. Deleting the repository
// (or rewriting its history so those commits are gone, and running
// `thinkingface gc`) is what actually frees the bytes -- and that is what the
// quota-refusal message and the user-facing storage docs have to say, because
// "delete the file" is advice that does nothing.
//
// notBefore is the age floor on top of that, and it is still load bearing. An
// object is uploaded and linked long before the commit naming it exists -- for
// a large dataset, hours before -- so an uncommitted link is as likely to be an
// upload in flight as an abandoned one. Only links settled before notBefore are
// considered, which also covers a second ref of the same repository being
// indexed concurrently (ref locks are per ref, so its LinkLFSObjects may have
// run while its ReplaceRepoFiles has not).
//
// The repo_files clause is redundant belt-and-braces: a link the syncer just
// earned is stamped committed by LinkLFSObjects before ReplaceRepoFiles writes
// the row. It stays because the cost of the two disagreeing is content that
// 404s, and the cost of the extra predicate is nothing.
//
// A NULL created_at -- possible only on a SQLite database migrated by
// sqlite/0028, whose ADD COLUMN could carry no default -- is kept rather than
// collected. The migration stamps every existing row, so this is the safe
// reading of a state that should not occur.
func (s *Store) PruneRepoLFSLinks(ctx context.Context, repoID int64, notBefore time.Time) (int64, error) {
	// No lockRepoRow: this deletes child rows of a single repository and
	// takes no lock the index rebuilders want, so a concurrent DeleteRepo
	// simply cascades over whatever is left.
	return s.db.Exec(ctx,
		`DELETE FROM repo_lfs_objects
		 WHERE repo_id = $1
		   AND committed_at IS NULL
		   AND created_at IS NOT NULL
		   AND created_at < $2
		   AND NOT EXISTS (
		       SELECT 1 FROM repo_files f
		       WHERE f.repo_id = $1 AND f.lfs_oid = repo_lfs_objects.oid)`,
		repoID, notBefore)
}

// ------------------------------------------------------------------ parquet

type ParquetFile struct {
	Path         string          `json:"path"`
	NumRows      int64           `json:"num_rows"`
	NumRowGroups int             `json:"num_row_groups"`
	NumColumns   int             `json:"num_columns"`
	Size         int64           `json:"size"`
	Schema       json.RawMessage `json:"schema"`
}

func (s *Store) UpsertParquetFile(ctx context.Context, repoID int64, ref, path string, numRows int64, numRowGroups int, schema json.RawMessage) error {
	if len(schema) == 0 {
		schema = json.RawMessage("[]")
	}
	// The same sanitising ReplaceRepoFiles applies, and for the same two
	// reasons (see text.go). A git path is any byte string without NUL or
	// '/', so a .parquet committed from an old workstation can be Shift_JIS:
	// PostgreSQL refuses those bytes outright (SQLSTATE 22021) and the refusal
	// parks the sync job, freezing the repository's whole index. And on
	// SQLite, where the insert succeeds, ListParquetFiles joins parquet_files
	// to repo_files on `f.path = p.path` -- with only one side folded the join
	// misses and the viewer reports a zero-byte file.
	path = sanitizeText(path)
	// Same parent-row-first ordering as ReplaceRepoFiles (see lockRepoRow):
	// the ON CONFLICT update would otherwise lock the parquet_files row
	// before the foreign-key check reaches repositories. ErrNotFound when the
	// repository is gone.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.lockRepoRow(ctx, tx, repoID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO parquet_files (repo_id, ref, path, num_rows, num_row_groups, schema, indexed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, now())
		 ON CONFLICT (repo_id, ref, path) DO UPDATE
		 SET num_rows = EXCLUDED.num_rows, num_row_groups = EXCLUDED.num_row_groups,
		     schema = EXCLUDED.schema, indexed_at = now()`,
		repoID, ref, path, numRows, numRowGroups, []byte(schema)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteParquetFiles(ctx context.Context, repoID int64, ref string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`DELETE FROM parquet_files WHERE repo_id = $1 AND ref = $2 AND path `+s.d.inArray("$3"), repoID, ref, s.d.stringArrayArg(paths))
	return err
}

func (s *Store) ListParquetFiles(ctx context.Context, repoID int64, ref string) ([]ParquetFile, error) {
	rows, err := s.db.Query(ctx,
		// repo_files is joined for f.lfs_oid (and for f.size as the fallback
		// inside the COALESCE), not for a column of its own: the file's size
		// is the LFS object's where there is one, and the indexed size
		// otherwise.
		`SELECT p.path, p.num_rows, p.num_row_groups, p.schema,
		        COALESCE(l.size, f.size, 0)
		 FROM parquet_files p
		 LEFT JOIN repo_files f ON f.repo_id = p.repo_id AND f.ref = p.ref AND f.path = p.path
		 LEFT JOIN lfs_objects l ON l.oid = f.lfs_oid
		 WHERE p.repo_id = $1 AND p.ref = $2 ORDER BY p.path`, repoID, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ParquetFile{}
	for rows.Next() {
		var p ParquetFile
		var schemaRaw []byte
		if err := rows.Scan(&p.Path, &p.NumRows, &p.NumRowGroups, &schemaRaw, &p.Size); err != nil {
			return nil, err
		}
		p.Schema = json.RawMessage(schemaRaw)
		var cols []any
		if json.Unmarshal(schemaRaw, &cols) == nil {
			p.NumColumns = len(cols)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
