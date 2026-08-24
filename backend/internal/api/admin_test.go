package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Password change (PATCH /api/v1/me/password) and site administration
// (/api/v1/admin/users), driven over real HTTP against the security fixture
// -- it is the only one whose accounts carry real bcrypt hashes, which every
// case here depends on.

// adminUser is secFixture.user for an account with users.is_admin set.
func (f *secFixture) adminUser(name, password string) *store.User {
	f.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		f.t.Fatalf("hash password: %v", err)
	}
	u, err := f.st.CreateUser(context.Background(), name, name+"@example.com", hash, true)
	if err != nil {
		f.t.Fatalf("create admin %s: %v", name, err)
	}
	return u
}

// mustUser re-reads an account the fixture created, for the calls that need
// the row (its id) rather than just the name.
func (f *secFixture) mustUser(name string) *store.User {
	f.t.Helper()
	u, err := f.st.GetUserByUsername(context.Background(), name)
	if err != nil {
		f.t.Fatalf("load user %s: %v", name, err)
	}
	return u
}

// login exchanges a password for a session cookie the way the web UI does.
func (f *secFixture) login(username, password string) *http.Cookie {
	f.t.Helper()
	rec := f.do(secRequest{method: "POST", path: "/api/v1/auth/login",
		body: map[string]any{"username": username, "password": password}})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("login %s: status %d, body %s", username, rec.Code, rec.Body.String())
	}
	c := sessionCookie(rec)
	if c == nil {
		f.t.Fatalf("login %s issued no session cookie", username)
	}
	return c
}

// session is login for the callers that only want to hand the cookie to
// secRequest. Site administration accepts nothing else -- requireSiteAdmin
// refuses tokens and HTTP Basic outright -- so every /api/v1/admin case below
// drives the API the way the web UI does.
func (f *secFixture) session(username, password string) []*http.Cookie {
	f.t.Helper()
	return []*http.Cookie{f.login(username, password)}
}

// basicAuth is the HTTP Basic header for a username/password pair, for the
// cases that check a credential the admin endpoints must refuse.
func basicAuth(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// canLogIn reports whether the credentials are currently accepted, without
// failing the test either way.
func (f *secFixture) canLogIn(username, password string) bool {
	f.t.Helper()
	rec := f.do(secRequest{method: "POST", path: "/api/v1/auth/login",
		body: map[string]any{"username": username, "password": password}})
	return rec.Code == http.StatusOK
}

// recErrorType is errorType (archive_test.go) for the secFixture's recorder,
// which is the raw *httptest.ResponseRecorder rather than a wrapped response.
func recErrorType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body apitypes.ApiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Type
}

// ------------------------------------------------- PATCH /api/v1/me/password

func TestChangePassword_ReplacesTheCredential(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	tok := f.token(f.mustUser("alice"), "write")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/me/password",
		headers: map[string]string{"Authorization": "Bearer " + tok},
		body: map[string]any{
			"current_password": "correct horse battery",
			"new_password":     "staple battery horse",
		}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if f.canLogIn("alice", "correct horse battery") {
		t.Fatalf("the old password still logs in")
	}
	if !f.canLogIn("alice", "staple battery horse") {
		t.Fatalf("the new password does not log in")
	}
}

