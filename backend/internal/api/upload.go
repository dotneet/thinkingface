// Browser uploads: the multipart endpoint behind the web UI's "Upload files"
// dialog. It is the one write path that accepts raw bytes straight from a
// form, so it is written to stream: parts are read one at a time and an LFS
// part goes to the object store as it arrives, never through a buffer the
// size of the file. This repository holds gigabyte model weights; an
// io.ReadAll here would be an out-of-memory bug waiting for the first real
// checkpoint.

package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/lfs"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

const (
	// maxUploadFiles bounds one request. A browser upload is a handful of
	// files a person picked in a dialog; a set large enough to exceed this is
	// a `git push` or `huggingface_hub.upload_folder`, both of which have
	// their own (resumable, parallel) path into this server.
	maxUploadFiles = 64

	// maxUploadFileBytes bounds a single file. LFS puts no ceiling of its own
	// on an object, but one unbounded HTTP request is a different thing from
	// a resumable transfer: it cannot be retried from the middle, and it
	// holds a connection for its whole duration. 10 GiB is past any weights
	// file a person uploads through a browser and still small enough that a
	// stalled request is not free storage for whoever opened it.
	maxUploadFileBytes = 10 << 30

	// maxUploadInlineBytes bounds a file that is *not* routed to LFS, since
	// that one really does pass through memory on its way into a git blob.
	// It only bites when a repository's .gitattributes negates LFS for a
	// pattern (`*.csv -filter=lfs`); everything else over
	// gitrepo.LFSInlineThreshold has already gone the LFS way by the time
	// this matters.
	maxUploadInlineBytes = 32 << 20

	// maxUploadInlineTotalBytes bounds the inline files of one request taken
	// together. Every non-LFS part is held as a git blob until the commit is
	// built, so the per-file ceiling alone would still allow
	// maxUploadFiles x maxUploadInlineBytes resident at once. This is the
	// number that actually bounds the handler's memory; the LFS parts, which
	// are the large ones, never contribute to it.
	maxUploadInlineTotalBytes = 128 << 20

	// maxUploadFieldBytes bounds one non-file form field (message,
	// description, path). Commit messages are prose, not payloads.
	maxUploadFieldBytes = 8 << 10
)

// errUploadTooLarge is what a part's reader returns once it has produced
// maxUploadFileBytes. It travels back out through storage.Put, which wraps
// reader errors, so the handler answers 413 instead of a blind 500.
var errUploadTooLarge = errors.New("api: uploaded file is too large")

// limitedReader is io.LimitedReader with an error instead of a silent EOF.
// Truncating a model file to the limit and committing the result would be
// worse than refusing it: the pointer would name a digest of bytes nobody
// meant to upload.
type limitedReader struct {
	r    io.Reader
	left int64
}

// Read enforces an *inclusive* cap: a file of exactly the limit is fine, only
// a byte past it is too large. That distinction has to be made by asking for
// one more byte rather than by assuming, because io.Copy always issues a
// follow-up Read after filling its buffer, and a part that ends exactly on the
// cap answers that Read with EOF. Failing on `left == 0` alone would reject
// every upload of exactly the documented ceiling.
func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		var probe [1]byte
		for {
			n, err := l.r.Read(probe[:])
			if n > 0 {
				return 0, errUploadTooLarge
			}
			if err != nil {
				return 0, err
			}
			// (0, nil) is legal but says nothing; ask again rather than
			// reading it as either an overflow or an end.
		}
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

// uploadSummary builds the commit message for an upload: the caller's own, or
// one that names what arrived.
func uploadSummary(paths []string, message, description string) string {
	summary := message
	if summary == "" {
		switch len(paths) {
		case 0:
			summary = "Upload files"
		case 1:
			summary = "Upload " + paths[0]
		default:
			summary = fmt.Sprintf("Upload %d files", len(paths))
		}
	}
	if description != "" {
		summary += "\n\n" + description
	}
	return summary
}

