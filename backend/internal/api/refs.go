// The HuggingFace-compatible branch, tag and commit-list endpoints:
// HfApi.create_branch / delete_branch / create_tag / delete_tag /
// list_repo_commits. Before these existed, `git push` was the only way to make
// a branch or a tag on this server, which broke every huggingface_hub workflow
// that versions a repository without cloning it.
//
// These are HF-compatible endpoints, so the external protocol -- not
// internal/apitypes -- is the contract: the request and response shapes below
// are hand-written to match what huggingface_hub actually sends and reads
// (see docs/dev/api-contract.md), and are deliberately outside `make gen-types`.

package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// hfDateFormat is what huggingface_hub's parse_datetime accepts: an ISO-8601
// instant ending in a literal "Z". A numeric "+00:00" offset -- which
// time.RFC3339 emits for any non-UTC time -- raises ValueError there, so the
// value is always converted to UTC first.
const hfDateFormat = "2006-01-02T15:04:05.000Z"

// hfRefResult is the body a successful branch/tag write answers with.
// huggingface_hub ignores it (create_branch and friends return None), but every
// response on this server is JSON, and a caller driving the API by hand wants
// to know what the ref ended up pointing at.
type hfRefResult struct {
	Name   string `json:"name"`
	Ref    string `json:"ref"`
	Target string `json:"targetCommit"`
}

// refParam reads a branch or tag name out of the URL path.
//
// The decoding is not optional: huggingface_hub percent-encodes the name with
// `quote(branch, safe="")`, so a branch called "feature/x" arrives as
// "feature%2Fx", and chi routes on the *escaped* path whenever one is present
// (r.URL.RawPath), handing the parameter over still encoded. Without this a
// slashed branch name would either 404 or create a ref literally named
// "feature%2Fx". It is also not unconditional -- pathParam explains why, and
// it matters here too: ValidateRefName permits "%" in a ref name, so a branch
// called "50%-trained" reaches this with RawPath empty and already decoded,
// and a second decode would reject it as bad percent-encoding.
//
// what names the thing in the error message ("branch" / "tag").
func refParam(w http.ResponseWriter, r *http.Request, key, what string) (string, bool) {
	name, err := pathParam(r, key)
	if err != nil {
		badRequest(w, what+" name is not valid percent-encoding")
		return "", false
	}
	if err := gitrepo.ValidateRefName(name); err != nil {
		badRequest(w, what+" "+strconv.Quote(name)+" "+err.Error())
		return "", false
	}
	return name, true
}

// revParam reads a revision out of the URL path, falling back to the
// repository's default branch. Unlike refParam it is not validated as a ref
// name, because a revision may also be a commit SHA.
//
// Every handler that takes a revision from the path goes through this. The
// four ref handlers used to be the only ones that did, which is why
// create_branch("feature/x") succeeded and every read or write that named the
// new branch afterwards came back 404 or empty.
func revParam(w http.ResponseWriter, r *http.Request, key string, repo *store.Repo) (string, bool) {
	rev, err := pathParam(r, key)
	if err != nil {
		badRequest(w, "revision is not valid percent-encoding")
		return "", false
	}
	if rev == "" {
		rev = repo.DefaultBranch
	}
	return rev, true
}

// revisionNotFound answers a revision that does not resolve. The X-Error-Code
// header is what makes huggingface_hub raise RevisionNotFoundError rather than
// a bare HfHubHTTPError -- the same trick handleHFTree uses for EntryNotFound.
func revisionNotFound(w http.ResponseWriter, message string) {
	w.Header().Set("X-Error-Code", "RevisionNotFound")
	notFound(w, message)
}

// The optional-body decoder these handlers use is decodeOptionalJSON in
// edit.go -- one helper for the package. It matters here because
// `HfApi.delete_branch` sends no body at all and `create_branch` without a
// revision sends `{}`, so only a body that is present and malformed may be
// an error.

// writeRefError maps the shared failures of the four write handlers.
func writeRefError(w http.ResponseWriter, what, name string, err error) {
	switch {
	case errors.Is(err, gitrepo.ErrRefExists):
		// 409 exactly: it is the status huggingface_hub's `exist_ok=True`
		// swallows in create_branch and create_tag. Anything else turns a
		// tolerated duplicate into a raised exception.
		conflict(w, what+" "+name+" already exists")
	case errors.Is(err, gitrepo.ErrRefNotFound):
		// RevisionNotFoundError is what delete_tag documents for a tag that
		// is not there.
		revisionNotFound(w, what+" "+name+" does not exist")
	case errors.Is(err, gitrepo.ErrInvalidRefName):
		badRequest(w, what+" "+strconv.Quote(name)+" "+err.Error())
	case errors.Is(err, errWALConflict):
		conflict(w, what+" "+name+" changed concurrently; retry")
	default:
		internalError(w, "write "+what, err)
	}
}