// The password and the tokens minted from it are independent credentials, so
// a change revokes the sessions and leaves the tokens alone
// (docs/dev/api-contract.md §1.3). Both halves are asserted here because
// either one silently flipping is a security-relevant surprise.
func TestChangePassword_RevokesSessionsButNotTokens(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	alice := f.mustUser("alice")
	tok := f.token(alice, "write")

	// A second browser, signed in before the change.
	other := f.login("alice", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/me/password",
		headers: map[string]string{"Authorization": "Bearer " + tok},
		body: map[string]any{
			"current_password": "correct horse battery",
			"new_password":     "staple battery horse",
		}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := f.do(secRequest{method: "GET", path: "/api/v1/me",
		cookies: []*http.Cookie{other}}); got.Code != http.StatusUnauthorized {
		t.Fatalf("the pre-change session cookie still works (status %d)", got.Code)
	}
	if got := f.do(secRequest{method: "GET", path: "/api/v1/me",
		headers: map[string]string{"Authorization": "Bearer " + tok}}); got.Code != http.StatusOK {
		t.Fatalf("the access token stopped working (status %d)", got.Code)
	}
}

// Changing your password must not sign you out of the tab you changed it in,
// even though the same call revokes every other session.
func TestChangePassword_CookieCallerIsReissued(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	cookie := f.login("alice", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/me/password",
		cookies: []*http.Cookie{cookie},
		headers: map[string]string{"Origin": "http://web.test.local"},
		body: map[string]any{
			"current_password": "correct horse battery",
			"new_password":     "staple battery horse",
		}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	fresh := sessionCookie(rec)
	if fresh == nil {
		t.Fatalf("no replacement session cookie was issued")
	}
	if fresh.Value == cookie.Value {
		t.Fatalf("the cookie value was not re-signed at the new epoch")
	}
	if got := f.do(secRequest{method: "GET", path: "/api/v1/me",
		cookies: []*http.Cookie{fresh}}); got.Code != http.StatusOK {
		t.Fatalf("the re-issued cookie does not authenticate (status %d)", got.Code)
	}
	if got := f.do(secRequest{method: "GET", path: "/api/v1/me",
		cookies: []*http.Cookie{cookie}}); got.Code != http.StatusUnauthorized {
		t.Fatalf("the superseded cookie still works (status %d)", got.Code)
	}
}

// A token-authenticated caller holds no cookie, so none is minted for it --
// otherwise a CI job's password change would hand back an ambient browser
// credential nobody asked for.
func TestChangePassword_TokenCallerGetsNoCookie(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	tok := f.token(f.mustUser("alice"), "write")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/me/password",
		headers: map[string]string{"Authorization": "Bearer " + tok},
		body: map[string]any{
			"current_password": "correct horse battery",
			"new_password":     "staple battery horse",
		}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if c := sessionCookie(rec); c != nil {
		t.Fatalf("a session cookie was issued to a token-authenticated caller")
	}
}

func TestChangePassword_Rejections(t *testing.T) {
	cases := []struct {
		name     string
		auth     string // "write", "read", "none"
		body     map[string]any
		want     int
		wantType string
	}{
		{
			name: "the current password must be right",
			auth: "write",
			body: map[string]any{"current_password": "wrong wrong wrong", "new_password": "staple battery horse"},
			want: http.StatusUnauthorized, wantType: "unauthorized",
		},
		{
			name: "an empty current password is not a way past the check",
			auth: "write",
			body: map[string]any{"current_password": "", "new_password": "staple battery horse"},
			want: http.StatusUnauthorized, wantType: "unauthorized",
		},
		{
			name: "the new password obeys the sign-up minimum",
			auth: "write",
			body: map[string]any{"current_password": "correct horse battery", "new_password": "short"},
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name: "the new password obeys bcrypt's ceiling",
			auth: "write",
			body: map[string]any{
				"current_password": "correct horse battery",
				"new_password":     "0123456789012345678901234567890123456789012345678901234567890123456789012345",
			},
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name: "a read-only token may not change the password",
			auth: "read",
			body: map[string]any{"current_password": "correct horse battery", "new_password": "staple battery horse"},
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name: "an anonymous caller may not change anyone's password",
			auth: "none",
			body: map[string]any{"current_password": "correct horse battery", "new_password": "staple battery horse"},
			want: http.StatusUnauthorized, wantType: "unauthorized",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSecFixture(t)
			f.user("alice", "correct horse battery")
			headers := map[string]string{}
			if tc.auth != "none" {
				headers["Authorization"] = "Bearer " + f.token(f.mustUser("alice"), tc.auth)
			}
			rec := f.do(secRequest{method: "PATCH", path: "/api/v1/me/password",
				headers: headers, body: tc.body})
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if got := recErrorType(t, rec); got != tc.wantType {
				t.Fatalf("error type = %q, want %q", got, tc.wantType)
			}
			// Whatever was refused, the stored credential is untouched.
			if !f.canLogIn("alice", "correct horse battery") {
				t.Fatalf("a refused request still changed the password")
			}
		})
	}
}

// ------------------------------------------------- GET /api/v1/admin/users

func TestAdminUsers_ListIsSiteAdminOnly(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")

	// Anonymous: 401, because there is nobody to refuse yet.
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}
	// A signed-in non-admin: 403, said out loud rather than hidden as a 404.
	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		cookies: f.session("alice", "correct horse battery")})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "forbidden" {
		t.Fatalf("error type = %q, want forbidden", got)
	}
	// The same answer for a non-administrator holding a token, and
	// deliberately not `session_required`: requireSiteAdmin runs the IsAdmin
	// check first, so the advice to sign in is never given to somebody whom
	// signing in would not help -- and never confirms that the account behind
	// a token is an administrator.
	aliceTok := f.token(f.mustUser("alice"), "write")
	rec = f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + aliceTok}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin token status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "forbidden" {
		t.Fatalf("non-admin token error type = %q, want forbidden", got)
	}
	// An administrator's browser session is the credential that works. There
	// is no read/write distinction to make here: a session always carries the
	// write scope (resolveCredential), so what a session may do is decided by
	// the endpoint alone. TestAdminAPI_AcceptsOnlyABrowserSession covers the
	// credentials this one no longer can.
	rec = f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		cookies: f.session("root", "correct horse battery")})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAdminUsers_ListSearchesAndPages(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	f.user("bob", "correct horse battery")
	rootSession := f.session("root", "correct horse battery")

	list := func(query string) apitypes.AdminUserListResponse {
		t.Helper()
		rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users" + query,
			cookies: rootSession})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
		var body apitypes.AdminUserListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return body
	}

	all := list("")
	if all.Total != 3 || len(all.Items) != 3 {
		t.Fatalf("listed %d of %d, want 3 of 3", len(all.Items), all.Total)
	}
	// The hash has no field on the wire type; assert on the raw JSON too, so
	// a future struct change that reintroduces one is caught here.
	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		cookies: rootSession})
	if body := rec.Body.String(); strings.Contains(body, "password") || strings.Contains(body, "hash") {
		t.Fatalf("the listing body mentions a password hash: %s", body)
	}

	if got := list("?search=ALICE"); got.Total != 1 || got.Items[0].Username != "alice" {
		t.Fatalf("search = %d rows, want just alice", got.Total)
	}
	if got := list("?search=bob@example.com"); got.Total != 1 || got.Items[0].Username != "bob" {
		t.Fatalf("email search = %d rows, want just bob", got.Total)
	}
	page := list("?limit=2&offset=1")
	if page.Total != 3 || len(page.Items) != 2 {
		t.Fatalf("page = %d rows of %d, want 2 of 3", len(page.Items), page.Total)
	}
	// The flag the UI badges on is the one the store holds.
	root := list("?search=root")
	if !root.Items[0].IsAdmin || root.Items[0].Email != "root@example.com" {
		t.Fatalf("root row = %+v, want an admin with an email", root.Items[0])
	}
}

// ------------------------------------- PATCH /api/v1/admin/users/{username}

