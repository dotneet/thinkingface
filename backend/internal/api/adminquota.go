// Site administration: storage quotas per namespace
// (docs/dev/api-contract.md §1.3).
//
// These two endpoints are deliberately not part of the organisation settings
// API even though a quota is most often set on an organisation. An
// organisation admin able to raise their own ceiling would not be under a
// ceiling at all, so the lever belongs to whoever pays the storage bill: a
// site administrator, through the same requireSiteAdmin gate as every other
// /api/v1/admin route.

package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// handleAdminListNamespaces answers GET /api/v1/admin/namespaces: every
// namespace on the instance with what it stores and what it may store.
// `search` is a case-insensitive substring of the namespace name; `limit`
// defaults to 50 and is capped at 200 by the store. It mirrors
// handleAdminListUsers, which is the screen next door.
func (s *Server) handleAdminListNamespaces(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSiteAdmin(w, r, false); !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	rows, total, err := s.store.ListNamespaceQuotas(r.Context(), q.Get("search"), limit, offset)
	if err != nil {
		internalError(w, "list namespace quotas", err)
		return
	}
	items := make([]apitypes.AdminNamespaceUsage, 0, len(rows))
	for i := range rows {
		items = append(items, s.toAdminNamespace(rows[i]))
	}
	writeJSON(w, http.StatusOK, apitypes.AdminNamespaceListResponse{
		Items: items, Total: total, DefaultQuotaBytes: s.defaultQuotaBytes(),
	})
}

// handleAdminSetNamespaceQuota answers PATCH /api/v1/admin/namespaces/{ns}.
//
// `quota_bytes` is required and nullable, and the two ways of "not having a
// number" mean different things: null clears the namespace's override so the
// instance default applies again, while 0 is a real quota of zero bytes -- a
// namespace that may hold repositories but upload nothing. A body that omits
// the field entirely is refused rather than guessed at, because guessing
// would have to pick one of those two.
func (s *Server) handleAdminSetNamespaceQuota(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireSiteAdmin(w, r, true)
	if !ok {
		return
	}
	ns := chi.URLParam(r, "ns")

	// Decoded through raw messages first: encoding/json cannot tell an absent
	// `quota_bytes` from an explicit null, and here that difference is the
	// whole API.
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, maxMetaBody, &raw, "request body must be JSON") {
		return
	}
	field, present := raw["quota_bytes"]
	if !present {
		badRequest(w, "quota_bytes is required; send null to clear the namespace's quota")
		return
	}
	var req apitypes.AdminNamespaceQuotaRequest
	if err := json.Unmarshal(field, &req.QuotaBytes); err != nil {
		badRequest(w, "quota_bytes must be a number of bytes, or null")
		return
	}
	if req.QuotaBytes != nil && *req.QuotaBytes < 0 {
		badRequest(w, "quota_bytes must not be negative")
		return
	}

	if err := s.store.SetNamespaceQuota(r.Context(), ns, req.QuotaBytes); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "no such namespace: "+ns)
			return
		}
		internalError(w, "set namespace quota", err)
		return
	}
	// Re-read rather than echoing the request back: the caller needs the
	// usage next to the new ceiling to see whether the namespace is already
	// over it, and the stored spelling of the name rather than the one the
	// URL happened to use.
	row, err := s.store.GetNamespaceQuota(r.Context(), ns)
	if err != nil {
		internalError(w, "read namespace quota", err)
		return
	}
	// Site administration keeps no audit table (see admin.go); the record is
	// this line. A quota is a spending limit on somebody else's namespace, so
	// who changed it, and to what, is worth having in the log.
	slog.Info("namespace storage quota changed by site administrator",
		"actor", actor.Username, "namespace", row.Namespace,
		"quota_bytes", quotaForLog(req.QuotaBytes), "used_bytes", row.UsedBytes)
	writeJSON(w, http.StatusOK, s.toAdminNamespace(row))
}

// toAdminNamespace copies one row onto the wire type, resolving the effective
// ceiling alongside the namespace's own override so a screen never has to
// re-implement that fallback (and get the "0 is not unlimited" half wrong).
func (s *Server) toAdminNamespace(q store.NamespaceQuota) apitypes.AdminNamespaceUsage {
	return apitypes.AdminNamespaceUsage{
		Namespace:           q.Namespace,
		Kind:                apitypes.NamespaceKind(q.Kind),
		LFSSize:             q.UsedBytes,
		NumRepos:            q.NumRepos,
		QuotaBytes:          q.QuotaBytes,
		EffectiveQuotaBytes: store.EffectiveQuota(q.QuotaBytes, s.cfg.DefaultStorageQuotaBytes),
	}
}

// defaultQuotaBytes is TF_DEFAULT_STORAGE_QUOTA_BYTES as the wire spells it:
// null for unlimited, which is what the configured 0 means.
func (s *Server) defaultQuotaBytes() *int64 {
	return store.EffectiveQuota(nil, s.cfg.DefaultStorageQuotaBytes)
}

// quotaForLog renders a nullable quota for a log line, where "cleared" has to
// be distinguishable from zero just as it is on the wire.
func quotaForLog(quota *int64) string {
	if quota == nil {
		return "cleared"
	}
	return strconv.FormatInt(*quota, 10)
}
