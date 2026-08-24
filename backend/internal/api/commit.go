package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

func (s *Server) handlePreupload(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
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
	rev := chi.URLParam(r, "rev")
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

// commitLine is one NDJSON operation in the commit payload.
type commitLine struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	kind := kindFromURL(chi.URLParam(r, "repoType"))
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	user := currentUser(r.Context())
	rev := chi.URLParam(r, "rev")
	if rev == "" {
		rev = repo.DefaultBranch
	}
	// Committing to a detached SHA is meaningless; the API only writes branches.
	if looksLikeSHA(rev) {
		badRequest(w, "commits must target a branch, not a commit SHA")
		return
	}

	// No Open here: commitThroughWAL (re-)opens the repository itself, since
	// an authoritative-mode materialisation may rebuild the directory and
	// invalidate any handle taken before it.

	summary := "Upload files"
	var ops []gitrepo.Op
	var lfsOIDs []string

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
			}
			if err := json.Unmarshal(line.Value, &v); err == nil {
				if v.Summary != "" {
					summary = v.Summary
				}
				if v.Description != "" {
					summary += "\n\n" + v.Description
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
			size := v.Size
			if size == 0 {
				size = info.Size
			}
			ops = append(ops, gitrepo.Op{
				Kind: gitrepo.OpAdd, Path: v.Path, Data: gitrepo.FormatLFSPointer(v.OID, size),
			})
			lfsOIDs = append(lfsOIDs, v.OID)

		case "deletedFile":
			var v struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(line.Value, &v); err == nil && v.Path != "" {
				ops = append(ops, gitrepo.Op{Kind: gitrepo.OpDelete, Path: v.Path})
			}

		case "deletedFolder":
			var v struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(line.Value, &v); err == nil && v.Path != "" {
				ops = append(ops, gitrepo.Op{Kind: gitrepo.OpDeleteDir, Path: strings.TrimSuffix(v.Path, "/")})
			}
		}
	}

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
		Branch: rev, Message: summary, Author: author, Ops: ops,
	}, true)
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