func TestAdminUpdateUser_ResetsPasswordAndRevokesSessions(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")
	aliceCookie := f.login("alice", "forgotten forever")
	aliceTok := f.token(f.mustUser("alice"), "write")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		cookies: root,
		body:    map[string]any{"password": "issued by the admin"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body apitypes.AdminUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body.User.Username != "alice" || body.User.IsAdmin {
		t.Fatalf("response user = %+v, want alice with is_admin false", body.User)
	}

	if f.canLogIn("alice", "forgotten forever") {
		t.Fatalf("the old password still logs in")
	}
	if !f.canLogIn("alice", "issued by the admin") {
		t.Fatalf("the reset password does not log in")
	}
	if got := f.do(secRequest{method: "GET", path: "/api/v1/me",
		cookies: []*http.Cookie{aliceCookie}}); got.Code != http.StatusUnauthorized {
		t.Fatalf("alice's session survived the reset (status %d)", got.Code)
	}
	// Same rule as the self-service change: tokens are a separate credential.
	if got := f.do(secRequest{method: "GET", path: "/api/v1/me",
		headers: map[string]string{"Authorization": "Bearer " + aliceTok}}); got.Code != http.StatusOK {
		t.Fatalf("alice's access token was revoked by the reset (status %d)", got.Code)
	}
}

func TestAdminUpdateUser_GrantsAndRevokesAdmin(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	root := f.session("root", "correct horse battery")

	// Alice's own session, taken once and reused across both changes: neither
	// SetUserAdmin nor its reverse touches session_epoch, so the flag is the
	// only thing that moves between these three probes.
	alice := f.session("alice", "correct horse battery")

	// Before: alice cannot reach the directory at all.
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		cookies: alice}); rec.Code != http.StatusForbidden {
		t.Fatalf("alice status before promotion = %d, want 403", rec.Code)
	}

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		cookies: root,
		body:    map[string]any{"is_admin": true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		cookies: alice}); rec.Code != http.StatusOK {
		t.Fatalf("alice status after promotion = %d, want 200", rec.Code)
	}

	// And back again, now that there are two administrators.
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		cookies: root,
		body:    map[string]any{"is_admin": false}})
	if rec.Code != http.StatusOK {
		t.Fatalf("demote status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		cookies: alice}); rec.Code != http.StatusForbidden {
		t.Fatalf("alice status after demotion = %d, want 403", rec.Code)
	}
}

// Both changes in one request, and both taking effect.
func TestAdminUpdateUser_PasswordAndFlagTogether(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		cookies: f.session("root", "correct horse battery"),
		body:    map[string]any{"password": "issued by the admin", "is_admin": true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body apitypes.AdminUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !body.User.IsAdmin {
		t.Fatalf("response user = %+v, want is_admin true", body.User)
	}
	if !f.canLogIn("alice", "issued by the admin") {
		t.Fatalf("the reset password does not log in")
	}
}

// cred names the credential the actor presents. "session" is the only one the
// endpoint accepts at all, so the token rows here are not about scopes -- a
// session carries the write scope unconditionally and there is no read
// session to compare against -- but about the credential *kind* being refused
// before the request is even looked at.
func TestAdminUpdateUser_Rejections(t *testing.T) {
	cases := []struct {
		name     string
		actor    string // username the credential belongs to
		cred     string // "session", "write-token" or "read-token"
		target   string
		body     map[string]any
		want     int
		wantType string
	}{
		{
			name:  "a non-admin cannot reset anyone's password",
			actor: "alice", cred: "session", target: "bob",
			body: map[string]any{"password": "issued by the admin"},
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "a non-admin cannot promote themselves",
			actor: "alice", cred: "session", target: "alice",
			body: map[string]any{"is_admin": true},
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			// A write token is a full-strength credential everywhere else in
			// this server; site administration is the one place it is not
			// enough. Answered as session_required rather than forbidden, so
			// the caller is told what would work.
			name:  "an administrator's write token does not reach site administration",
			actor: "root", cred: "write-token", target: "alice",
			body: map[string]any{"password": "issued by the admin"},
			want: http.StatusForbidden, wantType: "session_required",
		},
		{
			// Refused too, but one step earlier and for the narrower reason:
			// requireSiteAdmin gates a state change with requireWrite, which
			// answers the scope before the session check is reached. Both
			// ends of that are 403 and neither reaches the handler; which
			// sentence comes back is an ordering detail, pinned here so a
			// reshuffle is a deliberate change rather than a surprise.
			name:  "nor does an administrator's read-only token",
			actor: "root", cred: "read-token", target: "alice",
			body: map[string]any{"password": "issued by the admin"},
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "an unknown username is a 404",
			actor: "root", cred: "session", target: "nobody",
			body: map[string]any{"password": "issued by the admin"},
			want: http.StatusNotFound, wantType: "not_found",
		},
		{
			name:  "a body that changes nothing is refused, not a silent no-op",
			actor: "root", cred: "session", target: "alice",
			body: map[string]any{},
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "the reset password obeys the same policy as sign-up",
			actor: "root", cred: "session", target: "alice",
			body: map[string]any{"password": "short"},
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "an administrator cannot demote themselves",
			actor: "root", cred: "session", target: "root",
			body: map[string]any{"is_admin": false},
			want: http.StatusBadRequest, wantType: "self_demote",
		},
		{
			name:  "an administrator cannot suspend themselves",
			actor: "root", cred: "session", target: "root",
			body: map[string]any{"disabled": true},
			want: http.StatusBadRequest, wantType: "self_disable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSecFixture(t)
			f.adminUser("root", "correct horse battery")
			f.user("alice", "correct horse battery")
			f.user("bob", "correct horse battery")
			req := secRequest{method: "PATCH", path: "/api/v1/admin/users/" + tc.target,
				body: tc.body}
			switch tc.cred {
			case "session":
				req.cookies = f.session(tc.actor, "correct horse battery")
			case "write-token", "read-token":
				scope := strings.TrimSuffix(tc.cred, "-token")
				req.headers = map[string]string{
					"Authorization": "Bearer " + f.token(f.mustUser(tc.actor), scope)}
			default:
				t.Fatalf("unknown credential %q", tc.cred)
			}

			rec := f.do(req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if got := recErrorType(t, rec); got != tc.wantType {
				t.Fatalf("error type = %q, want %q", got, tc.wantType)
			}
			// Nothing was applied on the way to the refusal.
			root, err := f.st.GetUserByUsername(context.Background(), "root")
			if err != nil || !root.IsAdmin {
				t.Fatalf("root lost is_admin on a refused request (%v)", err)
			}
			if root.Disabled() {
				t.Fatalf("root was suspended by a refused request")
			}
			if !f.canLogIn("alice", "correct horse battery") {
				t.Fatalf("a refused request still changed alice's password")
			}
		})
	}
}

// Validation runs before either mutation, so an impossible password cannot
// leave the administrator flag granted behind it.
func TestAdminUpdateUser_InvalidPasswordDoesNotGrantAdmin(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		cookies: f.session("root", "correct horse battery"),
		body:    map[string]any{"password": "short", "is_admin": true}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	alice, err := f.st.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("reload alice: %v", err)
	}
	if alice.IsAdmin {
		t.Fatalf("the refused request still granted alice site administrator rights")
	}
}

