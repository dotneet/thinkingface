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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
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

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, errUploadTooLarge
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
	rev := chi.URLParam(r, "rev")
	if looksLikeSHA(rev) {
		badRequest(w, "uploads must target a branch, not a commit SHA")
		return
	}
	if rev == "" {
		rev = repo.DefaultBranch
	}

	mr, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "request must be multipart/form-data with one or more file parts")
		return
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
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

	user := currentUser(r.Context())
	author := gitrepo.Signature{Name: "thinkingface", Email: "noreply@thinkingface.local", When: time.Now()}
	if user != nil {
		author.Name = user.Username
		if user.Email != "" {
			author.Email = user.Email
		}
	}

	// retryOnStale=true, like the HF commit endpoint: an upload carries no
	// optimistic lock of its own, so rebuilding on a head that moved
	// underneath it is the right answer rather than a conflict the user
	// cannot act on.
	newHash, oldHash, err := s.commitThroughWAL(r.Context(), repo, gitrepo.CommitRequest{
		Branch: rev, Message: uploadSummary(paths, message, description), Author: author, Ops: ops,
	}, true)
	if errors.Is(err, errWALConflict) {
		writeError(w, http.StatusConflict, "conflict", "branch changed concurrently; retry the upload")
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

	// Otherwise the size decides, and the only way to learn it is to read.
	// Read one byte past the ceiling: that tells us the stream continued
	// without having to trust a Content-Length the part does not carry.
	limit := int64(maxUploadInlineBytes)
	if bySize {
		limit = gitrepo.LFSInlineThreshold
	}
	// Never read further than the request's remaining inline budget -- unless
	// the part is one the size check may still hand to LFS, where reading up
	// to the threshold is how that gets decided and the bytes are handed
	// straight on to storage rather than kept.
	if !bySize && limit > inlineBudget {
		limit = inlineBudget
	}
	head, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return gitrepo.Op{}, err
	}
	if int64(len(head)) > limit {
		if !bySize {
			// .gitattributes negates LFS for this path, so there is nowhere
			// for the bytes to go but a git blob -- and a blob this large is
			// exactly what LFS exists to prevent.
			return gitrepo.Op{}, errUploadTooLarge
		}
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

// storeUploadedLFS streams one part into the object store and records it
// against the repository, returning the pointer's oid and size.
//
// It is the same two-step the emulator's LFS proxy upload does, for the same
// reason: the digest -- and therefore the object's only legitimate key -- is
// not known until the last byte has been read, so the bytes land under a
// scratch key first and are copied to storage.LFSKey(oid) once it is.
//
// Ordering is deliberate and matches every other LFS path here: bytes into
// storage, *then* the index row (docs/dev/content-addressed-storage-design.md
// §3-§5). A crash between the two leaves an object no row references, which
// `thinkingface gc` reclaims; the reverse order would leave a row promising
// bytes that are not there, which nothing repairs. A part that fails midway
// leaves only the scratch key, which the bucket's own lifecycle rule on
// tmp/uploads/ drops after a day.
func (s *Server) storeUploadedLFS(r *http.Request, repo *store.Repo, src io.Reader) (string, int64, error) {
	ctx := r.Context()
	scratch, err := scratchUploadKey(repo.ID)
	if err != nil {
		return "", 0, err
	}
	hashed := newHashingReader(&limitedReader{r: src, left: maxUploadFileBytes})
	if err := s.storage.Put(ctx, scratch, hashed, "application/octet-stream"); err != nil {
		_ = s.storage.Delete(ctx, scratch)
		return "", 0, err
	}
	oid, size := hashed.Result()

	// Content-addressed, so this either creates the object or rewrites it
	// with the identical bytes; a second uploader of the same file costs a
	// copy and nothing else.
	if err := s.storage.Copy(ctx, scratch, storage.LFSKey(oid)); err != nil {
		_ = s.storage.Delete(ctx, scratch)
		return "", 0, fmt.Errorf("store lfs object: %w", err)
	}
	_ = s.storage.Delete(ctx, scratch)

	if err := s.store.RecordLFSObject(ctx, repo.ID, oid, size, func(key string) (bool, error) {
		info, err := s.storage.Stat(ctx, key)
		if errors.Is(err, storage.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return size <= 0 || info.Size == size, nil
	}); err != nil {
		return "", 0, fmt.Errorf("record lfs object: %w", err)
	}
	return oid, size, nil
}

// scratchUploadKey names a per-request scratch object. The oid cannot be part
// of it -- that is what the upload is computing -- so it is random: two
// concurrent uploads of different files must never share a key, or one would
// overwrite the other's bytes halfway through the digest.
func scratchUploadKey(repoID int64) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate upload key: %w", err)
	}
	return "tmp/uploads/web-" + strconv.FormatInt(repoID, 10) + "-" + hex.EncodeToString(nonce[:]), nil
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
	internalError(w, "store uploaded file", err)
}
