package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Request body ceilings. Every JSON endpoint reads through one of these: an
// unbounded json.Decoder on a network body lets one request buffer the whole
// process out of memory. The values are generous enough that no legitimate
// client sees them, and the file-carrying endpoints have their own, far
// larger, limits (maxCommitBody, maxEditBytes).
const (
	// maxAuthBody covers credentials and token metadata: a few short strings.
	maxAuthBody = 8 << 10
	// maxMetaBody covers repository create/delete and run bookkeeping.
	maxMetaBody = 64 << 10
	// maxYAMLBody covers the README front matter huggingface_hub sends to
	// /api/validate-yaml, which is the whole card file. 1MB of YAML is
	// already far past any real repository card, and yaml.Unmarshal is the
	// most expensive thing this endpoint does.
	maxYAMLBody = 1 << 20
	// maxBatchBody covers the per-file batches: preupload, paths-info and the
	// LFS batch protocol, all of which carry one small record per file.
	maxBatchBody = 8 << 20
	// maxIngestBody covers a live metric batch, which may hold many points.
	maxIngestBody = 32 << 20
)

// jsonContentType reports whether a body may be read as JSON.
//
// An absent Content-Type passes, and has to: `git`, `curl`, the e2e suite and
// this repository's own Go tests all send bodies without one, and a browser
// never does -- every form submission and every fetch with a body sets the
// header, so "no Content-Type at all" identifies a caller no page can steer.
//
// What must not pass is text/plain (and the other two form encodings). A
// cross-site `<form enctype="text/plain">` is a "simple request": it fires
// with no CORS preflight, carrying whatever ambient credential the browser
// holds for this origin, and its body can be shaped to parse as JSON. Reading
// any Content-Type therefore made every decodeJSON endpoint -- POST
// /api/v1/repos and POST /api/v1/tokens among them -- reachable from a
// hostile page. Insisting on a type no form can produce closes that without
// asking anything of a real client, since every one of them already sends
// application/json.
func jsonContentType(r *http.Request) bool {
	raw := r.Header.Get("Content-Type")
	if raw == "" {
		return true
	}
	base, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	base = strings.ToLower(base)
	return base == "application/json" || strings.HasSuffix(base, "+json")
}

// decodeJSON reads a bounded JSON body into v, writing the error response
// itself when the body is oversized, wrongly typed or malformed. badMsg is the
// message shown for a body that will not parse; the decoder's own text is
// never echoed, so a malformed request cannot reflect server-side detail back
// to the caller.
func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, v any, badMsg string) bool {
	if !jsonContentType(r) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
			"request body must be JSON; send Content-Type: application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("request body must be at most %d bytes", maxBytes))
			return false
		}
		badRequest(w, badMsg)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, errType, message string) {
	// huggingface_hub reads X-Error-Message before it looks at the body, and
	// it is the only place a message survives intact: hf_raise_for_status
	// falls back to `body["error"]`, which HF spells as a plain string while
	// this API spells it as an object, so without the header a caller is shown
	// the Python repr of a dict instead of the sentence inside it. The body
	// stays as it is -- the Web UI's client is typed against that shape.
	w.Header().Set("X-Error-Message", message)
	writeJSON(w, status, apitypes.ApiErrorBody{Error: apitypes.ApiError{Message: message, Type: errType}})
}

func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, "bad_request", message)
}

func notFound(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotFound, "not_found", message)
}

// repoNotFound answers a repository that does not exist (or that the caller
// may not see). huggingface_hub raises RepositoryNotFoundError only for this
// X-Error-Code or a 401 -- a bare 404 comes back as HfHubHTTPError, which
// repo_exists / file_exists / revision_exists do not catch, so they raise
// instead of answering False.
func repoNotFound(w http.ResponseWriter, message string) {
	w.Header().Set("X-Error-Code", "RepoNotFound")
	notFound(w, message)
}

// entryNotFound answers a path that does not exist at a revision that does.
// EntryNotFoundError is what huggingface_hub raises for it, and what
// hf_hub_download / HfFileSystem branch on to tell "no such file" apart from
// "no such repository".
func entryNotFound(w http.ResponseWriter, message string) {
	w.Header().Set("X-Error-Code", "EntryNotFound")
	notFound(w, message)
}

func unauthorized(w http.ResponseWriter, message string) {
	// Basic is what git and git-lfs know how to answer.
	w.Header().Set("WWW-Authenticate", `Basic realm="thinkingface"`)
	writeError(w, http.StatusUnauthorized, "unauthorized", message)
}

func forbidden(w http.ResponseWriter, message string) {
	writeError(w, http.StatusForbidden, "forbidden", message)
}

func conflict(w http.ResponseWriter, message string) {
	writeError(w, http.StatusConflict, "conflict", message)
}

// internalError logs the cause and tells the caller only that it failed. The
// operation name gives the log entry something to grep for.
func internalError(w http.ResponseWriter, op string, err error) {
	slog.Error(op, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", op+" failed")
}

// handleStoreError maps the common domain errors onto responses so handlers do
// not repeat the same three checks.
//
// gitrepo.ErrEmptyRepo is in the 404 branch despite its name saying "empty":
// gitrepo.Resolve reports an unborn HEAD and a revision that names nothing with
// that one error, because go-git answers plumbing.ErrReferenceNotFound for
// both. Leaving it out is what made /raw, /model-meta and /parquet -- the three
// single-file reads that map their errors through this helper instead of
// checking themselves -- answer 500 internal_error for a revision that does not
// exist, which reads as "the server is broken" rather than "that is not there".
//
// Folding the two cases into one 404 is right for those callers in particular:
// each asks for one named file, and in a repository with no commits that file is
// exactly as absent as it is at a revision that does not resolve. They are also
// Web UI routes, so nothing picks a huggingface_hub exception type off an
// X-Error-Code here. The endpoints that do need the distinction (repo-info /
// tree / paths-info, whose answer for an empty repository is a 200 rather than a
// 404) resolve through Server.revisionOrEmpty and never reach this helper with
// ErrEmptyRepo.
func handleStoreError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, gitrepo.ErrRepoNotFound),
		errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, gitrepo.ErrEmptyRepo),
		errors.Is(err, storage.ErrNotFound):
		notFound(w, op+": not found")
	case errors.Is(err, store.ErrConflict):
		conflict(w, op+": already exists")
	default:
		internalError(w, op, err)
	}
}
