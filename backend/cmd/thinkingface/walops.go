// Operational subcommands for the Continuity migration
// (docs/dev/continuity-design.md §10, §15):
//
//	wal-seed    Phase 3 — create WAL indexes for repositories that predate it
//	wal-verify  Phase 3 — materialise every WAL into a scratch dir and compare
//	            it against the on-disk repository
//	compact     Phase 6 — fold long WALs into a base snapshot, then collect
//	            orphaned packs past the safety age
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// gcOrphanAge is the §10 grace period: packs the index no longer references
// are deleted only once they are old enough that no instance can still be
// materialising from an index that referenced them.
const gcOrphanAge = 24 * time.Hour

func runWALSeed(ctx context.Context, db *store.Store, obj storage.Storage, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("wal-seed", flag.ContinueOnError)
	force := fs.Bool("force", false, "replace an existing index (dangerous: discards the WAL's view)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	git := gitrepo.NewManager(cfg.GitRoot)

	return forEachRepo(ctx, db, func(ref store.RepoRef) error {
		dir := git.Dir(ref.StoragePath)
		if _, err := os.Stat(dir); err != nil {
			slog.Warn("wal-seed: no git directory, skipping", "repo", refName(ref))
			return nil
		}
		seeded, err := wal.Seed(ctx, obj, dir, ref.StoragePath, *force)
		if err != nil {
			return fmt.Errorf("seed %s: %w", refName(ref), err)
		}
		if seeded {
			slog.Info("wal-seed: seeded", "repo", refName(ref))
		} else {
			slog.Info("wal-seed: index already present, skipped", "repo", refName(ref))
		}
		return nil
	})
}

func runWALVerify(ctx context.Context, db *store.Store, obj storage.Storage, cfg *config.Config, args []string) error {
	git := gitrepo.NewManager(cfg.GitRoot)
	scratchRoot, err := os.MkdirTemp("", "tf-wal-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratchRoot)

	failures := 0
	err = forEachRepo(ctx, db, func(ref store.RepoRef) error {
		pdDir := git.Dir(ref.StoragePath)
		if _, err := os.Stat(pdDir); err != nil {
			slog.Warn("wal-verify: no git directory, skipping", "repo", refName(ref))
			return nil
		}
		scratch := filepath.Join(scratchRoot, filepath.FromSlash(ref.StoragePath)+".git")
		report, err := wal.Verify(ctx, obj, scratch, pdDir, ref.StoragePath)
		if err != nil {
			return fmt.Errorf("verify %s: %w", refName(ref), err)
		}
		if report.Match {
			slog.Info("wal-verify: match", "repo", refName(ref), "generation", report.Generation)
			return nil
		}
		failures++
		slog.Error("wal-verify: MISMATCH", "repo", refName(ref), "reason", report.Reason,
			"missing", report.RefsMissing, "extra", report.RefsExtra, "differ", report.RefsDiffer)
		return nil
	})
	if err != nil {
		return err
	}
	if failures > 0 {
		return fmt.Errorf("wal-verify: %d repositories diverge (run wal-seed --force after inspecting)", failures)
	}
	slog.Info("wal-verify: all repositories match")
	return nil
}

func runCompact(ctx context.Context, db *store.Store, obj storage.Storage, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	threshold := fs.Int("threshold", wal.DefaultCompactionThreshold, "compact repositories with more WAL entries than this")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// One work directory per repository, reused across runs when the job's
	// filesystem persists and rebuilt from the WAL when it does not — exactly
	// the contract wal.Compact documents.
	//
	// This reuses ViewerCacheDir's directory purely as scratch space on the
	// memory-backed filesystem; the parquet viewer itself no longer caches
	// anything there (it reads via storage range requests).
	workRoot := filepath.Join(cfg.ViewerCacheDir, "compact-work")

	return forEachRepo(ctx, db, func(ref store.RepoRef) error {
		work := filepath.Join(workRoot, filepath.FromSlash(ref.StoragePath)+".git")
		res, err := wal.CompactAndSweep(ctx, obj, work, ref.StoragePath, *threshold, gcOrphanAge)
		switch {
		case errors.Is(err, wal.ErrIndexMissing):
			// Loud, and not fatal to the rest of the run: this repository's
			// packs are the only way its index comes back
			// (docs/dev/wal-index-recovery.md), so its sweep stops here — but
			// the other repositories still deserve their maintenance.
			slog.Error("compact: index missing, orphan sweep skipped to preserve the recovery material",
				"repo", refName(ref), "recovery", "docs/dev/wal-index-recovery.md", "error", err)
			return nil
		case err != nil:
			return fmt.Errorf("compact %s: %w", refName(ref), err)
		}
		if res.Raced {
			slog.Info("compact: lost to a concurrent push, will retry next run", "repo", refName(ref))
		}
		if len(res.Deleted) > 0 {
			// "protected" is the count the pre-run index shielded — the packs
			// this run superseded, plus whatever else it still named. They go
			// on the next run; see wal.CompactAndSweep for why that one run of
			// grace is what invariant 3 of §5 needs.
			slog.Info("compact: collected orphaned packs", "repo", refName(ref),
				"count", len(res.Deleted), "protected", res.Protected)
		}
		return nil
	})
}

func forEachRepo(ctx context.Context, db *store.Store, fn func(store.RepoRef) error) error {
	refs, err := db.AllRepoRefs(ctx)
	if err != nil {
		return fmt.Errorf("list repositories: %w", err)
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(ref); err != nil {
			return err
		}
	}
	return nil
}

func refName(ref store.RepoRef) string {
	return ref.Kind + "/" + ref.Namespace + "/" + ref.Name
}
