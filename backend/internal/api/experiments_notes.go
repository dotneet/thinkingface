package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// Project notes (docs/dev/agent-features.md §2.7, docs/dev/api-contract.md §7
// "Project notes") are an experiment notebook kept *in the repository*, as
// {project}/NOTES.md on the default branch, so that a `git clone` carries them
// next to the metrics parquet. They are ordinary git content: the endpoints
// below are a narrow, project-addressed front for reading and committing that
// one file, and every write goes through the same commit path the web editor
// uses (commitAndSync), so the WAL, the sync job, the repo.push webhook, the
// archived-repository refusal and commit author attribution all behave as for
// a Web UI edit.
const (
	// maxNotesBytes caps the notes a PUT may store. Notes are hand- or
	// agent-written prose; anything bigger belongs in a file of its own.
	maxNotesBytes = 256 << 10
	// maxNotesBody bounds the PUT body. JSON escaping can grow content up to
	// six-fold (\u00XX for a control byte), so the body limit leaves room for
	// that on top of the content cap; the content itself is measured after
	// decoding, against maxNotesBytes.
	maxNotesBody = 6*maxNotesBytes + maxMetaBody
	// maxNotesReadBytes bounds what GET will load. It is looser than
	// maxNotesBytes because the file can also arrive by git push, which this
	// endpoint does not police; the web editor's own ceiling is reused.
	maxNotesReadBytes = maxEditBytes
	// maxNotesMessageBytes caps a caller-supplied commit message.
	maxNotesMessageBytes = 200
	// notesFileName is the file inside the project directory.
	notesFileName = "NOTES.md"
)

// notesPath is where a project's notes live in the repository. The metrics
// flush writes "{project}/metrics.parquet"; the notes sit beside it.
func notesPath(project string) string {
	return project + "/" + notesFileName
}

// notesCommitMessage is the commit message for a notes update: the caller's
// single-line message, or the default naming the project.
func notesCommitMessage(project, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "docs(experiments): update notes for " + project, nil
	}
	if len(message) > maxNotesMessageBytes {
		return "", fmt.Errorf("message must be at most %d bytes", maxNotesMessageBytes)
	}
	if !utf8.ValidString(message) {
		return "", errors.New("message must be valid UTF-8")
	}
	for _, r := range message {
		if r == '\n' || r == '\r' {
			return "", errors.New("message must be a single line")
		}
		if r < 0x20 && r != '\t' || r == 0x7f {
			return "", errors.New("message must not contain control characters")
		}
	}
	return message, nil
}

// notesProject reads and validates the {project} segment. The rules are
// ingest's (validateIngestProject): a name ingest would refuse can never have
// metrics, and one Commit would refuse as a path segment (".", "..", ".git")
// must be a 400 here rather than a 500 out of gitrepo.Commit.
func notesProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	project, ok := expNameParam(w, r, "project", "project")
	if !ok {
		return "", false
	}
	if err := validateIngestProject(project); err != nil {
		badRequest(w, "project "+err.Error())
		return "", false
	}
	return project, true
}

// notesState is what the default branch holds at the notes path.
type notesState struct {
	entry  gitrepo.Entry
	exists bool
	// head is the branch head the state was read from; zero for an empty
	// repository.
	head string
}

// statNotes resolves the notes path on the default branch. A missing file and
// an empty repository are both "no notes"; a directory or an LFS pointer at
// that path is something neither endpoint can read or write as Markdown, and
// is answered here (ok=false). unreadable is the status those two get: the
// PUT refuses them as 400, as the web editor does, while a GET -- which did
// nothing wrong -- reports the repository's state as 409.
func statNotes(w http.ResponseWriter, gitRepo *gitrepo.Repo, branch, path string, unreadable int) (notesState, bool) {
	entry, head, err := gitRepo.Stat(branch, path)
	switch {
	case err == nil:
	case errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, gitrepo.ErrEmptyRepo):
		st := notesState{}
		if !head.IsZero() {
			st.head = head.String()
		}
		return st, true
	default:
		handleStoreError(w, "stat notes", err)
		return notesState{}, false
	}
	if entry.IsDir {
		writeError(w, unreadable, statusType(unreadable), path+" is a directory, not a notes file")
		return notesState{}, false
	}
	if entry.LFS != nil {
		writeError(w, unreadable, statusType(unreadable), lfsEditRejection(path))
		return notesState{}, false
	}
	return notesState{entry: entry, exists: true, head: head.String()}, true
}

// statusType is the error type the two statuses statNotes uses answer with.
func statusType(status int) string {
	if status == http.StatusConflict {
		return "conflict"
	}
	return "bad_request"
}