// resolveRev resolves rev, telling "this repository has no commits" apart from
// "this revision does not exist" and writing the 404 itself for both.
//
// The distinction has to be made here because gitrepo.Resolve reports both as
// ErrEmptyRepo: go-git answers plumbing.ErrReferenceNotFound for an unborn
// HEAD and for an unknown name alike, and Resolve folds the two together.
// gitRepo.IsEmpty is the tie-breaker -- a repository whose HEAD resolves is
// not empty, so the failure was the revision's.
func (s *Server) resolveRev(w http.ResponseWriter, repo *store.Repo, rev string) (*gitrepo.Repo, plumbing.Hash, bool) {
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return nil, plumbing.ZeroHash, false
	}
	target, err := gitRepo.Resolve(rev)
	if err == nil {
		return gitRepo, target, true
	}
	if errors.Is(err, gitrepo.ErrEmptyRepo) && gitRepo.IsEmpty() {
		revisionNotFound(w, repo.FullName()+" has no commits yet")
		return nil, plumbing.ZeroHash, false
	}
	revisionNotFound(w, "revision "+rev+" not found in "+repo.FullName())
	return nil, plumbing.ZeroHash, false
}

// revisionOrEmpty is resolveRev's read-only sibling, for the HF endpoints that
// answer with a listing rather than performing a write.
//
// empty=true means the repository has no commits at all. That is a legitimate
// 200 with nothing in it -- `create_repo` followed by `repo_info` is an
// ordinary huggingface_hub flow and must not 404 -- so the caller answers with
// an empty listing rather than an error. When the repository *does* have
// commits but rev is not one of them, this writes 404 +
// `X-Error-Code: RevisionNotFound` itself and reports ok=false. Without the
// distinction every unknown revision read as "empty", which is how
// `revision_exists(repo_id, "typo")` came back True and
// `snapshot_download(revision="typo")` quietly produced a zero-file snapshot.
//
// The tie-breaker is the same as resolveRev's, and for the same reason:
// gitrepo.Resolve reports an unborn HEAD and an unknown name alike as
// ErrEmptyRepo, because go-git answers plumbing.ErrReferenceNotFound for both.
//
// The returned hash is meant to be handed straight to gitRepo.Tree / Stat, so
// the revision is resolved exactly once per request and a concurrent push
// cannot make two lookups in one response disagree.
func (s *Server) revisionOrEmpty(w http.ResponseWriter, gitRepo *gitrepo.Repo, repo *store.Repo, rev string) (plumbing.Hash, bool, bool) {
	target, err := gitRepo.Resolve(rev)
	if err == nil {
		return target, false, true
	}
	if errors.Is(err, gitrepo.ErrEmptyRepo) && gitRepo.IsEmpty() {
		return plumbing.ZeroHash, true, true
	}
	revisionNotFound(w, "revision "+rev+" not found in "+repo.FullName())
	return plumbing.ZeroHash, false, false
}

// ------------------------------------------------------------------ branches

