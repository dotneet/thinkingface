// The Web UI's commit diff: what one commit changed, against its first parent.
//
// The commit list (handleUICommits in repotree.go) could say that a commit
// exists and who wrote it, but nothing on this server could say what it did,
// which made the history a list of messages nobody could check.

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// diffStatus maps a gitrepo change onto the wire enum.
func diffStatus(k gitrepo.ChangeKind) apitypes.DiffStatus {
	switch k {
	case gitrepo.ChangeAdd:
		return apitypes.DiffStatusAdded
	case gitrepo.ChangeDelete:
		return apitypes.DiffStatusDeleted
	default:
		return apitypes.DiffStatusModified
	}
}

// handleRepoDiff answers GET /api/v1/repos/{kind}/{ns}/{name}/diff/{rev}.
//
// Repository resolution and authorisation are handleUITree's: an ordinary
// read, redirecting through the UI's renamed-repository path. Revision
// handling is resolveRev's rather than revisionOrEmpty's, which is the one
// deliberate difference from the tree and commit-list endpoints next door: a
// listing of a repository with no commits is legitimately empty, but a diff of
// no commit has no commit to describe, and this response carries one
// unconditionally. Both "this repository has no commits" and "that revision
// does not resolve" are therefore 404 + X-Error-Code: RevisionNotFound.
func (s *Server) handleRepoDiff(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	gitRepo, commit, ok := s.resolveRev(w, repo, rev)
	if !ok {
		return
	}

	// The resolved hash, not rev: one resolution per request, so a push
	// landing mid-request cannot diff a different commit than the one the
	// response reports.
	d, err := gitRepo.CommitDiff(commit)
	if err != nil {
		// Not a 404: resolveRev already proved the commit is there, so
		// anything left is this server failing to read its own objects.
		internalError(w, "diff commit", err)
		return
	}

	resp := apitypes.CommitDiffResponse{
		Commit:         commitInfoUI(d.Commit),
		Files:          make([]apitypes.DiffFile, 0, len(d.Files)),
		NumFiles:       d.NumFiles,
		FilesTruncated: d.Truncated,
		Additions:      d.Additions,
		Deletions:      d.Deletions,
	}
	if d.Parent != nil {
		parent := d.Parent.String()
		resp.ParentOID = &parent
	}
	for _, f := range d.Files {
		resp.Files = append(resp.Files, apitypes.DiffFile{
			Path:           f.Path,
			Status:         diffStatus(f.Kind),
			Additions:      f.Additions,
			Deletions:      f.Deletions,
			Binary:         f.Binary,
			LFS:            f.LFS,
			HasPatch:       f.HasPatch,
			NoPatchReason:  apitypes.DiffNoPatchReason(f.NoPatchReason),
			Patch:          f.Patch,
			PatchTruncated: f.PatchTruncated,
			OldSize:        f.OldSize,
			Size:           f.Size,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
