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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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

// MaxTokenRepos caps the repository list of a restricted token
// (docs/dev/agent-features.md §3). A token that needs more is better off
// unrestricted, and the cap keeps the per-request restriction lookup small.
const MaxTokenRepos = 32

// TokenMintRequest is what minting a token takes, from either the HTTP
// handler or `thinkingface admin token create`.
type TokenMintRequest struct {
	Name  string
	Scope string
	// ExpiresInDays is 0 for a token that never expires.
	ExpiresInDays int
	// Repos restricts the token to these repositories, each spelled
	// "{datasets|models}/{ns}/{name}". Empty for an unrestricted token.
	Repos []string
}

// TokenMintError is a refusal to mint, carrying the HTTP answer the handler
// gives it. Its message is written for the person who made the request and
// is what the admin CLI prints as well.
type TokenMintError struct {
	Status int
	Type   string
	Msg    string
}

func (e *TokenMintError) Error() string { return e.Msg }

func mintBadRequest(format string, args ...any) error {
	return &TokenMintError{Status: http.StatusBadRequest, Type: "bad_request", Msg: fmt.Sprintf(format, args...)}
}

// TokenMintStore is the store surface MintToken needs; *store.Store
// implements it.
type TokenMintStore interface {
	namespaceRoleReader
	GetRepo(ctx context.Context, kind, ns, name string) (*store.Repo, error)
	CreateTokenWithRepos(ctx context.Context, userID int64, name, scope, tokenHash string, expiresAt *time.Time, repos []store.TokenRepo) (*store.AccessToken, error)
}

// MintToken validates a token request for user and creates the token,
// returning its plaintext value -- which exists nowhere else afterwards -- and
// the stored record. It is the whole of the rules for a new token: the HTTP
// handler and the admin CLI both call it, so a token the web UI would refuse
// cannot be minted from a shell either.
//
// A refusal is a *TokenMintError; any other error is the store's.
func MintToken(ctx context.Context, st TokenMintStore, user *store.User, req TokenMintRequest) (string, *store.AccessToken, error) {
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
		return "", nil, mintBadRequest(`scope must be "read" or "write"`)
	}
	if req.ExpiresInDays < 0 {
		return "", nil, mintBadRequest("expires_in_days must not be negative")
	}
	if req.ExpiresInDays > maxTokenExpiryDays {
		return "", nil, mintBadRequest("expires_in_days must be at most %d", maxTokenExpiryDays)
	}
	repos, err := resolveTokenRepos(ctx, st, user, req.Scope, req.Repos)
	if err != nil {
		return "", nil, err
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
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	rec, err := st.CreateTokenWithRepos(ctx, user.ID, req.Name, req.Scope, hash, expiresAt, repos)
	if err != nil {
		return "", nil, fmt.Errorf("create token: %w", err)
	}
	return token, rec, nil
}

// resolveTokenRepos turns the requested repository list into the rows a
// restricted token is stored with. Every entry must name a repository that
// exists under that exact name -- a former name that now redirects is refused
// rather than followed, so the list says precisely what it grants -- and that
// user may write to right now. The grant never exceeds what the owner holds;
// it is re-checked against their role on every request anyway (authz.go), so
// this check is about refusing a list that could not work, not the security
// boundary itself.
func resolveTokenRepos(ctx context.Context, st TokenMintStore, user *store.User, scope string, specs []string) ([]store.TokenRepo, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if scope != "write" {
		// A read token can change nothing, so a repository list would
		// restrict nothing -- while reading as though it limited what the
		// token can *see*, which no restriction here does.
		return nil, mintBadRequest("repos applies to write tokens only: a read token cannot change any repository, and a restriction does not limit reads")
	}
	if len(specs) > MaxTokenRepos {
		return nil, mintBadRequest("repos may name at most %d repositories", MaxTokenRepos)
	}
	out := make([]store.TokenRepo, 0, len(specs))
	seen := make(map[int64]bool, len(specs))
	for _, spec := range specs {
		kind, ns, name, ok := parseTokenRepo(spec)
		if !ok {
			return nil, mintBadRequest(`repos: %q must be "datasets/{namespace}/{name}" or "models/{namespace}/{name}"`, spec)
		}
		repo, err := st.GetRepo(ctx, kind, ns, name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, mintBadRequest("repos: repository %s does not exist (a renamed repository must be named by its current name)", spec)
			}
			return nil, fmt.Errorf("load repository %s: %w", spec, err)
		}
		role, err := roleFor(ctx, st, user, repo.Namespace)
		if err != nil {
			return nil, fmt.Errorf("check access to %s: %w", spec, err)
		}
		if role < RoleWrite {
			return nil, &TokenMintError{Status: http.StatusForbidden, Type: "forbidden",
				Msg: "repos: " + user.Username + " does not have write access to " + spec}
		}
		if seen[repo.ID] {
			continue
		}
		seen[repo.ID] = true
		out = append(out, store.TokenRepo{RepoID: repo.ID, Kind: repo.Kind, Namespace: repo.Namespace, Name: repo.Name})
	}
	return out, nil
}

