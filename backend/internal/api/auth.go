package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyScope
	// ctxKeyCookieAuth marks a request whose identity came from the session
	// cookie rather than a token. Only those are ambient-credential requests
	// a foreign page could ride, so only those need the origin check.
	ctxKeyCookieAuth
)

// identify resolves the caller from a bearer token, HTTP basic credentials, or
// the session cookie. It never rejects: handlers decide what they require, so
// anonymous reads of public repositories keep working.
func (s *Server) identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		user, scope, viaCookie := s.resolveIdentity(r)
		if user != nil {
			ctx = context.WithValue(ctx, ctxKeyUser, user)
			ctx = context.WithValue(ctx, ctxKeyScope, scope)
			ctx = context.WithValue(ctx, ctxKeyCookieAuth, viaCookie)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) resolveIdentity(r *http.Request) (user *store.User, scope string, viaCookie bool) {
	ctx := r.Context()

	if header := r.Header.Get("Authorization"); header != "" {
		if token, ok := strings.CutPrefix(header, "Bearer "); ok {
			u, sc := s.userForToken(ctx, strings.TrimSpace(token))
			return u, sc, false
		}
		// git and git-lfs authenticate with Basic; the password carries the
		// token and the username is ignored.
		if username, password, ok := r.BasicAuth(); ok {
			if strings.HasPrefix(password, auth.TokenPrefix) {
				u, sc := s.userForToken(ctx, password)
				return u, sc, false
			}
			// Basic-with-a-real-password is accepted on every route, so this
			// is the cheapest place in the server to force bcrypt work. Both
			// the failure buckets and the concurrency cap apply before the
			// hash is computed, and nothing is written to the response: a
			// throttled request simply looks anonymous, which every handler
			// already knows how to answer.
			if u, outcome := s.checkPassword(ctx, username, password); outcome == passwordOK {
				return u, "write", false
			}
			return nil, "", false
		}
	}

	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if userID, epoch, err := s.sessions.Verify(cookie.Value); err == nil {
			if u, err := s.store.GetUserByID(ctx, userID); err == nil && u.SessionEpoch == epoch {
				return u, "write", true
			}
		}
	}
	return nil, "", false
}

// checkPassword resolves a username/password pair under the brute-force and
// CPU guards. It reports failure without saying why -- an unknown user and a
// wrong password are indistinguishable, in both the answer and the time
// taken (auth.CheckPasswordMiss burns the same bcrypt work).
//
// The caller is responsible for deciding what a false means: handleLogin
// turns it into a 401, resolveIdentity into an anonymous request.
// passwordOutcome distinguishes the three ways a password check can end. They
// must not be collapsed: "the server is saturated" is not evidence about the
// credentials, so answering it like a wrong password both lies to a legitimate
// caller and spends their failure budget on the server's own load.
type passwordOutcome int

const (
	passwordOK passwordOutcome = iota
	// passwordWrong is a genuine credential failure: no such user, or the
	// hash did not match. Only this outcome deserves a penalty.
	passwordWrong
	// passwordThrottled means this username has already spent its failure
	// budget; the caller should be told to come back later.
	passwordThrottled
	// passwordOverloaded means no bcrypt slot came free in time. It says
	// nothing about the password -- the hash was never compared.
	passwordOverloaded
)

func (s *Server) checkPassword(ctx context.Context, username, password string) (*store.User, passwordOutcome) {
	if s.authGuard.retryAfter(usernameKey(username)) > 0 {
		return nil, passwordThrottled
	}
	if !s.authGuard.acquireBcrypt() {
		return nil, passwordOverloaded
	}
	defer s.authGuard.releaseBcrypt()

	user, err := s.store.GetUserByUsername(ctx, username)
	if err != nil {
		_ = auth.CheckPasswordMiss(password)
		s.authGuard.penalize(usernameKey(username))
		return nil, passwordWrong
	}
	if auth.CheckPassword(user.PasswordHash, password) != nil {
		s.authGuard.penalize(usernameKey(username))
		return nil, passwordWrong
	}
	s.authGuard.reset(usernameKey(username))
	return user, passwordOK
}

