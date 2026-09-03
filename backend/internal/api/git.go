package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/gitserver"
	"github.com/dotneet/thinkingface/backend/internal/lfs"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func (s *Server) handleInfoRefs(w http.ResponseWriter, r *http.Request, kind string) {
	service, ok := gitserver.ParseService(r.URL.Query().Get("service"))
	if !ok {
		// Dumb HTTP is not supported; tell the client plainly rather than
		// letting it fall back and fail obscurely.
		writeError(w, http.StatusBadRequest, "bad_request",
			"only the git smart HTTP protocol is supported; use a recent git client")
		return
	}

	ns, name := chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name"))
	var repo *store.Repo
	if service == gitserver.ReceivePack {
		repo, ok = s.loadRepoForWrite(w, r, kind, ns, name, redirectGit)
	} else {
		repo, ok = s.loadRepoForRead(w, r, kind, ns, name, redirectGit)
	}
	if !ok {
		return
	}

	if err := s.ensureRepoLocal(r.Context(), repo); err != nil {
		internalError(w, "materialize repository", err)
		return
	}
	if err := s.gitHTTP.AdvertiseRefs(r.Context(), w, repo.StoragePath, service, r.Header.Get("Git-Protocol")); err != nil {
		if errors.Is(err, gitserver.ErrResponseStarted) {
			// The advertisement was already going out, so there is no status
			// left to change; a log line is all this can still add.
			slog.Error("advertise refs", "repo", repo.FullName(), "error", err)
			return
		}
		// Without this the client got an empty 200 and read it as a
		// repository with no refs, which git reports as an empty clone rather
		// than as the server-side failure it is.
		internalError(w, "advertise refs", err)
	}
}

func (s *Server) handleUploadPack(w http.ResponseWriter, r *http.Request, kind string) {
	repo, ok := s.loadRepoForRead(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectGit)
	if !ok {
		return
	}
	if err := s.ensureRepoLocal(r.Context(), repo); err != nil {
		internalError(w, "materialize repository", err)
		return
	}
	if err := s.gitHTTP.Serve(w, r, repo.StoragePath, gitserver.UploadPack); err != nil {
		slog.Error("upload-pack", "repo", repo.FullName(), "error", err)
	}
}

func (s *Server) handleReceivePack(w http.ResponseWriter, r *http.Request, kind string) {
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectGit)
	if !ok {
		return
	}

	if err := s.ensureRepoLocal(r.Context(), repo); err != nil {
		internalError(w, "materialize repository", err)
		return
	}
	// Snapshot the branch tips so the sync worker learns what the push changed
	// without needing a git hook script on disk.
	before, err := s.gitHTTP.HeadsAfterPush(repo.StoragePath)
	if err != nil {
		internalError(w, "read refs", err)
		return
	}

	if err := s.gitHTTP.Serve(w, r, repo.StoragePath, gitserver.ReceivePack); err != nil {
		slog.Error("receive-pack", "repo", repo.FullName(), "error", err)
		return
	}

	// Before HeadsAfterPush: that call re-opens the repository, and without
	// the adopted state the materialisation would re-download the very entry
	// pack this push just uploaded.
	s.adoptAfterPush(context.WithoutCancel(r.Context()), repo)

	after, err := s.gitHTTP.HeadsAfterPush(repo.StoragePath)
	if err != nil {
		slog.Error("read refs after push", "repo", repo.FullName(), "error", err)
		return
	}
	// The response is already written, so failures here can only be logged.
	s.schedulePostPush(context.WithoutCancel(r.Context()), repo, before, after, "push")
}