// The handler refuses self-demotion before the store is reached, so the
// store's own last-administrator guard (TestIntegrationSetUserAdmin) is
// unreachable from here: any *other* account the actor could demote implies a
// second administrator exists. This pins that reasoning down -- if the
// self-demotion rule is ever relaxed, this test stops passing and the 409
// path has to be wired up instead.
func TestAdminUpdateUser_DemotingAnotherAdminAlwaysLeavesOne(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.adminUser("second", "correct horse battery")
	rootSession := f.session("root", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/second",
		cookies: rootSession,
		body:    map[string]any{"is_admin": false}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// root is now the only one left, and cannot demote itself.
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root",
		cookies: rootSession,
		body:    map[string]any{"is_admin": false}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self-demotion status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	root, err := f.st.GetUserByUsername(context.Background(), "root")
	if err != nil || !root.IsAdmin {
		t.Fatalf("the instance lost its last administrator (%v)", err)
	}
}

// -------------------------------------------------- POST /api/v1/admin/users

func TestAdminCreateUser_AddsAWorkingAccount(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")

	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		cookies: f.session("root", "correct horse battery"),
		body: map[string]any{
			"username": "dana", "email": "dana@example.com", "password": "issued by the admin",
		}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var body apitypes.AdminUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if body.User.Username != "dana" || body.User.Email != "dana@example.com" || body.User.IsAdmin {
		t.Fatalf("response user = %+v, want dana, non-admin", body.User)
	}
	if body.User.ID == 0 || body.User.CreatedAt.IsZero() {
		t.Fatalf("response user = %+v, want a persisted id and timestamp", body.User)
	}
	// Nothing about the stored credential may appear on the wire.
	if raw := rec.Body.String(); strings.Contains(raw, "password") || strings.Contains(raw, "hash") ||
		strings.Contains(raw, "issued by the admin") {
		t.Fatalf("the response body mentions the password: %s", raw)
	}
	// The account is real: it logs in, and it owns its namespace.
	if !f.canLogIn("dana", "issued by the admin") {
		t.Fatalf("the created account cannot log in")
	}
	if _, err := f.st.GetNamespace(context.Background(), "dana"); err != nil {
		t.Fatalf("no namespace was created for dana: %v", err)
	}
	// Creating somebody else must not disturb the administrator's own session.
	if c := sessionCookie(rec); c != nil {
		t.Fatalf("a session cookie was issued for the account that was created")
	}
}

func TestAdminCreateUser_CanCreateAnotherAdministrator(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")

	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		cookies: f.session("root", "correct horse battery"),
		body: map[string]any{
			"username": "dana", "email": "dana@example.com",
			"password": "issued by the admin", "is_admin": true,
		}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var body apitypes.AdminUserResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !body.User.IsAdmin {
		t.Fatalf("response user = %+v, want is_admin true", body.User)
	}
	// The flag is real, not just echoed: dana can sign in and reach the
	// directory from her own browser session.
	if got := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		cookies: f.session("dana", "issued by the admin")}); got.Code != http.StatusOK {
		t.Fatalf("the new administrator cannot read the directory (status %d)", got.Code)
	}
}

// The reason this endpoint exists. TF_ALLOW_SIGNUP=false closes the public
// form; an instance run that way still has to be able to gain accounts, so
// this route must ignore the flag entirely.
func TestAdminCreateUser_WorksWithSignupDisabled(t *testing.T) {
	f := newSecFixture(t)
	f.cfg.AllowSignup = false
	f.adminUser("root", "correct horse battery")
	root := f.session("root", "correct horse battery")

	// The public form is shut, as configured.
	if rec := f.do(secRequest{method: "POST", path: "/api/v1/auth/signup",
		body: map[string]any{
			"username": "intruder", "email": "intruder@example.com", "password": "correct horse battery",
		}}); rec.Code != http.StatusForbidden {
		t.Fatalf("public signup status = %d, want 403", rec.Code)
	}
	// The administrator's route is not.
	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		cookies: root,
		body: map[string]any{
			"username": "dana", "email": "dana@example.com", "password": "issued by the admin",
		}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if !f.canLogIn("dana", "issued by the admin") {
		t.Fatalf("the created account cannot log in")
	}
}