func (s *Server) userForToken(ctx context.Context, token string) (*store.User, string) {
	if token == "" {
		return nil, ""
	}
	user, tok, err := s.store.LookupToken(ctx, auth.HashToken(token))
	if err != nil {
		return nil, ""
	}
	go func() {
		// Detached from the request so a slow write never delays the response.
		_ = s.store.TouchToken(context.WithoutCancel(ctx), tok.ID)
	}()
	return user, tok.Scope
}

func currentUser(ctx context.Context) *store.User {
	user, _ := ctx.Value(ctxKeyUser).(*store.User)
	return user
}

func currentScope(ctx context.Context) string {
	scope, _ := ctx.Value(ctxKeyScope).(string)
	return scope
}

// cookieAuthenticated reports whether this request's identity came from the
// session cookie.
func cookieAuthenticated(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyCookieAuth).(bool)
	return v
}

// requireUser fails the request unless someone is authenticated.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	user := currentUser(r.Context())
	if user == nil {
		unauthorized(w, "authentication required")
		return nil, false
	}
	return user, true
}

// requireWrite additionally rejects read-scoped tokens.
func (s *Server) requireWrite(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return nil, false
	}
	if currentScope(r.Context()) != "write" {
		forbidden(w, "this token is read-only")
		return nil, false
	}
	return user, true
}

// loadRepoForRead fetches the repository named in the URL and enforces read
// access, writing the error response itself when it returns false. When the
// name is a former name of a repository that has since moved
// (docs/dev/repo-transfer-design.md §9), it answers according to mode instead of
// a plain 404 -- see resolveRepo and redirectMoved.
func (s *Server) loadRepoForRead(w http.ResponseWriter, r *http.Request, kind, ns, name string, mode redirectMode) (*store.Repo, bool) {
	ctx := r.Context()
	repo, err := s.resolveRepo(ctx, kind, ns, name)
	if err != nil {
		var moved *repoMovedError
		if errors.As(err, &moved) {
			if mode == redirectNone {
				notFound(w, "repository "+ns+"/"+name+" not found")
			} else {
				redirectMoved(w, r, mode, ns, name, moved)
			}
			return nil, false
		}
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "repository "+ns+"/"+name+" not found")
		} else {
			internalError(w, "load repository", err)
		}
		return nil, false
	}
	return repo, true
}

// loadRepoForWrite is the gate every content-changing endpoint passes
// through: git receive-pack, the HF commit/preupload pair, the LFS upload
// batch, in-browser editing, transfers and experiment ingest. On top of the
// write permission it refuses an archived repository, so archiving one
// stops all of them in a single place (docs/dev/api-contract.md §2 "archiving").
// The two operations that must keep working on an archive -- unarchiving and
// deleting it -- use loadRepoForWriteAllowArchived instead.
func (s *Server) loadRepoForWrite(w http.ResponseWriter, r *http.Request, kind, ns, name string, mode redirectMode) (*store.Repo, bool) {
	repo, ok := s.loadRepoForWriteAllowArchived(w, r, kind, ns, name, mode)
	if !ok {
		return nil, false
	}
	// After the permission check, so an archived repository does not answer
	// differently to someone who could not write to it anyway.
	if repo.Archived() {
		writeError(w, http.StatusForbidden, "repository_archived",
			repo.FullName()+" is archived and read-only; unarchive it in the repository settings to make changes")
		return nil, false
	}
	return repo, true
}

// loadRepoForWriteAllowArchived enforces the write permission only. Callers
// that are not changing repository content -- delete, archive/unarchive --
// use it directly; everything else wants loadRepoForWrite.
func (s *Server) loadRepoForWriteAllowArchived(w http.ResponseWriter, r *http.Request, kind, ns, name string, mode redirectMode) (*store.Repo, bool) {
	repo, ok := s.loadRepoForRead(w, r, kind, ns, name, mode)
	if !ok {
		return nil, false
	}
	if !s.canWriteIgnoringArchive(r.Context(), repo) {
		if currentUser(r.Context()) == nil {
			unauthorized(w, "authentication required to write to "+repo.FullName())
		} else {
			forbidden(w, "you do not have write access to "+repo.FullName())
		}
		return nil, false
	}
	return repo, true
}

