package store

import (
	"context"
	"errors"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"path"
	"time"
)

// LFSObjectRef is one row of lfs_objects, minimal enough for the GC scan.
// The bytes are always at storage.LFSKey(OID), so the collector derives the
// key rather than carrying one.
type LFSObjectRef struct {
	OID  string
	Size int64
}

// ListLFSObjects returns every LFS object recorded in the content-addressed
// store, regardless of whether any repository still references it.
func (s *Store) ListLFSObjects(ctx context.Context) ([]LFSObjectRef, error) {
	rows, err := s.db.Query(ctx, `SELECT oid, size FROM lfs_objects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LFSObjectRef{}
	for rows.Next() {
		var o LFSObjectRef
		if err := rows.Scan(&o.OID, &o.Size); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListReferencedLFSOIDs returns the set of oids that at least one repository
// still links to, via repo_lfs_objects.
func (s *Store) ListReferencedLFSOIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT oid FROM repo_lfs_objects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			return nil, err
		}
		out[oid] = true
	}
	return out, rows.Err()
}

// ListReferencedBlobSHAs returns the set of non-LFS git blob hashes that some
// indexed revision still carries. It is the reference count for the blobs/
// layer, the way ListReferencedLFSOIDs is for lfs/: `thinkingface gc` deletes
// a blobs/ object only when no repo_files row names its sha any more.
func (s *Store) ListReferencedBlobSHAs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT blob_sha FROM repo_files WHERE lfs_oid IS NULL`)
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

// OrphanedBlobs is the blob pass's decision, the counterpart of
// OrphanedLFSObjects: a blobs/ object qualifies when no repo_files row names
// its sha (referenced, keyed by sha) and it was last written before cutoff --
// the grace that stands in for the row lock the LFS pass has, since nothing
// records a blob at the moment it is written. Pure, so it can be unit tested
// directly.
func OrphanedBlobs(objects []storage.ObjectInfo, referenced map[string]bool, cutoff time.Time) []storage.ObjectInfo {
	out := make([]storage.ObjectInfo, 0)
	for _, o := range objects {
		if referenced[path.Base(o.Key)] || !o.Updated.Before(cutoff) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// OrphanedLFSObjects returns the objects in all that no repository
// references, per referenced. It is a pure function -- the GC's core
// decision, isolated from the database, so it can be unit tested directly.
func OrphanedLFSObjects(all []LFSObjectRef, referenced map[string]bool) []LFSObjectRef {
	out := make([]LFSObjectRef, 0)
	for _, o := range all {
		if !referenced[o.OID] {
			out = append(out, o)
		}
	}
	return out
}

// DeleteOrphanedLFSObject deletes one LFS object from storage and the
// database, but only if it is still unreferenced at delete time.
//
// The GC scan that produced the candidate oid is not atomic with deletion: a
// push or LFS verify can INSERT into repo_lfs_objects for a previously
// orphaned oid in between. To close that window this method:
//
//  1. Locks the lfs_objects row (SELECT FOR UPDATE). Writers that add a
//     repo_lfs_objects row take a FOR KEY SHARE lock on the same parent via
//     the foreign key (and RecordLFSObject / LinkLFSObjects also lock it
//     explicitly), so a concurrent link either commits before this SELECT
//     -- in which case NOT EXISTS fails -- or waits until we commit.
//  2. Re-checks repo_lfs_objects. PostgreSQL re-evaluates the WHERE clause
//     after waiting, so a link that committed while we waited is visible.
//  3. Calls removeStorage while still holding the lock, then deletes the
//     row. Storage must go first: once the row is gone nothing else will
//     notice bytes left in the bucket. If storage delete fails the
//     transaction rolls back and the row stays for a later retry.
//
// An LFS upload batch may already have Stat'ed the object as present
// before blocking on this lock. After we commit, RecordLFSObject's INSERT
// would recreate the row. RecordLFSObject therefore re-checks storage
// under the same lock and rolls back with ErrLFSObjectGone if the bytes
// are gone, so the batch issues an upload action instead of treating the
// oid as already stored.
//
// On SQLite there are no row locks; the same ordering holds because every
// write transaction runs alone on the single writer connection, so a link
// either committed before this transaction began or waits for it to end.
//
// Returns deleted=false with a nil error when the oid is gone or has gained
// a repository reference; the caller must not treat that as a storage fault.
func (s *Store) DeleteOrphanedLFSObject(ctx context.Context, oid string, removeStorage func() error) (deleted bool, err error) {
	if oid == "" {
		return false, nil
	}
	if removeStorage == nil {
		return false, errors.New("removeStorage is required")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var locked string
	err = tx.QueryRow(ctx, `
		SELECT oid FROM lfs_objects
		WHERE oid = $1
		  AND NOT EXISTS (SELECT 1 FROM repo_lfs_objects WHERE oid = $1)`+
		s.d.forUpdate(" OF lfs_objects"), oid).Scan(&locked)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if err := removeStorage(); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM lfs_objects WHERE oid = $1`, oid); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
