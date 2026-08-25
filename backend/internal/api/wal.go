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
	"net/http"

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

// writeCommitError answers the failures every commitThroughWAL caller shares,
// and reports whether it wrote a response. err == nil is the only false.
//
// The four outcomes:
//
//   - gitrepo.StaleParentError -> 412. The request carried a precondition of
//     its own (huggingface_hub's `parent_commit`, an If-Match by another
//     name) and the branch is not where the caller believed it was. Retrying
//     the identical request can only fail again -- they have to look at what
//     landed in between and decide whether their change still applies. 409 is
//     wrong for exactly that reason: it means "retryable contention"
//     everywhere else in this API. huggingface_hub raises HfHubHTTPError for
//     both and reads the sentence out of X-Error-Message either way, so
//     nothing is lost on the client.
//   - gitrepo.StalePathError -> 409. The web editor's base_oid check,
//     repeated inside Commit under the mutex that picks the parent. The
//     caller re-reads the file and retries with the oid it now holds.
//   - errWALConflict -> 409. Another writer won a race this request could
//     still win: retry as sent.
//   - anything else -> 500.
//
// Only two of the four can reach any one caller, and which two follows from
// what that caller sent rather than from a policy: a commit with no
// Preconditions can never produce StalePathError, and one with no
// ParentCommit can never produce StaleParentError. Answering all four in one
// place is what keeps that a property of the request instead of a per-handler
// decision -- which is what the five hand-written copies of this had drifted
// into, where the HF commit endpoint answered the parent case, the three web
// endpoints answered the path case, and the upload endpoint answered neither,
// with nothing in the code saying whether that was intended.
//
// The message is rebuilt from the error rather than from the handler's own
// variables, so the branch and path it names are the ones Commit actually
// refused on.
//
// what names the operation in the retry sentence ("edit", "deletion",
// "rename", "upload", "commit").
func writeCommitError(w http.ResponseWriter, err error, what string) bool {
	if err == nil {
		return false
	}
	var staleParent *gitrepo.StaleParentError
	if errors.As(err, &staleParent) {
		at := staleParent.Actual
		if at == "" {
			at = "no commits"
		}
		writeError(w, http.StatusPreconditionFailed, "stale_parent",
			"parentCommit "+staleParent.Expected+" is not the head of "+staleParent.Branch+
				" (now at "+at+"); fetch the branch and rebuild the commit on top of it")
		return true
	}
	var stalePath *gitrepo.StalePathError
	if errors.As(err, &stalePath) {
		writeError(w, http.StatusConflict, "conflict",
			stalePath.Path+" changed concurrently; re-read the file and retry with its current oid")
		return true
	}
	if errors.Is(err, errWALConflict) {
		writeError(w, http.StatusConflict, "conflict", "branch changed concurrently; retry the "+what)
		return true
	}
	internalError(w, "create commit", err)
	return true
}

// createRefThroughWAL creates refName pointing at target, obeying the same
// acknowledgement rule commitThroughWAL does. It is the branch/tag API's only
// write path: a ref created straight on disk would be invisible to the WAL,
// and in authoritative mode the next materialisation -- a new Cloud Run
// instance, a cache rebuild -- would simply delete it again, because the index
// is what refs are reconstructed from (§9).
//
// An existing ref is gitrepo.ErrRefExists and never a force: the caller maps
// that to the 409 huggingface_hub's `exist_ok=True` swallows.
func (s *Server) createRefThroughWAL(ctx context.Context, repo *store.Repo, refName string, target plumbing.Hash) error {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		return err
	}
	return s.createRefOn(ctx, repo, gitRepo, refName, target)
}

// createTagRefThroughWAL creates refs/tags/{tag}. An empty message makes a
// lightweight tag pointing straight at commit; otherwise an annotated tag
// object is written first and the ref names that, the way `git tag -m` does.
//
// The single Open is the reason this exists instead of the caller writing the
// tag object and then calling createRefThroughWAL. A freshly written tag object
// is *loose, local, and not yet in the WAL*: it lives only inside the
// materialisation that produced it. Every gitrepo.Manager.Open runs EnsureLocal,
// and in authoritative mode that can rebuild the directory from the index --
// wal.Materialize's rebuild path starts with os.RemoveAll(gitDir), reached
// after a compaction changed the base, after maybeEvict reclaimed the
// directory, or whenever the local state file cannot be trusted. An Open
// between the two writes therefore has a window in which the tag object is
// deleted before CreateRef and pack-objects ever look for it: the entry pack
// fails, the client gets a 500, and the tag it asked for was perfectly valid.
// Doing both on one handle removes the window rather than narrowing it.
//
// It returns what the ref ended up pointing at (the commit, or the tag object).
func (s *Server) createTagRefThroughWAL(ctx context.Context, repo *store.Repo, tag string,
	commit plumbing.Hash, message string, tagger gitrepo.Signature,
) (plumbing.Hash, error) {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	target := commit
	if message != "" {
		target, err = gitRepo.WriteTagObject(tag, commit, message, tagger)
		if err != nil {
			return plumbing.ZeroHash, err
		}
	}
	if err := s.createRefOn(ctx, repo, gitRepo, gitrepo.TagRef(tag), target); err != nil {
		return plumbing.ZeroHash, err
	}
	return target, nil
}

