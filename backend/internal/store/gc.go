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
//
// That table, not repo_files.lfs_oid, is the reference count -- which only
// works because every path that puts an oid into repo_files links it first.
// The syncer's post-push pipeline is the one that closes that: it calls
// LinkLFSObjects for the whole revision *before* ReplaceRepoFiles, so a
// pointer committed as a plain blob (a client with no git-lfs, which the LFS
// batch API therefore never hears about) cannot leave a file naming bytes this
// query would then report as unreferenced. Reading repo_files here instead
// would look equivalent and is not: nothing about writing a repo_files row
// takes the lfs_objects lock DeleteOrphanedLFSObject waits on, so a push
// racing the collector would still lose the bytes.
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

// UntrackedLFSObjects returns the lfs/ objects that no lfs_objects row
// accounts for at all -- the leak class OrphanedLFSObjects structurally
// cannot see, since it starts from the rows.
//
// Every write path puts the bytes at storage.LFSKey(oid) before it records
// the row, deliberately: the reverse order would publish a row promising
// bytes that are not there, which nothing repairs, whereas bytes without a
// row are merely unreferenced. The cost of that choice is that a crash
// between the two -- in LFS verify's promote, the emulator proxy upload,
// POST /api/v1/upload, or the experiment flusher -- strands an object no
// query can name. Left alone it is charged for forever, since the only thing
// that ever gives it a row again is somebody uploading byte-identical
// content.
//
// tracked is the lfs_objects set, read *after* the bucket listing so a row
// committed while the (slow) listing ran still counts. cutoff is the age
// floor: an object younger than it may be an upload whose row is about to be
// written, and looks exactly like a leak until it is. Objects whose key is
// not the canonical home of the oid in their basename are skipped rather
// than deleted -- nothing this system writes produces such a key, so it is
// not a shape to guess at.
func UntrackedLFSObjects(objects []storage.ObjectInfo, tracked []LFSObjectRef, cutoff time.Time) []storage.ObjectInfo {
	known := make(map[string]bool, len(tracked))
	for _, o := range tracked {
		known[o.OID] = true
	}
	out := make([]storage.ObjectInfo, 0)
	for _, o := range objects {
		oid := path.Base(o.Key)
		if known[oid] || storage.LFSKey(oid) != o.Key || !o.Updated.Before(cutoff) {
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

// DeleteUntrackedLFSObject removes an lfs/ object that has no lfs_objects row,
// but only if it still has none at delete time.
//
// The re-check matters more here than the age floor the caller applies does.
// An untracked object is not inert: an upload batch that finds the bytes
// already at storage.LFSKey(oid) deduplicates against them, and dedup links
// the oid to the repository *without rewriting anything*. So an object can be
// months old, invisible to every query, and gain a row a millisecond after
// the collector listed it -- with its storage timestamp still months old.
// Deleting it then would leave a repository holding a link to bytes that no
// longer exist, and the client that "uploaded" it was told to send nothing.
// No grace period, however long, closes that: the object's age says nothing
// about when the row appears.
//
// What closes it is taking the same lock RecordLFSObject takes. There is no
// row to lock, so this claims the oid by inserting one, and deletes it again
// in the same transaction:
//
//  1. INSERT ... ON CONFLICT DO NOTHING RETURNING oid. A returned row means
//     the oid was genuinely untracked *and* the collector now holds it: any
//     concurrent RecordLFSObject for the same oid blocks on that tuple until
//     this transaction ends. No row returned means somebody else owns the oid
//     -- it was already tracked, or an insert is in flight -- so this bails
//     out without touching storage.
//  2. removeStorage() while still holding it, then DELETE the row. Storage
//     goes first for the reason it does in DeleteOrphanedLFSObject: once the
//     row is gone nothing would notice bytes left behind. A storage failure
//     rolls the whole transaction back, so the claim row disappears too and
//     the object simply gets re-considered next run.
//
// A blocked RecordLFSObject resumes after this commits, re-inserts the row,
// and then fails its own confirmPresent check -- ErrLFSObjectGone, which the
// batch path turns into an upload action and verify turns into a retryable
// error. The client re-uploads instead of trusting bytes that are gone.
//
// On SQLite there are no row locks and none are needed: every write
// transaction runs alone on the single writer connection, so the insert and
// the delete are the same indivisible step. That holds within one process
// and only there -- a collector reading a Litestream-restored copy of the
// database would see neither the server's rows nor its locks, which is why
// `thinkingface gc` is refused outright in sqlite mode rather than guarded
// here (infra/README.md, backend/entrypoint.sh).
//
// Returns deleted=false with a nil error when the oid turned out to be
// tracked; that is a normal outcome, not a fault.
func (s *Store) DeleteUntrackedLFSObject(ctx context.Context, oid string, removeStorage func() error) (deleted bool, err error) {
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

	var claimed string
	err = tx.QueryRow(ctx,
		`INSERT INTO lfs_objects (oid, size) VALUES ($1, 0)
		 ON CONFLICT (oid) DO NOTHING
		 RETURNING oid`, oid).Scan(&claimed)
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
