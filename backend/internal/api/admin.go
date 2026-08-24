package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Site administration: the endpoints behind /api/v1/admin, reserved for
// accounts carrying users.is_admin (docs/dev/api-contract.md §1.3).
//
// They exist because everything else in this server is scoped to a namespace,
// and some things are not: an account that forgot its password has nobody to
// ask, and TF_ADMIN_PASSWORD only ever applies to the very first boot, so
// without these the answer was "edit the database".
//
// There is no separate audit table for any of this. The organisation audit
// log is keyed by namespace and a site-wide action has none, and adding a
// table for two verbs is not worth a migration -- so the record is a
// structured slog line per change, carrying who acted, on whom, and what
// changed. Never the password itself, in either direction.

// requireSiteAdmin gates the /admin endpoints. It answers 403 rather than
// hiding the route behind a 404: the endpoint's existence is documented and
// public, and a caller who is simply not an administrator is better told so
// than left guessing at a URL. write says whether the operation changes
// state, in which case a read-scoped token is refused too.
func (s *Server) requireSiteAdmin(w http.ResponseWriter, r *http.Request, write bool) (*store.User, bool) {
	var user *store.User
	var ok bool
	if write {
		user, ok = s.requireWrite(w, r)
	} else {
		user, ok = s.requireUser(w, r)
	}
	if !ok {
		return nil, false
	}
	if !user.IsAdmin {
		forbidden(w, "site administrator access is required")
		return nil, false
	}
	return user, true
}

// toAdminUser copies the fields the account directory shows. The stored
// password hash and the session epoch are on store.User and are deliberately
// not carried across -- apitypes is the wire contract, and nothing on it can
// be leaked by a later handler that forgets.
func toAdminUser(u *store.User) apitypes.AdminUser {
	return apitypes.AdminUser{
		ID: u.ID, Username: u.Username, Email: u.Email,
		IsAdmin: u.IsAdmin, CreatedAt: u.CreatedAt,
	}
}

// handleAdminListUsers answers GET /api/v1/admin/users. `search` is a
// case-insensitive substring of the username or the email address; `limit`
// defaults to 50 and is capped at 200 by the store.
func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSiteAdmin(w, r, false); !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	users, total, err := s.store.ListUsers(r.Context(), q.Get("search"), limit, offset)
	if err != nil {
		internalError(w, "list users", err)
		return
	}
	items := make([]apitypes.AdminUser, 0, len(users))
	for i := range users {
		items = append(items, toAdminUser(&users[i]))
	}
	writeJSON(w, http.StatusOK, apitypes.AdminUserListResponse{Items: items, Total: total})
}

// handleAdminCreateUser answers POST /api/v1/admin/users: a site
// administrator adds an account directly.
//
// It deliberately does **not** consult cfg.AllowSignup. That flag closes the
// public /auth/signup form; an instance that closes it still has to be able
// to gain accounts, and this is the route that does it -- gating it on the
// same flag would make TF_ALLOW_SIGNUP=false a one-way door with no way to
// add a colleague short of editing the database.
//
// Validation is shared with handleSignup down to the helper: the reserved
// name list applies because an account is also a namespace, and the password
// policy is the one validatePassword holds for every route.
//
// No session cookie is issued. handleSignup mints one because the caller
// *is* the new account; here the caller is somebody else, and replacing the
// administrator's own session with the new user's would sign them out of
// their own browser.
func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireSiteAdmin(w, r, true)
	if !ok {
		return
	}
	var req apitypes.AdminUserCreateRequest
	if !decodeJSON(w, r, maxAuthBody, &req,
		"request body must be JSON with username, email and password") {
		return
	}
	// Every check runs before the account is written, so a rejected request
	// leaves nothing behind (docs/dev/organization-design.md §6.3 for the
	// reserved list).
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
	hash, ok := s.hashNewPassword(w, r, req.Password)
	if !ok {
		return
	}
	user, err := s.store.CreateUser(r.Context(), req.Username, req.Email, hash, req.IsAdmin)
	if err != nil {
		// A username already taken by an account or an organisation is
		// store.ErrConflict, which handleStoreError turns into a 409.
		handleStoreError(w, "create user", err)
		return
	}
	slog.Info("account created by site administrator",
		"actor", actor.Username, "actor_id", actor.ID,
		"username", user.Username, "user_id", user.ID,
		"is_admin", user.IsAdmin)
	writeJSON(w, http.StatusCreated, apitypes.AdminUserResponse{User: toAdminUser(user)})
}