// uploadPath picks the repository path for one file part: the next unconsumed
// "path" field, or the browser's own file name. Paths are consumed in arrival
// order because the body is read as a stream -- a "path" field can only bind
// to a file that comes after it.
func uploadPath(pending []string, fileName string) (target string, rest []string) {
	if len(pending) > 0 {
		return pending[0], pending[1:]
	}
	return fileName, pending
}

// cleanUploadPath normalises and validates a target path. It is the same
// check gitrepo.Commit would apply, run early so a traversal attempt is a 400
// before a single byte is stored rather than a 500 after the whole upload.
func cleanUploadPath(raw string) (string, error) {
	p := strings.Trim(strings.TrimSpace(raw), "/")
	if p == "" {
		return "", errors.New("every file needs a path")
	}
	if err := gitrepo.ValidatePath(p); err != nil {
		return "", err
	}
	return p, nil
}

// lfsRoute decides where a file goes without knowing its size yet, which is
// the whole difficulty of streaming multipart: a part carries no length.
//
// It asks gitrepo's own rules twice rather than reimplementing them. A
// .gitattributes pattern wins over size in ShouldUseLFS, so probing at size 0
// and at the threshold separates the three cases: forced (a pattern says
// lfs), never (a pattern negates lfs), and undecided (no pattern -- size
// decides, and the caller finds that out by reading).
func lfsRoute(rules *gitrepo.LFSRules, path string) (forced, bySize bool) {
	return rules.ShouldUseLFS(path, 0), rules.ShouldUseLFS(path, gitrepo.LFSInlineThreshold)
}

// handleUploadFiles commits files picked in the browser. Every part becomes
// one op and all of them land in a single commit, so uploading three files
// produces one entry in the history rather than three.
func (s *Server) handleUploadFiles(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForWrite(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	if looksLikeSHA(rev) {
		badRequest(w, "uploads must target a branch, not a commit SHA")
		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "request must be multipart/form-data with one or more file parts")
		return
	}

	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return
	}
	// Before a single part is read: an upload to a rev that is a tag would be
	// stored on a branch nobody reads (see ensureBranchRev), and the LFS parts
	// would already be in the bucket by the time anyone noticed.
	if !ensureBranchRev(w, gitRepo, rev, "uploads") {
		return
	}
	rules := s.loadLFSRules(gitRepo, rev, repo.Kind)

	var (
		message     string
		description string
		pending     []string
		ops         []gitrepo.Op
		paths       []string
		// inline is the running total of bytes held in memory as git blobs.
		inline int64
	)

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			badRequest(w, "malformed multipart body")
			return
		}

		field := part.FormName()
		if field != "file" {
			value, err := readUploadField(part)
			if errors.Is(err, errUploadTooLarge) {
				badRequest(w, "form field "+field+" must be at most "+
					strconv.Itoa(maxUploadFieldBytes)+" bytes")
				return
			}
			if err != nil {
				badRequest(w, "malformed multipart body")
				return
			}
			switch field {
			case "message":
				message = value
			case "description":
				description = value
			case "path":
				pending = append(pending, value)
			}
			// Anything else is ignored: a browser form may carry fields this
			// endpoint has no opinion about.
			continue
		}

		if len(paths) >= maxUploadFiles {
			badRequest(w, fmt.Sprintf("at most %d files can be uploaded in one request", maxUploadFiles))
			return
		}
		// Quote the value as given -- which is the "path" field when there
		// was one, not necessarily the browser's file name -- so the message
		// names the thing the caller actually has to fix.
		var raw string
		raw, pending = uploadPath(pending, part.FileName())
		target, err := cleanUploadPath(raw)
		if err != nil {
			badRequest(w, "invalid upload path "+strconv.Quote(raw)+
				"; paths are relative to the repository root and may not escape it or write inside .git")
			return
		}

		op, err := s.readUploadPart(r, repo, rules, target, part, maxUploadInlineTotalBytes-inline)
		if err != nil {
			s.writeUploadPartError(w, target, err)
			return
		}
		inline += int64(len(op.Data))
		ops = append(ops, op)
		paths = append(paths, target)
	}

	if len(ops) == 0 {
		badRequest(w, "upload contains no file parts")
		return
	}

	author := commitAuthor(r.Context())

	// retryOnStale=true, like the HF commit endpoint: an upload carries no
	// optimistic lock of its own, so rebuilding on a head that moved
	// underneath it is the right answer rather than a conflict the user
	// cannot act on.
	newHash, oldHash, err := s.commitThroughWAL(r.Context(), repo, gitrepo.CommitRequest{
		Branch: rev, Message: uploadSummary(paths, message, description), Author: author, Ops: ops,
	}, true)
	if writeCommitError(w, err, "upload") {
		return
	}
	if err := s.sync.Enqueue(r.Context(), repo.ID, rev, oldHash.String(), newHash.String()); err != nil {
		internalError(w, "schedule sync", err)
		return
	}

	writeJSON(w, http.StatusOK, apitypes.UploadFilesResponse{CommitOID: newHash.String(), Paths: paths})
}

