package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"path"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/lfs"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// blobGrace is how recently a blobs/ object may have been written and still be
// spared. Nothing records a blob in the database, so "unreferenced" is only
// ever inferred; a young object is most likely one a push or the parquet
// viewer wrote moments ago, for a revision whose repo_files rows are not
// committed yet. A day is far longer than either takes and costs only the
// storage of a handful of objects until the next run.
//
// It is no longer what makes the pass safe, and it never could have been.
// gitrepo.PublishBlob skips an object that is already at its key, so a second
// repository starting to reference a year-old blob does not move that
// object's Updated timestamp by a single second -- no age threshold can see
// that reference arriving. store.DeleteOrphanedBlob is what does: it takes a
// blob_deletions row and re-checks repo_files under it, against the same row
// the sync pipeline's repair pass takes. What the grace still buys is what
// untrackedLFSGrace buys on the other layer -- an object being written right
// now is not offered to that machinery at all.
const blobGrace = 24 * time.Hour

// deletionLedgerGrace is how long a blob_deletions row is kept after nothing
// references its sha any more. The row exists so a revision that names a
// collected sha can have the bytes put back (store.RepairDeletedBlobs); once
// no revision names it, it is only waiting to be pruned. The floor matters
// because the intent is recorded *before* the bytes go, and the push that
// would claim the sha may still be minutes from committing its repo_files
// rows -- pruning on sight would throw away the repair record for exactly the
// race the ledger exists to survive. A day, for the same reason blobGrace is
// a day, and it costs one small row per collected blob until then.
const deletionLedgerGrace = 24 * time.Hour

// untrackedLFSGrace is how long an lfs/ object may exist with no lfs_objects
// row before gc reads it as leaked rather than mid-upload. Every write path
// puts the bytes at their content-addressed key before recording the row (the
// reverse order would advertise an object whose bytes are not there yet), so
// there is always a window in which a perfectly healthy upload is
// indistinguishable from a crash that never got to the row.
//
// Unlike blobGrace this is not what makes the pass safe -- the interesting
// race is against dedup, which links an existing object without rewriting it,
// so no age threshold can ever see it coming; store.DeleteUntrackedLFSObject
// takes the row lock that does. What the grace buys is that an upload in
// flight is not *failed*: gc deleting bytes a verify is a moment away from
// linking is answered with ErrLFSObjectGone and a re-upload, which is correct
// but is a push the user watches fail for no reason. A day is orders of
// magnitude more than the gap it covers -- a single database transaction --
// and costs only the storage of whatever a crash stranded since the previous
// run.
const untrackedLFSGrace = 24 * time.Hour

// minStagingGrace floors the window below, so that an operator who shortens
// TF_SIGNED_URL_MAX_TTL cannot shorten it to something a slow transfer can
// outlive. It matches blobGrace for the same reason blobGrace is a day.
const minStagingGrace = 24 * time.Hour

// stagingGrace is how long an object may sit under tmp/uploads/ before gc
// treats it as abandoned rather than mid-upload. Nothing records a staging
// object anywhere -- there is no table for it, unlike lfs_objects for lfs/ --
// so age is the only signal available, the same inference gcBlobs makes for
// blobs/.
//
// The floor that inference has to clear is the longest a signed PUT URL can
// legitimately still be in use. That is *not* TF_SIGNED_URL_MAX_TTL read
// literally: lfs.MaxSignedURLTTL is the authority, because a zero (no
// ceiling) means URLs live up to GCS's 7-day signing limit rather than
// expiring sooner. Deriving it there rather than restating it here is the
// point -- the two drifting apart is silent and expensive in both directions:
// a hardcoded 24h against a raised ceiling has gc deleting uploads that are
// still being written.
//
// Doubling leaves room for an upload that started just before its URL's
// nominal expiry, plus clock skew between whichever machine signed the URL and
// whichever machine runs gc.
func stagingGrace(signedURLMaxTTL time.Duration) time.Duration {
	if grace := 2 * lfs.MaxSignedURLTTL(signedURLMaxTTL); grace > minStagingGrace {
		return grace
	}
	return minStagingGrace
}