// handleAdminUpdateUser answers PATCH /api/v1/admin/users/{username}: reset
// the password, flip the administrator flag, or both. Absent fields are left
// alone; a body that sets neither is a 400 rather than a no-op 200, so a
// misspelled field name cannot look like a successful change.
//
// Everything that can fail on its own terms -- validation, the target lookup,
// the self-demotion rule, and the bcrypt hash -- completes before the first
// durable write, so a refused request cannot have granted admin rights on its
// way out.
func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requireSiteAdmin(w, r, true)
	if !ok {
		return
	}
	username := chi.URLParam(r, "username")
	var req apitypes.AdminUserUpdateRequest
	if !decodeJSON(w, r, maxAuthBody, &req, "request body must be JSON with password, is_admin, or both") {
		return
	}
	if req.Password == nil && req.IsAdmin == nil {
		badRequest(w, "request body must set password, is_admin, or both")
		return
	}
	if req.Password != nil {
		if err := validatePassword(*req.Password); err != nil {
			badRequest(w, err.Error())
			return
		}
	}
	target, err := s.store.GetUserByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w, "no user named "+username)
			return
		}
		internalError(w, "load user", err)
		return
	}
	// Refused before anything is written: an administrator who demoted
	// themselves by accident would need another administrator to undo it,
	// and on a single-admin instance there is none. Stepping down is
	// possible, just not as a self-service action.
	//
	// Its own error type rather than a plain "bad_request", so the web UI can
	// translate the sentence instead of echoing this English one. The UI
	// leaves the control off your own row, so this is the answer to a race
	// (someone else demoted you a moment ago) or to a hand-made request.
	if req.IsAdmin != nil && !*req.IsAdmin && target.ID == actor.ID {
		writeError(w, http.StatusBadRequest, "self_demote",
			"you cannot remove your own site administrator access; ask another administrator to do it")
		return
	}

	// Hash before the first write, not between the two.
	//
	// hashNewPassword can refuse -- a spent address bucket (429) or no free
	// bcrypt slot (503) -- and it used to run *after* SetUserAdmin had
	// committed, so `{"password": ..., "is_admin": true}` under load answered
	// an error with the administrator flag already changed. Every step that
	// can fail for a reason of its own now happens before anything durable
	// moves. Reported by Cursor Bugbot on #11.
	var hash string
	if req.Password != nil {
		var ok bool
		if hash, ok = s.hashNewPassword(w, r, *req.Password); !ok {
			return
		}
	}

	// The two writes are not wrapped in one transaction, and the order is
	// deliberate. SetUserAdmin goes first because it is the one that can
	// still refuse on its own terms (ErrLastSiteAdmin), so a logical
	// rejection happens before any write at all; UpdateUserPassword is a
	// single statement carrying its own session revocation. What is left
	// between them is an infrastructure failure -- a lost connection -- and
	// PATCH is idempotent here, so retrying the identical request converges.
	// Sharing one transaction would mean reimplementing SetUserAdmin's
	// advisory lock and last-admin count inline, which buys less than it
	// costs.
	if req.IsAdmin != nil && *req.IsAdmin != target.IsAdmin {
		if err := s.store.SetUserAdmin(r.Context(), target.ID, *req.IsAdmin); err != nil {
			if errors.Is(err, store.ErrLastSiteAdmin) {
				writeError(w, http.StatusConflict, "last_admin",
					"appoint another site administrator before removing the last one")
				return
			}
			handleStoreError(w, "update site administrator flag", err)
			return
		}
		slog.Info("site administrator flag changed",
			"actor", actor.Username, "actor_id", actor.ID,
			"username", target.Username, "user_id", target.ID,
			"is_admin", *req.IsAdmin)
	}

	if req.Password != nil {
		// The write revokes the target's sessions as part of the same
		// statement.
		epoch, err := s.store.UpdateUserPassword(r.Context(), target.ID, hash)
		if err != nil {
			handleStoreError(w, "update password", err)
			return
		}
		// Normally those sessions belong to the target, not the actor, and
		// there is nothing to re-issue. Resetting your *own* password from
		// this screen is the exception: the revocation would include the
		// cookie this request arrived on, signing the administrator out on
		// their next click. Re-issue it at the new epoch, the same way
		// PATCH /api/v1/me/password does.
		if target.ID == actor.ID && cookieAuthenticated(r.Context()) {
			actor.SessionEpoch = epoch
			s.setSessionCookie(w, actor)
		}
		slog.Info("password reset by site administrator",
			"actor", actor.Username, "actor_id", actor.ID,
			"username", target.Username, "user_id", target.ID)
	}

	// Re-read rather than patching the loaded copy, so the response reports
	// what the database actually holds.
	fresh, err := s.store.GetUserByID(r.Context(), target.ID)
	if err != nil {
		internalError(w, "reload account", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.AdminUserResponse{User: toAdminUser(fresh)})
}