func TestAdminCreateUser_Rejections(t *testing.T) {
	valid := map[string]any{
		"username": "dana", "email": "dana@example.com", "password": "issued by the admin",
	}
	merge := func(over map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range valid {
			out[k] = v
		}
		for k, v := range over {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		name     string
		actor    string // "" = anonymous
		cred     string // "session", "write-token" or "read-token"
		body     map[string]any
		want     int
		wantType string
	}{
		{
			name:  "an anonymous caller cannot create an account",
			actor: "", body: valid,
			want: http.StatusUnauthorized, wantType: "unauthorized",
		},
		{
			name:  "a non-admin cannot create an account",
			actor: "alice", cred: "session", body: valid,
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			// Creating accounts from a script is exactly what requiring the
			// cookie takes away: a leaked administrator token must not be
			// able to mint a second administrator.
			name:  "an administrator's write token cannot create an account",
			actor: "root", cred: "write-token", body: valid,
			want: http.StatusForbidden, wantType: "session_required",
		},
		{
			// Refused as read-only rather than as a token: requireWrite runs
			// before the session check on a state-changing route. See
			// TestAdminUpdateUser_Rejections for the same ordering note.
			name:  "an administrator's read-only token cannot either",
			actor: "root", cred: "read-token", body: valid,
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "a username already taken is a conflict",
			actor: "root", cred: "session", body: merge(map[string]any{"username": "alice"}),
			want: http.StatusConflict, wantType: "conflict",
		},
		{
			// The same rule sign-up applies: an account is also a namespace.
			name:  "a reserved username is refused",
			actor: "root", cred: "session", body: merge(map[string]any{"username": "settings"}),
			want: http.StatusBadRequest, wantType: "reserved_name",
		},
		{
			name:  "an invalid username is refused",
			actor: "root", cred: "session", body: merge(map[string]any{"username": "not a name"}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "the password obeys the same policy as sign-up",
			actor: "root", cred: "session", body: merge(map[string]any{"password": "short"}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "an address that is not an email is refused",
			actor: "root", cred: "session", body: merge(map[string]any{"email": "dana"}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "an empty email is refused",
			actor: "root", cred: "session", body: merge(map[string]any{"email": ""}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSecFixture(t)
			f.adminUser("root", "correct horse battery")
			f.user("alice", "correct horse battery")
			req := secRequest{method: "POST", path: "/api/v1/admin/users", body: tc.body}
			switch {
			case tc.actor == "":
			case tc.cred == "session":
				req.cookies = f.session(tc.actor, "correct horse battery")
			default:
				scope := strings.TrimSuffix(tc.cred, "-token")
				req.headers = map[string]string{
					"Authorization": "Bearer " + f.token(f.mustUser(tc.actor), scope)}
			}
			rec := f.do(req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if got := recErrorType(t, rec); got != tc.wantType {
				t.Fatalf("error type = %q, want %q", got, tc.wantType)
			}
			// Whatever was refused, no account came into existence, and the
			// pre-existing one is untouched.
			if _, err := f.st.GetUserByUsername(context.Background(), "dana"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("a refused request still created an account (%v)", err)
			}
			if !f.canLogIn("alice", "correct horse battery") {
				t.Fatalf("a refused request disturbed the existing account")
			}
		})
	}
}

// Validation runs before CreateUser, so a request carrying a bad password
// cannot leave a half-made account behind (the same shape of guarantee as
// TestAdminUpdateUser_InvalidPasswordDoesNotGrantAdmin).
func TestAdminCreateUser_InvalidPasswordCreatesNothing(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")

	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		cookies: f.session("root", "correct horse battery"),
		body: map[string]any{
			"username": "dana", "email": "dana@example.com", "password": "short",
		}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := f.st.GetUserByUsername(context.Background(), "dana"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused request still created dana (%v)", err)
	}
	// Nor a dangling namespace: CreateUser is one transaction, and it was
	// never reached.
	if _, err := f.st.GetNamespace(context.Background(), "dana"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused request still created a namespace (%v)", err)
	}
}

// ------------------------------------------------- ordering and saturation

// saturateBcrypt fills the password-hashing semaphore so the next hash can
// only time out, and returns a function that frees it again. Same technique
// as TestLogin_OverloadIsNotACredentialFailure.
func saturateBcrypt(t *testing.T, f *secFixture) func() {
	t.Helper()
	guard := f.s.authGuard
	for i := 0; i < cap(guard.sem); i++ {
		guard.sem <- struct{}{}
	}
	return func() {
		for i := 0; i < cap(guard.sem); i++ {
			<-guard.sem
		}
	}
}

// A PATCH that asks for both changes must not leave one of them behind when
// the other cannot be carried out. Hashing is the step that fails here --
// under saturation it refuses before bcrypt ever runs -- and it used to run
// *after* SetUserAdmin had already committed. Reported by Cursor Bugbot on
// #11.
func TestAdminUpdateUser_FailedHashLeavesAdminFlagUntouched(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	// Signed in before the semaphore is filled: logging in compares a bcrypt
	// hash too, so a session taken afterwards could not be issued at all.
	root := f.session("root", "correct horse battery")

	release := saturateBcrypt(t, f)

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		cookies: root,
		body:    map[string]any{"password": "issued by the admin", "is_admin": true}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}

	// The whole point: the request failed, so *nothing* may have happened.
	alice, err := f.st.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("reload alice: %v", err)
	}
	if alice.IsAdmin {
		t.Fatalf("the failed request still granted alice site administrator rights")
	}
	if alice.SessionEpoch != f.mustUser("alice").SessionEpoch {
		t.Fatalf("the failed request moved alice's session epoch")
	}

	release()
	if !f.canLogIn("alice", "forgotten forever") {
		t.Fatalf("the failed request still changed alice's password")
	}
}

// The same guarantee for the flag-only half: a demotion that the store
// refuses must not have been preceded by a password write.
func TestAdminUpdateUser_RefusedFlagLeavesPasswordUntouched(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")

	// Self-demotion is refused, and the request also carries a password.
	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root",
		cookies: f.session("root", "correct horse battery"),
		body:    map[string]any{"password": "issued by the admin", "is_admin": false}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !f.canLogIn("root", "correct horse battery") {
		t.Fatalf("the refused request still reset root's own password")
	}
	root, err := f.st.GetUserByUsername(context.Background(), "root")
	if err != nil || !root.IsAdmin {
		t.Fatalf("root lost is_admin on a refused request (%v)", err)
	}
}

// Running out of bcrypt slots is the server's problem, not the caller's, so
// it answers 503 `overloaded` -- the same way login already does. A spent
// address bucket is a real client limit and stays 429 `rate_limited`; the two
// must not be collapsed. Reported by Cursor Bugbot on #11.
//
// Each path is described by the credential its endpoint actually takes: the
// self-service change is reachable with a token, site administration only
// with a session. Both are acquired *before* the guard is spent, because
// signing in compares a bcrypt hash and spends the address budget like
// everything else here.
func TestPasswordWrites_SaturationIs503AndRateLimitIs429(t *testing.T) {
	paths := []struct {
		name    string
		request func(tok string, session []*http.Cookie) secRequest
	}{
		{
			name: "self-service change",
			request: func(tok string, _ []*http.Cookie) secRequest {
				return secRequest{method: "PATCH", path: "/api/v1/me/password",
					headers: map[string]string{"Authorization": "Bearer " + tok},
					body: map[string]any{
						"current_password": "correct horse battery",
						"new_password":     "staple battery horse",
					}}
			},
		},
		{
			name: "administrator reset",
			request: func(_ string, session []*http.Cookie) secRequest {
				return secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
					cookies: session,
					body:    map[string]any{"password": "issued by the admin"}}
			},
		},
		{
			name: "administrator create",
			request: func(_ string, session []*http.Cookie) secRequest {
				return secRequest{method: "POST", path: "/api/v1/admin/users",
					cookies: session,
					body: map[string]any{
						"username": "dana", "email": "dana@example.com",
						"password": "issued by the admin",
					}}
			},
		},
	}
	for _, tc := range paths {
		t.Run(tc.name+": no bcrypt slot is 503", func(t *testing.T) {
			f := newSecFixture(t)
			f.adminUser("root", "correct horse battery")
			f.user("alice", "correct horse battery")
			tok := f.token(f.mustUser("root"), "write")
			session := f.session("root", "correct horse battery")

			release := saturateBcrypt(t, f)
			defer release()

			rec := f.do(tc.request(tok, session))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
			}
			if got := recErrorType(t, rec); got != "overloaded" {
				t.Fatalf("error type = %q, want overloaded", got)
			}
			if rec.Header().Get("Retry-After") == "" {
				t.Error("Retry-After header missing: an honest client has nothing to go on")
			}
		})

		t.Run(tc.name+": a spent address bucket is 429", func(t *testing.T) {
			f := newSecFixture(t)
			f.adminUser("root", "correct horse battery")
			f.user("alice", "correct horse battery")
			tok := f.token(f.mustUser("root"), "write")
			session := f.session("root", "correct horse battery")

			// httptest's default RemoteAddr; drain its bucket outright.
			addr := "addr:192.0.2.1"
			for i := 0; i < 40; i++ {
				f.s.authGuard.penalize(addr)
			}
			rec := f.do(tc.request(tok, session))
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429 (body %s)", rec.Code, rec.Body.String())
			}
			if got := recErrorType(t, rec); got != "rate_limited" {
				t.Fatalf("error type = %q, want rate_limited", got)
			}
		})
	}
}