// gcDB is the store surface runGC needs. *store.Store implements it.
type gcDB interface {
	ListLFSObjects(ctx context.Context) ([]store.LFSObjectRef, error)
	ListReferencedLFSOIDs(ctx context.Context) (map[string]bool, error)
	DeleteOrphanedLFSObject(ctx context.Context, oid string, removeStorage func() error) (deleted bool, err error)
	DeleteUntrackedLFSObject(ctx context.Context, oid string, removeStorage func() error) (deleted bool, err error)
	ListReferencedBlobSHAs(ctx context.Context) (map[string]bool, error)
	DeleteOrphanedBlob(ctx context.Context, sha string, removeStorage func() error) (deleted bool, err error)
	PruneBlobDeletions(ctx context.Context, before time.Time) (int64, error)
}

// gcStorage is the object-store surface runGC needs.
type gcStorage interface {
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error)
}

// runGC reclaims the two content-addressed layers -- lfs/, whose objects are
// tracked in lfs_objects and referenced through repo_lfs_objects, and blobs/,
// which is tracked nowhere and referenced by repo_files.blob_sha -- plus the
// tmp/uploads/ staging area LFS PUTs land in before verify promotes them into
// lfs/. Neither content-addressed layer shrinks on its own -- repositories get
// deleted and files get overwritten, but a content-addressed key is immutable
// and may be shared by any number of repositories, so no push or delete may
// remove one. Staging objects are different: nothing shares them, so an
// interrupted upload just sits there until gc removes it.
//
// Both content-addressed layers are also scanned from the bucket, not only
// from the database: an object whose bytes were written and whose row never
// was is invisible to any query, and gcLFS covers that shape for lfs/ the way
// gcBlobs has always had to for blobs/.
//
// Every pass reports first and only deletes with --yes (or --dry-run=false).
func runGC(ctx context.Context, db gcDB, obj gcStorage, signedURLMaxTTL time.Duration, args []string) error {
	// ContinueOnError, like every other subcommand's flag set: ExitOnError
	// calls os.Exit(2) from inside Parse, which skips run()'s `defer
	// db.Close()` and the signal context's `defer stop()`. A bad flag is a
	// returned error, so the same teardown a successful run gets happens.
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", true, "report orphaned objects without deleting anything (default)")
	yes := fs.Bool("yes", false, "actually delete the orphaned objects from storage and postgres")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Either flag on its own is enough to authorise a real delete: --yes is
	// the explicit confirmation, and --dry-run=false says the same thing the
	// other way round. The default (dry-run=true, yes=false) only reports.
	execute := *yes || !*dryRun

	// Every pass runs even if an earlier one failed, and the failures are
	// reported together. The passes share no state and each one already
	// tolerates a per-object failure by logging it and moving on, so letting
	// one abort the rest only means whatever it would have reclaimed waits
	// for the next scheduled run -- a week, at the shipped schedule.
	return errors.Join(
		gcLFS(ctx, db, obj, execute),
		gcBlobs(ctx, db, obj, execute),
		gcStaging(ctx, obj, signedURLMaxTTL, execute),
	)
}

