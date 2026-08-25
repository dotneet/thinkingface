package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// maxEditBytes bounds an in-browser edit. Unlike maxCommitBody, this endpoint
// only ever handles small text files edited by hand, so the limit is far
// tighter; anything bigger belongs in an LFS upload anyway.
const maxEditBytes = 2 << 20

// commitSummary builds the commit message for an edit: the caller's message,
// or a generic default keyed on the path, plus an optional body.
func commitSummary(path, message, description string) string {
	summary := message
	if summary == "" {
		summary = "Update " + path
	}
	if description != "" {
		summary += "\n\n" + description
	}
	return summary
}

// editConflict checks the caller's base_oid, captured when they opened the
// editor, against the path's current state. An empty base_oid means the
// caller isn't tracking staleness (e.g. creating a new file) and is always
// accepted.
func editConflict(baseOID string, exists bool, currentOID string) (message string, isConflict bool) {
	if baseOID == "" {
		return "", false
	}
	if !exists {
		return "the file no longer exists at this revision", true
	}
	if currentOID != baseOID {
		return "the file changed since you started editing (current blob is " + currentOID + ")", true
	}
	return "", false
}

// lfsEditRejection is the message returned when a path can't be edited
// in-browser because it is (or would become) an LFS-tracked object. The web
// editor only ever writes small text blobs directly into git.
func lfsEditRejection(path string) string {
	return fmt.Sprintf("%s is tracked by Git LFS and can't be edited from the web UI; use git or huggingface_hub instead", path)
}

// handleEditFile lets the web UI save small text-file edits straight to a
// branch. It is a shortcut around the NDJSON commit protocol huggingface_hub
// clients use, meant only for one file at a time from the browser.
func (s *Server) handleEditFile(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	path := wildcardPath(r)
	if path == "" {
		badRequest(w, "no file path given")
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	// Committing to a detached SHA is meaningless; the API only writes branches.
	if looksLikeSHA(rev) {
		badRequest(w, "edits must target a branch, not a commit SHA")
		return
	}

	var req apitypes.EditFileRequest
	// decodeJSON, like every other JSON endpoint: an oversized body is a 413
	// payload_too_large rather than a 400, and the decoder's own text never
	// reaches the caller.
	if !decodeJSON(w, r, maxEditBytes, &req, "request body must be JSON with a content field") {
		return
	}
	content := []byte(req.Content)
	if !utf8.ValidString(req.Content) {
		badRequest(w, "content must be valid UTF-8")
		return
	}
	summary := commitSummary(path, req.Message, req.Description)

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}

	if !ensureBranchRev(w, gitRepo, rev, "edits") {
		return
	}

	rules := s.loadLFSRules(gitRepo, rev, repo.Kind)
	if rules.ShouldUseLFS(path, int64(len(content))) {
		badRequest(w, lfsEditRejection(path))
		return
	}

	// Optimistic locking: read the path's current state before writing, so a
	// stale base_oid is rejected instead of overwriting someone else's commit.
	entry, headCommit, err := gitRepo.Stat(rev, path)
	exists := true
	switch {
	case err == nil:
		if entry.IsDir {
			badRequest(w, path+" is a directory")
			return
		}
		if entry.LFS != nil {
			badRequest(w, lfsEditRejection(path))
			return
		}
	case errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, gitrepo.ErrEmptyRepo):
		exists = false
	default:
		handleStoreError(w, "stat file", err)
		return
	}

	currentOID := ""
	if exists {
		currentOID = entry.Hash.String()
	}
	if message, isConflict := editConflict(req.BaseOID, exists, currentOID); isConflict {
		writeError(w, http.StatusConflict, "conflict", message)
		return
	}

	if exists {
		// Nothing to commit when the save didn't actually change the file;
		// report the current head instead of creating an empty commit.
		existing, err := gitRepo.ReadBlob(entry.Hash, maxEditBytes)
		if err == nil && string(existing) == req.Content {
			writeJSON(w, http.StatusOK, apitypes.EditFileResponse{
				Path: path, CommitOID: headCommit.String(), OID: entry.Hash.String(), Size: entry.Size,
			})
			return
		}
	}

	user := currentUser(r.Context())
	author := gitrepo.Signature{Name: "thinkingface", Email: "noreply@thinkingface.local", When: time.Now()}
	if user != nil {
		author.Name = user.Username
		if user.Email != "" {
			author.Email = user.Email
		}
	}

	// retryOnStale=false: this endpoint's base_oid check is an optimistic
	// lock, and rebuilding on a concurrently moved head would overwrite the
	// very change that check exists to catch. Stale surfaces as 409 instead.
	//
	// The Precondition repeats the base_oid check *inside* Commit, under the
	// mutex that picks the parent: the early check above gives a friendly
	// message, but only this one is atomic — commitThroughWAL re-opens (and
	// may re-materialise) the repository before committing, so a concurrent
	// push landing in that window would otherwise become the parent and
	// slip past a check made against the old head.
	//
	// Only when the caller supplied a base_oid, though: an empty one means
	// "not tracking staleness" (see editConflict), which must keep allowing
	// overwrites of existing files. An unconditional precondition would read
	// the empty OID as "path must be absent" and turn those into 409s.
	var preconditions []gitrepo.PathPrecondition
	if req.BaseOID != "" {
		preconditions = []gitrepo.PathPrecondition{{Path: path, OID: req.BaseOID}}
	}
	newHash, oldHash, err := s.commitThroughWAL(r.Context(), repo, gitrepo.CommitRequest{
		Branch: rev, Message: summary, Author: author,
		Ops:           []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: content}},
		Preconditions: preconditions,
	}, false)
	var stale *gitrepo.StalePathError
	if errors.As(err, &stale) {
		writeError(w, http.StatusConflict, "conflict",
			path+" changed concurrently; re-read the file and retry with its current oid")
		return
	}
	if errors.Is(err, errWALConflict) {
		writeError(w, http.StatusConflict, "conflict", "branch changed concurrently; retry the edit")
		return
	}
	if err != nil {
		internalError(w, "create commit", err)
		return
	}
	if err := s.sync.Enqueue(r.Context(), repo.ID, rev, oldHash.String(), newHash.String()); err != nil {
		internalError(w, "schedule sync", err)
		return
	}

	// Re-stat rather than hashing locally: it confirms the write landed where
	// we think it did and picks up the mode git actually stored. Re-open
	// first: commitThroughWAL may have rebuilt the directory in
	// authoritative mode, invalidating the handle taken earlier.
	gitRepo, err = s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "reopen git repository", err)
		return
	}
	newEntry, _, err := gitRepo.Stat(rev, path)
	if err != nil {
		internalError(w, "stat committed file", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.EditFileResponse{
		Path: path, CommitOID: newHash.String(), OID: newEntry.Hash.String(), Size: newEntry.Size,
	})
}