// TestAdminUpdateUser_SelfResetKeepsTheActorSignedIn pins the exception in
// handleAdminUpdateUser's cookie handling. The write revokes the target's
// sessions, which is right when the target is somebody else. Resetting your
// own password from the same screen would otherwise revoke the very cookie
// the request arrived on and sign the administrator out on their next click.
func TestAdminUpdateUser_SelfResetKeepsTheActorSignedIn(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	rootCookie := f.login("root", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root",
		cookies: []*http.Cookie{rootCookie},
		body:    map[string]any{"password": "a brand new passphrase"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	fresh := sessionCookie(rec)
	if fresh == nil {
		t.Fatal("no session cookie re-issued: the administrator is signed out by their own reset")
	}
	// Present is not enough -- it has to still authenticate at the new epoch.
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/me",
		cookies: []*http.Cookie{fresh}}); rec.Code != http.StatusOK {
		t.Fatalf("GET /me with the re-issued cookie = %d, want 200", rec.Code)
	}
	// The cookie the request arrived with is genuinely dead, so this is a
	// re-issue rather than the revocation having been skipped.
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/me",
		cookies: []*http.Cookie{rootCookie}}); rec.Code == http.StatusOK {
		t.Fatal("the pre-reset cookie still authenticates: sessions were not revoked")
	}
}

// The mirror case: resetting somebody else's password must not hand the actor
// a cookie, since the revoked sessions were never theirs.
func TestAdminUpdateUser_ResettingAnotherAccountIssuesNoCookie(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	rootCookie := f.login("root", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		cookies: []*http.Cookie{rootCookie},
		body:    map[string]any{"password": "issued by the admin"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if c := sessionCookie(rec); c != nil {
		t.Fatalf("session cookie issued (%q) while resetting another account's password", c.Value)
	}
}

// --------------------------------------- the credential /admin insists on

// Site administration is the one part of this API a token cannot reach
// (docs/dev/api-contract.md §1.3). Every /admin route is checked here, with
// every credential that is full-strength everywhere else -- a write token, a
// read token, and HTTP Basic carrying the administrator's real password --
// because a cross-cutting rule applied to only some of the routes is exactly
// the way this kind of gate fails. The session run at the end is what keeps a
// passing result honest: without it, a route that answered 403 to everybody
// would look like a success.
func TestAdminAPI_AcceptsOnlyABrowserSession(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	root := f.session("root", "correct horse battery")

	routes := []struct {
		name, method, path string
		// write says whether the route changes state, which is the one thing
		// that varies in the refusal below: requireSiteAdmin gates those with
		// requireWrite, so a read-scoped token is turned away for its scope
		// before the session check is reached. Every other credential, and
		// every credential at all on a read-only route, is refused as
		// session_required.
		write           bool
		body            map[string]any
		wantWithSession int
	}{
		{"list users", "GET", "/api/v1/admin/users", false, nil, http.StatusOK},
		{"create user", "POST", "/api/v1/admin/users", true, map[string]any{
			"username": "dana", "email": "dana@example.com", "password": "issued by the admin",
		}, http.StatusCreated},
		{"update user", "PATCH", "/api/v1/admin/users/alice", true,
			map[string]any{"disabled": true}, http.StatusOK},
		{"revoke credentials", "POST", "/api/v1/admin/users/alice/revoke-credentials",
			true, nil, http.StatusNoContent},
		{"list sync jobs", "GET", "/api/v1/admin/sync-jobs", false, nil, http.StatusOK},
		// A well-formed id matching no parked job. The credential check runs
		// long before the lookup, so 404 is what "the session got through"
		// looks like on this route.
		{"retry sync job", "POST", "/api/v1/admin/sync-jobs/4242/retry", true, nil, http.StatusNotFound},
	}
	credentials := []struct {
		name    string
		headers map[string]string
		// readOnly marks the credential that requireWrite refuses first.
		readOnly bool
	}{
		{name: "a write token", headers: map[string]string{
			"Authorization": "Bearer " + f.token(f.mustUser("root"), "write")}},
		{name: "a read token", readOnly: true, headers: map[string]string{
			"Authorization": "Bearer " + f.token(f.mustUser("root"), "read")}},
		// The password itself, which git and git-lfs authenticate with and
		// which carries the write scope on every other route.
		{name: "HTTP Basic with the right password", headers: map[string]string{
			"Authorization": basicAuth("root", "correct horse battery")}},
	}

	for _, rt := range routes {
		for _, cred := range credentials {
			t.Run(rt.name+" with "+cred.name, func(t *testing.T) {
				wantType := "session_required"
				if rt.write && cred.readOnly {
					wantType = "forbidden"
				}
				rec := f.do(secRequest{method: rt.method, path: rt.path,
					headers: cred.headers, body: rt.body})
				// The status is 403 for every one of them: whichever check
				// answers first, no token reaches the handler.
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
				}
				if got := recErrorType(t, rec); got != wantType {
					t.Fatalf("error type = %q, want %q", got, wantType)
				}
			})
		}
	}
	// None of what those requests asked for happened on the way to the 403.
	if _, err := f.st.GetUserByUsername(context.Background(), "dana"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused create still made an account (%v)", err)
	}
	if f.mustUser("alice").Disabled() {
		t.Fatal("a refused update still suspended alice")
	}

	for _, rt := range routes {
		t.Run(rt.name+" with a session", func(t *testing.T) {
			rec := f.do(secRequest{method: rt.method, path: rt.path,
				cookies: root, body: rt.body})
			if rec.Code != rt.wantWithSession {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, rt.wantWithSession, rec.Body.String())
			}
		})
	}
}

