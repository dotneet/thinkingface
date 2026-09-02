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
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
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

// refContentionRetryAfter is what a caller that lost the WAL index CAS is told
// to wait. Contention on one repository's index settles in milliseconds, so
// this is really just the smallest value the header can carry -- Retry-After is
// integer seconds.
const refContentionRetryAfter = time.Second

// writeRefError maps the shared failures of the four write handlers.
//
// The split between "this ref already exists" and "somebody else won the race"
// is the load-bearing part, and it is a compatibility requirement rather than a
// nicety. `create_branch(exist_ok=True)` and `create_tag(exist_ok=True)` swallow
// *every* 409 -- the client only looks at the status, never at the body -- so
// answering contention with 409 would tell a client that tolerates duplicates
// "the ref is there" for a ref that was rolled back and never recorded. It
// would then carry on committing to a branch that does not exist.
//
// 503 `overloaded` + Retry-After is the honest answer: nothing is wrong with
// the request, the ref does not exist, and retrying is the right move.
// huggingface_hub raises HfHubHTTPError for it in all four calls (503 falls
// through hf_raise_for_status to the generic branch), and these calls go
// through `get_session()` directly rather than `http_backoff`, so the client
// surfaces it immediately instead of silently retrying.
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
		// Never 409 here: see the doc comment. The local change was rolled
		// back, so the operation is safe to repeat.
		//
		// "unchanged" rather than "not written": the delete handlers share
		// this helper, and a refused delete did not fail to write anything --
		// it failed to remove something. Unchanged is the one word true of
		// both, and it is also the fact the caller needs: whatever they asked
		// for did not happen, and retrying cannot double-apply.
		serviceOverloadedWith(w, refContentionRetryAfter,
			what+" "+name+" is unchanged: another writer is updating this repository; retry")
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
	gitRepo, ok := s.openGit(w, repo)
	if !ok {
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

// fireRefDeleted announces a branch or tag that is no longer there.
//
// Creation and movement already reach subscribers as repo.push -- the sync job
// every write path schedules fires it -- but deletion reached nobody, from
// either direction: `git push --delete` was silent because handleReceivePack
// only looks at the branches present *after* a push, and the API deletes were
// silent because they schedule no sync job at all. A subscriber mirroring this
// instance therefore watched refs appear and never watched them go away, which
// is worse than not knowing: it leaves a ref in the mirror that the source
// says nothing more about.
//
// The payload deliberately mirrors the one syncer.processPush builds for
// repo.push, so a subscriber parses one shape for both. new_sha is empty
// because there is no new value -- that is what "deleted" means here -- and
// ref_type is what tells a branch from a tag, since one short name can be
// both at once.
//
// No sync job goes with it, and that asymmetry with creation is deliberate:
// the file index rows for a deleted ref are already unreachable (every read
// resolves the revision in git first, and it no longer resolves) and are
// dropped with the repository, so there is nothing left to re-index. Only
// something to announce.
func (s *Server) fireRefDeleted(ctx context.Context, repo *store.Repo, refType, name, oldSHA string) {
	s.fireWebhook(ctx, string(apitypes.WebhookEventRepoRefDeleted), repo.Namespace, &repo.ID, map[string]any{
		"namespace": repo.Namespace, "repo": repo.Name,
		"full_name": repo.FullName(), "kind": repo.Kind,
		"ref": name, "ref_type": refType, "old_sha": oldSHA, "new_sha": "",
	})
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
	repo, _, ok := s.loadHFRepoForWrite(w, r)
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
	repo, _, ok := s.loadHFRepoForWrite(w, r)
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
	//
	// A webhook, though: nothing to re-index is not the same as nothing to
	// announce, and the sync job is what used to carry the announcement.
	s.fireRefDeleted(r.Context(), repo, "branch", branch, old.String())
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
	repo, _, ok := s.loadHFRepoForWrite(w, r)
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

	_, target, ok := s.resolveRev(w, repo, rev)
	if !ok {
		return
	}
	tagger := commitAuthor(r.Context())
	// The tag object -- when there is one -- is written inside
	// createTagRefThroughWAL, on the same repository handle the ref write and
	// the WAL entry pack use. Writing it out here instead would put a
	// gitrepo.Manager.Open (and therefore a materialisation) between the loose
	// object and the code that needs it; see that function's comment.
	refTarget, err := s.createTagRefThroughWAL(r.Context(), repo, req.Tag, target, req.Message, tagger)
	if err != nil {
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
	repo, _, ok := s.loadHFRepoForWrite(w, r)
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
	// Creating a tag schedules nothing (see handleHFCreateTag) and so
	// announced nothing; removing one has to be announced anyway, because a
	// tag disappearing is the whole event -- there is no later push that will
	// tell a subscriber about it.
	s.fireRefDeleted(r.Context(), repo, "tag", tag, old.String())
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

// commitPageParams reads the `limit` / `after` window both commit listings
// take -- the HuggingFace-compatible one and the Web UI's -- and answers the
// request itself on an unusable value.
//
// Parsed in one place because the two must not drift apart. They were
// nineteen near-identical lines apiece, over the same history, so raising
// maxCommitPage or loosening the cursor rule on one side would silently have
// left the other where it was.
//
// Strict, unlike the UI listings' pageParams (urlparams.go), and the same
// strictness on both endpoints even though only one of them is
// HF-compatible: huggingface_hub's `paginate` follows the Link header this
// server builds out of the parameters it was sent, so a `limit` quietly
// replaced by a default is a client that pages wrongly rather than one that
// reports an error -- and a cursor read as "start again from the top" would
// walk the same page forever.
func commitPageParams(w http.ResponseWriter, r *http.Request) (limit int, after plumbing.Hash, ok bool) {
	q := r.URL.Query()
	limit = defaultCommitPage
	if v := q.Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 {
			badRequest(w, "limit must be a positive integer")
			return 0, plumbing.ZeroHash, false
		}
		limit = min(n, maxCommitPage)
	}
	after = plumbing.ZeroHash
	if v := q.Get("after"); v != "" {
		// The cursor names an object, so it has to be a full hex hash; a
		// branch name here would silently restart the walk.
		parsed := plumbing.NewHash(v)
		if parsed.IsZero() || parsed.String() != strings.ToLower(v) {
			badRequest(w, "after must be a full commit hash")
			return 0, plumbing.ZeroHash, false
		}
		after = parsed
	}
	return limit, after, true
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
	repo, _, ok := s.loadHFRepoForRead(w, r)
	if !ok {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	// Resolved up front so an unknown revision is a RevisionNotFoundError and
	// not an empty list: ListCommits cannot tell the two apart on its own.
	gitRepo, commit, ok := s.resolveRev(w, repo, rev)
	if !ok {
		return
	}

	limit, after, ok := commitPageParams(w, r)
	if !ok {
		return
	}

	// The resolved hash, not rev -- the same rule handleUICommits and
	// serveResolve follow, and for the same reason. Passing the name back would
	// throw away everything the resolve just established: a branch deleted
	// between the two calls sends ListCommits down its ErrEmptyRepo branch, so
	// list_repo_commits answers 200 [] and the caller reads a live repository
	// as one with no history -- precisely the confusion resolving first exists
	// to prevent. It also pins every page of one walk to the commit that was
	// validated, rather than to whatever a push has since moved the branch to.
	metas, next, err := gitRepo.ListCommits(commit.String(), r.URL.Query().Get("path"), after, limit)
	if err != nil {
		// Not a RevisionNotFound: resolveRev already proved rev exists a few
		// lines above, so answering every failure that way sends the client
		// looking for a revision problem it does not have. What is left is a
		// cursor naming an object this repository has no commit for
		// (ListCommits wraps ErrPathNotFound for exactly that), which is the
		// caller's input and a 400 -- and anything else, which is ours.
		if errors.Is(err, gitrepo.ErrPathNotFound) {
			badRequest(w, "after must name a commit reachable from "+rev)
			return
		}
		internalError(w, "list commits", err)
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
