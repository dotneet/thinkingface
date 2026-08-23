package wal

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// DefaultGCGracePeriod is the age a pack must reach before it may be collected
// (§10). It is not a tidiness knob: it is the window in which an instance that
// read an older index can still be applying the packs that index named.
const DefaultGCGracePeriod = 24 * time.Hour

// GCOrphans deletes packs no index revision needs any more — CAS losers whose
// upload completed before they lost the race (§13), and the base and entries a
// compaction superseded (§10 step 5) — once they are older than minAge.
//
// Two rules keep this from deleting live data, and the implementation is shaped
// around them rather than checking them afterwards:
//
//  1. **Listing happens before the index is read.** A pack uploaded after the
//     listing is not a candidate at all, so it cannot be mistaken for an orphan
//     during the window between its upload and the CAS that names it. Reading
//     the index first would invert this and delete exactly those packs.
//  2. **Age is the grace of invariant 3 (§5).** An instance that read the
//     previous index may still be materialising from it. minAge must exceed the
//     longest plausible materialisation; DefaultGCGracePeriod is that number.
//
// index.json is structurally out of reach: it lives under neither base/ nor
// entries/, so it is never listed as a candidate.
//
// Deletion is per object and best effort in the sense that a failure stops the
// sweep — the packs already deleted are returned so the caller can log what
// happened — but never in the sense of deleting something referenced.
func GCOrphans(ctx context.Context, st storage.Storage, storagePath string, minAge time.Duration) ([]string, error) {
	candidates, err := listPacks(ctx, st, storagePath)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Read the index *after* listing, and use nothing older: it must reflect at
	// least everything the listing saw (rule 1 above).
	idx, _, err := ReadIndex(ctx, st, storagePath)
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]bool, len(idx.Entries)+1)
	if idx.Base != "" {
		referenced[storage.WALKey(storagePath, idx.Base)] = true
	}
	for _, entry := range idx.Entries {
		referenced[storage.WALKey(storagePath, entry)] = true
	}

	cutoff := time.Now().Add(-minAge)
	deleted := make([]string, 0)
	for _, obj := range candidates {
		if referenced[obj.Key] || obj.Updated.After(cutoff) {
			continue
		}
		if err := st.Delete(ctx, obj.Key); err != nil {
			return deleted, fmt.Errorf("gc %s: delete %s: %w", storagePath, obj.Key, err)
		}
		deleted = append(deleted, obj.Key)
	}
	return deleted, nil
}

// listPacks enumerates both pack prefixes in one sorted slice, so the sweep
// order is deterministic and the returned list reads the same way every run.
func listPacks(ctx context.Context, st storage.Storage, storagePath string) ([]storage.ObjectInfo, error) {
	var out []storage.ObjectInfo
	for _, prefix := range []string{
		storage.WALBasePrefix(storagePath),
		storage.WALEntriesPrefix(storagePath),
	} {
		objs, err := st.List(ctx, prefix)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		out = append(out, objs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Purge removes a repository's entire WAL. It is the storage-side half of
// deleting a repository: leaving the index behind would let a recreated
// repository of the same name inherit the deleted one's refs and history.
//
// The index goes first on purpose. Between the two steps the repository reads
// as "never written through the WAL", which Materialize treats as "leave the
// local copy alone" — a harmless state — whereas removing the packs first would
// leave an index naming objects that no longer exist, which fails loudly and
// looks like corruption.
//
// Best effort: the first failure returns, and a later Purge (or age-based GC)
// finishes the job. A partly purged WAL costs storage, not correctness.
func Purge(ctx context.Context, st storage.Storage, storagePath string) error {
	if err := st.Delete(ctx, storage.WALIndexKey(storagePath)); err != nil {
		return fmt.Errorf("purge %s: delete index: %w", storagePath, err)
	}
	objs, err := st.List(ctx, storage.WALPrefix(storagePath))
	if err != nil {
		return fmt.Errorf("purge %s: %w", storagePath, err)
	}
	for _, obj := range objs {
		if err := st.Delete(ctx, obj.Key); err != nil {
			return fmt.Errorf("purge %s: delete %s: %w", storagePath, obj.Key, err)
		}
	}
	return nil
}
