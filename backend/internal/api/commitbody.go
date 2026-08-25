// Parsing the NDJSON body of the HuggingFace-compatible commit endpoint.
//
// Split out of handleCommit, which had grown to hold six unrelated jobs at
// once: authorisation, this parse, the LFS ownership checks inside it, the WAL
// commit, the error mapping and the response. The body format is the external
// protocol (huggingface_hub's `_commit_api.py` writes it line by line), so it
// is worth reading on its own -- and the fifty lines of `lfsFile` checks it
// contains are the ones that decide whether a caller may commit a pointer to
// bytes somebody else uploaded.

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

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// commitPlan is one commit body, decoded: the message, the optimistic lock the
// caller may have attached, the operations in the order they arrived, and the
// LFS objects the commit will have to be linked to.
//
// copies are the operations whose source blob is not known yet -- each holds
// its index into ops, so resolveCopies can fill the hash in without disturbing
// the order the adds and deletes around it were sent in.
type commitPlan struct {
	summary      string
	parentCommit string
	ops          []gitrepo.Op
	lfsOIDs      []store.LFSObjectRef
	copies       []pendingCopy
}

// parseCommitBody reads the newline-delimited commit payload, answering the
// request itself and reporting ok=false on anything malformed.
//
// Nothing here writes: the plan it produces is applied by the caller in one
// commitThroughWAL. That is what makes refusing a bad line safe -- half a
// commit is never on disk to be undone.
func (s *Server) parseCommitBody(w http.ResponseWriter, r *http.Request, repo *store.Repo) (*commitPlan, bool) {
	plan := &commitPlan{summary: "Upload files"}

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
			return nil, false
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
				return nil, false
			}
			if v.Summary != "" {
				plan.summary = v.Summary
			}
			if v.Description != "" {
				plan.summary += "\n\n" + v.Description
			}
			if v.ParentCommit != "" {
				plan.parentCommit = strings.ToLower(strings.TrimSpace(v.ParentCommit))
				if !validParentCommit(plan.parentCommit) {
					badRequest(w, "header: parentCommit must be a commit hash, or its first 7 or more characters")
					return nil, false
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
				return nil, false
			}
			data := []byte(v.Content)
			if v.Encoding == "base64" || v.Encoding == "" {
				decoded, err := base64.StdEncoding.DecodeString(v.Content)
				if err != nil {
					if v.Encoding == "base64" {
						badRequest(w, "file "+v.Path+": content is not valid base64")
						return nil, false
					}
				} else {
					data = decoded
				}
			}
			plan.ops = append(plan.ops, gitrepo.Op{Kind: gitrepo.OpAdd, Path: v.Path, Data: data})

		case "lfsFile":
			var v struct {
				Path string `json:"path"`
				Algo string `json:"algo"`
				OID  string `json:"oid"`
				Size int64  `json:"size"`
			}
			if err := json.Unmarshal(line.Value, &v); err != nil {
				badRequest(w, "invalid lfsFile entry")
				return nil, false
			}
			size, ok := s.verifyCommitLFSFile(w, r, repo, v.Path, v.OID, v.Size)
			if !ok {
				return nil, false
			}
			plan.ops = append(plan.ops, gitrepo.Op{
				Kind: gitrepo.OpAdd, Path: v.Path, Data: gitrepo.FormatLFSPointer(v.OID, size),
			})
			// size is the object as stored, already checked against v.Size --
			// so this is the declared size, verified.
			plan.lfsOIDs = append(plan.lfsOIDs, store.LFSObjectRef{OID: v.OID, Size: size})

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
				return nil, false
			}
			plan.ops = append(plan.ops, gitrepo.Op{Kind: gitrepo.OpDelete, Path: v.Path})

		case "deletedFolder":
			var v struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(line.Value, &v); err != nil || v.Path == "" {
				badRequest(w, "deletedFolder entry must be an object with a non-empty path")
				return nil, false
			}
			plan.ops = append(plan.ops, gitrepo.Op{Kind: gitrepo.OpDeleteDir, Path: strings.TrimSuffix(v.Path, "/")})

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
				return nil, false
			}
			if v.Path == "" || v.SrcPath == "" {
				badRequest(w, "copyFile: path and srcPath are both required")
				return nil, false
			}
			// The source is resolved after the whole body has been read, so
			// the placeholder holds the operation's position in the meantime.
			plan.copies = append(plan.copies, pendingCopy{op: len(plan.ops), dst: v.Path, src: v.SrcPath, rev: v.SrcRevision})
			plan.ops = append(plan.ops, gitrepo.Op{Kind: gitrepo.OpCopy, Path: v.Path})

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
			return nil, false
		}
	}

	return plan, true
}

// verifyCommitLFSFile checks that an `lfsFile` line names an object this
// repository may point at, that the bytes are really in the bucket, and that
// any size the caller declared matches. It answers the request itself on a
// failure and reports the object's true size on success.
//
// The ownership check is the load-bearing one. Objects are content-addressed
// and shared instance-wide, so accepting a pointer because the oid happens to
// exist in the bucket would let a caller commit a pointer to bytes they were
// never given -- and from then on fetch them through their own repository's
// resolve. The normal flow (preupload -> LFS batch upload -> verify, or a
// git-lfs push) always links the object to the repository first.
func (s *Server) verifyCommitLFSFile(w http.ResponseWriter, r *http.Request,
	repo *store.Repo, path, oid string, declaredSize int64,
) (int64, bool) {
	if !gitrepo.ValidOID(oid) {
		badRequest(w, "lfsFile "+path+": oid must be a sha256 hex digest")
		return 0, false
	}
	owned, err := s.store.RepoHasLFSObject(r.Context(), repo.ID, oid)
	if err != nil {
		internalError(w, "check lfs object ownership", err)
		return 0, false
	}
	if !owned {
		badRequest(w, "lfsFile "+path+": object "+oid+" has not been uploaded")
		return 0, false
	}
	// Still confirm the bytes are actually in the bucket: a link can outlive
	// the object if a GC ran between upload and commit.
	info, err := s.storage.Stat(r.Context(), storage.LFSKey(oid))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			badRequest(w, "lfsFile "+path+": object "+oid+" has not been uploaded")
			return 0, false
		}
		internalError(w, "stat lfs object", err)
		return 0, false
	}
	// The pointer always carries the object's real size, and a declared size
	// that disagrees with it is refused rather than quietly corrected. The
	// pointer's size is what resolve declares as Content-Length before
	// streaming the object, so a client-chosen one is a client-chosen
	// truncation: net/http cuts the body off at the declared length, and a lie
	// of "1" hands every downloader a one-byte file that looks completely
	// downloaded. Too large hangs the connection instead, and either way
	// repo_files.size and the repository's total size are indexed from the
	// pointer. Omitting the field stays legal -- the object itself is the
	// source of truth -- and a caller that sends a size is simply told when it
	// does not match, rather than being ignored.
	if declaredSize != 0 && declaredSize != info.Size {
		badRequest(w, fmt.Sprintf("lfsFile %s: size %d does not match the uploaded object's %d bytes",
			path, declaredSize, info.Size))
		return 0, false
	}
	return info.Size, true
}