// handleGetExperimentNotes answers GET /api/v1/experiments/{ns}/{repo}/{project}/notes.
func (s *Server) handleGetExperimentNotes(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadExperimentRepo(w, r)
	if !ok {
		return
	}
	project, ok := notesProject(w, r)
	if !ok {
		return
	}
	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return
	}
	path := notesPath(project)
	st, ok := statNotes(w, gitRepo, repo.DefaultBranch, path, http.StatusConflict)
	if !ok {
		return
	}
	resp := apitypes.ExpNotesResponse{Path: path, CommitSHA: st.head}
	if !st.exists {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	content, err := gitRepo.ReadBlob(st.entry.Hash, maxNotesReadBytes)
	if err != nil {
		if errors.Is(err, gitrepo.ErrBlobTooLarge) {
			conflict(w, fmt.Sprintf("%s is %d bytes, over the %d bytes the notes endpoint reads; edit it with git",
				path, st.entry.Size, maxNotesReadBytes))
			return
		}
		internalError(w, "read notes", err)
		return
	}
	resp.Content = string(content)
	resp.Exists = true
	resp.BlobSHA = st.entry.Hash.String()
	writeJSON(w, http.StatusOK, resp)
}

// handlePutExperimentNotes answers PUT /api/v1/experiments/{ns}/{repo}/{project}/notes:
// it commits the notes to the default branch.
//
// base_sha is an optimistic lock with the web editor's semantics (see
// editConflict): present and equal to the current blob, or "" for "the file
// must not exist yet", or absent to overwrite unconditionally. It is checked
// twice, like the editor's base_oid -- once up front for a readable 409, and
// again inside Commit as a PathPrecondition, against the parent the commit
// actually builds on.
//
// Unlike the web editor, the commit is retried on a stale branch
// (retryOnStale=true). The editor cannot: without a precondition a rebuilt
// commit would overwrite whatever moved the head. Here that is either what
// the caller asked for (no base_sha: overwrite) or still guarded (the
// precondition is re-evaluated against the fresh head on every attempt). And
// the branch this endpoint writes is exactly the one the metrics flusher
// commits to every few seconds while a run is live -- refusing a notes save
// with 409 every time a flush of an unrelated file won the WAL's CAS would
// make the endpoint unusable during the very runs the notes are about.
func (s *Server) handlePutExperimentNotes(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, "dataset", chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "repo")), redirectUI)
	if !ok {
		return
	}
	project, ok := notesProject(w, r)
	if !ok {
		return
	}
	var req apitypes.ExpNotesUpdateRequest
	if !decodeJSON(w, r, maxNotesBody, &req, "request body must be JSON with a content field") {
		return
	}
	if len(req.Content) > maxNotesBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("notes must be at most %d bytes", maxNotesBytes))
		return
	}
	if !utf8.ValidString(req.Content) {
		badRequest(w, "content must be valid UTF-8")
		return
	}
	message, err := notesCommitMessage(project, req.Message)
	if err != nil {
		badRequest(w, err.Error())
		return
	}

	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return
	}
	branch := repo.DefaultBranch
	if !ensureBranchRev(w, gitRepo, branch, "notes") {
		return
	}
	path := notesPath(project)
	content := []byte(req.Content)
	if s.loadLFSRules(gitRepo, branch, repo.Kind).ShouldUseLFS(path, int64(len(content))) {
		badRequest(w, lfsEditRejection(path))
		return
	}
	st, ok := statNotes(w, gitRepo, branch, path, http.StatusBadRequest)
	if !ok {
		return
	}

	currentSHA := ""
	if st.exists {
		currentSHA = st.entry.Hash.String()
	}
	var preconditions []gitrepo.PathPrecondition
	if req.BaseSHA != nil {
		base := strings.TrimSpace(*req.BaseSHA)
		if base != currentSHA {
			switch {
			case base == "":
				conflict(w, path+" already exists (blob "+currentSHA+"); re-read the notes and retry with its blob_sha")
			case !st.exists:
				conflict(w, path+" no longer exists; re-read the notes and retry with an empty base_sha")
			default:
				conflict(w, path+" changed since it was read (current blob is "+currentSHA+"); re-read the notes and retry")
			}
			return
		}
		preconditions = []gitrepo.PathPrecondition{{Path: path, OID: base}}
	}

	// Nothing to commit when the notes are unchanged: answer with the current
	// state rather than an empty commit, as the web editor does.
	if st.exists {
		if existing, err := gitRepo.ReadBlob(st.entry.Hash, maxNotesReadBytes); err == nil && string(existing) == req.Content {
			writeJSON(w, http.StatusOK, apitypes.ExpNotesResponse{
				Path: path, Content: req.Content, Exists: true, BlobSHA: currentSHA, CommitSHA: st.head,
			})
			return
		}
	}

	newHash, ok := s.commitAndSync(w, r, repo, gitrepo.CommitRequest{
		Branch: branch, Message: message, Author: commitAuthor(r.Context()),
		Ops:           []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: content}},
		Preconditions: preconditions,
	}, true, "notes update")
	if !ok {
		return
	}

	// Re-open before re-reading: commitThroughWAL may have rebuilt the
	// directory in authoritative mode, invalidating the handle taken earlier.
	// Stat at the new commit rather than the branch, which a flush may
	// already have moved on.
	gitRepo, ok = s.openGit(w, repo)
	if !ok {
		return
	}
	entry, _, err := gitRepo.Stat(newHash.String(), path)
	if err != nil {
		internalError(w, "stat committed notes", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.ExpNotesResponse{
		Path: path, Content: req.Content, Exists: true,
		BlobSHA: entry.Hash.String(), CommitSHA: newHash.String(),
	})
}
