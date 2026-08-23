package wal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dotneet/thinkingface/backend/internal/storage"
)

// Seed creates a WAL index from an existing on-disk repository — the migration
// step of §15 Phase 3, and the repair for a repository that Verify found
// diverged.
//
// The snapshot goes into base/ rather than entries/: it is a complete,
// self-contained picture of the repository at one instant, which is exactly
// what compaction produces, and putting it there means a materialising instance
// applies one pack instead of replaying a history that never happened.
//
// Without force it refuses to touch an existing index and reports seeded=false.
// That default matters: seeding over a live index would silently discard
// whatever the WAL had accepted since, and by Phase 4 that means acknowledged
// pushes. force=true is the deliberate repair, and it is still a CAS against
// the generation just read, so a push landing mid-seed makes it fail rather
// than roll the repository back.
//
// The reconstructed index carries no symbolic HEAD; §9's alignHEAD rebuilds it
// by rule, with the same caveat recorded there.
func Seed(ctx context.Context, st storage.Storage, gitDir, storagePath string, force bool) (bool, error) {
	idx, gen, err := ReadIndex(ctx, st, storagePath)
	if err != nil {
		return false, err
	}
	if gen != 0 && !force {
		return false, nil
	}

	refs, err := listRefs(ctx, gitDir)
	if err != nil {
		return false, fmt.Errorf("seed %s: %w", storagePath, err)
	}

	next := &Index{
		Version: IndexVersion,
		Refs:    refs,
		// Entry numbering continues across a re-seed. Restarting it would let a
		// future entry collide, key for key, with an orphan left behind by the
		// index being replaced (§3 relies on seq+ULID being unique per push).
		Seq: idx.Seq,
	}

	if len(refs) > 0 {
		base, err := uploadSeedBase(ctx, st, gitDir, storagePath, refs)
		if err != nil {
			return false, err
		}
		next.Base = base
	}
	// A repository with no refs seeds to an index with no base and no entries.
	// That is a real state — a freshly created repository — and it must be
	// representable, or the first push would have to invent the index instead.

	newGen, err := PutIndex(ctx, st, storagePath, gen, next)
	if err != nil {
		if errors.Is(err, storage.ErrPreconditionFailed) {
			return false, fmt.Errorf("seed %s: index changed under us (orphan base %s): %w",
				storagePath, next.Base, err)
		}
		return false, err
	}

	// The repository we seeded from now holds exactly what the index describes,
	// so record that instead of making the next Materialize rebuild it from the
	// base we just uploaded. AdoptIfConverged re-checks the refs itself, so a
	// commit landing between listRefs and here leaves the state unwritten
	// rather than wrong.
	if err := AdoptIfConverged(ctx, st, gitDir, storagePath); err != nil {
		slog.Warn("wal seed: adopt local state", "repo", storagePath, "error", err)
	}
	slog.Info("wal seeded", "repo", storagePath,
		"generation", newGen, "refs", len(refs), "base", next.Base)
	return true, nil
}

// uploadSeedBase packs everything reachable from every ref with nothing
// excluded, which is what makes the result self-contained: a fresh cache with
// no entries to fall back on has to be rebuildable from this one pack alone.
func uploadSeedBase(ctx context.Context, st storage.Storage, gitDir, storagePath string, refs map[string]string) (string, error) {
	rc, err := PackObjects(ctx, gitDir, sortedRefValues(refs), nil)
	if err != nil {
		return "", err
	}
	defer rc.Close()

	br := bufio.NewReader(rc)
	count, err := packObjectCount(br)
	if err != nil {
		return "", err
	}
	if count == 0 {
		// Refs that resolve to no objects at all should be impossible, but an
		// empty base would be indistinguishable from "no base" to Materialize
		// while still costing a fetch on every rebuild.
		return "", nil
	}
	return UploadBase(ctx, st, storagePath, br)
}
