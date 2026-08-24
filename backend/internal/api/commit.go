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
	rules := s.loadLFSRules(gitRepo, chi.URLParam(r, "rev"), repo.Kind)

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
		out = append(out, result{Path: f.Path, UploadMode: mode, ShouldIgnore: false, OID: nil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
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
