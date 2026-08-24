package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
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
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
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
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
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
func resolveCopies(w http.ResponseWriter, gitRepo *gitrepo.Repo, rev string, copies []pendingCopy, ops []gitrepo.Op) ([]string, bool) {
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

	var lfsOIDs []string
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
				lfsOIDs = append(lfsOIDs, e.LFS.OID)
			}
		}
	}
	return lfsOIDs, true
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	if rejectCreatePR(w, r) {
		return
	}
	user := currentUser(r.Context())
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
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	if !ensureBranchRev(w, gitRepo, rev, "commits") {
		return
	}

	summary := "Upload files"
	parentCommit := ""
	var ops []gitrepo.Op
	var lfsOIDs []string
	var copies []pendingCopy

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommitBody))
	for {
		var line commitLine
		if err := dec.Decode(&line); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			// The decoder's own text describes the server's parse state;
			// errors.go's decodeJSON deliberately never echoes it and this
			// hand-rolled NDJSON loop should not either.
			badRequest(w, "commit body must be newline-delimited JSON")
			return
		}

		switch line.Key {
		case "header":
			var v struct {
				Summary     string `json:"summary"`
				Description string `json:"description"`
				// ParentCommit is huggingface_hub's optimistic lock: it is
				// only present when the caller passed parent_commit, and it
				// means "commit only if the branch is still where I last saw
				// it". Ignoring it would answer 200 to a caller who asked
				// precisely not to overwrite somebody else's push.
				ParentCommit string `json:"parentCommit"`
			}
			// A header that will not parse is an error rather than a header
			// that was not there. It used to be silently skipped, which was
			// harmless while the header carried nothing but a commit message
			// and is not now: the one field that must never be dropped lives
			// in it.
			if err := json.Unmarshal(line.Value, &v); err != nil {
				badRequest(w, "invalid header entry")
				return
			}
			if v.Summary != "" {
				summary = v.Summary
			}
			if v.Description != "" {
				summary += "\n\n" + v.Description
			}
			if v.ParentCommit != "" {
				parentCommit = strings.ToLower(strings.TrimSpace(v.ParentCommit))
				if !validParentCommit(parentCommit) {
					badRequest(w, "header: parentCommit must be a commit hash, or its first 7 or more characters")
					return
				}
			}

		case "file":
			var v struct {
				Path     string `json:"path"`
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
			}
			if err := json.Unmarshal(line.Value, &v); err != nil {
				badRequest(w, "invalid file entry")
				return
			}
			data := []byte(v.Content)
			if v.Encoding == "base64" || v.Encoding == "" {
				decoded, err := base64.StdEncoding.DecodeString(v.Content)
				if err != nil {
					if v.Encoding == "base64" {
						badRequest(w, "file "+v.Path+": content is not valid base64")
						return
					}
				} else {
					data = decoded
				}
			}
			ops = append(ops, gitrepo.Op{Kind: gitrepo.OpAdd, Path: v.Path, Data: data})

		case "lfsFile":
			var v struct {
				Path string `json:"path"`
				Algo string `json:"algo"`
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			}
			if err := json.Unmarshal(line.Value, &v); err != nil {
				badRequest(w, "invalid lfsFile entry")
				return
			}
			if !gitrepo.ValidOID(v.OID) {
				badRequest(w, "lfsFile "+v.Path+": oid must be a sha256 hex digest")
				return
			}
			// The object must already be recorded against *this* repository,
			// not merely present in the bucket. Objects are content-addressed
			// and shared instance-wide, so accepting on presence alone lets a
			// caller commit a pointer to bytes they were never given -- and
			// from then on fetch them through their own repository's resolve.
			// The normal flow (preupload -> LFS batch upload -> verify, or a
			// git-lfs push) always links the object first.
			owned, err := s.store.RepoHasLFSObject(r.Context(), repo.ID, v.OID)
			if err != nil {
				internalError(w, "check lfs object ownership", err)
				return
			}
			if !owned {
				badRequest(w, "lfsFile "+v.Path+": object "+v.OID+" has not been uploaded")
				return
			}
			// Still confirm the bytes are actually in the bucket: a link can
			// outlive the object if a GC ran between upload and commit.
			info, err := s.storage.Stat(r.Context(), storage.LFSKey(v.OID))
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					badRequest(w, "lfsFile "+v.Path+": object "+v.OID+" has not been uploaded")
					return
				}
				internalError(w, "stat lfs object", err)
				return
			}
			// The pointer always carries the object's real size, and a
			// declared size that disagrees with it is refused rather than
			// quietly corrected. The pointer's size is what resolve declares
			// as Content-Length before streaming the object, so a client-chosen
			// one is a client-chosen truncation: net/http cuts the body off at
			// the declared length, and a lie of "1" hands every downloader a
			// one-byte file that looks completely downloaded. Too large hangs
			// the connection instead, and either way repo_files.size and the
			// repository's total size are indexed from the pointer. Omitting
			// the field stays legal -- the object itself is the source of
			// truth -- and a caller that sends a size is simply told when it
			// does not match, rather than being ignored.
			if v.Size != 0 && v.Size != info.Size {
				badRequest(w, fmt.Sprintf("lfsFile %s: size %d does not match the uploaded object's %d bytes",
					v.Path, v.Size, info.Size))
				return
			}
			ops = append(ops, gitrepo.Op{
				Kind: gitrepo.OpAdd, Path: v.Path, Data: gitrepo.FormatLFSPointer(v.OID, info.Size),
			})
			lfsOIDs = append(lfsOIDs, v.OID)

		// The two delete operations refuse a malformed entry rather than
		// skipping it, for the same reason the default case below refuses an
		// unknown key: a commit that drops one of its operations and answers
		// 200 tells the caller the deletion happened. Silently keeping a file
		// somebody asked to remove is the worse half of that -- it can leave
		// data published that was meant to be gone.
		case "deletedFile":
			var v struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(line.Value, &v); err != nil || v.Path == "" {
				badRequest(w, "deletedFile entry must be an object with a non-empty path")
				return
			}
			ops = append(ops, gitrepo.Op{Kind: gitrepo.OpDelete, Path: v.Path})

		case "deletedFolder":
			var v struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(line.Value, &v); err != nil || v.Path == "" {
				badRequest(w, "deletedFolder entry must be an object with a non-empty path")
				return
			}
			ops = append(ops, gitrepo.Op{Kind: gitrepo.OpDeleteDir, Path: strings.TrimSuffix(v.Path, "/")})

		case "copyFile":
			// huggingface_hub's CommitOperationCopy. srcRevision arrives as
			// JSON null when it was left unset, which decodes to "" here and
			// is resolved against the revision being committed to.
			var v struct {
				Path        string `json:"path"`
				SrcPath     string `json:"srcPath"`
				SrcRevision string `json:"srcRevision"`
			}
			if err := json.Unmarshal(line.Value, &v); err != nil {
				badRequest(w, "invalid copyFile entry")
				return
			}
			if v.Path == "" || v.SrcPath == "" {
				badRequest(w, "copyFile: path and srcPath are both required")
				return
			}
			// The source is resolved after the whole body has been read, so
			// the placeholder holds the operation's position in the meantime.
			copies = append(copies, pendingCopy{op: len(ops), dst: v.Path, src: v.SrcPath, rev: v.SrcRevision})
			ops = append(ops, gitrepo.Op{Kind: gitrepo.OpCopy, Path: v.Path})

		default:
			// An operation this server does not implement is refused, never
			// skipped. There was no default here, so an unknown key fell
			// through in silence: a commit that mixed one add with one
			// unsupported operation applied the add, answered 200 `success`,
			// and the caller had no way to learn that half of what they sent
			// never happened. That is the same trade rejectCreatePR makes --
			// an error the caller can act on beats a partial write they
			// cannot see.
			badRequest(w, "unsupported commit operation "+strconv.Quote(line.Key))
			return
		}
	}

	lfsCopies, ok := resolveCopies(w, gitRepo, rev, copies, ops)
	if !ok {
		return
	}
	lfsOIDs = append(lfsOIDs, lfsCopies...)

	if len(ops) == 0 {
		badRequest(w, "commit contains no file operations")
		return
	}

	author := gitrepo.Signature{Name: "thinkingface", Email: "noreply@thinkingface.local", When: time.Now()}
	if user != nil {
		author.Name = user.Username
		if user.Email != "" {
			author.Email = user.Email
		}
	}

	newHash, oldHash, err := s.commitThroughWAL(r.Context(), repo, gitrepo.CommitRequest{
		Branch: rev, Message: summary, Author: author, Ops: ops, ParentCommit: parentCommit,
	}, true)
	// The two conflicts below are deliberately different answers, because the
	// caller's next move differs. errWALConflict means another writer won a
	// race this request could still win: 409, retry as sent. A stale parent
	// means the branch is not where the caller believed it was: 412, and
	// retrying the identical request can only fail again -- they have to look
	// at what landed in between and decide whether their change still applies.
	//
	// 412 rather than 409 for the second one for exactly that reason: the
	// precondition is the request's own (`parentCommit`, an If-Match by
	// another name), not the server's state being momentarily busy, and 409
	// already means "retryable contention" everywhere else in this API.
	// huggingface_hub raises HfHubHTTPError for both, and reads the sentence
	// out of X-Error-Message either way, so nothing is lost on the client.
	var staleParent *gitrepo.StaleParentError
	if errors.As(err, &staleParent) {
		at := staleParent.Actual
		if at == "" {
			at = "no commits"
		}
		writeError(w, http.StatusPreconditionFailed, "stale_parent",
			"parentCommit "+parentCommit+" is not the head of "+rev+" (now at "+at+"); "+
				"fetch the branch and rebuild the commit on top of it")
		return
	}
	if errors.Is(err, errWALConflict) {
		writeError(w, http.StatusConflict, "conflict", "branch changed concurrently; retry the commit")
		return
	}
	if err != nil {
		internalError(w, "create commit", err)
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
