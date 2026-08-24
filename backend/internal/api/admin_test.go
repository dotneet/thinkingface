package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
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
	aliceTok := f.token(f.mustUser("alice"), "write")
	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + aliceTok}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "forbidden" {
		t.Fatalf("error type = %q, want forbidden", got)
	}
	// A read-scoped token is enough to *read* the directory.
	rootTok := f.token(f.mustUser("root"), "read")
	rec = f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + rootTok}})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAdminUsers_ListSearchesAndPages(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	f.user("bob", "correct horse battery")
	tok := f.token(f.mustUser("root"), "read")

	list := func(query string) apitypes.AdminUserListResponse {
		t.Helper()
		rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users" + query,
			headers: map[string]string{"Authorization": "Bearer " + tok}})
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
		headers: map[string]string{"Authorization": "Bearer " + tok}})
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
	rootTok := f.token(f.mustUser("root"), "write")
	aliceCookie := f.login("alice", "forgotten forever")
	aliceTok := f.token(f.mustUser("alice"), "write")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		headers: map[string]string{"Authorization": "Bearer " + rootTok},
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
	rootTok := f.token(f.mustUser("root"), "write")

	// Before: alice cannot reach the directory at all.
	aliceTok := f.token(f.mustUser("alice"), "write")
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + aliceTok}}); rec.Code != http.StatusForbidden {
		t.Fatalf("alice status before promotion = %d, want 403", rec.Code)
	}

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		headers: map[string]string{"Authorization": "Bearer " + rootTok},
		body:    map[string]any{"is_admin": true}})
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + aliceTok}}); rec.Code != http.StatusOK {
		t.Fatalf("alice status after promotion = %d, want 200", rec.Code)
	}

	// And back again, now that there are two administrators.
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		headers: map[string]string{"Authorization": "Bearer " + rootTok},
		body:    map[string]any{"is_admin": false}})
	if rec.Code != http.StatusOK {
		t.Fatalf("demote status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + aliceTok}}); rec.Code != http.StatusForbidden {
		t.Fatalf("alice status after demotion = %d, want 403", rec.Code)
	}
}