// schedulePostPush turns one push's before/after branch tips into the two
// kinds of follow-up work a push creates: a sync job for every branch it moved
// or created, and a repo.ref_deleted webhook for every branch it removed.
//
// Both transports go through it -- HTTP above and SSH in gitssh.go -- because
// they had already been drifting apart in comments while running identical
// loops, and a push that lands over SSH has to reach subscribers exactly as an
// HTTP one does.
//
// The deletion half is what a loop over `after` structurally cannot see: a
// branch that is gone is absent from `after`, so scanning it announced every
// case except the one a mirror most needs to hear about. `before` is where a
// deleted branch still exists.
//
// Only branches, because that is all HeadsAfterPush lists: `git push --delete
// v1.0` on a *tag* is invisible from here and stays invisible. The API's tag
// delete (handleHFDeleteTag) is what announces those, and the same blind spot
// already governs the sync side -- a pushed tag schedules no job either.
//
// what names the transport in the log line ("push" / "ssh push").
func (s *Server) schedulePostPush(ctx context.Context, repo *store.Repo, before, after map[string]string, what string) {
	for branch, newSHA := range after {
		if before[branch] == newSHA {
			continue
		}
		if err := s.sync.Enqueue(ctx, repo.ID, branch, before[branch], newSHA); err != nil {
			slog.Error("schedule sync after "+what, "repo", repo.FullName(), "branch", branch, "error", err)
		}
	}
	for branch, oldSHA := range before {
		if _, kept := after[branch]; kept {
			continue
		}
		s.fireRefDeleted(ctx, repo, "branch", branch, oldSHA)
	}
}

// -------------------------------------------------------------------- LFS

func (s *Server) handleLFSBatch(w http.ResponseWriter, r *http.Request, kind string) {
	ns, name := chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name"))

	var req lfs.BatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBatchBody)).Decode(&req); err != nil {
		writeLFSError(w, http.StatusBadRequest, "batch request must be JSON")
		return
	}

	var repo *store.Repo
	var ok bool
	if req.Operation == "upload" {
		repo, ok = s.loadRepoForWrite(w, r, kind, ns, name, redirectHF)
	} else {
		repo, ok = s.loadRepoForRead(w, r, kind, ns, name, redirectHF)
	}
	if !ok {
		return
	}

	resp, err := s.lfs.Batch(r.Context(), repo.ID, &req, r.Header.Get("Authorization"))
	if err != nil {
		if errors.Is(err, lfs.ErrUnsupportedOperation) {
			writeLFSError(w, http.StatusBadRequest, `operation must be "upload" or "download"`)
			return
		}
		internalError(w, "lfs batch", err)
		return
	}
	w.Header().Set("Content-Type", lfs.ContentType)
	writeRawJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLFSVerify(w http.ResponseWriter, r *http.Request, kind string) {
	repo, ok := s.loadRepoForWrite(w, r, kind, chi.URLParam(r, "ns"), repoName(chi.URLParam(r, "name")), redirectHF)
	if !ok {
		return
	}
	var req lfs.ObjectRef
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMetaBody)).Decode(&req); err != nil {
		writeLFSError(w, http.StatusBadRequest, "verify request must be JSON")
		return
	}
	if err := s.lfs.Verify(r.Context(), repo.ID, req.OID, req.Size); err != nil {
		writeLFSVerifyError(w, err)
		return
	}
	// writeRawJSON, not writeJSON: the latter sets application/json itself and
	// so overwrote the line above, leaving this one success response as the
	// only LFS answer on the endpoint not typed application/vnd.git-lfs+json.
	// git-lfs accepts both, so nothing was broken by it -- but the batch
	// response, verify-by-id and every LFS error already answer the media type
	// the protocol names, and one of them silently not doing so is the kind of
	// difference that is only noticed by whatever eventually depends on it.
	w.Header().Set("Content-Type", lfs.ContentType)
	writeRawJSON(w, http.StatusOK, map[string]any{"oid": req.OID, "size": req.Size})
}

// maxLFSProxyObjectBytes bounds one object arriving over the transfer proxy.
//
// Nothing here can consult a declared length: the LFS transfer protocol PUTs
// the raw object with no size the server has already agreed to, so the only
// ceiling available is an explicit one. Without it the handler streamed an
// unbounded body straight into the bucket -- no MaxBytesReader, no
// server-wide body cap, and streamingRoute exempts this path from
// handlerTimeout as well, so a single request could write for as long as it
// liked.
//
// It is the browser upload endpoint's per-file ceiling (maxUploadFileBytes),
// for the same reason that one was chosen: 10 GiB is past any weights file
// that travels as one non-resumable HTTP request, and the two write paths
// having different ideas of "too large" would be a difference nothing could
// justify to whoever hit it.
//
// A var rather than a const only so a test can lower it -- streaming 10 GiB to
// prove the ceiling exists would cost more than the ceiling is worth. Nothing
// in the server assigns to it.
var maxLFSProxyObjectBytes int64 = maxUploadFileBytes