// deleteSummary builds the commit message for a deletion, mirroring
// commitSummary's shape with a verb that says what actually happened.
func deleteSummary(path, message, description string) string {
	summary := message
	if summary == "" {
		summary = "Delete " + path
	}
	if description != "" {
		summary += "\n\n" + description
	}
	return summary
}

// decodeOptionalJSON is decodeJSON for a body that may legitimately be absent:
// DELETE carries its options in a body, but "delete this path, no message, no
// staleness check" is a request with nothing to say. An empty body leaves v at
// its zero value; anything present still has to parse and still has to fit.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, v any, badMsg string) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("request body must be at most %d bytes", maxBytes))
			return false
		}
		badRequest(w, badMsg)
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		badRequest(w, badMsg)
		return false
	}
	return true
}

// handleDeleteFile removes a single file in a commit of its own. It is the
// mirror of handleEditFile and shares its rules -- branch-only revisions,
// base_oid as an optimistic lock, one path per request -- with one deliberate
// difference: an LFS-tracked path *may* be deleted. Editing one is refused
// because the browser would be writing pointer bytes it cannot produce;
// deleting one only drops the pointer from the tree. The object itself stays
// in the bucket, content-addressed and shared, until `thinkingface gc` finds
// that nothing references it (docs/dev/content-addressed-storage-design.md §5).
func (s *Server) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	path := wildcardPath(r)
	if path == "" {
		badRequest(w, "no file path given")
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	if looksLikeSHA(rev) {
		badRequest(w, "deletions must target a branch, not a commit SHA")
		return
	}

	var req apitypes.DeleteFileRequest
	if !decodeOptionalJSON(w, r, maxMetaBody, &req, "request body must be JSON") {
		return
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}

	if !ensureBranchRev(w, gitRepo, rev, "deletions") {
		return
	}

	// Read the path's current state first: a delete of something that is not
	// there is a 404, not an empty commit, and a stale base_oid is a 409.
	entry, _, err := gitRepo.Stat(rev, path)
	switch {
	case err == nil:
		if entry.IsDir {
			badRequest(w, path+" is a directory; delete its files individually")
			return
		}
	case errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, gitrepo.ErrEmptyRepo):
		notFound(w, path+" does not exist at "+rev)
		return
	default:
		handleStoreError(w, "stat file", err)
		return
	}

	if message, isConflict := editConflict(req.BaseOID, true, entry.Hash.String()); isConflict {
		writeError(w, http.StatusConflict, "conflict", message)
		return
	}

	user := currentUser(r.Context())
	author := gitrepo.Signature{Name: "thinkingface", Email: "noreply@thinkingface.local", When: time.Now()}
	if user != nil {
		author.Name = user.Username
		if user.Email != "" {
			author.Email = user.Email
		}
	}

	// Same reasoning as handleEditFile: the precondition repeats the base_oid
	// check under the mutex that picks the parent, and retryOnStale is false
	// so a concurrently moved head surfaces as 409 instead of quietly
	// deleting a version the caller never saw.
	var preconditions []gitrepo.PathPrecondition
	if req.BaseOID != "" {
		preconditions = []gitrepo.PathPrecondition{{Path: path, OID: req.BaseOID}}
	}
	newHash, oldHash, err := s.commitThroughWAL(r.Context(), repo, gitrepo.CommitRequest{
		Branch: rev, Message: deleteSummary(path, req.Message, req.Description), Author: author,
		Ops:           []gitrepo.Op{{Kind: gitrepo.OpDelete, Path: path}},
		Preconditions: preconditions,
	}, false)
	var stale *gitrepo.StalePathError
	if errors.As(err, &stale) {
		writeError(w, http.StatusConflict, "conflict",
			path+" changed concurrently; re-read the file and retry with its current oid")
		return
	}
	if errors.Is(err, errWALConflict) {
		writeError(w, http.StatusConflict, "conflict", "branch changed concurrently; retry the deletion")
		return
	}
	if err != nil {
		internalError(w, "create commit", err)
		return
	}
	if err := s.sync.Enqueue(r.Context(), repo.ID, rev, oldHash.String(), newHash.String()); err != nil {
		internalError(w, "schedule sync", err)
		return
	}

	// The same shape handleEditFile answers with, so one client type covers
	// both. oid/size describe the file that is no longer there: empty and 0.
	writeJSON(w, http.StatusOK, apitypes.EditFileResponse{Path: path, CommitOID: newHash.String()})
}