// Both changes in one request, and both taking effect.
func TestAdminUpdateUser_PasswordAndFlagTogether(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	rootTok := f.token(f.mustUser("root"), "write")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		headers: map[string]string{"Authorization": "Bearer " + rootTok},
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

func TestAdminUpdateUser_Rejections(t *testing.T) {
	cases := []struct {
		name     string
		actor    string // username whose token is used
		scope    string
		target   string
		body     map[string]any
		want     int
		wantType string
	}{
		{
			name:  "a non-admin cannot reset anyone's password",
			actor: "alice", scope: "write", target: "bob",
			body: map[string]any{"password": "issued by the admin"},
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "a non-admin cannot promote themselves",
			actor: "alice", scope: "write", target: "alice",
			body: map[string]any{"is_admin": true},
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "an admin's read-only token cannot change anything",
			actor: "root", scope: "read", target: "alice",
			body: map[string]any{"password": "issued by the admin"},
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "an unknown username is a 404",
			actor: "root", scope: "write", target: "nobody",
			body: map[string]any{"password": "issued by the admin"},
			want: http.StatusNotFound, wantType: "not_found",
		},
		{
			name:  "a body that changes nothing is refused, not a silent no-op",
			actor: "root", scope: "write", target: "alice",
			body: map[string]any{},
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "the reset password obeys the same policy as sign-up",
			actor: "root", scope: "write", target: "alice",
			body: map[string]any{"password": "short"},
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "an administrator cannot demote themselves",
			actor: "root", scope: "write", target: "root",
			body: map[string]any{"is_admin": false},
			want: http.StatusBadRequest, wantType: "self_demote",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSecFixture(t)
			f.adminUser("root", "correct horse battery")
			f.user("alice", "correct horse battery")
			f.user("bob", "correct horse battery")
			tok := f.token(f.mustUser(tc.actor), tc.scope)

			rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/" + tc.target,
				headers: map[string]string{"Authorization": "Bearer " + tok},
				body:    tc.body})
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
	tok := f.token(f.mustUser("root"), "write")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
	tok := f.token(f.mustUser("root"), "write")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/second",
		headers: map[string]string{"Authorization": "Bearer " + tok},
		body:    map[string]any{"is_admin": false}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// root is now the only one left, and cannot demote itself.
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
	tok := f.token(f.mustUser("root"), "write")

	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
	tok := f.token(f.mustUser("root"), "write")

	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
	// The flag is real, not just echoed: dana can reach the directory.
	danaTok := f.token(f.mustUser("dana"), "read")
	if got := f.do(secRequest{method: "GET", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + danaTok}}); got.Code != http.StatusOK {
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
	tok := f.token(f.mustUser("root"), "write")

	// The public form is shut, as configured.
	if rec := f.do(secRequest{method: "POST", path: "/api/v1/auth/signup",
		body: map[string]any{
			"username": "intruder", "email": "intruder@example.com", "password": "correct horse battery",
		}}); rec.Code != http.StatusForbidden {
		t.Fatalf("public signup status = %d, want 403", rec.Code)
	}
	// The administrator's route is not.
	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
		scope    string
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
			actor: "alice", scope: "write", body: valid,
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "an admin's read-only token cannot create an account",
			actor: "root", scope: "read", body: valid,
			want: http.StatusForbidden, wantType: "forbidden",
		},
		{
			name:  "a username already taken is a conflict",
			actor: "root", scope: "write", body: merge(map[string]any{"username": "alice"}),
			want: http.StatusConflict, wantType: "conflict",
		},
		{
			// The same rule sign-up applies: an account is also a namespace.
			name:  "a reserved username is refused",
			actor: "root", scope: "write", body: merge(map[string]any{"username": "settings"}),
			want: http.StatusBadRequest, wantType: "reserved_name",
		},
		{
			name:  "an invalid username is refused",
			actor: "root", scope: "write", body: merge(map[string]any{"username": "not a name"}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "the password obeys the same policy as sign-up",
			actor: "root", scope: "write", body: merge(map[string]any{"password": "short"}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "an address that is not an email is refused",
			actor: "root", scope: "write", body: merge(map[string]any{"email": "dana"}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
		{
			name:  "an empty email is refused",
			actor: "root", scope: "write", body: merge(map[string]any{"email": ""}),
			want: http.StatusBadRequest, wantType: "bad_request",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSecFixture(t)
			f.adminUser("root", "correct horse battery")
			f.user("alice", "correct horse battery")
			headers := map[string]string{}
			if tc.actor != "" {
				headers["Authorization"] = "Bearer " + f.token(f.mustUser(tc.actor), tc.scope)
			}
			rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
				headers: headers, body: tc.body})
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
	tok := f.token(f.mustUser("root"), "write")

	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
	tok := f.token(f.mustUser("root"), "write")

	release := saturateBcrypt(t, f)

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
	tok := f.token(f.mustUser("root"), "write")

	// Self-demotion is refused, and the request also carries a password.
	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root",
		headers: map[string]string{"Authorization": "Bearer " + tok},
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
func TestPasswordWrites_SaturationIs503AndRateLimitIs429(t *testing.T) {
	paths := []struct {
		name    string
		request func(f *secFixture, tok string) secRequest
	}{
		{
			name: "self-service change",
			request: func(_ *secFixture, tok string) secRequest {
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
			request: func(_ *secFixture, tok string) secRequest {
				return secRequest{method: "PATCH", path: "/api/v1/admin/users/alice",
					headers: map[string]string{"Authorization": "Bearer " + tok},
					body:    map[string]any{"password": "issued by the admin"}}
			},
		},
		{
			name: "administrator create",
			request: func(_ *secFixture, tok string) secRequest {
				return secRequest{method: "POST", path: "/api/v1/admin/users",
					headers: map[string]string{"Authorization": "Bearer " + tok},
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

			release := saturateBcrypt(t, f)
			defer release()

			rec := f.do(tc.request(f, tok))
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

			// httptest's default RemoteAddr; drain its bucket outright.
			addr := "addr:192.0.2.1"
			for i := 0; i < 40; i++ {
				f.s.authGuard.penalize(addr)
			}
			rec := f.do(tc.request(f, tok))
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