// handleHFCreateBranch answers POST /api/{type}s/{ns}/{name}/branch/{branch}
// with an optional `{"startingPoint": "<rev>"}` body -- the exact shape
// HfApi.create_branch sends.
//
// Authorisation is loadRepoForWrite, the gate every content-changing endpoint
// shares: it requires a write-scoped token held by someone with at least
// `write` in the namespace, and refuses an archived repository. A branch is
// repository content as much as a commit is.
func (s *Server) handleHFCreateBranch(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	branch, ok := refParam(w, r, "branch", "branch")
	if !ok {
		return
	}
	var req struct {
		StartingPoint string `json:"startingPoint"`
	}
	if !decodeOptionalJSON(w, r, maxMetaBody, &req, "request body must be JSON with an optional startingPoint field") {
		return
	}
	start := req.StartingPoint
	if start == "" {
		start = repo.DefaultBranch
	}
	_, target, ok := s.resolveRev(w, repo, start)
	if !ok {
		return
	}

	if err := s.createRefThroughWAL(r.Context(), repo, gitrepo.BranchRef(branch), target); err != nil {
		writeRefError(w, "branch", branch, err)
		return
	}

	// A new branch is a new value of repo_files.ref (the index is keyed by
	// (repo_id, ref, path)), so without a sync job the branch would exist in
	// git but have no file index -- and the GCS access script, which is built
	// from that index rather than from git, would come back empty for it. This
	// is the same job handleReceivePack enqueues for a branch a push created;
	// the blobs it publishes are content-addressed and already in the bucket,
	// so the work is an index refresh, not a re-upload. It also fires the
	// repo.push webhook, which is why no new webhook event exists for this.
	//
	// Logged rather than surfaced: the ref is already durable at this point,
	// so failing the request would tell the client the opposite of the truth.
	// The job is re-created by the next push to the branch.
	if err := s.sync.Enqueue(r.Context(), repo.ID, branch, "", target.String()); err != nil {
		slog.Error("schedule sync after branch creation",
			"repo", repo.FullName(), "branch", branch, "error", err)
	}

	writeJSON(w, http.StatusCreated, hfRefResult{
		Name: branch, Ref: gitrepo.BranchRef(branch), Target: target.String(),
	})
}

// handleHFDeleteBranch answers DELETE /api/{type}s/{ns}/{name}/branch/{branch}.
//
// The repository's default branch is refused with 409: deleting it would leave
// HEAD dangling, every listing without a revision to read, and -- since the
// default branch is what the metadata index is built from -- the repository
// card frozen at its last state. huggingface_hub documents exactly this
// ("If trying to delete a protected branch. Ex: `main` cannot be deleted") and
// raises HfHubHTTPError for it, so the status only has to be an error status.
func (s *Server) handleHFDeleteBranch(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	branch, ok := refParam(w, r, "branch", "branch")
	if !ok {
		return
	}
	if branch == repo.DefaultBranch {
		conflict(w, branch+" is the default branch of "+repo.FullName()+" and cannot be deleted")
		return
	}

	old, err := s.deleteRefThroughWAL(r.Context(), repo, gitrepo.BranchRef(branch))
	if err != nil {
		writeRefError(w, "branch", branch, err)
		return
	}
	// No sync job: the file index rows for a deleted ref are unreachable
	// (every read resolves the revision in git first, and it no longer
	// resolves) and are dropped with the repository. This is what
	// `git push --delete` already does -- handleReceivePack only enqueues for
	// branches present *after* the push.
	writeJSON(w, http.StatusOK, hfRefResult{
		Name: branch, Ref: gitrepo.BranchRef(branch), Target: old.String(),
	})
}

// ---------------------------------------------------------------------- tags

// handleHFCreateTag answers POST /api/{type}s/{ns}/{name}/tag/{rev} with a
// `{"tag": "...", "message": "..."}` body. Note the asymmetry with branches,
// which is huggingface_hub's and not ours: the *revision being tagged* is in
// the path and the tag name is in the body, while delete_tag puts the tag name
// in the path.
//
// A message produces a real annotated tag object, the way `git tag -m` does,
// rather than being dropped on the floor; without one the tag is lightweight.
func (s *Server) handleHFCreateTag(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	var req struct {
		Tag     string `json:"tag"`
		Message string `json:"message"`
	}
	if !decodeOptionalJSON(w, r, maxMetaBody, &req, "request body must be JSON with a tag field") {
		return
	}
	if err := gitrepo.ValidateRefName(req.Tag); err != nil {
		badRequest(w, "tag "+strconv.Quote(req.Tag)+" "+err.Error())
		return
	}

	gitRepo, target, ok := s.resolveRev(w, repo, rev)
	if !ok {
		return
	}
	refTarget := target
	if req.Message != "" {
		tagger := gitrepo.Signature{
			Name: "thinkingface", Email: "noreply@thinkingface.local", When: time.Now(),
		}
		if user := currentUser(r.Context()); user != nil {
			tagger.Name = user.Username
			if user.Email != "" {
				tagger.Email = user.Email
			}
		}
		var err error
		refTarget, err = gitRepo.WriteTagObject(req.Tag, target, req.Message, tagger)
		if err != nil {
			internalError(w, "write tag object", err)
			return
		}
	}

	if err := s.createRefThroughWAL(r.Context(), repo, gitrepo.TagRef(req.Tag), refTarget); err != nil {
		writeRefError(w, "tag", req.Tag, err)
		return
	}
	// No sync job, deliberately: the indexing worker is driven by branch tips
	// (handleReceivePack enqueues from HeadsAfterPush, which lists branches
	// only), so `git push v1.0` schedules nothing either. Making the API
	// behave differently from the push it mirrors would be the surprise.
	// docs/users/guides/git.md states this for pushes and now for the API too.
	writeJSON(w, http.StatusCreated, hfRefResult{
		Name: req.Tag, Ref: gitrepo.TagRef(req.Tag), Target: refTarget.String(),
	})
}

