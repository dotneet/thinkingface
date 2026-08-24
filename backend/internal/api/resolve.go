package api

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/lfs"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

const maxRawPreviewBytes = 512 << 10

// executableTypes are the media types a browser will run script from when it
// renders them. resolve serves whatever a user pushed, so a repository with a
// single .html or .svg in it would otherwise be a stored XSS on the API
// origin -- same origin as the session cookie, which is game over.
//
// image/svg+xml is deliberately absent: an <img src=...> of an SVG runs no
// script, and the repository file browser embeds README images through
// exactly that path. Its top-level case is covered by Content-Disposition
// instead, which is the defence that actually matters here.
var executableTypes = map[string]bool{
	"text/html":              true,
	"application/xhtml+xml":  true,
	"application/xml":        true,
	"text/xml":               true,
	"application/rdf+xml":    true,
	"application/mathml+xml": true,
	"text/vtt":               true,
}

// safeContentType downgrades the types a browser would execute. The extension
// is attacker-controlled, so the guess coming out of mime.TypeByExtension is
// too.
func safeContentType(contentType string) string {
	base, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "application/octet-stream"
	}
	if executableTypes[strings.ToLower(base)] {
		return "application/octet-stream"
	}
	return contentType
}

// attachmentDisposition builds the Content-Disposition for a download.
//
// `attachment` is what stops a top-level navigation to a pushed .html or .svg
// from rendering in the API origin: the browser saves it instead. It does not
// affect subresource loads, so <img src=".../resolve/main/plot.png"> in a
// rendered README keeps working. The LFS signed-URL path already asks GCS for
// the same header (storage/gcs.go), so this brings the git-blob path in line
// with it rather than inventing a new policy.
//
// Both the quoted ASCII form and RFC 5987's filename* are emitted: the first
// is what every client understands, the second is what carries a non-ASCII
// name intact.
func attachmentDisposition(name string) string {
	ascii := make([]rune, 0, len(name))
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			ascii = append(ascii, '_')
			continue
		}
		ascii = append(ascii, r)
	}
	fallback := string(ascii)
	if fallback == "" {
		fallback = "download"
	}
	return "attachment; filename=\"" + fallback + "\"; filename*=UTF-8''" + url.PathEscape(name)
}