// gcLFS reclaims the lfs/ layer, which leaks in two different shapes and
// needs a pass for each.
//
//   - Objects lfs_objects knows about that no repository links to any more:
//     the ordinary outcome of deleting a repository or dropping a file. The
//     rows are the starting point, and repo_lfs_objects is the reference count
//     (gcLFSUnreferenced).
//   - Objects lfs_objects knows nothing about, because the bytes were written
//     and the row never was. Starting from the rows cannot find these by
//     construction, so this pass starts from the bucket the way gcBlobs does
//     (gcLFSUntracked).
//
// The reads are shared and ordered deliberately. The listing goes first
// because it is the slow one, and both database reads come after it, so a row
// that commits while the listing runs is seen rather than mistaken for a leak
// -- the same ordering, for the same reason, that gcBlobs uses. The untracked
// set is then computed from the rows as they stand before either pass deletes
// anything, so whatever the unreferenced pass removes is already excluded
// from it and no key is offered to both.
func gcLFS(ctx context.Context, db gcDB, obj gcStorage, execute bool) error {
	// A read that fails aborts both passes rather than either: an unusable
	// listing leaves the untracked pass with nothing to reason about, and
	// treating an unreadable lfs_objects as an empty one would make every
	// stored object look untracked. Deletes go to the same bucket the
	// listing failed against, so there is nothing to salvage by continuing.
	objects, err := obj.List(ctx, storage.LFSPrefix)
	if err != nil {
		return fmt.Errorf("list lfs objects in storage: %w", err)
	}
	all, err := db.ListLFSObjects(ctx)
	if err != nil {
		return fmt.Errorf("list lfs objects: %w", err)
	}
	referenced, err := db.ListReferencedLFSOIDs(ctx)
	if err != nil {
		return fmt.Errorf("list referenced lfs oids: %w", err)
	}

	untracked := store.UntrackedLFSObjects(objects, all, time.Now().Add(-untrackedLFSGrace))

	// Deleting is a different matter: the two candidate sets are disjoint and
	// every delete is guarded on its own, so one object gc could not remove
	// is no reason to leave the other pass's objects for another week.
	return errors.Join(
		gcLFSUnreferenced(ctx, db, obj, all, referenced, execute),
		gcLFSUntracked(ctx, db, obj, untracked, len(objects), execute),
	)
}

// gcLFSUnreferenced collects lfs/ objects that no repository links to any more.
//
// The initial scan is a snapshot: between that listing and each delete, a
// push or LFS verify can attach a previously orphaned oid to a repository.
// Actual deletion therefore goes through DeleteOrphanedLFSObject, which
// re-checks under a row lock and only then removes storage and the row.
// Storage goes first inside that lock: if it fails, the row stays so a later
// run can retry. A concurrent upload batch that Stat'ed a hit before
// waiting on that lock is told to re-upload via RecordLFSObject's
// confirmPresent check (ErrLFSObjectGone).
func gcLFSUnreferenced(ctx context.Context, db gcDB, obj gcStorage, all []store.LFSObjectRef, referenced map[string]bool, execute bool) error {
	orphaned := store.OrphanedLFSObjects(all, referenced)

	var totalBytes int64
	for _, o := range orphaned {
		totalBytes += o.Size
		fmt.Printf("orphaned lfs    %s  %d bytes\n", o.OID, o.Size)
	}
	fmt.Printf("%d of %d lfs objects are orphaned (%d bytes total)\n", len(orphaned), len(all), totalBytes)

	if !execute {
		fmt.Println("dry run: nothing deleted. Re-run with --yes to delete these objects.")
		return nil
	}

	var deleted int
	var deletedBytes int64
	var skipped int
	var storageFailures int
	for _, o := range orphaned {
		key := storage.LFSKey(o.OID)
		ok, err := db.DeleteOrphanedLFSObject(ctx, o.OID, func() error {
			return obj.Delete(ctx, key)
		})
		if err != nil {
			slog.Error("gc: delete failed, keeping the row for a later retry",
				"oid", o.OID, "key", key, "error", err)
			storageFailures++
			continue
		}
		if !ok {
			skipped++
			continue
		}
		deleted++
		deletedBytes += o.Size
	}
	fmt.Printf("deleted %d of %d orphaned lfs objects (%d bytes)\n", deleted, len(orphaned), deletedBytes)
	if skipped > 0 {
		fmt.Printf("skipped %d objects that gained a repository reference since the scan\n", skipped)
	}
	if storageFailures > 0 {
		return fmt.Errorf("%d lfs objects failed to delete from storage; see the logged errors above", storageFailures)
	}
	return nil
}