// handleHFDeleteTag answers DELETE /api/{type}s/{ns}/{name}/tag/{rev}, where
// {rev} is the tag name (huggingface_hub reuses the create route's path
// parameter for a different thing).
func (s *Server) handleHFDeleteTag(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	tag, ok := refParam(w, r, "rev", "tag")
	if !ok {
		return
	}
	old, err := s.deleteRefThroughWAL(r.Context(), repo, gitrepo.TagRef(tag))
	if err != nil {
		writeRefError(w, "tag", tag, err)
		return
	}
	writeJSON(w, http.StatusOK, hfRefResult{
		Name: tag, Ref: gitrepo.TagRef(tag), Target: old.String(),
	})
}

// ------------------------------------------------------------------- commits

// hfCommit is one element of the commits array, shaped for
// huggingface_hub.GitCommitInfo. Every field it reads is always present:
// `item["message"]` and `item["title"]` are indexed directly (a missing key is
// a KeyError, not a None), and each author is a *dict* it takes "user" from.
type hfCommit struct {
	ID      string           `json:"id"`
	Authors []hfCommitAuthor `json:"authors"`
	Date    string           `json:"date"`
	Title   string           `json:"title"`
	Message string           `json:"message"`
}

type hfCommitAuthor struct {
	User string `json:"user"`
}

// handleHFCommits answers GET /api/{type}s/{ns}/{name}/commits/{rev} for
// HfApi.list_repo_commits. Authorisation is an ordinary read, the same as
// every other HF-compatible GET on a repository.
//
// Paging follows GitHub's Link-header convention, because that is what
// huggingface_hub's `paginate` helper understands: it reads
// `response.links["next"]["url"]` and follows it verbatim, so the URL has to be
// absolute. A client that ignores the header simply gets the first page, which
// is what `limit` is for.
func (s *Server) handleHFCommits(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForRead(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	// Resolved up front so an unknown revision is a RevisionNotFoundError and
	// not an empty list: ListCommits cannot tell the two apart on its own.
	gitRepo, _, ok := s.resolveRev(w, repo, rev)
	if !ok {
		return
	}

	limit := defaultCommitPage
	if v := r.URL.Query().Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 {
			badRequest(w, "limit must be a positive integer")
			return
		}
		limit = min(n, maxCommitPage)
	}
	after := plumbing.ZeroHash
	if v := r.URL.Query().Get("after"); v != "" {
		parsed := plumbing.NewHash(v)
		if parsed.IsZero() || parsed.String() != strings.ToLower(v) {
			badRequest(w, "after must be a full commit hash")
			return
		}
		after = parsed
	}

	metas, next, err := gitRepo.ListCommits(rev, r.URL.Query().Get("path"), after, limit)
	if err != nil {
		revisionNotFound(w, "revision "+rev+" not found in "+repo.FullName())
		return
	}

	out := make([]hfCommit, 0, len(metas))
	for _, m := range metas {
		out = append(out, hfCommit{
			ID:      m.Hash.String(),
			Authors: []hfCommitAuthor{{User: m.Author}},
			Date:    m.When.UTC().Format(hfDateFormat),
			Title:   m.Message,
			Message: m.Body,
		})
	}
	if !next.IsZero() {
		w.Header().Set("Link", `<`+s.commitsPageURL(r, next.String())+`>; rel="next"`)
	}
	writeJSON(w, http.StatusOK, out)
}

// commitsPageURL rebuilds this request's URL with the cursor advanced. It is
// built on the configured public URL rather than on the Host header, so a
// client behind a proxy is never sent to an internal address.
func (s *Server) commitsPageURL(r *http.Request, cursor string) string {
	q := r.URL.Query()
	q.Set("after", cursor)
	return strings.TrimSuffix(s.cfg.PublicURL, "/") + r.URL.EscapedPath() + "?" + q.Encode()
}