// handleResolve serves a file at a revision. Regular blobs stream from git;
// LFS files redirect to a signed URL, or are proxied when the storage driver
// cannot sign (the local emulator).
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request, kind string) {
	ns := chi.URLParam(r, "ns")
	name := repoName(chi.URLParam(r, "name"))
	repo, ok := s.loadRepoForRead(w, r, kind, ns, name, redirectHF)
	if !ok {
		return
	}
	filePath := wildcardPath(r)
	if filePath == "" {
		badRequest(w, "no file path given")
		return
	}

	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	rev := chi.URLParam(r, "rev")
	entry, commit, err := gitRepo.Stat(rev, filePath)
	if err != nil {
		if errors.Is(err, gitrepo.ErrPathNotFound) || errors.Is(err, gitrepo.ErrEmptyRepo) {
			notFound(w, filePath+" does not exist at revision "+rev)
			return
		}
		internalError(w, "stat file", err)
		return
	}
	if entry.IsDir {
		badRequest(w, filePath+" is a directory")
		return
	}

	// One resolve request is one count for the 30-day download-stats table,
	// whether it is a HEAD, a plain GET, or a redirect to an LFS signed URL --
	// the client's actual transfer against GCS never touches this server, so
	// this is the only point that ever sees the request.
	s.recordDownload(r.Context(), repo.ID)

	w.Header().Set("X-Repo-Commit", commit.String())
	w.Header().Set("Accept-Ranges", "bytes")
	contentType := mime.TypeByExtension(path.Ext(filePath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	contentType = safeContentType(contentType)
	// nosniff is also set globally by the securityHeaders middleware; repeated
	// here because this handler is the reason it exists and the pairing with
	// the downgrade above should not be separable by an unrelated refactor.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", attachmentDisposition(path.Base(filePath)))

	if entry.LFS != nil {
		s.serveLFSFile(w, r, repo, entry, contentType)
		return
	}

	w.Header().Set("ETag", `"`+entry.Hash.String()+`"`)
	w.Header().Set("Content-Type", contentType)
	if r.Method == http.MethodHead {
		// A HEAD reports the whole file even when a Range is attached (same as
		// the LFS path): huggingface_hub reads Content-Length here to learn the
		// real size before it decides how to fetch the body.
		w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	rc, _, err := gitRepo.BlobReader(entry.Hash)
	if err != nil {
		internalError(w, "read blob", err)
		return
	}
	defer rc.Close()

	// Honour a single Range, the same way serveLFSFile does. An unparseable or
	// unsatisfiable one falls back to the whole body with a 200 rather than a
	// 416, again matching that path.
	body := io.Reader(rc)
	if offset, length, partial := parseRange(r.Header.Get("Range"), entry.Size); partial {
		// go-git blob readers are sequential, so the prefix has to be read
		// past rather than seeked over -- and these blobs run to gigabytes, so
		// it is discarded as it streams instead of being buffered.
		if _, err := io.CopyN(io.Discard, rc, offset); err != nil {
			internalError(w, "read blob", err)
			return
		}
		span := writePartialContent(w, offset, length, entry.Size)
		body = io.LimitReader(rc, span)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	}
	s.store.IncrementDownloads(r.Context(), repo.ID)
	if _, err := io.Copy(w, body); err != nil {
		// The client hung up mid-transfer; nothing useful left to do.
		return
	}
}

// writePartialContent writes the 206 status line and the range headers for one
// satisfiable range, and reports how many bytes the body must carry. A length
// of -1 means "through the end of the object".
func writePartialContent(w http.ResponseWriter, offset, length, size int64) int64 {
	end := size - 1
	if length >= 0 {
		end = offset + length - 1
	}
	span := end - offset + 1
	w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(offset, 10)+"-"+
		strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
	w.Header().Set("Content-Length", strconv.FormatInt(span, 10))
	w.WriteHeader(http.StatusPartialContent)
	return span
}

// recordDownload records a resolve hit off the request path. It must never
// delay the response, so it hands the write to a goroutine detached from the
// request context (the request may already be finished by the time this
// runs); RecordDownload itself only logs on failure.
func (s *Server) recordDownload(ctx context.Context, repoID int64) {
	go s.store.RecordDownload(context.WithoutCancel(ctx), repoID)
}

// lfsObjectOwned guards every path that turns a pointer in a repository's
// tree into the bytes behind it. The pointer is just text a writer can commit,
// and storage.LFSKey(oid) has no repository in it, so "the caller may read
// this repository" says nothing about the object: without this check a writer
// could commit a pointer naming another repository's oid and read the bytes
// out through their own repository. It answers the question the LFS batch and
// commit paths already ask (store.RepoHasLFSObject).
//
// It writes the error response itself and reports false when the caller must
// stop. An unlinked oid is a 404 rather than a 403, matching the LFS batch
// path: the object does not exist as far as this repository is concerned.
func (s *Server) lfsObjectOwned(w http.ResponseWriter, r *http.Request, repo *store.Repo, oid string) bool {
	_, err := s.ownedLFSKey(r.Context(), repo, oid)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "object not found")
		return false
	}
	if err != nil {
		internalError(w, "check lfs object ownership", err)
		return false
	}
	return true
}

// ownedLFSKey is the non-HTTP form of the same gate, for code that resolves
// a key without answering a request directly: the storage key of oid if repo
// links it, store.ErrNotFound if it does not. It is the only way a tree
// entry's pointer should become a key -- callers that skipped it would
// re-open the hole lfsObjectOwned closes.
func (s *Server) ownedLFSKey(ctx context.Context, repo *store.Repo, oid string) (string, error) {
	owned, err := s.store.RepoHasLFSObject(ctx, repo.ID, oid)
	if err != nil {
		return "", err
	}
	if !owned {
		return "", store.ErrNotFound
	}
	return storage.LFSKey(oid), nil
}

func (s *Server) serveLFSFile(w http.ResponseWriter, r *http.Request, repo *store.Repo, entry gitrepo.Entry, contentType string) {
	oid := entry.LFS.OID
	// Before any header is written, so a pointer the repository does not own
	// cannot even leak the object's size through a HEAD.
	if !s.lfsObjectOwned(w, r, repo, oid) {
		return
	}
	// hf_hub_download reads the linked headers to learn the real object's
	// identity before it follows the redirect.
	w.Header().Set("ETag", `"`+oid+`"`)
	w.Header().Set("X-Linked-Etag", `"`+oid+`"`)
	w.Header().Set("X-Linked-Size", strconv.FormatInt(entry.LFS.Size, 10))
	w.Header().Set("Content-Length", strconv.FormatInt(entry.LFS.Size, 10))
	w.Header().Set("Content-Type", contentType)

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The ownership check above is what authorises this; the key itself is
	// just the content hash, identical for every repository holding the same
	// bytes.
	key := storage.LFSKey(oid)
	if s.storage.SupportsSignedURL() {
		// One object, so the URL only has to outlive this single transfer --
		// but a 10 GiB file over a slow link still needs far more than the
		// base TTL.
		ttl := lfs.TTLFor(s.cfg.SignedURLTTL, s.cfg.SignedURLMaxTTL, entry.LFS.Size)
		url, err := s.storage.SignedGetURL(r.Context(), key, ttl, path.Base(entry.Path))
		if err != nil {
			internalError(w, "sign download url", err)
			return
		}
		s.store.IncrementDownloads(r.Context(), repo.ID)
		// Content-Length must not describe the redirect body.
		w.Header().Del("Content-Length")
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	// Emulator mode: stream the object through, honouring Range so partial
	// reads (parquet footers, resumed downloads) still work.
	offset, length, partial := parseRange(r.Header.Get("Range"), entry.LFS.Size)
	rc, err := s.storage.GetRange(r.Context(), key, offset, length)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			notFound(w, "object "+oid+" is missing from storage")
			return
		}
		internalError(w, "read lfs object", err)
		return
	}
	defer rc.Close()

	if partial {
		writePartialContent(w, offset, length, entry.LFS.Size)
	}
	s.store.IncrementDownloads(r.Context(), repo.ID)
	_, _ = io.Copy(w, rc)
}