// gcLFSUntracked collects lfs/ objects that have no lfs_objects row at all.
//
// Every upload path writes the bytes to the content-addressed key before it
// records the row, so a crash or a failed request in between strands an object
// that no query names -- and the unreferenced pass above, which enumerates
// rows, can never see it. Without this pass such an object is charged for
// until somebody happens to upload byte-identical content, which is to say
// indefinitely.
//
// Deletion goes through DeleteUntrackedLFSObject rather than straight to
// storage. The candidate list is as stale as any snapshot, and staleness is
// especially dangerous for this class: an upload batch that finds these bytes
// already present deduplicates against them and links the oid *without
// rewriting anything*, so the object gains a row while its storage timestamp
// stays as old as it ever was. That method claims the oid under the same lock
// RecordLFSObject takes, which is what makes the delete safe; untrackedLFSGrace
// only keeps gc away from uploads that are still in flight.
func gcLFSUntracked(ctx context.Context, db gcDB, obj gcStorage, untracked []storage.ObjectInfo, listed int, execute bool) error {
	var totalBytes int64
	for _, o := range untracked {
		totalBytes += o.Size
		fmt.Printf("untracked lfs   %s  %d bytes\n", o.Key, o.Size)
	}
	fmt.Printf("%d of %d stored lfs objects have no lfs_objects row (%d bytes total)\n", len(untracked), listed, totalBytes)

	if !execute {
		fmt.Println("dry run: nothing deleted. Re-run with --yes to delete these objects.")
		return nil
	}

	var deleted int
	var deletedBytes int64
	var skipped int
	var storageFailures int
	for _, o := range untracked {
		ok, err := db.DeleteUntrackedLFSObject(ctx, path.Base(o.Key), func() error {
			return obj.Delete(ctx, o.Key)
		})
		if err != nil {
			slog.Error("gc: delete failed, leaving the untracked lfs object for a later retry",
				"key", o.Key, "error", err)
			storageFailures++
			continue
		}
		if !ok {
			skipped++
			continue
		}
		deleted++
		deletedBytes += o.Size
	}
	fmt.Printf("deleted %d of %d untracked lfs objects (%d bytes)\n", deleted, len(untracked), deletedBytes)
	if skipped > 0 {
		fmt.Printf("skipped %d objects that gained an lfs_objects row since the scan\n", skipped)
	}
	if storageFailures > 0 {
		return fmt.Errorf("%d untracked lfs objects failed to delete from storage; see the logged errors above", storageFailures)
	}
	return nil
}

// gcBlobs collects blobs/ objects no indexed revision carries any more.
//
// The reference set is read after the (slow) bucket listing, so a push whose
// repo_files rows commit while the listing runs is seen rather than mistaken
// for a leak -- the same ordering, for the same reason, that gcLFS uses. That
// snapshot is only how candidates are *chosen*, though. What makes a delete
// safe is store.DeleteOrphanedBlob, which records the removal in
// blob_deletions, re-checks repo_files under that row and deletes storage
// before committing, against the same row store.RepairDeletedBlobs takes at
// the end of every push's sync pipeline. So a push that starts referencing a
// candidate after the scan either commits first and is seen by the re-check,
// or is blocked and then re-publishes the bytes it needs.
//
// The comment this replaces asked for the pass to be run "against a quiet
// instance", which the shipped deployment -- a Cloud Run Job on a schedule,
// beside a service that is always accepting pushes -- was never going to be.
//
// The prune at the end is what keeps the ledger from growing with every
// object ever reclaimed: a sha nothing references has nothing left to repair.
func gcBlobs(ctx context.Context, db gcDB, obj gcStorage, execute bool) error {
	objects, err := obj.List(ctx, "blobs/")
	if err != nil {
		return fmt.Errorf("list blobs: %w", err)
	}
	referenced, err := db.ListReferencedBlobSHAs(ctx)
	if err != nil {
		return fmt.Errorf("list referenced blob shas: %w", err)
	}
	orphaned := store.OrphanedBlobs(objects, referenced, time.Now().Add(-blobGrace))

	var totalBytes int64
	for _, o := range orphaned {
		totalBytes += o.Size
		fmt.Printf("orphaned blob   %s  %d bytes\n", o.Key, o.Size)
	}
	fmt.Printf("%d of %d blobs are orphaned (%d bytes total)\n", len(orphaned), len(objects), totalBytes)

	if !execute {
		fmt.Println("dry run: nothing deleted. Re-run with --yes to delete these objects.")
		return nil
	}

	var deleted int
	var deletedBytes int64
	var skipped int
	var storageFailures int
	for _, o := range orphaned {
		sha := path.Base(o.Key)
		ok, err := db.DeleteOrphanedBlob(ctx, sha, func() error {
			return obj.Delete(ctx, o.Key)
		})
		if err != nil {
			slog.Error("gc: delete failed, leaving the blob for a later retry", "key", o.Key, "error", err)
			storageFailures++
			continue
		}
		if !ok {
			skipped++
			continue
		}
		deleted++
		deletedBytes += o.Size
	}
	fmt.Printf("deleted %d of %d orphaned blobs (%d bytes)\n", deleted, len(orphaned), deletedBytes)
	if skipped > 0 {
		fmt.Printf("skipped %d blobs that gained a revision reference since the scan\n", skipped)
	}

	// Pruning runs whatever the deletes did: the rows it clears are last
	// run's, not this one's, and a storage failure above is no reason to let
	// the ledger keep growing.
	pruned, pruneErr := db.PruneBlobDeletions(ctx, time.Now().Add(-deletionLedgerGrace))
	if pruneErr != nil {
		pruneErr = fmt.Errorf("prune blob deletion ledger: %w", pruneErr)
	} else if pruned > 0 {
		fmt.Printf("forgot %d blob deletion records nothing references any more\n", pruned)
	}

	if storageFailures > 0 {
		return errors.Join(
			fmt.Errorf("%d blobs failed to delete from storage; see the logged errors above", storageFailures),
			pruneErr)
	}
	return pruneErr
}