// handleLFSProxyUpload receives object bytes when the storage driver cannot
// issue signed URLs (the local emulator). It verifies the digest before
// accepting, so a corrupted transfer never becomes a valid-looking object.
func (s *Server) handleLFSProxyUpload(w http.ResponseWriter, r *http.Request) {
	repoID, oid, ok := s.lfsProxyTarget(w, r)
	if !ok {
		return
	}
	// In signed-URL mode nothing ever hands a client this href: uploadAction
	// mints a proxy URL only when the driver cannot sign one, so in a real GCS
	// deployment every legitimate transfer goes straight to the bucket and this
	// route is reachable but unused. Leaving it answering there anyway meant
	// any holder of a write-scoped token for any repository could stream
	// volume into the bucket through an emulator affordance -- and, because the
	// signature check falls back to s.canWrite, without even holding a batch
	// response. It is refused rather than left unregistered so the route table
	// does not change shape with the storage driver, and it is a 404 because
	// that is what every other refusal on this route answers.
	//
	// handleLFSProxyDownload is gated identically, for the same reason in the
	// other direction. Verify (POST /api/v1/lfs/{repoID}/verify) is
	// deliberately not gated: it runs in both modes, since a directly-signed
	// upload still has to be recorded against the repository.
	if s.storage.SupportsSignedURL() {
		writeLFSError(w, http.StatusNotFound,
			"this instance transfers LFS objects directly to object storage; use the upload href from the batch response")
		return
	}
	// A missing repository and an unauthorised one answer identically, so
	// scanning ids cannot count the repositories on the instance
	// (handleLFSProxyDownload has always worked this way).
	repo, err := s.store.GetRepoByID(r.Context(), repoID)
	if err != nil || (!s.lfsProxyAuthorized(r, "upload", repoID, oid) && !s.canWrite(r.Context(), repo)) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(w, "load repository", err)
			return
		}
		notFound(w, "object not found")
		return
	}

	// Write to a staging key first, exactly as the signed-URL path does: the
	// digest is only known once the whole body has been read, and a
	// mismatched object must never land on the shared content-addressed key.
	// This path can hash the bytes as they pass through the server, so the
	// digest is settled by the time promotion runs; the signed-URL path never
	// sees them and pays for the same guarantee later instead, by reading the
	// staged object back and hashing it inside lfs.Verify.
	//
	// The key is random rather than storage.LFSStagingKey(repoID, oid), for
	// the same reason the browser upload endpoint uses one: that name is
	// derived from nothing but the caller's own request, so two requests
	// naming the same repository and oid share one staging object and each
	// hashes a body the other may already have replaced -- and the digest
	// computed here is then a statement about bytes that are no longer at the
	// key. lfs.PromoteStagedFrom catches that (it requires the staged object's
	// generation to be the one it checked), but catching a collision only
	// turns it into a failed push; a key nothing else can name removes it. It
	// matters beyond this handler because this route is registered in both
	// storage modes, so a proxy upload writing to the shared name could also
	// overwrite a signed-URL upload's staged bytes while its verify runs.
	stagingKey, err := incomingUploadKey(repoID)
	if err != nil {
		internalError(w, "buffer upload", err)
		return
	}
	// limitedReader rather than http.MaxBytesReader: the cap has to travel back
	// out through storage.Put as an error this handler can recognise, so an
	// oversized object is a 413 naming the ceiling rather than a blind 500.
	hashReader := newHashingReader(&limitedReader{r: r.Body, left: maxLFSProxyObjectBytes})
	if err := s.storage.Put(r.Context(), stagingKey, hashReader, "application/octet-stream"); err != nil {
		// Nothing else will ever name this key, so a partial write here is
		// garbage the moment this request ends. Best effort: `thinkingface gc`
		// sweeps the staging prefix by age if the delete does not land.
		_ = s.storage.Delete(r.Context(), stagingKey)
		if errors.Is(err, errUploadTooLarge) {
			writeLFSError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("object %s is too large; a single object may be at most %d bytes over this endpoint",
					oid, maxLFSProxyObjectBytes))
			return
		}
		internalError(w, "buffer upload", err)
		return
	}
	gotOID, size := hashReader.Result()
	if gotOID != oid {
		_ = s.storage.Delete(r.Context(), stagingKey)
		writeLFSError(w, http.StatusBadRequest,
			"uploaded content hashes to "+gotOID+" but was declared as "+oid)
		return
	}
	// Promotion (copy to lfs/{oid}, link, drop the staged object) is shared
	// with Verify so both upload paths publish objects the same way.
	if err := s.lfs.PromoteStagedFrom(r.Context(), repoID, oid, size, stagingKey); err != nil {
		// The staged bytes are this request's alone (the key is random), so a
		// promotion that did not happen leaves nothing anyone will ever name.
		// Dropping them now keeps a refusal -- the quota one especially -- from
		// occupying the bucket until gc's grace period is up.
		_ = s.storage.Delete(r.Context(), stagingKey)
		var overQuota *lfs.QuotaExceededError
		if errors.As(err, &overQuota) {
			writeLFSError(w, http.StatusInsufficientStorage, overQuota.Error())
			return
		}
		if errors.Is(err, store.ErrLFSObjectGone) || errors.Is(err, lfs.ErrNotStaged) {
			writeLFSError(w, http.StatusConflict, "object was removed; retry the upload")
			return
		}
		internalError(w, "store lfs object", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLFSProxyDownload(w http.ResponseWriter, r *http.Request) {
	repoID, oid, ok := s.lfsProxyTarget(w, r)
	if !ok {
		return
	}
	// Gated exactly as handleLFSProxyUpload is, and for the same reason:
	// downloadAction mints a proxy href only when the driver cannot sign one,
	// so in a real GCS deployment no client is ever given this URL -- the
	// batch response names the bucket, and resolve redirects to it rather than
	// here. Left answering anyway, it was a worse affordance than the
	// upload half -- it takes no token at all, since the fallback below asks
	// only that the repository link the oid, and both repoID and oid are
	// public -- so an anonymous caller could stream whole objects through this
	// process, repeatedly and concurrently, converting the signed-URL offload
	// the deployment exists for back into API egress and CPU.
	//
	// The emulator (SupportsSignedURL() == false) is untouched: it is the only
	// mode in which this href is minted, and the only mode `make up` and the
	// E2E suite run in.
	if s.storage.SupportsSignedURL() {
		writeLFSError(w, http.StatusNotFound,
			"this instance transfers LFS objects directly from object storage; use the download href from the batch response")
		return
	}
	// The signed href is only ever handed out for an object the batch
	// endpoint already found in this repository, so that path needs no
	// further check. The fallback -- a caller hitting the URL directly --
	// has to prove that the repository named in the path actually holds this
	// oid. LFS bytes are shared instance-wide by content hash, so without that
	// check any object whose oid a caller can name would come back through a
	// repository of their own.
	//
	// It does *not* check that the caller may read that repository, and the
	// comment here used to claim it did. There is no repository visibility on
	// this server yet (docs/dev/thinkingface-design.md §11): every repository is
	// readable by everyone, resolve and the batch endpoint included, so the
	// check would be a call that always answers yes. What matters is that this
	// is the place it has to go the moment private repositories exist -- one
	// s.canRead(repo) beside the ownership test below -- because this route
	// hands out object bytes without going through loadRepoForRead at all.
	if !s.lfsProxyAuthorized(r, "download", repoID, oid) {
		repo, err := s.store.GetRepoByID(r.Context(), repoID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(w, "load repository", err)
			return
		}
		owned := false
		if err == nil && repo != nil {
			if owned, err = s.store.RepoHasLFSObject(r.Context(), repoID, oid); err != nil {
				internalError(w, "check lfs object ownership", err)
				return
			}
		}
		if !owned {
			notFound(w, "object not found")
			return
		}
	}

	key := storage.LFSKey(oid)
	info, err := s.storage.Stat(r.Context(), key)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// Same sentence as the unlinked-oid branch above: a distinct
			// body here ("object <oid> not found") lets anyone who can
			// name a repo id and a sha256 tell a registered-but-missing
			// object from one this repository never linked.
			notFound(w, "object not found")
			return
		}
		internalError(w, "stat lfs object", err)
		return
	}
	rc, err := s.storage.Get(r.Context(), key)
	if err != nil {
		internalError(w, "read lfs object", err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("ETag", `"`+oid+`"`)
	// Same reasoning as resolve.go: never let a pushed byte stream be
	// rendered as a document on this origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", attachmentDisposition(oid))
	_, _ = io.Copy(w, rc)
}

// handleLFSVerifyByID is the verify callback advertised in batch responses. It
// is addressed by repository id because the client only knows the href we gave
// it, not the repository's URL shape.
func (s *Server) handleLFSVerifyByID(w http.ResponseWriter, r *http.Request) {
	repoID, ok := int64Param(w, r, "repoID", "repository")
	if !ok {
		return
	}
	repo, err := s.store.GetRepoByID(r.Context(), repoID)
	if err != nil || (!s.lfsProxyAuthorized(r, "verify", repoID, "") && !s.canWrite(r.Context(), repo)) {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			internalError(w, "load repository", err)
			return
		}
		notFound(w, "object not found")
		return
	}
	var req lfs.ObjectRef
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMetaBody)).Decode(&req); err != nil {
		writeLFSError(w, http.StatusBadRequest, "verify request must be JSON")
		return
	}
	if err := s.lfs.Verify(r.Context(), repoID, req.OID, req.Size); err != nil {
		writeLFSVerifyError(w, err)
		return
	}
	w.Header().Set("Content-Type", lfs.ContentType)
	writeRawJSON(w, http.StatusOK, map[string]any{"oid": req.OID, "size": req.Size})
}