// -------------------------------------------------------------- handlers

// userResponse assembles the account shape the web UI reads. The stored
// namespace rows carry ids and ownership columns that stay on the server, so
// only the three fields the UI needs are copied across.
//
// The display name and avatar come from the caller's own namespace row rather
// than the users row -- profiles live on namespaces so a user and an
// organisation have one shape (docs/dev/namespace-design.md §5.3). Straight after
// sign-up they are empty strings.
func (s *Server) userResponse(ctx context.Context, u *store.User) apitypes.User {
	rows, err := s.store.NamespacesForUser(ctx, u.ID)
	if err != nil {
		rows = nil
	}
	namespaces := make([]apitypes.Namespace, 0, len(rows))
	for _, n := range rows {
		namespaces = append(namespaces, apitypes.Namespace{
			Name: n.Name, Kind: apitypes.NamespaceKind(n.Kind), Role: n.Role,
		})
	}
	out := apitypes.User{ID: u.ID, Username: u.Username, Email: u.Email, IsAdmin: u.IsAdmin, Namespaces: namespaces}
	if p := s.namespaceProfile(ctx, u.Username); p != nil {
		out.DisplayName = p.DisplayName
		out.AvatarURL = p.AvatarURL
	}
	return out
}

func (s *Server) setSessionCookie(w http.ResponseWriter, user *store.User) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    s.sessions.Issue(user.ID, user.SessionEpoch),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cookieSecure(),
		MaxAge:   int(s.sessions.TTL().Seconds()),
	})
}

// cookieSecure decides the Secure attribute. TF_COOKIE_SECURE is authoritative
// when set, because a deployment that terminates TLS at a load balancer and
// speaks plain HTTP internally cannot be recognised from the public URL alone
// -- and getting it wrong there puts the session cookie on the wire in clear.
func (s *Server) cookieSecure() bool {
	if s.cfg.CookieSecure != nil {
		return *s.cfg.CookieSecure
	}
	return strings.HasPrefix(s.cfg.PublicURL, "https://")
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, maxAuthBody, &req, "request body must be JSON with username and password") {
		return
	}
	addrKey, userKey := s.clientAddrKey(r), usernameKey(req.Username)
	if wait := s.authGuard.retryAfter(addrKey, userKey); wait > 0 {
		tooManyAttempts(w, wait)
		return
	}
	user, outcome := s.checkPassword(r.Context(), req.Username, req.Password)
	switch outcome {
	case passwordThrottled:
		// The username bucket is spent even though the address bucket was
		// not. Do not penalize the address for it.
		tooManyAttempts(w, s.authGuard.retryAfter(userKey))
		return
	case passwordOverloaded:
		// The password was never compared, so this is the server's problem,
		// not the caller's: no penalty, and a status that says "retry".
		serviceOverloaded(w, bcryptWait)
		return
	case passwordWrong:
		s.authGuard.penalize(addrKey)
		writeError(w, http.StatusUnauthorized, "unauthorized", "username or password is incorrect")
		return
	}
	s.authGuard.reset(addrKey)
	s.setSessionCookie(w, user)
	writeJSON(w, http.StatusOK, apitypes.UserResponse{User: s.userResponse(r.Context(), user)})
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowSignup {
		forbidden(w, "sign-up is disabled on this instance")
		return
	}
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, maxAuthBody, &req, "request body must be JSON with username, email and password") {
		return
	}
	// A sign-up creates a namespace, so the reserved list applies here as
	// well as to organisation creation (docs/dev/organization-design.md §6.3).
	if err := validateNamespaceName(req.Username); err != nil {
		writeNamespaceNameError(w, "username", err)
		return
	}
	if err := validatePassword(req.Password); err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := validateEmail(req.Email); err != nil {
		badRequest(w, "email: "+err.Error())
		return
	}
	// Signup mints an account and a session, so it is as much an
	// unauthenticated bcrypt trigger as login is.
	addrKey := s.clientAddrKey(r)
	hash, ok := s.hashNewPassword(w, r, req.Password)
	if !ok {
		return
	}
	user, err := s.store.CreateUser(r.Context(), req.Username, req.Email, hash, false)
	if err != nil {
		s.authGuard.penalize(addrKey)
		handleStoreError(w, "create user", err)
		return
	}
	s.setSessionCookie(w, user)
	writeJSON(w, http.StatusOK, apitypes.UserResponse{User: s.userResponse(r.Context(), user)})
}