// gcStaging collects tmp/uploads/ objects abandoned by interrupted LFS
// uploads. A signed PUT now lands in staging, not lfs/ directly; only a
// successful verify (which checks the transferred size) promotes it into
// lfs/ via a server-side copy. A client that never verifies -- it crashed,
// the connection dropped, the user gave up -- leaves its bytes sitting
// under tmp/uploads/ forever unless something removes them, and with
// datasets running past 10GB per file that adds up fast.
//
// Unlike gcLFS there is no lfs_objects row to consult and nothing to lock:
// no table records a staging object at all, so there is no reference count
// to check and no way to distinguish "abandoned" from "still uploading"
// except elapsed time. That is the same inference gcBlobs makes for
// blobs/, and stagingGrace plays the role blobGrace does there -- except it
// has to clear a known floor (the longest a client may still legitimately
// be using a signed URL) rather than an estimate of how long a push takes, so
// it is derived from the configured ceiling instead of picked directly.
func gcStaging(ctx context.Context, obj gcStorage, signedURLMaxTTL time.Duration, execute bool) error {
	objects, err := obj.List(ctx, storage.LFSStagingPrefix)
	if err != nil {
		return fmt.Errorf("list staging objects: %w", err)
	}

	cutoff := time.Now().Add(-stagingGrace(signedURLMaxTTL))
	orphaned := make([]storage.ObjectInfo, 0, len(objects))
	var totalBytes int64
	for _, o := range objects {
		if !o.Updated.Before(cutoff) {
			continue
		}
		orphaned = append(orphaned, o)
		totalBytes += o.Size
		fmt.Printf("orphaned upload %s  %d bytes\n", o.Key, o.Size)
	}
	fmt.Printf("%d of %d staging objects are orphaned (%d bytes total)\n", len(orphaned), len(objects), totalBytes)

	if !execute {
		fmt.Println("dry run: nothing deleted. Re-run with --yes to delete these objects.")
		return nil
	}

	var deleted int
	var deletedBytes int64
	var storageFailures int
	for _, o := range orphaned {
		if err := obj.Delete(ctx, o.Key); err != nil {
			slog.Error("gc: delete failed, leaving the staging object for a later retry", "key", o.Key, "error", err)
			storageFailures++
			continue
		}
		deleted++
		deletedBytes += o.Size
	}
	fmt.Printf("deleted %d of %d orphaned staging objects (%d bytes)\n", deleted, len(orphaned), deletedBytes)
	if storageFailures > 0 {
		return fmt.Errorf("%d staging objects failed to delete from storage; see the logged errors above", storageFailures)
	}
	return nil
}
