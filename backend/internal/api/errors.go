package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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

// decodeJSON reads a bounded JSON body into v, writing the error response
// itself when the body is oversized or malformed. badMsg is the message shown
// for a body that will not parse; the decoder's own text is never echoed, so a
// malformed request cannot reflect server-side detail back to the caller.
func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, v any, badMsg string) bool {
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
	writeJSON(w, status, apitypes.ApiErrorBody{Error: apitypes.ApiError{Message: message, Type: errType}})
}

func badRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, "bad_request", message)
}

func notFound(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotFound, "not_found", message)
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
func handleStoreError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, gitrepo.ErrRepoNotFound),
		errors.Is(err, gitrepo.ErrPathNotFound), errors.Is(err, storage.ErrNotFound):
		notFound(w, op+": not found")
	case errors.Is(err, store.ErrConflict):
		conflict(w, op+": already exists")
	default:
		internalError(w, op, err)
	}
}
