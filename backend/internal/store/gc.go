package store

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
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
	return collect(ctx, s.db, `SELECT oid, size FROM lfs_objects`, nil,
		func(row rowScanner) (LFSObjectRef, error) {
			var o LFSObjectRef
			err := row.Scan(&o.OID, &o.Size)
			return o, err
		})
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
	return collectSet(ctx, s.db, `SELECT DISTINCT oid FROM repo_lfs_objects`)
}

// ListReferencedBlobSHAs returns the set of non-LFS git blob hashes that some
// indexed revision still carries. It is the reference count for the blobs/
// layer, the way ListReferencedLFSOIDs is for lfs/: `thinkingface gc` deletes
// a blobs/ object only when no repo_files row names its sha any more.
func (s *Store) ListReferencedBlobSHAs(ctx context.Context) (map[string]bool, error) {
	return collectSet(ctx, s.db, `SELECT DISTINCT blob_sha FROM repo_files WHERE lfs_oid IS NULL`)
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

// deleteLFSObjectUnderClaim is the transaction both LFS collectors run. They
// differ only in how they claim the oid -- the orphan pass locks the existing
// row, the untracked pass inserts one to take ownership of a row that is not
// there -- so claim is the only thing passed in. It reports whether the
// caller now holds the oid; false means somebody else does, which is a normal
// outcome and not a fault.
//
// The ordering after the claim is the part worth having in one place. Storage
// goes first, and it goes *inside* the transaction: once the lfs_objects row
// is gone nothing would ever notice bytes left behind in the bucket, and a
// storage failure has to roll the claim back so the object is simply
// re-considered on the next run. That means a network round trip while
// holding a row lock, deliberately -- the alternative is losing track of
// bytes that are still being charged for.
func (s *Store) deleteLFSObjectUnderClaim(ctx context.Context, oid string, removeStorage func() error, claim func(context.Context, tx) (bool, error)) (bool, error) {
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

	held, err := claim(ctx, tx)
	if err != nil || !held {
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
// The NOT EXISTS below reads repo_lfs_objects by oid alone, which neither
// PRIMARY KEY (repo_id, oid) nor the partial (repo_id, created_at) index can
// serve -- oid leads neither. Migration 0004 adds idx_repo_lfs_objects_oid
// for it, and the foreign-key check the lfs_objects DELETE triggers uses the
// same index: without it a collection pass scanned the whole link table twice
// per candidate, while holding this lock across a storage round trip.
//
// Returns deleted=false with a nil error when the oid is gone or has gained
// a repository reference; the caller must not treat that as a storage fault.
func (s *Store) DeleteOrphanedLFSObject(ctx context.Context, oid string, removeStorage func() error) (deleted bool, err error) {
	return s.deleteLFSObjectUnderClaim(ctx, oid, removeStorage, func(ctx context.Context, t tx) (bool, error) {
		var locked string
		err := t.QueryRow(ctx, `
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
		return true, nil
	})
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
	return s.deleteLFSObjectUnderClaim(ctx, oid, removeStorage, func(ctx context.Context, t tx) (bool, error) {
		var claimed string
		err := t.QueryRow(ctx,
			`INSERT INTO lfs_objects (oid, size) VALUES ($1, 0)
			 ON CONFLICT (oid) DO NOTHING
			 RETURNING oid`, oid).Scan(&claimed)
		if isNoRows(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	})
}

// ---------------------------------------------------- blobs/ deletion ledger

// DeleteOrphanedBlob removes one blobs/ object, but only if no indexed
// revision names its sha at the moment it goes -- and it records the removal
// so that a revision which names it *anyway* can be repaired.
//
// It is the blob layer's answer to DeleteOrphanedLFSObject, and it has to
// build the thing that method is handed for free. An LFS object has a row,
// and every writer of a reference to it takes that row's lock, so "claim,
// re-check, delete" is a single transaction. A blob has no row: the push path
// writes the bytes (gitrepo.PublishBlob) and the index rows
// (ReplaceRepoFiles) with nothing in between that a collector could block on,
// and PublishBlob skips an object that is already at its key, so the object's
// own Updated timestamp does not even move when a second repository starts
// referencing it. Age -- the only signal the pass used to have -- therefore
// cannot see the reference coming.
//
// blob_deletions is the row that was missing, and it is written by the
// collector rather than by the push path so that the common case (every file
// of every push) stays free. The sequence is:
//
//  1. Record the intent in its own transaction, so it is durable *before* any
//     byte is removed. A crash anywhere after this leaves a ledger row for an
//     object that may still exist, which the repair pass answers with one
//     idempotent PublishBlob -- the safe direction. Recording it inside the
//     delete transaction would have the opposite failure: bytes gone, no
//     record, and nothing short of `thinkingface resync` to notice.
//  2. Take that row FOR UPDATE, re-check repo_files under it, and delete
//     storage before committing. RepairDeletedBlobs takes the same rows, so
//     the collector and the sync pipeline's repair serialise on them: if the
//     repair got there first, its ReplaceRepoFiles has already committed and
//     the re-check below sees the reference; if the collector got there
//     first, the repair blocks, then sees the ledger row and re-publishes.
//  3. Keep the row. It is what stops ListIndexedBlobSHAs' "already published"
//     shortcut from making the loss permanent; RepairDeletedBlobs removes it
//     once the bytes are back, and PruneBlobDeletions removes it once nothing
//     references the sha at all.
//
// Returns deleted=false with a nil error when the sha gained a reference, or
// when somebody else already holds the ledger row; neither is a fault.
//
// **The serialisation is Postgres'.** forUpdate renders nothing on SQLite,
// where there are no row locks -- but `thinkingface gc` is refused outright in
// SQLite mode (backend/entrypoint.sh), so this method has no concurrent
// counterpart there to race with.
func (s *Store) DeleteOrphanedBlob(ctx context.Context, sha string, removeStorage func() error) (bool, error) {
	if sha == "" {
		return false, nil
	}
	if removeStorage == nil {
		return false, errors.New("removeStorage is required")
	}

	// Step 1: the intent, committed on its own. ON CONFLICT DO UPDATE rather
	// than DO NOTHING so a sha considered by two runs (or reconsidered after
	// a failure) has its timestamp refreshed and cannot be pruned out from
	// under the second one.
	if _, err := s.db.Exec(ctx,
		`INSERT INTO blob_deletions (blob_sha, deleted_at) VALUES ($1, now())
		 ON CONFLICT (blob_sha) DO UPDATE SET deleted_at = now()`, sha); err != nil {
		return false, fmt.Errorf("record blob deletion: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Step 2. A row that is not there any more means a repair pass took it
	// between the insert above and this lock, which is that pass saying the
	// sha is referenced and the bytes are wanted. Backing off is the whole
	// point of asking.
	var claimed string
	err = tx.QueryRow(ctx,
		`SELECT blob_sha FROM blob_deletions WHERE blob_sha = $1`+s.d.forUpdate(""), sha).Scan(&claimed)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim blob deletion: %w", err)
	}

	var referenced bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM repo_files WHERE blob_sha = $1 AND lfs_oid IS NULL)`,
		sha).Scan(&referenced); err != nil {
		return false, fmt.Errorf("re-check blob references: %w", err)
	}
	if referenced {
		// Rolled back rather than committed: nothing was removed, so the
		// ledger row is left as the repair pass found it. If a *previous*
		// run really did take these bytes, that row is the only record of
		// it, and dropping it here would throw the repair away.
		return false, nil
	}

	if err := removeStorage(); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RepairDeletedBlobs re-publishes the blobs of one ref that the collector has
// removed, and forgets them once they are back. It runs at the end of the
// post-push pipeline, after ReplaceRepoFiles has committed, and in the
// ordinary case it is one indexed SELECT that returns nothing.
//
// It is the other half of DeleteOrphanedBlob, and the reason the pair closes
// the window rather than merely narrowing it. The rows are taken FOR UPDATE,
// so:
//
//   - a collector that already committed its intent holds the row until its
//     own delete is done; this blocks, then sees the row and re-publishes
//   - a collector that has not started yet blocks on this transaction, and by
//     the time it runs its re-check the caller's repo_files rows are
//     committed, so it refuses to delete
//
// republish must be idempotent and must tolerate an object that is still
// there -- gitrepo.PublishBlob is exactly that, one Stat when the bytes
// survived. It is called with the transaction open, deliberately: releasing
// the rows first would put the delete back in play while the bytes are still
// missing. That is a storage round trip under a row lock, the same trade
// deleteLFSObjectUnderClaim makes, and it is paid only when there is
// something to repair.
//
// Returns how many shas were re-published.
//
// The EXISTS is a semi-join either engine may drive from whichever side is
// smaller, which is what keeps it off the push path's critical path: the
// ledger is normally empty or close to it (PruneBlobDeletions), and even
// straight after a large collection the repo_files probe is the partial index
// migration 0006 adds. SQLite never writes this table at all -- `thinkingface
// gc` is refused there -- so the query is a scan of nothing.
func (s *Store) RepairDeletedBlobs(ctx context.Context, repoID int64, ref string, republish func(sha string) error) (int, error) {
	if republish == nil {
		return 0, errors.New("republish is required")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// ORDER BY so two refs of one repository that share a damaged sha take
	// the rows in the same sequence rather than deadlocking on each other.
	shas, err := collect(ctx, tx,
		`SELECT d.blob_sha FROM blob_deletions d
		 WHERE EXISTS (
		     SELECT 1 FROM repo_files f
		     WHERE f.repo_id = $1 AND f.ref = $2 AND f.lfs_oid IS NULL
		       AND f.blob_sha = d.blob_sha)
		 ORDER BY d.blob_sha`+s.d.forUpdate(" OF d"),
		[]any{repoID, ref},
		func(row rowScanner) (string, error) {
			var sha string
			err := row.Scan(&sha)
			return sha, err
		})
	if err != nil {
		return 0, fmt.Errorf("list deleted blobs: %w", err)
	}
	if len(shas) == 0 {
		// Nothing locked, nothing to write: end the transaction rather than
		// leave it open across the caller's remaining work.
		return 0, tx.Commit(ctx)
	}

	for _, sha := range shas {
		if err := republish(sha); err != nil {
			return 0, fmt.Errorf("republish blob %s: %w", sha, err)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM blob_deletions WHERE blob_sha `+s.d.inArray("$1"), s.d.stringArrayArg(shas)); err != nil {
		return 0, fmt.Errorf("forget blob deletions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(shas), nil
}

// PruneBlobDeletions drops ledger rows that have outlived their purpose: a
// sha no indexed revision names is one nothing will ever ask to be repaired,
// and the row would otherwise sit there for the life of the instance. That is
// the ordinary outcome of a successful collection, so this is what keeps the
// table proportional to the damage rather than to the bytes reclaimed.
//
// before is an age floor rather than a nicety. A row is inserted before the
// bytes are removed, and the push that would claim the sha may be minutes
// from committing its repo_files rows, so a row deleted the instant it looks
// unreferenced could take the repair record with it.
func (s *Store) PruneBlobDeletions(ctx context.Context, before time.Time) (int64, error) {
	return s.db.Exec(ctx,
		`DELETE FROM blob_deletions
		 WHERE deleted_at < $1
		   AND NOT EXISTS (
		       SELECT 1 FROM repo_files f
		       WHERE f.blob_sha = blob_deletions.blob_sha AND f.lfs_oid IS NULL)`, before)
}
