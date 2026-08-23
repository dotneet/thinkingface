package wal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// DefaultCompactionThreshold is the entry count above which materialisation
// starts paying for the WAL's length (§10).
const DefaultCompactionThreshold = 50

// ErrCompactionRaced means a push landed while the snapshot was being built, so
// the CAS was declined. §10 says not to retry: compaction has no deadline and
// pushes must not queue behind it. The next run picks the repository up again.
var ErrCompactionRaced = errors.New("wal: compaction lost the CAS to a concurrent write")

// NeedsCompaction is the selection rule of §10, kept next to the implementation
// so the scheduling job does not re-derive it.
func NeedsCompaction(ix *Index, maxEntries int) bool {
	if maxEntries <= 0 {
		maxEntries = DefaultCompactionThreshold
	}
	return len(ix.Entries) > maxEntries
}

// Compact folds base+entries into a single snapshot pack (§10):
//
//	materialize → repack -a -d → upload base → CAS(entries=[], refs unchanged)
//
// workDir is a scratch bare repository; it may be empty, stale, or absent.
// It must be dedicated to this one repository and this one caller: reusing a
// directory across repositories would smuggle one repository's objects into
// another's snapshot, and two concurrent Compact calls on the same directory
// corrupt it. The scheduling job owns that discipline (one directory per
// repository, runs serialised), exactly as gitrepo.Manager's per-repository
// lock does for the transport paths.
//
// The refs and seq written back come from the *materialised* generation, not
// from a fresh read: publishing refs newer than the pack we just built would
// point the index at objects the snapshot does not contain. The CAS is anchored
// to the same generation, so any push in between makes it fail rather than
// silently roll refs back.
//
// Superseded packs are never deleted here (invariant 3 of §5): an instance may
// still be materialising from the index this call replaced. They are logged and
// left for age-based GC.
func Compact(ctx context.Context, st storage.Storage, workDir, storagePath string) error {
	if err := Materialize(ctx, st, workDir, storagePath); err != nil {
		return fmt.Errorf("compact %s: materialize: %w", storagePath, err)
	}
	local, ok := readLocalState(workDir)
	if !ok {
		// Materialize returns without writing state when no index exists yet;
		// there is nothing to compact in that case.
		return nil
	}

	// --no-write-bitmap-index: repack.writeBitmaps defaults to true in a bare
	// repository, so without this every compaction computes a reachability
	// bitmap that is then thrown away -- only the .pack is uploaded, and a
	// materialising instance rebuilds the .idx with index-pack. Should clone
	// performance ever want bitmaps, they have to be built on the
	// materialising side, where they survive.
	if _, err := runGit(ctx, workDir, "repack", "-a", "-d", "--no-write-bitmap-index", "--depth=50", "--window=250"); err != nil {
		return fmt.Errorf("compact %s: %w", storagePath, err)
	}
	packPath, err := solePack(workDir)
	if err != nil {
		return fmt.Errorf("compact %s: %w", storagePath, err)
	}
	if packPath == "" {
		// A repository with no objects at all (index exists, refs empty).
		// There is nothing to snapshot, and an empty base would only add work
		// to every later materialisation.
		return nil
	}

	f, err := os.Open(packPath)
	if err != nil {
		return fmt.Errorf("open repacked pack: %w", err)
	}
	base, err := UploadBase(ctx, st, storagePath, f)
	_ = f.Close()
	if err != nil {
		return err
	}

	next := &Index{
		Version: IndexVersion,
		Seq:     local.Seq, // entry numbering does not restart; orphans keep their names distinct
		Base:    base,
		Entries: nil,
		Refs:    local.Refs,
	}
	newGen, err := PutIndex(ctx, st, storagePath, local.Generation, next)
	if err != nil {
		if errors.Is(err, storage.ErrPreconditionFailed) {
			// The freshly uploaded base is now an orphan. Harmless: nothing
			// references it, and GC collects it on age.
			return fmt.Errorf("%w (%s, orphan base %s)", ErrCompactionRaced, storagePath, base)
		}
		return err
	}

	superseded := make([]string, 0, len(local.Applied)+1)
	if local.Base != "" {
		superseded = append(superseded, storage.WALKey(storagePath, local.Base))
	}
	for _, e := range local.Applied {
		superseded = append(superseded, storage.WALKey(storagePath, e))
	}
	slog.Info("wal compaction done", "repo", storagePath,
		"base", base, "folded_entries", len(local.Applied), "superseded", superseded)

	// Keep the scratch copy usable for the next run: it holds exactly the new
	// base's objects, and newGen is the generation of *this* write — reported
	// by the CAS itself, never read back, so an interleaved push cannot trick
	// the state file into claiming a generation whose entries were not
	// applied. If a push does land right after, the state is merely stale
	// (its generation no longer matches the index), which the next
	// Materialize resolves incrementally.
	if err := writeLocalState(workDir, newGen, next); err != nil {
		slog.Warn("wal compaction: refresh local state", "repo", storagePath, "error", err)
	}
	return nil
}

// solePack returns the single pack left by `repack -a -d`, or "" when the
// repository holds no objects. More than one means something (a .keep pack, a
// concurrent writer) broke the assumption that the snapshot is self-contained,
// and uploading an arbitrary one would lose objects.
func solePack(gitDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(gitDir, "objects", "pack", "*.pack"))
	if err != nil {
		return "", fmt.Errorf("scan pack dir: %w", err)
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("expected exactly one pack after repack, found %d", len(matches))
	}
}
