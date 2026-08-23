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