// ------------------------------------------- GET /api/v1/admin/sync-jobs
//
// A parked sync job is the one failure mode that leaves the instance looking
// healthy: the repository still serves its previous push, so only the file
// index, the search entry and the blobs/ export are stale. These cases drive
// the listing and the retry over real HTTP, the same way the rest of this file
// does.

// syncFixture is a secFixture whose SQLite file the test also opens directly.
//
// Parking a job through the public store API is not reachable from a test: it
// takes SyncMaxAttempts claims, and every failed attempt sets next_attempt_at
// far enough ahead that the next claim finds nothing due. The retry pacing is
// the store's business and is covered there; what these cases are about is the
// HTTP surface over a row that is already 'failed', so the row is written
// straight into the database. Both connections use WAL and a busy timeout, so
// the second handle is safe alongside the store's own.
type syncFixture struct {
	*secFixture
	dbPath string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(ctx, "sqlite://"+dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	gitMgr := gitrepo.NewManager(t.TempDir())
	obj := newMemStore()
	cfg := &config.Config{
		PublicURL:              "http://test.local",
		WALMode:                "off",
		SessionSecret:          "test-secret-test-secret-test-secret",
		AllowSignup:            true,
		AllowedOrigins:         []string{"http://web.test.local"},
		AuthRateLimitPerMinute: 10,
	}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  obj,
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})
	return &syncFixture{
		secFixture: &secFixture{t: t, s: srv, st: st, git: gitMgr, obj: obj, cfg: cfg},
		dbPath:     dbPath,
	}
}

