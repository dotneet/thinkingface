// Opening a repository for one request: the git handle, and the
// (repository, revision, path) triple every Web UI read is built from.

package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// openGit opens a repository's git directory, answering the request itself
// when it cannot. Every handler that needs a *gitrepo.Repo goes through this
// rather than calling s.git.Open directly, so the operation recorded on the
// error line is one string: it used to be spelled "open git repository",
// "open repository" and "reopen git repository" in different handlers, and a
// grep for a failing open therefore found only part of them.
func (s *Server) openGit(w http.ResponseWriter, repo *store.Repo) (*gitrepo.Repo, bool) {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return nil, false
	}
	return gitRepo, true
}

// uiRepoTarget is the prologue of a Web UI read that names a repository and a
// revision: resolve the repository, read {rev} (falling back to the default
// branch), then open git.
//
// The order is the point. It used to be spelled both ways round -- some
// handlers opened git and then read the revision, others the reverse -- and
// opening first means a request whose revision is not even valid
// percent-encoding still pays for a git open, which in authoritative WAL mode
// can materialise the whole directory from the index before anything looks at
// the 400 that was coming anyway. Parameters are cheap to reject; git handles
// are not.
func (s *Server) uiRepoTarget(w http.ResponseWriter, r *http.Request) (*store.Repo, *gitrepo.Repo, string, bool) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"),
		repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return nil, nil, "", false
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return nil, nil, "", false
	}
	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return nil, nil, "", false
	}
	return repo, gitRepo, rev, true
}

// uiFileTarget resolves the repository, the revision and the trailing
// wildcard path for the reads that name one file (or one directory) inside a
// revision. It deliberately does *not* open git: the caller does that with
// openGit once it is happy with the path.
//
// That split is the same economy uiRepoTarget applies to the revision. A
// handler with a stricter rule on the path -- a checkpoint extension, a
// .parquet suffix -- rejects it with a 400 that costs nothing, and opening
// git first would make that request materialise the whole directory from the
// index in authoritative WAL mode before anything looked at the 400 that was
// coming anyway.
//
// The path is returned as given and not checked for emptiness: "" is the
// repository root, which is exactly what the file browser asks for first.
func (s *Server) uiFileTarget(w http.ResponseWriter, r *http.Request) (*store.Repo, string, string, bool) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"),
		repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return nil, "", "", false
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return nil, "", "", false
	}
	return repo, rev, wildcardPath(r), true
}
