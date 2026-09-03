package gitrepo

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// PublishBlob makes sure the blob hash names is at its content-addressed key
// in object storage (storage.BlobKey) and returns that key. It is the single
// write path of the blobs/ layer: the sync worker runs it for every file of
// every pushed ref, and the readers that may get ahead of the worker -- the
// parquet viewer on an arbitrary revision, the experiment indexer -- run the
// same function to repair a gap rather than a copy of it.
//
// Idempotent: a blob already there costs one Stat. The content type is
// deliberately always octet-stream -- the key says nothing about the path the
// bytes arrived on, and the same object may be a .json in one repository and
// a .txt in another.
//
// **The Stat skip means a re-reference leaves no trace on the object.** A
// second repository committing content that has been in blobs/ for a year
// does not move its Updated timestamp, so nothing about the object says it
// has just become live again -- which is precisely why `thinkingface gc`'s
// blob pass cannot be an age judgement (its 24h grace only ever protected
// objects that were freshly *written*). The collector guards the gap on the
// database side instead, with a blob_deletions row it holds while it
// re-checks repo_files and that store.RepairDeletedBlobs takes to put the
// bytes back; see store.DeleteOrphanedBlob. Rewriting the object here to
// refresh its timestamp was the alternative and is deliberately not done: it
// would copy whole model files on every push that mentions them, and would
// still lose to a collector that had already listed the bucket.
func (r *Repo) PublishBlob(ctx context.Context, obj storage.Storage, hash plumbing.Hash) (string, error) {
	key := storage.BlobKey(hash.String())
	_, err := obj.Stat(ctx, key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return "", fmt.Errorf("stat %s: %w", key, err)
	}
	rc, _, err := r.BlobReader(hash)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if err := obj.Put(ctx, key, rc, "application/octet-stream"); err != nil {
		return "", fmt.Errorf("write %s: %w", key, err)
	}
	return key, nil
}