// parkJob enqueues a job for the repository and flips it to the state the
// worker leaves behind once the attempt budget is spent.
func (f *syncFixture) parkJob(repo *store.Repo, ref, lastError string) int64 {
	f.t.Helper()
	if err := f.st.EnqueueSync(context.Background(), repo.ID, ref, "", strings.Repeat("a", 40)); err != nil {
		f.t.Fatalf("enqueue sync: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+f.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		f.t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	var id int64
	if err := db.QueryRow(
		`SELECT id FROM sync_jobs WHERE repo_id = ? AND ref = ? ORDER BY id DESC LIMIT 1`,
		repo.ID, ref).Scan(&id); err != nil {
		f.t.Fatalf("find sync job: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE sync_jobs SET status = 'failed', attempts = ?, last_error = ? WHERE id = ?`,
		store.SyncMaxAttempts, lastError, id); err != nil {
		f.t.Fatalf("park sync job: %v", err)
	}
	return id
}

// jobStatus reads a row's status back, for the cases that assert what the
// retry actually wrote rather than just what the listing shows.
func (f *syncFixture) jobStatus(id int64) (status string, attempts int) {
	f.t.Helper()
	db, err := sql.Open("sqlite", "file:"+f.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		f.t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT status, attempts FROM sync_jobs WHERE id = ?`, id).
		Scan(&status, &attempts); err != nil {
		f.t.Fatalf("read sync job %d: %v", id, err)
	}
	return status, attempts
}

func decodeSyncJobs(t *testing.T, rec *httptest.ResponseRecorder) apitypes.SyncJobListResponse {
	t.Helper()
	var body apitypes.SyncJobListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

// The repository is reported with its kind segment, so an operator can open
// the listed name directly instead of guessing which of the two kinds it is.
func TestAdminListSyncJobs_ReportsTheParkedWork(t *testing.T) {
	f := newSyncFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	ds := f.repo("alice", "imdb-ja", "dataset")
	md := f.repo("alice", "bert-ja", "model")
	f.parkJob(ds, "refs/heads/main", "publish blob: storage unavailable")
	f.parkJob(md, "refs/heads/main", "index metadata: bad safetensors header")

	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/sync-jobs",
		cookies: []*http.Cookie{f.login("root", "correct horse battery")}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := decodeSyncJobs(t, rec)
	if body.Total != 2 {
		t.Fatalf("total = %d, want 2", body.Total)
	}
	if len(body.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(body.Items))
	}
	byRepo := map[string]apitypes.SyncJob{}
	for _, j := range body.Items {
		byRepo[j.Repo] = j
	}
	dsJob, ok := byRepo["datasets/alice/imdb-ja"]
	if !ok {
		t.Fatalf("dataset job missing; got %v", byRepo)
	}
	if _, ok := byRepo["models/alice/bert-ja"]; !ok {
		t.Fatalf("model job missing; got %v", byRepo)
	}
	if dsJob.Ref != "refs/heads/main" {
		t.Fatalf("ref = %q", dsJob.Ref)
	}
	if dsJob.Attempts != store.SyncMaxAttempts {
		t.Fatalf("attempts = %d, want %d", dsJob.Attempts, store.SyncMaxAttempts)
	}
	if dsJob.LastError != "publish blob: storage unavailable" {
		t.Fatalf("last_error = %q", dsJob.LastError)
	}
	if dsJob.UpdatedAt.IsZero() {
		t.Fatal("updated_at is zero")
	}
}

// A job still retrying is not an operator's problem yet, so the listing is
// only ever the parked ones -- and it is empty rather than absent when there
// is nothing wrong.
func TestAdminListSyncJobs_OnlyReportsFailedJobs(t *testing.T) {
	f := newSyncFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "imdb-ja", "dataset")
	if err := f.st.EnqueueSync(context.Background(), repo.ID, "refs/heads/main", "", strings.Repeat("b", 40)); err != nil {
		t.Fatalf("enqueue sync: %v", err)
	}

	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/sync-jobs",
		cookies: []*http.Cookie{f.login("root", "correct horse battery")}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := decodeSyncJobs(t, rec)
	if body.Total != 0 || len(body.Items) != 0 {
		t.Fatalf("pending job was listed: total %d, items %v", body.Total, body.Items)
	}
	// Never null: the frontend maps over it directly.
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty page is not an empty array: %s", rec.Body.String())
	}
}

// The queue is instance-wide, so reading it is site administration and not
// something a repository owner is entitled to.
func TestAdminSyncJobs_AreSiteAdministrationOnly(t *testing.T) {
	f := newSyncFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "imdb-ja", "dataset")
	id := f.parkJob(repo, "refs/heads/main", "boom")
	retry := "/api/v1/admin/sync-jobs/" + strconv.FormatInt(id, 10) + "/retry"

	endpoints := []struct {
		name, method, path string
		write              bool
	}{
		{"list", "GET", "/api/v1/admin/sync-jobs", false},
		{"retry", "POST", retry, true},
	}

	// The repository's own owner, signed in: the queue is instance-wide, so
	// owning the repository a job belongs to entitles them to nothing here.
	alice := f.session("alice", "correct horse battery")
	for _, c := range endpoints {
		rec := f.do(secRequest{method: c.method, path: c.path, cookies: alice})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s as a non-administrator = %d, want 403 (body %s)", c.name, rec.Code, rec.Body.String())
		}
		if got := recErrorType(t, rec); got != "forbidden" {
			t.Fatalf("%s as a non-administrator: error type = %q, want forbidden", c.name, got)
		}
	}

	// Anonymous, which is a different answer: log in, not "you may not".
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/sync-jobs"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", rec.Code)
	}

	// An administrator's token reaches neither endpoint, whatever its scope.
	// Reading the parked queue used to be open to a read-scoped token; it is
	// site administration like the rest of /admin, and the operator screen
	// that shows it is in the web UI.
	for _, scope := range []string{"read", "write"} {
		tok := f.token(f.mustUser("root"), scope)
		for _, c := range endpoints {
			// Retrying with a read token is refused one check earlier, for
			// its scope rather than for being a token; both are 403 and
			// neither reaches the handler (see
			// TestAdminAPI_AcceptsOnlyABrowserSession).
			wantType := "session_required"
			if c.write && scope == "read" {
				wantType = "forbidden"
			}
			rec := f.do(secRequest{method: c.method, path: c.path,
				headers: map[string]string{"Authorization": "Bearer " + tok}})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s with a %s token = %d, want 403 (body %s)",
					c.name, scope, rec.Code, rec.Body.String())
			}
			if got := recErrorType(t, rec); got != wantType {
				t.Fatalf("%s with a %s token: error type = %q, want %q", c.name, scope, got, wantType)
			}
		}
	}
	if status, _ := f.jobStatus(id); status != "failed" {
		t.Fatalf("a refused retry moved the job to %q", status)
	}

	// And the administrator's own session does reach the listing, so the
	// refusals above are about the credential rather than a route that
	// answers 403 to everybody.
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/sync-jobs",
		cookies: f.session("root", "correct horse battery")}); rec.Code != http.StatusOK {
		t.Fatalf("list with an administrator session = %d, want 200", rec.Code)
	}
}

// ------------------------ POST /api/v1/admin/sync-jobs/{id}/retry

func TestAdminRetrySyncJob_RequeuesTheJobOnce(t *testing.T) {
	f := newSyncFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "imdb-ja", "dataset")
	id := f.parkJob(repo, "refs/heads/main", "publish blob: storage unavailable")
	root := f.login("root", "correct horse battery")
	retry := "/api/v1/admin/sync-jobs/" + strconv.FormatInt(id, 10) + "/retry"

	rec := f.do(secRequest{method: "POST", path: retry, cookies: []*http.Cookie{root}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	status, attempts := f.jobStatus(id)
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
	// A fresh budget, otherwise the requeued job parks again on its first
	// claim without having been given a real chance.
	if attempts != 0 {
		t.Fatalf("attempts = %d, want 0", attempts)
	}

	// It leaves the listing, which is what tells the operator it was taken.
	rec = f.do(secRequest{method: "GET", path: "/api/v1/admin/sync-jobs",
		cookies: []*http.Cookie{root}})
	if body := decodeSyncJobs(t, rec); body.Total != 0 || len(body.Items) != 0 {
		t.Fatalf("the requeued job is still listed: %v", body)
	}

	// A second retry -- the same operator double-clicking, or a colleague on
	// the same screen -- is 404 rather than a silent 204 that would reset the
	// attempt counter of work already in flight.
	rec = f.do(secRequest{method: "POST", path: retry, cookies: []*http.Cookie{root}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second retry = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAdminRetrySyncJob_RejectsUnusableIDs(t *testing.T) {
	f := newSyncFixture(t)
	f.adminUser("root", "correct horse battery")
	root := f.login("root", "correct horse battery")

	tests := []struct {
		id   string
		want int
	}{
		{"not-a-number", http.StatusBadRequest},
		{"12x", http.StatusBadRequest},
		// Well-formed but nonexistent: the caller asked about a job, and the
		// answer is that there is no such job.
		{"4242", http.StatusNotFound},
	}
	for _, tc := range tests {
		rec := f.do(secRequest{method: "POST",
			path:    "/api/v1/admin/sync-jobs/" + tc.id + "/retry",
			cookies: []*http.Cookie{root}})
		if rec.Code != tc.want {
			t.Fatalf("retry %q = %d, want %d (body %s)", tc.id, rec.Code, tc.want, rec.Body.String())
		}
	}
}