// readUploadField drains one small text field.
func readUploadField(part *multipart.Part) (string, error) {
	value, err := io.ReadAll(io.LimitReader(part, maxUploadFieldBytes+1))
	if err != nil {
		return "", err
	}
	if len(value) > maxUploadFieldBytes {
		return "", errUploadTooLarge
	}
	return strings.TrimSpace(string(value)), nil
}

// readUploadPart turns one file part into a commit op, routing it to LFS or
// to a plain git blob. Only the plain-blob branch ever holds the file in
// memory, and only up to maxUploadInlineBytes.
// inlineBudget is what is left of maxUploadInlineTotalBytes; a non-LFS part
// larger than that is refused rather than pushing the request past its memory
// bound.
func (s *Server) readUploadPart(r *http.Request, repo *store.Repo, rules *gitrepo.LFSRules,
	target string, part *multipart.Part, inlineBudget int64,
) (gitrepo.Op, error) {
	forced, bySize := lfsRoute(rules, target)

	// Forced by .gitattributes: no need to look at a single byte first.
	if forced {
		oid, size, err := s.storeUploadedLFS(r, repo, part)
		if err != nil {
			return gitrepo.Op{}, err
		}
		return gitrepo.Op{Kind: gitrepo.OpAdd, Path: target, Data: gitrepo.FormatLFSPointer(oid, size)}, nil
	}

	// Otherwise size decides, and the only way to learn it is to read.
	if bySize {
		// Read exactly the threshold. Filling the buffer means the file is
		// at least LFSInlineThreshold bytes, which is precisely
		// ShouldUseLFS's `size >= LFSInlineThreshold` -- the comparison has
		// to match it exactly, or a file of exactly 10MiB takes a different
		// route here than it does through preupload and lands in the object
		// database as a large blob the contract says belongs in LFS.
		// Reading *up to* the threshold (rather than one byte past it)
		// leaves the two indistinguishable cases -- exactly the threshold,
		// and more than it -- on the same side, which is where they belong.
		head, err := io.ReadAll(io.LimitReader(part, gitrepo.LFSInlineThreshold))
		if err != nil {
			return gitrepo.Op{}, err
		}
		if int64(len(head)) >= gitrepo.LFSInlineThreshold {
			oid, size, err := s.storeUploadedLFS(r, repo, io.MultiReader(bytes.NewReader(head), part))
			if err != nil {
				return gitrepo.Op{}, err
			}
			return gitrepo.Op{Kind: gitrepo.OpAdd, Path: target, Data: gitrepo.FormatLFSPointer(oid, size)}, nil
		}
		if int64(len(head)) > inlineBudget {
			return gitrepo.Op{}, errUploadTooLarge
		}
		return gitrepo.Op{Kind: gitrepo.OpAdd, Path: target, Data: head}, nil
	}

	// .gitattributes negates LFS for this path, so there is nowhere for the
	// bytes to go but a git blob however big the file turns out to be. The
	// ceilings are caps rather than thresholds, so a file of exactly the cap
	// is allowed and one byte more is refused -- hence reading one past it.
	limit := int64(maxUploadInlineBytes)
	if limit > inlineBudget {
		limit = inlineBudget
	}
	head, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return gitrepo.Op{}, err
	}
	if int64(len(head)) > limit {
		return gitrepo.Op{}, errUploadTooLarge
	}
	return gitrepo.Op{Kind: gitrepo.OpAdd, Path: target, Data: head}, nil
}