// minPasswordBytes is the shortest password this instance accepts. It is
// counted in bytes rather than runes, deliberately on the strict side: a
// short multi-byte passphrase is refused rather than let through under a
// rune count that bcrypt's 72-byte ceiling would then contradict.
const minPasswordBytes = 8

// validatePassword is the one password policy this server has. Sign-up, the
// self-service change (PATCH /api/v1/me/password) and an administrator's
// reset (PATCH /api/v1/admin/users/{username}) all call it, so a password
// that could not be registered cannot be reached by changing to it either.
// The returned error is written straight back to the caller.
func validatePassword(password string) error {
	if len(password) < minPasswordBytes {
		return fmt.Errorf("password must be at least %d characters", minPasswordBytes)
	}
	// bcrypt refuses anything longer, and without this the refusal arrives as
	// a 500 from HashPassword. A Japanese passphrase reaches 72 bytes at
	// around 24 characters, so this is reachable by ordinary use, not abuse.
	if len(password) > auth.MaxPasswordBytes {
		return fmt.Errorf("password must be at most %d bytes", auth.MaxPasswordBytes)
	}
	return nil
}

// hashNewPassword runs the bcrypt hash behind the same two guards login and
// sign-up use -- the per-address failure budget and the process-wide
// concurrency cap -- and writes the error response itself when either bites.
// Every route that turns a plaintext password into a stored hash goes through
// here so no endpoint becomes the cheap way to spend the server's CPU.
func (s *Server) hashNewPassword(w http.ResponseWriter, r *http.Request, password string) (string, bool) {
	// The two refusals are different things and must not answer alike. A
	// spent address bucket is this caller's own failure budget: 429. No free
	// bcrypt slot is the server running out of hashing capacity, which says
	// nothing about the request: 503, the same answer checkPassword's
	// passwordOverloaded gets (see TestLogin_OverloadIsNotACredentialFailure).
	if wait := s.authGuard.retryAfter(s.clientAddrKey(r)); wait > 0 {
		tooManyAttempts(w, wait)
		return "", false
	}
	if !s.authGuard.acquireBcrypt() {
		serviceOverloaded(w, bcryptWait)
		return "", false
	}
	hash, err := auth.HashPassword(password)
	s.authGuard.releaseBcrypt()
	if err != nil {
		internalError(w, "hash password", err)
		return "", false
	}
	return hash, true
}

