package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"time"

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

// runGC reclaims both content-addressed layers: lfs/, whose objects are
// tracked in lfs_objects and referenced through repo_lfs_objects, and blobs/,
// which is tracked nowhere and referenced by repo_files.blob_sha. Neither
// shrinks on its own -- repositories get deleted and files get overwritten,
// but a content-addressed key is immutable and may be shared by any number of
// repositories, so no push or delete may remove one.
//
// Both passes report first and only delete with --yes (or --dry-run=false).
func runGC(ctx context.Context, db gcDB, obj gcStorage, args []string) error {
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
	return gcBlobs(ctx, db, obj, execute)
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
