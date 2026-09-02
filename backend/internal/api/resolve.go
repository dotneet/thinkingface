package api

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5/plumbing"

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
// rendered README keeps working.
//
// The header itself is built by storage.ContentDisposition, the same call the
// LFS signed-URL path hands to GCS, so the two paths cannot drift. They did:
// this function used to build filename* with url.PathEscape, which is not RFC
// 5987's attr-char set -- it leaves `=`, `:`, `@`, `'`, `(` and `)` alone --
// so "epoch=12-step=500.ckpt", the name PyTorch Lightning gives every
// checkpoint by default, produced a header that mime.ParseMediaType (and any
// other RFC 2231 parser) rejects outright.
func attachmentDisposition(name string) string {
	return storage.ContentDisposition(name)
}

// resolveCacheControl picks the caching policy for one resolve response.
//
// A blob's bytes are immutable -- git names them by their own hash -- but the
// URL only names those bytes when the revision in it does. `/resolve/main/x`
// is a moving target: the next push gives the same URL different content, so
// the response may be stored but has to be revalidated every time, which is
// what `no-cache` means (it is not "do not store"; that is `no-store`).
// `/resolve/<commit sha>/x` can never mean anything else, so it gets the
// year-long immutable form browsers and CDNs already understand.
//
// Either way the revalidation is cheap: If-None-Match against the ETag is
// answered by notModified with a 304 instead of a repeated gigabyte.
//
// `public` is accurate here rather than merely convenient: this server has no
// per-repository visibility at all (docs/dev/thinkingface-design.md §11), so a
// shared cache holding a resolve response cannot expose it to anyone who could
// not have asked for it directly.
func resolveCacheControl(rev string, commit plumbing.Hash) string {
	// EqualFold, because a client may spell the sha in either case; the
	// revision has already been resolved *to* this commit, so an equal string
	// means the URL is pinned to it rather than to a ref that happens to be
	// there today.
	if strings.EqualFold(rev, commit.String()) {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

// notModified answers a conditional request whose If-None-Match already names
// the entity this request would return, and reports whether it did.
//
// It is what makes the ETag this handler has always emitted worth anything:
// without it a client holding a 40 GB checkpoint that asked whether its copy
// was still current was answered with the whole file again. The 304 carries
// no body and keeps the ETag (RFC 9110 §15.4.5); net/http drops Content-Type
// and Content-Length for this status on its own.
//
// Range is deliberately not consulted: RFC 9110 §13.2.2 evaluates
// If-None-Match before Range, so a matched precondition wins over a partial
// request. The download counters are not consulted either -- recordDownload
// has already run by the time any caller reaches this, which keeps the "one
// resolve request is one count" rule that HEAD and the LFS 302 also obey.
func notModified(w http.ResponseWriter, r *http.Request, etag string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if !etagMatches(r.Header.Get("If-None-Match"), etag) {
		return false
	}
	w.WriteHeader(http.StatusNotModified)
	return true
}

// etagMatches applies RFC 9110 §8.8.3.2's weak comparison to an If-None-Match
// list: "*" matches any current representation, and a `W/` prefix is ignored
// on both sides. Ignoring it is safe here because every ETag this package
// emits is a content hash -- two representations that share one can only be
// byte-identical -- so weak and strong comparison agree.
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	switch header {
	case "":
		return false
	case "*":
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == want {
			return true
		}
	}
	return false
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

	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return
	}
	rev, ok := revParam(w, r, "rev", repo)
	if !ok {
		return
	}
	// The revision is resolved on its own, before the file is looked for, so
	// that "this revision does not exist" and "this file does not exist at a
	// revision that does" stay separable. gitRepo.Stat cannot separate them:
	// it reports an unknown revision and an unborn HEAD alike as ErrEmptyRepo,
	// which is why this used to answer a bare 404 for both -- and a bare 404
	// is an HfHubHTTPError, so neither `file_exists()` nor
	// `hf_hub_download()` could tell what was actually missing. Answering
	// RevisionNotFound for both would have been worse still: a typo in a
	// *path* would then be reported as a missing revision.
	//
	// revisionOrEmpty (refs.go) makes exactly that split and writes the
	// RevisionNotFound 404 itself when the revision does not resolve.
	commit, empty, ok := s.revisionOrEmpty(w, gitRepo, repo, rev)
	if !ok {
		return
	}
	if empty {
		// The repository has no commits at all. The revision is not what is
		// wrong here -- there is simply no file -- so this is the same
		// EntryNotFound the missing-path branch below answers with, and
		// file_exists() reports False rather than raising.
		entryNotFound(w, filePath+" does not exist at revision "+rev)
		return
	}
	// Stat against the resolved commit rather than the name: the revision is
	// resolved exactly once per request, so a push landing mid-request cannot
	// make X-Repo-Commit disagree with the bytes served under it.
	entry, _, err := gitRepo.Stat(commit.String(), filePath)
	if err != nil {
		if errors.Is(err, gitrepo.ErrPathNotFound) {
			// EntryNotFound, not a bare 404: huggingface_hub only raises
			// EntryNotFoundError -- the error `hf_hub_download` documents for a
			// file that is not in the repository -- when this header is
			// present. Without it a missing file came back as a generic
			// HfHubHTTPError, indistinguishable from the repository itself
			// being gone.
			entryNotFound(w, filePath+" does not exist at revision "+rev)
			return
		}
		internalError(w, "stat file", err)
		return
	}
	if entry.IsDir {
		badRequest(w, filePath+" is a directory")
		return
	}

	// One resolve request is one count for both download counters, whether it
	// is a HEAD, a plain GET, or a redirect to an LFS signed URL -- the
	// client's actual transfer against GCS never touches this server, so this
	// is the only point that ever sees the request. Counting the two at
	// different points is what let the 30-day window exceed the all-time
	// total the UI shows it against.
	s.recordDownload(r.Context(), repo.ID)

	contentType := writeResolveHeaders(w, commit, rev, filePath)

	if entry.LFS != nil {
		s.serveLFSFile(w, r, repo, entry, contentType)
		return
	}
	serveGitBlob(w, r, gitRepo, entry, contentType)
}

