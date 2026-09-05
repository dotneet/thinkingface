// Personal access tokens: the caller's own credentials, listed, minted and
// revoked from /settings.
//
// Not part of the HF-compatible surface -- nothing in huggingface_hub mints a
// token -- so the shapes here are internal/apitypes' and the rules (the expiry
// cap, the write scope on every state change) are ours alone to define. What
// makes a token *work* on a request lives in auth.go; this file is only its
// lifecycle.

package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	tokens, err := s.store.ListTokens(r.Context(), user.ID)
	if err != nil {
		internalError(w, "list tokens", err)
		return
	}
	items := make([]apitypes.TokenItem, 0, len(tokens))
	for _, t := range tokens {
		items = append(items, toTokenItem(&t))
	}
	writeJSON(w, http.StatusOK, apitypes.TokenListResponse{Items: items})
}

// maxTokenExpiryDays bounds how far out a token's expiry can be set. The cap
// exists so "no expiry" stays a deliberate choice rather than the only
// practical one, and so a client can't request something absurd like a
// 100-year token. It is not part of the HF-compatible surface: nothing in
// huggingface_hub mints tokens, so this endpoint and its cap are ours alone
// to define.
const maxTokenExpiryDays = 365

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	// Write scope, not merely authentication: minting is how a read-only
	// token would otherwise escalate itself into a write-scoped one.
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
		// ExpiresInDays is omitted, null, or 0 for a token that never
		// expires -- encoding/json leaves a plain (non-pointer) int
		// untouched for both a missing key and an explicit `null`, so all
		// three spellings collapse to the same zero value here.
		ExpiresInDays int `json:"expires_in_days"`
	}
	if !decodeJSON(w, r, maxAuthBody, &req, "request body must be JSON with name and scope") {
		return
	}
	if req.Name == "" {
		req.Name = "token"
	}
	// Unknown scopes are refused rather than downgraded to read. The downgrade
	// used to be silent: a typo (e.g. transposed letters in "write") minted
	// a read-only token with a 200,
	// and the caller learned about it only when the first write failed -- far
	// from the request that caused it, and with nothing pointing back at it.
	// An empty scope is the same mistake (every client of this endpoint sends
	// one), so it is refused the same way rather than given a second meaning.
	if req.Scope != "read" && req.Scope != "write" {
		badRequest(w, `scope must be "read" or "write"`)
		return
	}
	if req.ExpiresInDays < 0 {
		badRequest(w, "expires_in_days must not be negative")
		return
	}
	if req.ExpiresInDays > maxTokenExpiryDays {
		badRequest(w, fmt.Sprintf("expires_in_days must be at most %d", maxTokenExpiryDays))
		return
	}
	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		// Computed here in Go (UTC) rather than left to the database's own
		// "now + N days": PostgreSQL and SQLite spell that arithmetic
		// differently (see dialect.nowPlusSeconds), and resolving it to a
		// single absolute instant before it ever reaches SQL keeps the two
		// backends from being able to disagree about it.
		t := time.Now().UTC().AddDate(0, 0, req.ExpiresInDays)
		expiresAt = &t
	}
	token, hash, err := auth.NewToken()
	if err != nil {
		internalError(w, "generate token", err)
		return
	}
	rec, err := s.store.CreateToken(r.Context(), user.ID, req.Name, req.Scope, hash, expiresAt)
	if err != nil {
		internalError(w, "create token", err)
		return
	}
	// Minting a credential is an auditable event, so it is logged by id,
	// name and scope. The token value itself appears in the response and
	// nowhere else -- not in this line, not truncated, not as a prefix.
	slog.Info("access token created", "username", user.Username, "user_id", user.ID,
		"token_id", rec.ID, "token_name", rec.Name, "scope", rec.Scope,
		"client_ip", s.clientIP(r))
	// The plaintext value appears here and nowhere else.
	writeJSON(w, http.StatusOK, apitypes.CreateTokenResponse{TokenItem: toTokenItem(rec), Token: token})
}

// toTokenItem drops the owning user id, which never leaves the server.
func toTokenItem(t *store.AccessToken) apitypes.TokenItem {
	return apitypes.TokenItem{
		ID: t.ID, Name: t.Name, Scope: apitypes.TokenScope(t.Scope),
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
	}
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	// Revocation is a state change, so a read-only token may not do it.
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	id, ok := int64Param(w, r, "id", "token")
	if !ok {
		return
	}
	if err := s.store.DeleteToken(r.Context(), user.ID, id); err != nil {
		handleStoreError(w, "delete token", err)
		return
	}
	slog.Info("access token revoked", "username", user.Username, "user_id", user.ID,
		"token_id", id, "actor", "self", "client_ip", s.clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}