// createRefOn is the shared body: create the ref on an already-open handle and
// record it, rolling the local ref back if the WAL refuses. Callers own the
// Open so that everything one write needs -- a tag object included -- happens
// against a single materialisation.
func (s *Server) createRefOn(ctx context.Context, repo *store.Repo, gitRepo *gitrepo.Repo,
	refName string, target plumbing.Hash,
) error {
	if err := gitRepo.CreateRef(refName, target); err != nil {
		return err
	}
	update := wal.RefUpdate{Ref: refName, Old: "", New: target.String()}
	return s.recordRefUpdate(ctx, repo, update, func() error {
		_, derr := gitRepo.DeleteRef(refName)
		return derr
	})
}

// deleteRefThroughWAL removes refName and records the removal, returning the
// object it pointed at. A missing ref is gitrepo.ErrRefNotFound.
func (s *Server) deleteRefThroughWAL(ctx context.Context, repo *store.Repo, refName string) (plumbing.Hash, error) {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	old, err := gitRepo.DeleteRef(refName)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	update := wal.RefUpdate{Ref: refName, Old: old.String(), New: ""}
	if err := s.recordRefUpdate(ctx, repo, update, func() error {
		return gitRepo.CreateRef(refName, old)
	}); err != nil {
		return plumbing.ZeroHash, err
	}
	return old, nil
}

// recordRefUpdate mirrors commitThroughWAL's mode handling for an update that
// carries no new commit. The differences from a commit are both deliberate:
//
//   - there is no retry-on-stale loop. A commit that loses the CAS can be
//     rebuilt on the fresh head and still mean what the client asked for; a
//     ref creation cannot -- losing the CAS means somebody else already
//     created (or moved, or deleted) that exact ref, which is a conflict the
//     client has to see, not paper over.
//   - the entry pack is usually empty, because the target commit is normally
//     already in the WAL. wal.packAndUpload notices that and skips the upload,
//     so a branch pointing at existing history costs one index CAS and no
//     object bytes at all.
//
// rollback undoes the local ref change on any failure, for the reason
// commitThroughWAL rolls back a commit: a ref the WAL never accepted must not
// be served to readers, and the index generation did not change, so no
// materialisation would ever repair it.
func (s *Server) recordRefUpdate(ctx context.Context, repo *store.Repo, update wal.RefUpdate, rollback func() error) error {
	dir := s.git.Dir(repo.StoragePath)
	updates := []wal.RefUpdate{update}

	switch s.cfg.WALMode {
	case "off":
		return nil
	case "shadow":
		// Disk is authoritative: a WAL failure is logged, never surfaced.
		if werr := wal.ShadowPush(ctx, s.storage, dir, repo.StoragePath, updates); werr != nil {
			slog.Warn("wal shadow write failed for api ref update",
				"repo", repo.FullName(), "ref", update.Ref, "error", werr)
		}
		return nil
	default: // authoritative
		werr := wal.AuthoritativePush(ctx, s.storage, dir, repo.StoragePath, updates)
		if werr == nil {
			if aerr := s.git.AdoptLocal(ctx, repo.StoragePath); aerr != nil {
				slog.Warn("wal adopt after api ref update", "repo", repo.FullName(), "error", aerr)
			}
			return nil
		}
		if rerr := rollback(); rerr != nil {
			slog.Error("roll back local ref after failed WAL write",
				"repo", repo.FullName(), "ref", update.Ref, "error", rerr)
		}
		switch {
		case errors.Is(werr, wal.ErrStaleRef), errors.Is(werr, wal.ErrRetryExhausted):
			return errWALConflict
		default:
			return fmt.Errorf("record ref update in WAL: %w", werr)
		}
	}
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
