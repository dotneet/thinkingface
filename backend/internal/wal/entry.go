package wal

import (
	"context"
	"fmt"
	"io"

	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/ulid"
)

// packContentType is what git itself uses for packfiles over HTTP.
const packContentType = "application/x-git-packfile"

// EntryName builds the index-relative name of a WAL entry (§3):
//
//	entries/{seq:06d}-{ulid}.pack
//
// Two entries may legitimately carry the same seq — a writer that lost the CAS
// has already uploaded its pack, and that orphan keeps its number. The ULID is
// what keeps the winner and the orphan from colliding on a key; the index, not
// the file name, decides which one counts.
func EntryName(seq int, id string) string {
	return fmt.Sprintf("entries/%06d-%s.pack", seq, id)
}

// BaseName builds the index-relative name of a compaction snapshot (§3).
func BaseName(id string) string { return "base/" + id + ".pack" }

// UploadEntry writes one push's pack and returns its index-relative name, ready
// to hand to UpdateIndex. It returns only after the object is fully durable:
// invariant 2 of §5 forbids referencing a pack from the index before then.
func UploadEntry(ctx context.Context, st storage.Storage, storagePath string, seq int, r io.Reader) (string, error) {
	rel := EntryName(seq, newULID())
	if err := st.Put(ctx, storage.WALKey(storagePath, rel), r, packContentType); err != nil {
		return "", fmt.Errorf("upload wal entry %s: %w", rel, err)
	}
	return rel, nil
}

// UploadBase writes a compaction snapshot and returns its index-relative name.
func UploadBase(ctx context.Context, st storage.Storage, storagePath string, r io.Reader) (string, error) {
	rel := BaseName(newULID())
	if err := st.Put(ctx, storage.WALKey(storagePath, rel), r, packContentType); err != nil {
		return "", fmt.Errorf("upload wal base %s: %w", rel, err)
	}
	return rel, nil
}

// newULID is kept as a thin alias so call sites and tests read as before.
func newULID() string { return ulid.New() }
