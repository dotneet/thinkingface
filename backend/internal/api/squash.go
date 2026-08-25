// The HuggingFace-compatible history squash: HfApi.super_squash_history().
//
// This is an HF-compatible endpoint, so the external protocol -- not
// internal/apitypes -- is the contract: the request body and the path shape
// below are what huggingface_hub actually sends (see docs/dev/api-contract.md),
// and are deliberately outside `make gen-types`.

package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// handleHFSuperSquash answers POST /api/{type}s/{ns}/{name}/super-squash/{branch}
// with a `{"message": "..."}` body -- exactly what HfApi.super_squash_history
// sends (the message is never empty on that path: the client fills in
// "Super-squash branch '<branch>' using huggingface_hub" when the caller gives
// none).
//
// It matters more here than on the Hub. This server is built for repositories
// of multi-gigabyte checkpoints, where every superseded revision keeps its
// blobs alive forever: without a way to collapse history, the only way to stop
// paying for a hundred rewrites of one weights file was to delete the
// repository and push it again.
//
// Authorisation is loadRepoForWrite, the gate every content-changing endpoint
// shares -- a write-scoped token held by someone with at least `write` in the
// namespace -- and it refuses an archived repository. Discarding history is
// the most destructive write this API offers, so it is nobody's read
// operation, and "archived" has to mean it too: a repository frozen read-only
// must not be able to lose its history.
func (s *Server) handleHFSuperSquash(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.loadHFRepoForWrite(w, r)
	if !ok {
		return
	}
	// refParam, not revParam: this names a branch to rewrite, so it has to be
	// a valid ref name, and huggingface_hub percent-encodes it
	// (`quote(branch, safe="")`) exactly as it does for create_branch.
	branch, ok := refParam(w, r, "branch", "branch")
	if !ok {
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if !decodeOptionalJSON(w, r, maxMetaBody, &req, "request body must be JSON with an optional message field") {
		return
	}

	author := commitAuthor(r.Context())

	newHash, oldHash, err := s.squashThroughWAL(r.Context(), repo, branch, req.Message, author)
	if err != nil {
		// writeRefError already maps gitrepo.ErrRefNotFound to the
		// RevisionNotFound 404 huggingface_hub raises RevisionNotFoundError
		// for -- which is what it documents for a branch that cannot be
		// squashed because it is not there -- and WAL contention to a 503
		// that says to retry.
		writeRefError(w, "branch", branch, err)
		return
	}

	if newHash != oldHash {
		// The same job a push to this branch schedules, for the same reason
		// handleHFCreateBranch schedules one: the file index is keyed by
		// (repo_id, ref, path) and is rebuilt from the ref's tree, and the
		// repository row's head commit is refreshed from it. The tree did not
		// change, but the commit it hangs from did, and the index would
		// otherwise still name a commit that is now unreachable. It also
		// fires the repo.push webhook, which is why squashing needs no
		// webhook event of its own.
		//
		// Logged rather than surfaced: the new head is already durable, so
		// failing the request would tell the client the opposite of the
		// truth. The job is re-created by the next push to the branch.
		if err := s.sync.Enqueue(r.Context(), repo.ID, branch, oldHash.String(), newHash.String()); err != nil {
			slog.Error("schedule sync after super-squash",
				"repo", repo.FullName(), "branch", branch, "error", err)
		}
	}

	// huggingface_hub reads nothing but the status (super_squash_history
	// returns None). The body is the same shape the branch and tag writes
	// answer with, so a caller driving the API by hand learns what the branch
	// now points at.
	writeJSON(w, http.StatusOK, hfRefResult{
		Name: branch, Ref: gitrepo.BranchRef(branch), Target: newHash.String(),
	})
}

// squashThroughWAL rewrites the branch and records the move, obeying the same
// acknowledgement rule every other ref write does (see wal.go): a ref the WAL
// never accepted must not survive locally, or this instance would serve a head
// the index disagrees with and every later commit on the branch would be
// rejected as stale.
//
// The recorded update is a genuine force: <old> is the head being discarded
// and <new> is not a descendant of it. That is the point of the operation, and
// the WAL's CAS is on the ref's current value rather than on ancestry, so
// somebody else's push landing in between loses the CAS and is reported as
// contention instead of being silently overwritten.
//
// A no-op squash (a branch already down to one parentless commit) records
// nothing: SquashBranch reports the same hash for both, there is no ref move
// to write, and an entry that changes nothing would still burn an index
// revision.
func (s *Server) squashThroughWAL(ctx context.Context, repo *store.Repo, branch, message string,
	author gitrepo.Signature,
) (newHash, oldHash plumbing.Hash, err error) {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, err
	}
	newHash, oldHash, err = gitRepo.SquashBranch(branch, message, author)
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, err
	}
	if newHash == oldHash {
		return newHash, oldHash, nil
	}
	update := wal.RefUpdate{Ref: gitrepo.BranchRef(branch), Old: oldHash.String(), New: newHash.String()}
	if err := s.recordRefUpdate(ctx, repo, update, func() error {
		return gitRepo.ResetBranch(branch, oldHash)
	}); err != nil {
		return plumbing.ZeroHash, oldHash, err
	}
	return newHash, oldHash, nil
}