// renameSummary builds the commit message for a rename, mirroring
// commitSummary's shape with a verb that says what actually happened.
func renameSummary(oldPath, newPath, message, description string) string {
	summary := message
	if summary == "" {
		summary = "Rename " + oldPath + " to " + newPath
	}
	if description != "" {
		summary += "\n\n" + description
	}
	return summary
}

// lfsRenameRejection is the message returned when the source and the
// destination of a rename disagree about LFS routing. Moving a pointer to a
// path .gitattributes does not track leaves the pointer's text sitting there
// as ordinary file content -- git clients would never smudge it -- and moving
// ordinary bytes onto a tracked path is the same mistake in reverse.
func lfsRenameRejection(oldPath, newPath string) string {
	return fmt.Sprintf("%s and %s are not tracked the same way by Git LFS; "+
		"move the file with git, or adjust .gitattributes first", oldPath, newPath)
}

// handleRenameFile moves one file to a new path in a single commit. It is the
// third of the browser's write endpoints and follows handleEditFile's and
// handleDeleteFile's rules exactly -- branch-only revisions, base_oid as an
// optimistic lock, one path per request -- with two rename-specific ones:
//
//   - The destination must be free. An occupied path is a 409 rather than a
//     silent overwrite: the browser asks for "move this file over there", and
//     "there" already holding something is the caller's mistake to see, not
//     a version of somebody else's file to destroy. That check is repeated as
//     an absent-precondition inside Commit, so a file appearing at the
//     destination between the two is still refused instead of overwritten.
//   - An LFS-tracked file may be renamed, unlike edited. The tree entry is
//     copied by hash (gitrepo.OpCopy), so a pointer moves as a pointer, the
//     object in the bucket is neither read nor re-uploaded, and the set of
//     LFS oids this repository references is unchanged -- which is why no
//     store.LinkLFSObjects call belongs here.
func (s *Server) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	oldPath := wildcardPath(r)
	if oldPath == "" {
		badRequest(w, "no file path given")
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	if looksLikeSHA(rev) {
		badRequest(w, "renames must target a branch, not a commit SHA")
		return
	}

	var req apitypes.RenameFileRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with a new_path field") {
		return
	}
	// cleanUploadPath is the upload endpoint's check, which is gitrepo's own
	// (ValidatePath): no traversal, no absolute path, nothing under .git.
	// Reused rather than restated so every browser-facing write agrees on
	// what a path may be.
	newPath, err := cleanUploadPath(req.NewPath)
	if err != nil {
		badRequest(w, "new_path: "+err.Error())
		return
	}
	if newPath == oldPath {
		badRequest(w, "new_path is the file's current path")
		return
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	if !ensureBranchRev(w, gitRepo, rev, "renames") {
		return
	}

	entry, _, err := gitRepo.Stat(rev, oldPath)
	switch {
	case err == nil:
		if entry.IsDir {
			badRequest(w, oldPath+" is a directory; rename its files individually")
			return
		}
		// A symlink's blob holds a path, not content, so moving it to another
		// directory silently changes what it resolves to. Refused for the
		// same reason copyFile refuses one (commit.go).
		if entry.Mode == filemode.Symlink {
			badRequest(w, oldPath+" is a symlink, which cannot be renamed from the web UI")
			return
		}
	case errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, gitrepo.ErrEmptyRepo):
		notFound(w, oldPath+" does not exist at "+rev)
		return
	default:
		handleStoreError(w, "stat file", err)
		return
	}

	if message, isConflict := editConflict(req.BaseOID, true, entry.Hash.String()); isConflict {
		writeError(w, http.StatusConflict, "conflict", message)
		return
	}

	// The destination must be free -- including as a directory, which would
	// otherwise turn into "write a file inside it".
	if _, _, err := gitRepo.Stat(rev, newPath); err == nil {
		conflict(w, newPath+" already exists; delete it first or pick another path")
		return
	} else if !errors.Is(err, gitrepo.ErrPathNotFound) && !errors.Is(err, gitrepo.ErrEmptyRepo) {
		handleStoreError(w, "stat destination", err)
		return
	}

	// Both paths are weighed at the file's own size, so the comparison only
	// reports a disagreement .gitattributes actually causes: a large plain
	// blob is above the inline threshold at either path and does not trip
	// this, and neither does a pointer moving between two tracked patterns.
	rules := s.loadLFSRules(gitRepo, rev, repo.Kind)
	size := entry.TargetSize()
	if rules.ShouldUseLFS(oldPath, size) != rules.ShouldUseLFS(newPath, size) {
		badRequest(w, lfsRenameRejection(oldPath, newPath))
		return
	}

	user := currentUser(r.Context())
	author := gitrepo.Signature{Name: "thinkingface", Email: "noreply@thinkingface.local", When: time.Now()}
	if user != nil {
		author.Name = user.Username
		if user.Email != "" {
			author.Email = user.Email
		}
	}

	// The destination precondition is unconditional (an empty OID asserts the
	// path is absent) because "the destination is free" is this endpoint's
	// rule rather than the caller's claim; the source one, like everywhere
	// else here, exists only when the caller is tracking staleness.
	preconditions := []gitrepo.PathPrecondition{{Path: newPath, OID: ""}}
	if req.BaseOID != "" {
		preconditions = append(preconditions, gitrepo.PathPrecondition{Path: oldPath, OID: req.BaseOID})
	}
	// OpCopy then OpDelete, in that order and in one commit: the copy is a
	// hash copy, so nothing is read or rewritten however big the file is, and
	// the history records one rename rather than the create-then-delete pair
	// the browser used to have to send.
	newHash, oldHash, err := s.commitThroughWAL(r.Context(), repo, gitrepo.CommitRequest{
		Branch: rev, Message: renameSummary(oldPath, newPath, req.Message, req.Description), Author: author,
		Ops: []gitrepo.Op{
			{Kind: gitrepo.OpCopy, Path: newPath, SrcHash: entry.Hash, Executable: entry.Mode == filemode.Executable},
			{Kind: gitrepo.OpDelete, Path: oldPath},
		},
		Preconditions: preconditions,
	}, false)
	var stale *gitrepo.StalePathError
	if errors.As(err, &stale) {
		if stale.Path == newPath {
			conflict(w, newPath+" appeared concurrently; pick another path and retry")
			return
		}
		writeError(w, http.StatusConflict, "conflict",
			oldPath+" changed concurrently; re-read the file and retry with its current oid")
		return
	}
	if errors.Is(err, errWALConflict) {
		writeError(w, http.StatusConflict, "conflict", "branch changed concurrently; retry the rename")
		return
	}
	if err != nil {
		internalError(w, "create commit", err)
		return
	}
	if err := s.sync.Enqueue(r.Context(), repo.ID, rev, oldHash.String(), newHash.String()); err != nil {
		internalError(w, "schedule sync", err)
		return
	}

	// Re-stat for the same reason handleEditFile does: it confirms the entry
	// landed where we think it did, and commitThroughWAL may have rebuilt the
	// directory underneath the handle taken earlier.
	gitRepo, err = s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "reopen git repository", err)
		return
	}
	newEntry, _, err := gitRepo.Stat(rev, newPath)
	if err != nil {
		internalError(w, "stat renamed file", err)
		return
	}
	// TargetSize, not the blob's: for an LFS file the caller wants the size
	// of the object it points at, which is what the tree listing shows too.
	writeJSON(w, http.StatusOK, apitypes.RenameFileResponse{
		Path: newPath, OldPath: oldPath, CommitOID: newHash.String(),
		OID: newEntry.Hash.String(), Size: newEntry.TargetSize(),
	})
}
