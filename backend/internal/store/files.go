package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
			rows = append(rows, []any{repoID, ref, f.Path, f.Size, f.BlobSHA, f.LFSOID})
		}
		if err := tx.BulkInsert(ctx, "repo_files",
			[]string{"repo_id", "ref", "path", "size", "blob_sha", "lfs_oid"}, rows); err != nil {
			return fmt.Errorf("insert repo_files: %w", err)
		}
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
		`INSERT INTO repo_lfs_objects (repo_id, oid) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		repoID, oid); err != nil {
		return fmt.Errorf("link lfs object: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Store) LinkLFSObjects(ctx context.Context, repoID int64, oids []string) error {
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
	oidArg := s.d.stringArrayArg(oids)
	if _, err := tx.Exec(ctx,
		`SELECT oid FROM lfs_objects WHERE oid `+s.d.inArray("$1")+s.d.forUpdate(""), oidArg); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, s.d.queries().linkLFSObjectsInsert, repoID, oidArg); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
		`SELECT p.path, p.num_rows, p.num_row_groups, p.schema,
		        COALESCE(f.size, 0), COALESCE(l.size, f.size, 0)
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
		var blobSize, realSize int64
		var schemaRaw []byte
		if err := rows.Scan(&p.Path, &p.NumRows, &p.NumRowGroups, &schemaRaw, &blobSize, &realSize); err != nil {
			return nil, err
		}
		p.Schema = json.RawMessage(schemaRaw)
		p.Size = realSize
		var cols []any
		if json.Unmarshal(schemaRaw, &cols) == nil {
			p.NumColumns = len(cols)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