// storeUploadedLFS streams one part into the object store and records it
// against the repository, returning the pointer's oid and size.
//
// The bytes land under storage.LFSIncomingKey first because the digest --
// and therefore the object's only legitimate key -- is not known until the
// last byte has been read. That is the one thing this path does differently
// from every other LFS upload, where the client declares the oid up front;
// from there it hands over to lfs.PromoteStagedFrom, so the size check, the
// server-side copy onto the content-addressed key, the link, the staging
// cleanup and their ordering are the sequence the batch/verify and emulator
// proxy paths run, not a second copy of it.
//
// Nothing streams through this process twice and nothing is buffered: Put
// writes as the part arrives, and the promotion is a server-side copy.
func (s *Server) storeUploadedLFS(r *http.Request, repo *store.Repo, src io.Reader) (string, int64, error) {
	ctx := r.Context()
	staging, err := incomingUploadKey(repo.ID)
	if err != nil {
		return "", 0, err
	}
	hashed := newHashingReader(&limitedReader{r: src, left: maxUploadFileBytes})
	if err := s.storage.Put(ctx, staging, hashed, "application/octet-stream"); err != nil {
		// Best effort: an object left here is swept by `thinkingface gc`'s
		// staging pass once the grace period is up, which is exactly what
		// that pass exists for.
		_ = s.storage.Delete(ctx, staging)
		return "", 0, err
	}
	oid, size := hashed.Result()

	if err := s.lfs.PromoteStagedFrom(ctx, repo.ID, oid, size, staging); err != nil {
		_ = s.storage.Delete(ctx, staging)
		return "", 0, fmt.Errorf("publish lfs object: %w", err)
	}
	return oid, size, nil
}

// incomingUploadKey names the staging object one part is streamed into. The
// oid cannot be part of it -- that is what the upload is computing -- so it is
// random: two concurrent uploads must never share a key, or one would
// overwrite the other's bytes halfway through the digest.
func incomingUploadKey(repoID int64) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate upload key: %w", err)
	}
	return storage.LFSIncomingKey(repoID, hex.EncodeToString(nonce[:])), nil
}

// writeUploadPartError maps a failed part onto a response. Only the size
// ceiling is the caller's fault; everything else is storage or the database,
// whose error text names buckets and connections and never reaches a client.
func (s *Server) writeUploadPartError(w http.ResponseWriter, target string, err error) {
	if errors.Is(err, errUploadTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
			fmt.Sprintf("%s is too large; a single file may be at most %d bytes, and files not stored in LFS at most %d bytes each and %d bytes per request",
				target, int64(maxUploadFileBytes), int64(maxUploadInlineBytes), int64(maxUploadInlineTotalBytes)))
		return
	}
	// The namespace has no room for this file. 507 rather than 413: the request
	// is not too large in itself, it is the account that is full, and RFC 4918
	// reserves this status for exactly that -- the same one the LFS batch
	// endpoint answers a `git push` with, so both ways into a repository refuse
	// an over-quota upload identically.
	//
	// The type is not one frontend/lib/api-error-message.ts maps, so the Web UI
	// renders the message verbatim. That is the right fallback here: the
	// sentence names the namespace, the limit and the shortfall, which no
	// generic translated string could.
	var overQuota *lfs.QuotaExceededError
	if errors.As(err, &overQuota) {
		writeError(w, http.StatusInsufficientStorage, "insufficient_storage", overQuota.Error())
		return
	}
	// A garbage collection that removed the object between the copy and the
	// link is contention, not a fault: the same bytes uploaded again succeed.
	// The emulator's LFS proxy upload answers the identical condition with a
	// conflict, so this one does too rather than telling the browser the
	// server broke.
	if errors.Is(err, store.ErrLFSObjectGone) || errors.Is(err, lfs.ErrNotStaged) {
		writeError(w, http.StatusConflict, "conflict",
			target+" was removed from storage while it was being uploaded; retry the upload")
		return
	}
	internalError(w, "store uploaded file", err)
}