// writeLFSVerifyError answers a failed verify. Everything lfs.Verify reports is
// "this object is not there as you described it", which is the 404 git-lfs
// expects -- except a namespace that has run out of room, which is a 507 and a
// sentence the operator can act on. Answering that as a 404 would tell the
// pusher its own upload had vanished.
func writeLFSVerifyError(w http.ResponseWriter, err error) {
	var overQuota *lfs.QuotaExceededError
	if errors.As(err, &overQuota) {
		writeLFSError(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	writeLFSError(w, http.StatusNotFound, err.Error())
}

// lfsProxyAuthorized accepts the signature embedded in the href we handed the
// client. LFS transfer requests carry no Authorization header of their own,
// because the protocol assumes the href is pre-signed.
func (s *Server) lfsProxyAuthorized(r *http.Request, op string, repoID int64, oid string) bool {
	q := r.URL.Query()
	return q.Get("op") == op && s.lfs.VerifyProxySignature(op, repoID, oid, q.Get("exp"), q.Get("sig"))
}

func (s *Server) lfsProxyTarget(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	repoID, ok := int64Param(w, r, "repoID", "repository")
	if !ok {
		return 0, "", false
	}
	oid := chi.URLParam(r, "oid")
	if !gitrepo.ValidOID(oid) {
		badRequest(w, "oid must be a sha256 hex digest")
		return 0, "", false
	}
	return repoID, oid, true
}

// writeLFSError answers in the shape the LFS batch protocol specifies: a
// `message` at the top level of the body, under the protocol's own media type,
// rather than this API's `{"error": {...}}`. git-lfs reads that field and
// prints it.
//
// X-Error-Message is set as well. The body shape is the protocol's to dictate,
// but "every error response carries X-Error-Message" is this server's own
// promise (docs/dev/api-contract.md §0), and huggingface_hub -- which speaks the
// LFS endpoints too, through its own uploader -- reads the header before it
// looks at any body.
func writeLFSError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", lfs.ContentType)
	w.Header().Set("X-Error-Message", message)
	writeRawJSON(w, status, map[string]string{"message": message})
}

// hashingReader digests a stream while it is being copied elsewhere, so the
// proxy upload path never buffers a whole object in memory just to verify it.
type hashingReader struct {
	src  io.Reader
	hash hash.Hash
	size int64
}

func newHashingReader(src io.Reader) *hashingReader {
	return &hashingReader{src: src, hash: sha256.New()}
}

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.src.Read(p)
	if n > 0 {
		h.hash.Write(p[:n])
		h.size += int64(n)
	}
	return n, err
}

// Result reports the digest and byte count. Valid once the stream is drained.
func (h *hashingReader) Result() (string, int64) {
	return hex.EncodeToString(h.hash.Sum(nil)), h.size
}

func writeRawJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}