// handleChangeMyPassword answers PATCH /api/v1/me/password: the caller
// replaces their own password, proving they hold the current one first.
//
// Two things deliberately do *not* happen here. Access tokens are left alone
// -- a token is an independent credential, and a password change is not
// evidence that any of them leaked (docs/dev/api-contract.md §1.3). And while
// every session is revoked, the caller's own cookie is re-issued at the new
// epoch, so changing a password does not log you out of the tab you changed
// it in. A token-authenticated caller holds no cookie to re-issue and gets
// none.
func (s *Server) handleChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return
	}
	var req apitypes.PasswordChangeRequest
	if !decodeJSON(w, r, maxAuthBody, &req,
		"request body must be JSON with current_password and new_password") {
		return
	}
	// Shape first: refusing an impossible new password costs nothing, while
	// verifying the current one costs a bcrypt comparison.
	if err := validatePassword(req.NewPassword); err != nil {
		badRequest(w, err.Error())
		return
	}
	addrKey := s.clientAddrKey(r)
	if wait := s.authGuard.retryAfter(addrKey); wait > 0 {
		tooManyAttempts(w, wait)
		return
	}
	// checkPassword applies the username bucket and the bcrypt cap, exactly
	// as the login form does -- holding a session is not a reason to let
	// someone brute-force the current password for free.
	switch _, outcome := s.checkPassword(r.Context(), user.Username, req.CurrentPassword); outcome {
	case passwordThrottled:
		tooManyAttempts(w, s.authGuard.retryAfter(usernameKey(user.Username)))
		return
	case passwordOverloaded:
		serviceOverloaded(w, bcryptWait)
		return
	case passwordWrong:
		s.authGuard.penalize(addrKey)
		// writeError rather than unauthorized(): this is a form submitted by
		// the web UI, and the WWW-Authenticate header the latter sets can
		// make a browser pop its own credential dialog over the page.
		// handleLogin answers a wrong password the same way.
		writeError(w, http.StatusUnauthorized, "unauthorized", "current password is incorrect")
		return
	}
	hash, ok := s.hashNewPassword(w, r, req.NewPassword)
	if !ok {
		return
	}
	// One statement: the hash and the revocation land together or not at all,
	// so a failure can never leave the old cookies working against a new
	// password.
	epoch, err := s.store.UpdateUserPassword(r.Context(), user.ID, hash)
	if err != nil {
		handleStoreError(w, "update password", err)
		return
	}
	if cookieAuthenticated(r.Context()) {
		// Signed with the epoch the write just returned; the stale one would
		// revoke the caller along with everyone else. Copied rather than
		// mutated -- `user` is the request context's value.
		refreshed := *user
		refreshed.SessionEpoch = epoch
		s.setSessionCookie(w, &refreshed)
	}
	slog.Info("password changed", "username", user.Username, "user_id", user.ID, "actor", "self")
	w.WriteHeader(http.StatusNoContent)
}

// handleLogout clears the cookie and, when the caller was actually holding a
// session, revokes every session that user has. Clearing the cookie alone
// leaves the signed value working for the rest of its TTL, which is exactly
// the case logging out on a shared machine is supposed to close.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if user := currentUser(r.Context()); user != nil && cookieAuthenticated(r.Context()) {
		if err := s.store.BumpSessionEpoch(r.Context(), user.ID); err != nil {
			internalError(w, "revoke sessions", err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cookieSecure(),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, apitypes.UserResponse{User: s.userResponse(r.Context(), user)})
}

// handleWhoami answers the shape huggingface_hub expects from /api/whoami-v2.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	scope := currentScope(r.Context())
	// fullname and avatarUrl come from the caller's profile, the same rule
	// whoamiOrgs already applies to organisations
	// (docs/dev/namespace-design.md §5.3). `hf auth whoami` prints fullname.
	profile := s.namespaceProfile(r.Context(), user.Username)
	writeJSON(w, http.StatusOK, map[string]any{
		"type":          "user",
		"id":            strconv.FormatInt(user.ID, 10),
		"name":          user.Username,
		"fullname":      displayNameOr(profile, user.Username),
		"email":         user.Email,
		"emailVerified": true,
		"canPay":        false,
		"isPro":         false,
		"periodEnd":     nil,
		"avatarUrl":     avatarOf(profile),
		"orgs":          s.whoamiOrgs(r.Context(), user),
		"auth": map[string]any{
			"type": "access_token",
			"accessToken": map[string]any{
				"displayName": "thinkingface",
				"role":        scope,
			},
		},
	})
}

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
	if req.Scope != "read" && req.Scope != "write" {
		req.Scope = "read"
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
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		badRequest(w, "token id must be a number")
		return
	}
	if err := s.store.DeleteToken(r.Context(), user.ID, id); err != nil {
		handleStoreError(w, "delete token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
