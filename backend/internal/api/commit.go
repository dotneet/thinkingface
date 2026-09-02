package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// maxCommitBody bounds an inline upload. Anything larger is expected to arrive
// through LFS, which never passes through this endpoint.
const maxCommitBody = 512 << 20

// loadLFSRules reads .gitattributes at a revision. A repository whose own copy
// cannot be read falls back to the list its kind was seeded with, so the
// fallback routes files the same way the repository itself would.
func (s *Server) loadLFSRules(repo *gitrepo.Repo, rev, kind string) *gitrepo.LFSRules {
	content, err := repo.ReadFile(rev, ".gitattributes", 1<<20)
	if err != nil {
		return gitrepo.ParseGitAttributes([]byte(gitrepo.DefaultGitAttributes(kind)))
	}
	return gitrepo.ParseGitAttributes(content)
}

type preuploadFile struct {
	Path   string `json:"path"`
	Sample string `json:"sample"`
	Size   int64  `json:"size"`
}

// rejectCreatePR refuses a commit that asked to be opened as a pull request,
// and reports whether it did.
//
// thinkingface has no pull requests. huggingface_hub asks for one by adding
// `?create_pr=1` to preupload and to commit (_commit_api.py), and both
// endpoints used to read no query parameters at all -- so `create_pr=True`
// wrote straight to the target branch and answered 200, and the caller went
// away believing a reviewable PR existed while `main` had already moved. A
// silently ignored safety flag is worse than an unsupported feature, so this
// is an explicit 400.
//
// Any value other than the falsey spellings counts as asking for it: the
// failure mode of guessing wrong in that direction is a clear error message,
// and the failure mode of guessing wrong in the other is the silent overwrite
// this exists to stop. huggingface_hub itself only ever sends "1", and only
// when create_pr is true -- a client that does not want a PR omits the
// parameter entirely.
func rejectCreatePR(w http.ResponseWriter, r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("create_pr"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	badRequest(w, "this instance does not support pull requests: retry without create_pr, "+
		"or commit to a branch of your own and merge it yourself")
	return true
}

func (s *Server) handlePreupload(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.loadHFRepoForWrite(w, r)
	if !ok {
		return
	}
	// Refused before the body is read, and before any LFS object is uploaded
	// against the answer this would give: huggingface_hub sends `?create_pr=1`
	// to preupload as well as to commit, and letting preupload succeed would
	// mean the client pushes its blobs to the bucket and only then learns the
	// commit cannot happen.
	if rejectCreatePR(w, r) {
		return
	}
	var req struct {
		Files []preuploadFile `json:"files"`
	}
	if !decodeJSON(w, r, maxBatchBody, &req, "request body must be JSON with a files array") {
		return
	}
	// The same three bounds paths-info applies, and reusing its constants
	// rather than inventing a second pair: the two endpoints are the same
	// shape (one small record per file, one tree lookup per record) and had no
	// reason to disagree about how many records that may be.
	//
	// maxBatchBody alone is not a bound on the work: 8 MiB of minimal records
	// is roughly 380k of them, and each costs a rules.ShouldUseLFS pass over
	// every .gitattributes pattern plus a FindEntry inside StatMany. A write
	// token and the handler deadline keep that amplification rather than an
	// outage, which is why this is a tightening and not a fix for a hole.
	//
	// checkOpPath is the same validation the commit endpoint runs on every path
	// in its body. preupload is the step immediately before that commit, so a
	// path it answers happily and the commit then refuses is a round trip --
	// and, for an LFS-routed path, a whole transfer -- spent to learn something
	// this could have said first.
	if len(req.Files) > maxPathsInfoPaths {
		badRequest(w, fmt.Sprintf("files may contain at most %d entries", maxPathsInfoPaths))
		return
	}
	for _, f := range req.Files {
		if len(f.Path) > maxPathBytes {
			badRequest(w, fmt.Sprintf("each path must be at most %d bytes", maxPathBytes))
			return
		}
		if !checkOpPath(w, "file", f.Path) {
			return
		}
	}
	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	rules := s.loadLFSRules(gitRepo, rev, repo.Kind)

	// What each path already holds at this revision, so the answer can carry
	// an oid the client can diff against. Deliberately best-effort: preupload
	// is the step *before* a write, and the revision it names is routinely one
	// the following commit is about to create, so a revision that does not
	// resolve is normal and means only that there is nothing to compare with.
	// Answering 404 here would break creating a branch by committing to it.
	paths := make([]string, 0, len(req.Files))
	for _, f := range req.Files {
		paths = append(paths, f.Path)
	}
	existing, _, err := gitRepo.StatMany(rev, paths)
	if err != nil {
		existing = nil
	}

	type result struct {
		Path         string `json:"path"`
		UploadMode   string `json:"uploadMode"`
		ShouldIgnore bool   `json:"shouldIgnore"`
		OID          any    `json:"oid"`
	}
	out := make([]result, 0, len(req.Files))
	for _, f := range req.Files {
		mode := "regular"
		if rules.ShouldUseLFS(f.Path, f.Size) {
			mode = "lfs"
		}
		out = append(out, result{
			Path:         f.Path,
			UploadMode:   mode,
			ShouldIgnore: false,
			OID:          preuploadOID(existing[f.Path], mode),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}

// preuploadOID is the oid huggingface_hub compares against the hash it computes
// locally, and the reason it can skip re-uploading a file that has not changed
// (_commit_api.py sets CommitOperationAdd._remote_oid from it; create_commit
// drops any addition whose _remote_oid equals its _local_oid).
//
// Which hash that is follows from the uploadMode *we* just returned, not from
// what the repository happens to hold: _local_oid is the git blob sha1 of the
// content for "regular" and the sha256 of the content for "lfs". So the only
// safe values are ones from the matching hash space -- the entry's own blob
// sha1 for a regular file, the pointer's sha256 for an LFS file.
//
// Anything else must be nil. A value from the wrong hash space would not merely
// fail to match; it would make the comparison meaningless, and a collision in
// the wrong direction means a file that *did* change is quietly dropped from
// the commit. nil only costs one re-upload of an unchanged file, so every case
// that is not exactly right falls back to it: directories, missing paths, and
// the two mismatched pairings (an LFS pointer answered as "regular" because the
// new content is small, a regular file answered as "lfs" because it is not).
func preuploadOID(e gitrepo.Entry, mode string) any {
	if e.IsDir || e.Hash.IsZero() {
		return nil
	}
	// A symlink's blob holds the target path, not the file's bytes, while the
	// client hashes whatever the link resolves to. The two disagree in all but
	// a contrived case -- and that case is exactly the one that would drop a
	// real change from the commit, so it is not worth the one saved upload.
	if e.Mode == filemode.Symlink {
		return nil
	}
	switch mode {
	case "regular":
		if e.LFS != nil {
			return nil
		}
		return e.Hash.String()
	case "lfs":
		if e.LFS == nil {
			return nil
		}
		return e.LFS.OID
	}
	return nil
}

// looksLikeSHA reports whether rev is a full commit hash rather than a branch
// name. Branch names may not be 40 hex characters, so this is unambiguous.
func looksLikeSHA(rev string) bool {
	if len(rev) != 40 {
		return false
	}
	for _, c := range rev {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// checkOpPath refuses a commit operation whose path gitrepo.Commit would
// refuse anyway, before anything is committed.
//
// The check is not new -- Commit calls ValidatePath on every op it applies --
// but its failure used to reach the handler as an unclassified error from
// commitThroughWAL and become a 500 internal_error. That is wrong three times
// over for what is only ever the caller's mistake: huggingface_hub's
// http_backoff retries a 5xx, so a permanently invalid request is re-sent
// until it gives up; the sentence in X-Error-Message says "create commit
// failed" instead of what to fix; and every typo lands in the log as
// slog.Error. The upload endpoint has always answered 400 for the identical
// check (cleanUploadPath), so the same input got two different statuses
// depending on which route it arrived through.
//
// what names the operation ("file", "deletedFolder", "copyFile srcPath", ...)
// so a caller sending a batch can see which line is wrong.
func checkOpPath(w http.ResponseWriter, what, path string) bool {
	if err := gitrepo.ValidatePath(path); err != nil {
		badRequest(w, what+": invalid path "+strconv.Quote(path)+
			"; paths are relative to the repository root and may not escape it or write inside .git")
		return false
	}
	return true
}

// ensureBranchRev refuses a write whose {rev} names something in this
// repository that is not a branch. Every write path commits to
// refs/heads/{rev}, while every read resolves {rev} through go-git's
// RefRevParseRules, which tries refs/tags/%s *before* refs/heads/%s. Writing
// to a rev that is a tag would therefore read one ref and write another: the
// branch would be created out of nothing, as a parentless root commit, and the
// tag would keep winning every subsequent read -- so the caller would be told
// the write succeeded and then never see it again. The same goes for anything
// else that resolves without being a branch (an abbreviated SHA, "HEAD",
// "main~1"): there is no branch there to extend.
//
// A rev that resolves to nothing at all is still allowed through. That is the
// first commit on a new branch, which these endpoints are expected to create.
//
// 409 rather than 400, like every other collision with a ref that is already
// there (writeRefError's ErrRefExists, StalePathError): the request is
// well-formed and only this repository's current state refuses it, so deleting
// the tag or picking another name makes the identical request succeed. A full
// commit SHA stays a 400 in looksLikeSHA, since that one is refused by its
// shape without the repository having any say.
//
// what names the operation in the message ("commits" / "uploads" / ...), as in
// the looksLikeSHA messages it sits next to.
func ensureBranchRev(w http.ResponseWriter, gitRepo *gitrepo.Repo, rev, what string) bool {
	// A name git could not hold as a branch under any circumstances is the
	// caller's mistake, and it is refused before the repository is consulted:
	// go-git's ref lookup answers some of these ("..") with an error rather
	// than a miss, which used to surface as 500 "read branch ref failed", and
	// the rest reached gitrepo.Commit and became 500 "create commit failed".
	//
	// "HEAD" is deliberately exempt. ValidateRefName reserves it, but it also
	// resolves in every repository, and the answer the contract fixes for it
	// (docs/dev/api-contract.md §"{rev} is a branch name") is the 409 below --
	// the same one a tag gets, for the same reason: the request is
	// well-formed and only this repository's state refuses it.
	if rev != "HEAD" {
		if err := gitrepo.ValidateRefName(rev); err != nil {
			badRequest(w, what+" must target a branch: "+strconv.Quote(rev)+" "+err.Error())
			return false
		}
	}
	isBranch, err := gitRepo.HasBranch(rev)
	if err != nil {
		internalError(w, "read branch ref", err)
		return false
	}
	if isBranch {
		return true
	}
	if _, err := gitRepo.Resolve(rev); err != nil {
		return true
	}
	isTag, err := gitRepo.HasTag(rev)
	if err != nil {
		internalError(w, "read tag ref", err)
		return false
	}
	if isTag {
		conflict(w, rev+" is a tag, not a branch; "+what+" must target a branch")
		return false
	}
	conflict(w, rev+" is not a branch of this repository; "+what+" must target a branch")
	return false
}

// commitLine is one NDJSON operation in the commit payload.
type commitLine struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// validParentCommit reports whether s is the shape huggingface_hub sends for
// `create_commit(parent_commit=...)`: a commit hash, or the seven-character
// (or longer) shorthand its docstring documents. Refusing the shape here means
// a typo cannot be mistaken for a hash that simply does not match -- the two
// deserve different answers, since one is the caller's mistake and the other
// is the branch having moved.
func validParentCommit(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// pendingCopy is one `copyFile` line waiting for its source to be resolved:
// op indexes the placeholder already appended to the operation list, so the
// copy keeps its position among the adds and deletes around it.
type pendingCopy struct {
	op  int
	dst string
	src string
	rev string
}

// resolveCopies fills in the source blob of every copyFile operation, and
// returns the LFS oids among them.
//
// The sources are looked up per revision with StatMany rather than one Stat
// per file: `create_commit` sends copies in batches, and Stat re-resolves the
// revision and reloads the root tree every time it is called.
//
// A copy of an LFS file copies the *pointer*, which is the file as far as git
// is concerned, and the object it names is by construction already linked to
// this repository -- the source is a path in this same repository. The oid is
// returned anyway so the caller re-links it with the rest of the commit's
// objects: the insert is idempotent, and it costs one row to be certain a copy
// never leaves a pointer whose object GC believes nothing references.
func resolveCopies(w http.ResponseWriter, gitRepo *gitrepo.Repo, rev string, copies []pendingCopy, ops []gitrepo.Op) ([]store.LFSObjectRef, bool) {
	// Grouped in first-appearance order, so an error names whichever line the
	// caller would read first rather than whatever the map iterated to.
	order := []string{}
	byRev := map[string][]pendingCopy{}
	for _, c := range copies {
		srcRev := c.rev
		if srcRev == "" {
			// "Copy from the revision being committed to" -- the same default
			// huggingface_hub applies when CommitOperationCopy leaves
			// src_revision unset.
			srcRev = rev
		}
		if _, seen := byRev[srcRev]; !seen {
			order = append(order, srcRev)
		}
		byRev[srcRev] = append(byRev[srcRev], c)
	}

	var lfsOIDs []store.LFSObjectRef
	for _, srcRev := range order {
		group := byRev[srcRev]
		paths := make([]string, 0, len(group))
		for _, c := range group {
			paths = append(paths, c.src)
		}
		entries, _, err := gitRepo.StatMany(srcRev, paths)
		if err != nil {
			// ErrEmptyRepo covers both "no commits at all" and "no such
			// revision": go-git cannot tell them apart, and for a copy source
			// they lead to the same answer either way.
			if errors.Is(err, gitrepo.ErrEmptyRepo) {
				revisionNotFound(w, "copyFile: source revision "+srcRev+" does not exist")
				return nil, false
			}
			internalError(w, "read copy source", err)
			return nil, false
		}
		for _, c := range group {
			e, ok := entries[c.src]
			if !ok {
				entryNotFound(w, "copyFile "+c.dst+": "+c.src+" does not exist at "+srcRev)
				return nil, false
			}
			if e.IsDir {
				badRequest(w, "copyFile "+c.dst+": "+c.src+" is a directory; copy files one at a time")
				return nil, false
			}
			// A symlink's blob holds a path, not content, and copying it to
			// another directory silently changes what it resolves to. Refused
			// rather than guessed at, for the same reason preuploadOID
			// refuses to answer for one.
			if e.Mode == filemode.Symlink {
				badRequest(w, "copyFile "+c.dst+": "+c.src+" is a symlink, which cannot be copied")
				return nil, false
			}
			ops[c.op].SrcHash = e.Hash
			ops[c.op].Executable = e.Mode == filemode.Executable
			if e.LFS != nil {
				lfsOIDs = append(lfsOIDs, store.LFSObjectRef{OID: e.LFS.OID, Size: e.LFS.Size})
			}
		}
	}
	return lfsOIDs, true
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := s.loadHFRepoForWrite(w, r)
	if !ok {
		return
	}
	if rejectCreatePR(w, r) {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	// Committing to a detached SHA is meaningless; the API only writes branches.
	if looksLikeSHA(rev) {
		badRequest(w, "commits must target a branch, not a commit SHA")
		return
	}

	// Opened only to check the revision, and the handle is dropped again
	// straight away: commitThroughWAL (re-)opens the repository itself, since
	// an authoritative-mode materialisation may rebuild the directory and
	// invalidate any handle taken before it.
	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return
	}
	if !ensureBranchRev(w, gitRepo, rev, "commits") {
		return
	}

	plan, ok := s.parseCommitBody(w, r, repo)
	if !ok {
		return
	}
	lfsCopies, ok := resolveCopies(w, gitRepo, rev, plan.copies, plan.ops)
	if !ok {
		return
	}
	lfsOIDs := append(plan.lfsOIDs, lfsCopies...)

	if len(plan.ops) == 0 {
		badRequest(w, "commit contains no file operations")
		return
	}

	newHash, oldHash, err := s.commitThroughWAL(r.Context(), repo, gitrepo.CommitRequest{
		Branch: rev, Message: plan.summary, Author: commitAuthor(r.Context()),
		Ops: plan.ops, ParentCommit: plan.parentCommit,
	}, true)
	// The stale-parent 412 and the contention 409 are deliberately different
	// answers, and writeCommitError (wal.go) is where the difference is
	// explained and applied for every commitThroughWAL caller.
	if writeCommitError(w, err, "commit") {
		return
	}

	if err := s.store.LinkLFSObjects(r.Context(), repo.ID, lfsOIDs); err != nil {
		internalError(w, "link lfs objects", err)
		return
	}
	if err := s.sync.Enqueue(r.Context(), repo.ID, rev, oldHash.String(), newHash.String()); err != nil {
		internalError(w, "schedule sync", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":        true,
		"commitUrl":      fmt.Sprintf("%s/commit/%s", s.repoWebURL(repo), newHash),
		"commitOid":      newHash.String(),
		"hookOutput":     "",
		"pullRequestUrl": nil,
	})
}