// parseTokenRepo splits "{datasets|models}/{ns}/{name}", the spelling the web
// UI's own URLs use. Only the plural kinds are accepted: a bare "ns/name" is
// ambiguous between a model and a dataset, and guessing which would put the
// wrong repository on the list.
func parseTokenRepo(spec string) (kind, ns, name string, ok bool) {
	parts := strings.Split(spec, "/")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	switch parts[0] {
	case "datasets":
		kind = "dataset"
	case "models":
		kind = "model"
	default:
		return "", "", "", false
	}
	return kind, parts[1], parts[2], true
}

// TokenRepoSpec renders a restriction entry the way the API spells it:
// "{datasets|models}/{ns}/{name}".
func TokenRepoSpec(r store.TokenRepo) string {
	return kindPlural(r.Kind) + "/" + r.Namespace + "/" + r.Name
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	// Write scope, not merely authentication: minting is how a read-only
	// token would otherwise escalate itself into a write-scoped one. And
	// requireWrite's restricted-token refusal is what stops a
	// repository-restricted token from minting itself an unrestricted one.
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
		// Repos is omitted, null or [] for an unrestricted token.
		Repos []string `json:"repos"`
	}
	if !decodeJSON(w, r, maxAuthBody, &req, "request body must be JSON with name and scope") {
		return
	}
	token, rec, err := MintToken(r.Context(), s.store, user, TokenMintRequest{
		Name: req.Name, Scope: req.Scope, ExpiresInDays: req.ExpiresInDays, Repos: req.Repos,
	})
	if err != nil {
		var refused *TokenMintError
		if errors.As(err, &refused) {
			writeError(w, refused.Status, refused.Type, refused.Msg)
			return
		}
		internalError(w, "create token", err)
		return
	}
	item := toTokenItem(rec)
	// Minting a credential is an auditable event, so it is logged by id,
	// name, scope and restriction. The token value itself appears in the
	// response and nowhere else -- not in this line, not truncated, not as a
	// prefix.
	slog.Info("access token created", "username", user.Username, "user_id", user.ID,
		"token_id", rec.ID, "token_name", rec.Name, "scope", rec.Scope,
		"repos", item.Repos, "client_ip", s.clientIP(r))
	// The plaintext value appears here and nowhere else.
	writeJSON(w, http.StatusOK, apitypes.CreateTokenResponse{TokenItem: item, Token: token})
}

// toTokenItem drops the owning user id, which never leaves the server. Repos
// is never nil, so the wire always carries [] for an unrestricted token.
func toTokenItem(t *store.AccessToken) apitypes.TokenItem {
	repos := make([]string, 0, len(t.Repos))
	for _, r := range t.Repos {
		repos = append(repos, TokenRepoSpec(r))
	}
	return apitypes.TokenItem{
		ID: t.ID, Name: t.Name, Scope: apitypes.TokenScope(t.Scope),
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
		Repos: repos,
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
