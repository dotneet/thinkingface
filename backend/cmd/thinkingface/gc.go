package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
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
const blobGrace = 24 * time.Hour

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
	ListReferencedBlobSHAs(ctx context.Context) (map[string]bool, error)
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
// All three passes report first and only delete with --yes (or --dry-run=false).
func runGC(ctx context.Context, db gcDB, obj gcStorage, signedURLMaxTTL time.Duration, args []string) error {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", true, "report orphaned objects without deleting anything (default)")
	yes := fs.Bool("yes", false, "actually delete the orphaned objects from storage and postgres")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Either flag on its own is enough to authorise a real delete: --yes is
	// the explicit confirmation, and --dry-run=false says the same thing the
	// other way round. The default (dry-run=true, yes=false) only reports.
	execute := *yes || !*dryRun

	if err := gcLFS(ctx, db, obj, execute); err != nil {
		return err
	}
	if err := gcBlobs(ctx, db, obj, execute); err != nil {
		return err
	}
	return gcStaging(ctx, obj, signedURLMaxTTL, execute)
}

// gcLFS collects lfs/ objects that no repository links to any more.
//
// The initial scan is a snapshot: between that listing and each delete, a
// push or LFS verify can attach a previously orphaned oid to a repository.
// Actual deletion therefore goes through DeleteOrphanedLFSObject, which
// re-checks under a row lock and only then removes storage and the row.
// Storage goes first inside that lock: if it fails, the row stays so a later
// run can retry. A concurrent upload batch that Stat'ed a hit before
// waiting on that lock is told to re-upload via RecordLFSObject's
// confirmPresent check (ErrLFSObjectGone).
func gcLFS(ctx context.Context, db gcDB, obj gcStorage, execute bool) error {
	all, err := db.ListLFSObjects(ctx)
	if err != nil {
		return fmt.Errorf("list lfs objects: %w", err)
	}
	referenced, err := db.ListReferencedLFSOIDs(ctx)
	if err != nil {
		return fmt.Errorf("list referenced lfs oids: %w", err)
	}
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

// gcBlobs collects blobs/ objects no indexed revision carries any more.
//
// There is no row to lock here, so the reference set is read after the (slow)
// bucket listing: a push whose repo_files rows commit while the listing runs
// is seen. The remaining window -- a push landing between that read and the
// delete, on a sha that had been orphaned for over a day -- is what blobGrace
// cannot cover; running the collector against a quiet instance is the answer,
// and a lost blob is re-published by the next push to any ref carrying it.
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
	var storageFailures int
	for _, o := range orphaned {
		if err := obj.Delete(ctx, o.Key); err != nil {
			slog.Error("gc: delete failed, leaving the blob for a later retry", "key", o.Key, "error", err)
			storageFailures++
			continue
		}
		deleted++
		deletedBytes += o.Size
	}
	fmt.Printf("deleted %d of %d orphaned blobs (%d bytes)\n", deleted, len(orphaned), deletedBytes)
	if storageFailures > 0 {
		return fmt.Errorf("%d blobs failed to delete from storage; see the logged errors above", storageFailures)
	}
	return nil
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