// writeResolveHeaders sets everything a resolve response carries before its
// body -- the commit it was served from, the cache policy, and the
// content-type defence -- and returns the content type it settled on, which
// the two body paths need.
//
// It runs for the LFS branch and the git-blob branch alike, and before either
// decides anything: the headers describe the file the request named, not how
// the bytes happen to be stored, so a client must not be able to tell a
// pointer from a plain blob by what comes back above the body.
func writeResolveHeaders(w http.ResponseWriter, commit plumbing.Hash, rev, filePath string) string {
	w.Header().Set("X-Repo-Commit", commit.String())
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", resolveCacheControl(rev, commit))
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
	return contentType
}

// serveGitBlob streams a plain (non-LFS) file out of the object database,
// honouring a conditional request and a single Range.
//
// The sibling of serveLFSFile, and deliberately not merged with it: that one
// redirects to a signed URL or proxies the bucket, this one reads a *sequential*
// go-git reader and therefore has to discard the prefix of a range rather than
// seek over it. What the two genuinely share is already shared -- parseRange
// and writePartialContent.
func serveGitBlob(w http.ResponseWriter, r *http.Request, gitRepo *gitrepo.Repo, entry gitrepo.Entry, contentType string) {
	etag := `"` + entry.Hash.String() + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", contentType)
	// Before the HEAD branch: a revalidation is a conditional HEAD as often as
	// it is a conditional GET, and both must answer 304 rather than repeating
	// the metadata (or the body) the client already holds.
	if notModified(w, r, etag) {
		return
	}
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

	// Honour a single Range, the same way serveLFSFile does: a range this
	// server cannot make sense of falls back to the whole body with a 200,
	// while one that starts past the end is a 416 (see parseRange).
	body := io.Reader(rc)
	offset, length, verdict := parseRange(r.Header.Get("Range"), entry.Size)
	if verdict == rangeUnsatisfiable {
		writeRangeNotSatisfiable(w, entry.Size)
		return
	}
	if verdict == rangePartial {
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

// recordDownload records a resolve hit off the request path, advancing both
// counters the UI shows side by side: the repository's running total
// (repositories.downloads) and today's bucket in the 30-day stats table. They
// are written from this one place so a single rule -- one resolve request is
// one count -- governs both; counting them under different rules is what let
// a 30-day window come out larger than the all-time total it is a window of.
//
// Neither write may delay the response, so both go to a goroutine detached
// from the request context (the request may well be finished by the time this
// runs), and neither can fail a download: IncrementDownloads and
// RecordDownload only log.
//
// Detached does not mean unbounded: one resolve is one goroutine and two
// writes, and resolve needs no authentication, so a database that stops
// answering would otherwise pile them up without limit. detachedWrite
// (auth.go) gives the pair a deadline of its own.
func (s *Server) recordDownload(ctx context.Context, repoID int64) {
	detached, cancel := detachedWrite(ctx)
	go func() {
		defer cancel()
		s.store.IncrementDownloads(detached, repoID)
		s.store.RecordDownload(detached, repoID)
	}()
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

// lfsObjectSize answers how many bytes the object really has, from the ledger
// rather than from the pointer text in the tree.
//
// lfs_objects.size is written by promotion from the length the object was
// measured at (lfs.promoteFrom stats the staged bytes and refuses anything
// that disagrees with what the client declared), and store.LinkLFSObjects
// refuses to create a link whose declared size does not match that row, so it
// is the one number on this path that no committer chose.
//
// A missing row on an object this repository is linked to should not happen --
// the link has a foreign key to it -- but "the ledger does not know how big
// this is" must not become "serve it as zero bytes", so it is answered as the
// absent object it describes. It writes the response and reports false when
// the caller must stop.
func (s *Server) lfsObjectSize(w http.ResponseWriter, r *http.Request, oid string) (int64, bool) {
	size, known, err := s.store.HasLFSObject(r.Context(), oid)
	if err != nil {
		internalError(w, "read lfs object size", err)
		return 0, false
	}
	if !known {
		notFound(w, "object not found")
		return 0, false
	}
	return size, true
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
	// The size the *object* has, not the one the pointer claims.
	//
	// entry.LFS.Size is parsed out of a blob a writer committed, and nothing on
	// this path re-checks it: the create-a-link paths do (store.LinkLFSObjects
	// refuses a size that disagrees with the ledger, verifyCommitLFSFile
	// refuses one on the HF commit path), but a repository already linked to an
	// object could then push a hand-written pointer naming it with "size 5" and
	// have this hand every downloader a five-byte file. On the emulator path
	// net/http truncates the body at the declared Content-Length; on the
	// signed-URL path GCS serves the whole object and hf_hub_download's own
	// size check fails instead. Neither is a lie the reader can see through.
	//
	// The header set is unchanged in shape -- huggingface_hub reads exactly
	// these -- only the value is now one the server stands behind. The ledger
	// is the authority rather than a storage Stat because it is written from
	// the object's measured length at promotion (lfs.promoteFrom) and it is a
	// row this request is already talking to the database for, where a Stat
	// would be a GCS round trip added to every download.
	size, ok := s.lfsObjectSize(w, r, oid)
	if !ok {
		return
	}
	// hf_hub_download reads the linked headers to learn the real object's
	// identity before it follows the redirect.
	etag := `"` + oid + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Linked-Etag", etag)
	w.Header().Set("X-Linked-Size", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Type", contentType)

	// Deliberately after lfsObjectOwned: a 304 confirms that the oid the
	// caller put in If-None-Match is the content of this path, which is
	// precisely what the ownership check refuses to tell a caller whose
	// repository does not link that object.
	//
	// This is also where the conditional request pays for itself most: the
	// answer is a few bytes of headers instead of a signed URL and a
	// multi-gigabyte transfer out of GCS.
	if notModified(w, r, etag) {
		return
	}

	if r.Method == http.MethodHead {
		// Unlike GET below, a HEAD never touches storage, so nothing here can
		// fail after this point -- Content-Length is safe to set unconditionally.
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
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
		ttl := lfs.TTLFor(s.cfg.SignedURLTTL, s.cfg.SignedURLMaxTTL, size)
		url, err := s.storage.SignedGetURL(r.Context(), key, ttl, path.Base(entry.Path))
		if err != nil {
			internalError(w, "sign download url", err)
			return
		}
		// The redirect names a URL that expires (lfs.TTLFor bounds the TTL to
		// what this one transfer needs), so the *redirect* must never be
		// served from a cache: a stored 302 would hand a later client a
		// signed URL GCS has since started rejecting, which looks like a
		// corrupt repository rather than a stale cache entry. This overwrites
		// the revision-derived value handleResolve set, and only on this
		// path -- the object bytes themselves are still cacheable under the
		// ETag above, which is what makes the next request a cheap 304.
		w.Header().Set("Cache-Control", "no-store")
		// Content-Length is never set on this path, so there is nothing to
		// strip before the redirect: it must not describe the redirect body.
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	// Emulator mode: stream the object through, honouring Range so partial
	// reads (parquet footers, resumed downloads) still work. Content-Length
	// is set only once storage has confirmed the object still exists --
	// setting it earlier would leave it describing the object's length even
	// when GetRange fails below and a JSON error body (a different length) is
	// written instead, corrupting the response the same way a redirect body
	// would be corrupted by it.
	offset, length, verdict := parseRange(r.Header.Get("Range"), size)
	if verdict == rangeUnsatisfiable {
		writeRangeNotSatisfiable(w, size)
		return
	}
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

	if verdict == rangePartial {
		writePartialContent(w, offset, length, size)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.Copy(w, rc)
}

// rangeVerdict is what parseRange concluded about a Range header, which is
// three answers rather than two.
type rangeVerdict int

const (
	// rangeNone: no Range, or one this server does not understand (a unit
	// other than bytes, several ranges, a malformed spec). The whole body is
	// served with a 200, which RFC 9110 §14.2 explicitly allows -- a recipient
	// may ignore a Range header it cannot make sense of.
	rangeNone rangeVerdict = iota
	// rangePartial: a satisfiable range. 206 with Content-Range.
	rangePartial
	// rangeUnsatisfiable: a well-formed byte range whose first position is at
	// or past the end of the file. 416.
	rangeUnsatisfiable
)

// parseRange understands the single-range forms clients actually send. It
// returns length -1 for "to the end".
//
// The one case that must not fall back to a 200 is a first position at or past
// the end of the file, and the reason is huggingface_hub's resumed download.
// http_get resumes by sending `Range: bytes=N-` and appending whatever comes
// back to the partial file it already holds -- without ever checking that the
// status was 206. So when the ".incomplete" file is already the whole object
// (the previous attempt finished the transfer and died before the rename), a
// 200 hands it the entire file again and it is appended to a copy of itself.
// The result is a file of twice the size that fails its hash check, and every
// retry doubles it again. A 416 stops that dead: the client raises instead of
// writing. This is the only status this server can use to say "there is
// nothing after byte N", so it is worth the strictness (RFC 9110 §15.5.17).
func parseRange(header string, size int64) (offset, length int64, verdict rangeVerdict) {
	spec, found := strings.CutPrefix(header, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, -1, rangeNone
	}
	startStr, endStr, _ := strings.Cut(spec, "-")
	if startStr == "" {
		// Suffix range: the last N bytes. A suffix is never unsatisfiable in
		// the sense above -- it is clamped to the file, and "the last N bytes
		// of nothing" is the empty file rather than a request for bytes that
		// are missing.
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, -1, rangeNone
		}
		if n > size {
			n = size
		}
		if n == 0 {
			// An empty file: there is no last byte to hand back, and a
			// "bytes 0--1/0" would be nonsense.
			return 0, -1, rangeNone
		}
		return size - n, n, rangePartial
	}
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, -1, rangeNone
	}
	if start >= size {
		return 0, -1, rangeUnsatisfiable
	}
	if endStr == "" {
		return start, -1, rangePartial
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return 0, -1, rangeNone
	}
	if end >= size {
		end = size - 1
	}
	return start, end - start + 1, rangePartial
}

// writeRangeNotSatisfiable answers a range that starts at or past the end of
// the file. Content-Range carries the file's real length, which is what lets a
// client that was resuming discover it already holds everything (RFC 9110
// §14.4). Every identity header the 200 would have carried is already set by
// the caller, so a conditional retry still works.
func writeRangeNotSatisfiable(w http.ResponseWriter, size int64) {
	w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
	// The body is this API's error shape, not a range response, so the
	// Content-Type set for the file must not stand.
	w.Header().Del("Content-Disposition")
	writeError(w, http.StatusRequestedRangeNotSatisfiable, "range_not_satisfiable",
		"the requested range starts at or past the end of this "+strconv.FormatInt(size, 10)+"-byte file")
}

// handleRaw returns a bounded preview for the UI, base64-encoding anything that
// is not valid UTF-8 so the response is always JSON-safe.
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	repo, rev, filePath, ok := s.uiFileTarget(w, r)
	if !ok {
		return
	}
	gitRepo, ok := s.openGit(w, repo)
	if !ok {
		return
	}
	entry, _, err := gitRepo.Stat(rev, filePath)
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
		// The ledger's size, not the pointer's, for the same reason
		// serveLFSFile uses it: this one feeds `truncated`, and a pointer
		// understating its object turns a cut-off preview into one the UI
		// presents as the whole file.
		if size, ok = s.lfsObjectSize(w, r, entry.LFS.OID); !ok {
			return
		}
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