// parseRange understands the single-range forms clients actually send. It
// returns length -1 for "to the end".
func parseRange(header string, size int64) (offset, length int64, ok bool) {
	spec, found := strings.CutPrefix(header, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, -1, false
	}
	startStr, endStr, _ := strings.Cut(spec, "-")
	if startStr == "" {
		// Suffix range: the last N bytes.
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, -1, false
		}
		if n > size {
			n = size
		}
		if n == 0 {
			// An empty file: there is no last byte to hand back, and a
			// "bytes 0--1/0" would be nonsense.
			return 0, -1, false
		}
		return size - n, n, true
	}
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, -1, false
	}
	if endStr == "" {
		return start, -1, true
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return 0, -1, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end - start + 1, true
}

// handleRaw returns a bounded preview for the UI, base64-encoding anything that
// is not valid UTF-8 so the response is always JSON-safe.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.loadRepoForRead(w, r, chi.URLParam(r, "kind"), chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectUI)
	if !ok {
		return
	}
	filePath := wildcardPath(r)
	gitRepo, err := s.git.Open(repo.StoragePath)
	if err != nil {
		internalError(w, "open git repository", err)
		return
	}
	entry, _, err := gitRepo.Stat(chi.URLParam(r, "rev"), filePath)
	if err != nil {
		handleStoreError(w, "stat file", err)
		return
	}
	if entry.IsDir {
		badRequest(w, filePath+" is a directory")
		return
	}

	var reader io.ReadCloser
	size := entry.Size
	if entry.LFS != nil {
		if !s.lfsObjectOwned(w, r, repo, entry.LFS.OID) {
			return
		}
		size = entry.LFS.Size
		reader, err = s.storage.GetRange(r.Context(), storage.LFSKey(entry.LFS.OID), 0, maxRawPreviewBytes)
	} else {
		reader, _, err = gitRepo.BlobReader(entry.Hash)
	}
	if err != nil {
		handleStoreError(w, "read file", err)
		return
	}
	defer reader.Close()

	data, err := io.ReadAll(io.LimitReader(reader, maxRawPreviewBytes))
	if err != nil {
		internalError(w, "read file", err)
		return
	}

	resp := apitypes.RawFileResponse{
		Path:      filePath,
		Size:      size,
		Truncated: size > int64(len(data)),
	}
	if utf8.Valid(data) {
		resp.Content, resp.Encoding = string(data), apitypes.FileEncodingUTF8
	} else {
		resp.Content, resp.Encoding = base64.StdEncoding.EncodeToString(data), apitypes.FileEncodingBase64
	}
	writeJSON(w, http.StatusOK, resp)
}
