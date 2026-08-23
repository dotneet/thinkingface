// WAL wiring for the write paths (docs/dev/continuity-design.md §6, §7, §15): the
// HF commit API and the web edit endpoint funnel through commitThroughWAL, so
// a server-side commit obeys the same acknowledgement rule as a git push —
// off: disk only; shadow: mirror best-effort; authoritative: no 200 without a
// durable WAL entry and a won CAS.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// errWALConflict is returned when an authoritative commit keeps losing its
// branch to concurrent pushes; handlers map it to 409.
var errWALConflict = errors.New("api: branch moved concurrently, retry")

// commitThroughWAL builds a server-side commit and records it in the WAL
// according to the configured mode. It owns the retry loop of §7: in
// authoritative mode a lost CAS on the same branch means the local branch
// head is stale, so — when retryOnStale is true — the commit is rebuilt on
// the fresh head rather than force-published over somebody else's push.
//
// retryOnStale must be false for callers with their own optimistic
// concurrency story: the web edit endpoint validates a base_oid before
// committing, and silently rebuilding on a moved head would overwrite the
// concurrent change that validation exists to catch. Those callers get
// errWALConflict on the first stale and surface it as 409.
//
// Every failure path after Commit rolls the local ref back to oldHash. Commit
// advances the on-disk ref before the CAS runs, and a ref the WAL never
// accepted must not survive: it would be served to readers, and — because the
// index generation did not change — no materialisation would ever repair it,
// leaving the branch permanently rejecting commits as stale.
//
// It (re-)opens the repository itself: in authoritative mode EnsureLocal may
// rebuild the directory, which invalidates any *gitrepo.Repo opened earlier.
func (s *Server) commitThroughWAL(ctx context.Context, repo *store.Repo, req gitrepo.CommitRequest, retryOnStale bool) (newHash, oldHash plumbing.Hash, err error) {
	const maxAttempts = 3
	dir := s.git.Dir(repo.StoragePath)
	mode := s.cfg.WALMode

	for attempt := 0; attempt < maxAttempts; attempt++ {
		gitRepo, err := s.git.Open(repo.StoragePath)
		if err != nil {
			return plumbing.ZeroHash, plumbing.ZeroHash, err
		}
		newHash, oldHash, err = gitRepo.Commit(req)
		if err != nil {
			return newHash, oldHash, err
		}

		update := []wal.RefUpdate{{
			Ref: "refs/heads/" + req.Branch,
			Old: oldHash.String(),
			New: newHash.String(),
		}}

		switch mode {
		case "off":
			return newHash, oldHash, nil
		case "shadow":
			// Disk is authoritative: a WAL failure is logged, never surfaced.
			if werr := wal.ShadowPush(ctx, s.storage, dir, repo.StoragePath, update); werr != nil {
				slog.Warn("wal shadow write failed for api commit",
					"repo", repo.FullName(), "branch", req.Branch, "error", werr)
			}
			return newHash, oldHash, nil
		default: // authoritative
			werr := wal.AuthoritativePush(ctx, s.storage, dir, repo.StoragePath, update)
			if werr == nil {
				// Best-effort: lets the next materialisation skip re-applying
				// the entry we just uploaded.
				if aerr := s.git.AdoptLocal(ctx, repo.StoragePath); aerr != nil {
					slog.Warn("wal adopt after api commit", "repo", repo.FullName(), "error", aerr)
				}
				return newHash, oldHash, nil
			}

			// The WAL refused or was unreachable: the local ref advance must
			// not outlive this attempt (see the function comment).
			if rerr := gitRepo.ResetBranch(req.Branch, oldHash); rerr != nil {
				slog.Error("roll back local ref after failed WAL write",
					"repo", repo.FullName(), "branch", req.Branch, "error", rerr)
			}

			switch {
			case errors.Is(werr, wal.ErrStaleRef) && retryOnStale:
				// A concurrent push moved the branch. Materialise the fresh
				// state and rebuild the commit on top of it (§7 step 5). The
				// rolled-back commit's objects become unreferenced garbage,
				// not corruption.
				if merr := s.git.EnsureLocal(ctx, repo.StoragePath); merr != nil {
					return newHash, oldHash, merr
				}
				continue
			case errors.Is(werr, wal.ErrStaleRef), errors.Is(werr, wal.ErrRetryExhausted):
				// Both are contention, not faults: the client retries (409),
				// exactly as §7 specifies for attempts running out.
				return newHash, oldHash, errWALConflict
			default:
				// WAL unreachable: the commit must not be acknowledged
				// (§5 invariant 4).
				return newHash, oldHash, fmt.Errorf("record commit in WAL: %w", werr)
			}
		}
	}
	return newHash, oldHash, errWALConflict
}

// ensureRepoLocal materialises the on-disk copy before a git smart-HTTP
// operation touches it. A no-op unless the WAL is authoritative.
func (s *Server) ensureRepoLocal(ctx context.Context, repo *store.Repo) error {
	return s.git.EnsureLocal(ctx, repo.StoragePath)
}

// adoptAfterPush stamps the local state file when disk and index agree, so
// the next read does not re-apply the entry this push just uploaded. It must
// run before anything re-opens the repository (HeadsAfterPush included), or
// that open's materialisation downloads the entry pack this very push
// produced. Shadow mode never materialises, so only authoritative benefits.
func (s *Server) adoptAfterPush(ctx context.Context, repo *store.Repo) {
	if s.cfg.WALMode != "authoritative" {
		return
	}
	if err := s.git.AdoptLocal(ctx, repo.StoragePath); err != nil {
		slog.Warn("wal adopt after push", "repo", repo.FullName(), "error", err)
	}
}

// purgeWAL removes a deleted repository's WAL objects. Best-effort: the
// repository row and git directory are already gone, and an orphaned WAL
// prefix costs storage, not correctness.
func (s *Server) purgeWAL(ctx context.Context, repo *store.Repo) {
	if s.cfg.WALMode == "off" {
		return
	}
	if err := wal.Purge(ctx, s.storage, repo.StoragePath); err != nil &&
		!errors.Is(err, storage.ErrNotFound) {
		slog.Warn("purge wal after repo delete", "repo", repo.FullName(), "error", err)
	}
}
