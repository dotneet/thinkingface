// Sign-up control (TF_SIGNUP_EMAIL_DOMAINS, TF_SIGNUP_REQUIRE_APPROVAL) and
// the dormancy timestamp that goes with it (users.last_login_at).
//
// The waiting room is a second gate of exactly the same kind as the
// suspension switch in disabled_test.go, so the central test here is written
// the same way: one account is given every credential this server accepts and
// each one is checked while the account is pending, and again once it has
// been approved. A gate that covers the browser and forgets the SSH key is
// the failure worth a test at all.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// signup drives POST /api/v1/auth/signup the way the web UI's form does.
func (f *secFixture) signup(username, email, password string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(secRequest{method: "POST", path: "/api/v1/auth/signup",
		body: map[string]any{"username": username, "email": email, "password": password}})
}

// setApproval drives the administration endpoint and insists on the status,
// so a test that meant to approve somebody cannot quietly go on asserting
// against an account that was never touched. setDisabled's counterpart.
func (f *secFixture) setApproval(admin []*http.Cookie, username string, approval apitypes.UserApproval) {
	f.t.Helper()
	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/" + username,
		cookies: admin, body: map[string]any{"approval": string(approval)}})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("set approval=%s on %s: status %d, body %s",
			approval, username, rec.Code, rec.Body.String())
	}
}

// adminUserRow reads one account out of the administrator's directory, which
// is the only place approval and last-login are visible on the wire.
func (f *secFixture) adminUserRow(admin []*http.Cookie, username string) apitypes.AdminUser {
	f.t.Helper()
	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/users?search=" + username, cookies: admin})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("list users: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body apitypes.AdminUserListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		f.t.Fatalf("decode account directory %q: %v", rec.Body.String(), err)
	}
	for _, u := range body.Items {
		if u.Username == username {
			return u
		}
	}
	f.t.Fatalf("no %s in the account directory (%d rows)", username, len(body.Items))
	return apitypes.AdminUser{}
}

// ------------------------------------------------------- the waiting room

func TestPendingAccount_EveryIdentityPathRefuses(t *testing.T) {
	f := newSecFixture(t)
	f.cfg.SignupRequireApproval = true
	f.adminUser("root", "correct horse battery")
	root := f.session("root", "correct horse battery")

	rec := f.signup("alice", "alice@example.com", "forgotten forever")
	// Error-shaped on purpose: the account exists, but the thing the caller
	// asked for -- a session -- did not happen, and this response is the only
	// place they will ever be told why.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("signup while approval is required = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "approval_pending" {
		t.Fatalf("signup error type = %q, want approval_pending", got)
	}
	if c := sessionCookie(rec); c != nil {
		t.Fatal("signup issued a session cookie for an account that cannot authenticate")
	}
	// The account is nonetheless real: the administrator has something to
	// approve, and it is marked as waiting rather than silently active.
	if got := f.adminUserRow(root, "alice").Approval; got != apitypes.UserApprovalPending {
		t.Fatalf("approval after signup = %q, want pending", got)
	}

	// Give the pending account the rest of the credential set directly, the
	// way an instance would end up with them if approval were flipped on
	// after the fact -- and so the token and SSH paths are tested at all,
	// since a pending account cannot mint them through the API.
	alice := f.mustUser("alice")
	token := f.token(alice, "write")
	if _, err := f.st.CreateSSHKey(context.Background(), alice.ID, "laptop",
		"ssh-ed25519 AAAAalice", "SHA256:alice"); err != nil {
		t.Fatalf("create ssh key: %v", err)
	}

	// Password. Reachable only with the *correct* password, so the answer
	// names the reason rather than sending somebody round a reset loop for a
	// password that is perfectly good.
	rec = f.do(secRequest{method: "POST", path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": "forgotten forever"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login while pending = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "account_pending" {
		t.Fatalf("login error type = %q, want account_pending", got)
	}
	// HTTP Basic with the same password: a different branch of
	// resolveCredential, and the one `git` and `huggingface_hub` use.
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": basicAuth("alice", "forgotten forever")}}); got != http.StatusUnauthorized {
		t.Fatalf("HTTP Basic while pending = %d, want 401", got)
	}
	// Access token.
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + token}}); got != http.StatusUnauthorized {
		t.Fatalf("token while pending = %d, want 401", got)
	}
	// SSH key. internal/sshserver authenticates before this package runs, so
	// the gate there is the store lookup.
	if f.sshResolves("SHA256:alice") {
		t.Fatal("the SSH key resolves for an account that has never been approved")
	}
	// And ServeGit's own check, which takes its user from another package
	// rather than from the lookup that just refused.
	if got := f.serveGitError(f.mustUser("alice"), "alice", "no-such-repo"); !strings.Contains(got, "approve") {
		t.Fatalf("ServeGit while pending = %q, want a refusal naming the approval", got)
	}

	// Approve, and every one of them starts working. Without this half the
	// test would pass just as well against credentials that never worked.
	f.setApproval(root, "alice", apitypes.UserApprovalApproved)

	if !f.canLogIn("alice", "forgotten forever") {
		t.Fatal("the approved account cannot log in")
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": basicAuth("alice", "forgotten forever")}}); got != http.StatusOK {
		t.Fatalf("HTTP Basic after approval = %d, want 200", got)
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + token}}); got != http.StatusOK {
		t.Fatalf("token after approval = %d, want 200", got)
	}
	if !f.sshResolves("SHA256:alice") {
		t.Fatal("the SSH key still does not resolve after approval")
	}
	if got := f.adminUserRow(root, "alice").Approval; got != apitypes.UserApprovalApproved {
		t.Fatalf("approval after approving = %q, want approved", got)
	}
}

// Sending an account back to the waiting room is the reverse of approving it,
// and it has to reach the cookies already issued -- otherwise the decision
// only takes effect when they expire.
func TestPendingAccount_UnapprovingRevokesSessions(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")
	alice := f.session("alice", "forgotten forever")

	if got := f.meStatus(secRequest{cookies: alice}); got != http.StatusOK {
		t.Fatalf("session before = %d, want 200", got)
	}
	f.setApproval(root, "alice", apitypes.UserApprovalPending)
	if got := f.meStatus(secRequest{cookies: alice}); got != http.StatusUnauthorized {
		t.Fatalf("session after un-approving = %d, want 401", got)
	}
}

// The account that predates the column, and every account an administrator
// creates, is approved. This is the migration's whole safety property: a
// column whose default meant "not allowed in" would have locked every
// existing account out of the instance the moment it ran.
func TestApproval_ExistingAccountsAreApproved(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")

	for _, name := range []string{"root", "alice"} {
		if got := f.adminUserRow(root, name).Approval; got != apitypes.UserApprovalApproved {
			t.Errorf("%s approval = %q, want approved", name, got)
		}
	}
	if !f.canLogIn("alice", "forgotten forever") {
		t.Fatal("an account created before approval existed cannot log in")
	}

	// An administrator adding somebody is not a self-registration, so it does
	// not land in the waiting room either -- there would be nobody left to
	// approve it.
	f.cfg.SignupRequireApproval = true
	rec := f.do(secRequest{method: "POST", path: "/api/v1/admin/users", cookies: root,
		body: map[string]any{"username": "dana", "email": "dana@example.com", "password": "another good one"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if got := f.adminUserRow(root, "dana").Approval; got != apitypes.UserApprovalApproved {
		t.Fatalf("an administrator-created account is %q, want approved", got)
	}
	if !f.canLogIn("dana", "another good one") {
		t.Fatal("an administrator-created account cannot log in while approval is required")
	}
}

// The two self-lockout rules, the same pair suspension has.
func TestApproval_RefusesSelfAndTheLastAdministrator(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.adminUser("second", "another good passphrase")
	root := f.session("root", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root", cookies: root,
		body: map[string]any{"approval": "pending"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un-approving yourself = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "self_pending" {
		t.Fatalf("error type = %q, want self_pending", got)
	}

	// With two administrators the last-administrator rule does not bite, so
	// the second one can be sent back; after that the first is the only one
	// left and the guard refuses.
	f.setApproval(root, "second", apitypes.UserApprovalPending)
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root", cookies: root,
		body: map[string]any{"approval": "pending"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un-approving the last administrator (as yourself) = %d, want 400", rec.Code)
	}
	// Reached through somebody else, the count guard is what answers, and it
	// answers with the same 409 `last_admin` the neighbouring fields do.
	f.setApproval(root, "second", apitypes.UserApprovalApproved)
	second := f.session("second", "another good passphrase")
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root", cookies: second,
		body: map[string]any{"approval": "pending"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("un-approving one of two administrators = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/second", cookies: second,
		body: map[string]any{"approval": "pending"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("un-approving yourself as the last administrator = %d, want 400", rec.Code)
	}
}

// An unrecognised value must not be read as one of the two. Without this the
// zero value of the string would silently mean "pending" and a typo would
// lock somebody out.
func TestApproval_RejectsAnUnknownValue(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/alice", cookies: root,
		body: map[string]any{"approval": "maybe"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approval=maybe = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if got := f.adminUserRow(root, "alice").Approval; got != apitypes.UserApprovalApproved {
		t.Fatalf("alice is %q after a refused request, want approved", got)
	}
}

// ------------------------------------------------------- the domain list

func TestSignupEmailDomain_MatchesExactlyAndIgnoresCase(t *testing.T) {
	allowed := []string{"example.com", "corp.example.org"}
	cases := []struct {
		email string
		ok    bool
		why   string
	}{
		{"alice@example.com", true, "the plain case"},
		{"Alice@EXAMPLE.COM", true, "domains are case-insensitive"},
		{"alice@Example.Com", true, "so is a mixed-case domain"},
		{"bob@corp.example.org", true, "a second entry in the list"},
		{"mallory@evil.com", false, "a domain that is not listed"},
		{"mallory@sub.example.com", false, "a subdomain does not inherit its parent"},
		{"mallory@notexample.com", false, "a suffix match is not a match"},
		{"mallory@example.com.evil.com", false, "nor is a prefix one"},
		{"mallory@example.co", false, "nor a shorter one"},
	}
	for _, tc := range cases {
		err := checkSignupEmailDomain(allowed, tc.email)
		if (err == nil) != tc.ok {
			t.Errorf("checkSignupEmailDomain(%q) error = %v, want ok=%v (%s)", tc.email, err, tc.ok, tc.why)
		}
	}
	// An empty list is no restriction, which is the default and the only
	// behaviour that existed before it.
	if err := checkSignupEmailDomain(nil, "anyone@anywhere.test"); err != nil {
		t.Errorf("an empty allow list refused %v", err)
	}
}

func TestSignup_RefusesAnEmailOutsideTheAllowedDomains(t *testing.T) {
	f := newSecFixture(t)
	f.cfg.SignupEmailDomains = []string{"example.com"}
	f.adminUser("root", "correct horse battery")
	root := f.session("root", "correct horse battery")

	rec := f.signup("mallory", "mallory@evil.test", "a good long password")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("signup from an unlisted domain = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	// The refusal names the domains it wants. A sign-up form that refuses an
	// address without saying which addresses it accepts is one nobody can
	// fill in, and the list is the deployment's own domain, not a secret.
	if body := rec.Body.String(); !strings.Contains(body, "example.com") {
		t.Errorf("the refusal does not name the accepted domains: %s", body)
	}
	// Nothing was written: a rejected sign-up must not leave a namespace
	// behind that the name can never be reused for.
	if _, err := f.st.GetUserByUsername(context.Background(), "mallory"); err == nil {
		t.Fatal("a refused sign-up created the account anyway")
	}

	// The listed domain still works, and lands approved -- the two settings
	// are independent.
	rec = f.signup("alice", "alice@example.com", "a good long password")
	if rec.Code != http.StatusOK {
		t.Fatalf("signup from the allowed domain = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := f.adminUserRow(root, "alice").Approval; got != apitypes.UserApprovalApproved {
		t.Fatalf("approval = %q, want approved: the domain list is not the approval switch", got)
	}
}

// ------------------------------------------------------- last_login_at

func TestLastLogin_MovesOnlyForAPasswordLogin(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")
	token := f.token(f.mustUser("alice"), "write")

	// A fresh account has never signed in, and says so rather than claiming
	// its creation date.
	if got := f.adminUserRow(root, "alice").LastLoginAt; got != nil {
		t.Fatalf("last_login_at on a fresh account = %v, want null", got)
	}

	// A token is not a sign-in. It carries its own last-used timestamp, and
	// the question this column answers -- "is anybody still using this
	// account" -- is one a nightly CI job answers wrongly.
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + token}}); got != http.StatusOK {
		t.Fatalf("token request = %d, want 200", got)
	}
	if got := f.adminUserRow(root, "alice").LastLoginAt; got != nil {
		t.Fatalf("last_login_at after a token request = %v, want null", got)
	}
	// Neither is HTTP Basic: no session is minted there, and a `git fetch`
	// loop would otherwise write to the users table on every request.
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": basicAuth("alice", "forgotten forever")}}); got != http.StatusOK {
		t.Fatalf("HTTP Basic request = %d, want 200", got)
	}
	if got := f.adminUserRow(root, "alice").LastLoginAt; got != nil {
		t.Fatalf("last_login_at after HTTP Basic = %v, want null", got)
	}

	before := time.Now().Add(-time.Minute)
	f.login("alice", "forgotten forever")
	got := f.adminUserRow(root, "alice").LastLoginAt
	if got == nil {
		t.Fatal("last_login_at is still null after a password login")
	}
	if got.Before(before) {
		t.Fatalf("last_login_at = %s, want something at least as recent as %s", got, before)
	}

	// A failed login does not move it either: the column is "when did this
	// account last get in", not "when was it last tried".
	first := *got
	f.do(secRequest{method: "POST", path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": "not the password"}})
	after := f.adminUserRow(root, "alice").LastLoginAt
	if after == nil || !after.Equal(first) {
		t.Fatalf("last_login_at moved on a failed login: %v -> %v", first, after)
	}
}

// A pending account never signs in, so its last-login stays null through the
// refusal -- the timestamp is written after the gates, not before them.
func TestLastLogin_NotWrittenForARefusedLogin(t *testing.T) {
	f := newSecFixture(t)
	f.cfg.SignupRequireApproval = true
	f.adminUser("root", "correct horse battery")
	root := f.session("root", "correct horse battery")

	if rec := f.signup("alice", "alice@example.com", "forgotten forever"); rec.Code != http.StatusForbidden {
		t.Fatalf("signup = %d, want 403", rec.Code)
	}
	f.do(secRequest{method: "POST", path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": "forgotten forever"}})
	if got := f.adminUserRow(root, "alice").LastLoginAt; got != nil {
		t.Fatalf("last_login_at after a refused login = %v, want null", got)
	}
}

// The store's own view of the same rule, for the paths that do not go through
// HTTP at all.
func TestStore_TouchUserLoginAndApproval(t *testing.T) {
	f := newSecFixture(t)
	ctx := context.Background()
	f.user("alice", "forgotten forever")

	alice := f.mustUser("alice")
	if alice.LastLoginAt != nil || alice.PendingApproval() {
		t.Fatalf("a fresh account is %+v, want no last login and approved", alice)
	}
	if err := f.st.TouchUserLogin(ctx, alice.ID); err != nil {
		t.Fatalf("TouchUserLogin: %v", err)
	}
	if f.mustUser("alice").LastLoginAt == nil {
		t.Fatal("TouchUserLogin did not write the column")
	}

	if err := f.st.SetUserApproval(ctx, "ALICE", false); err != nil {
		t.Fatalf("SetUserApproval: %v", err)
	}
	// Addressed case-insensitively, like every other username lookup here.
	pending := f.mustUser("alice")
	if !pending.PendingApproval() {
		t.Fatal("SetUserApproval(false) did not put the account back in the waiting room")
	}
	if pending.SessionEpoch != alice.SessionEpoch+1 {
		t.Fatalf("session_epoch = %d, want %d: un-approving must revoke sessions",
			pending.SessionEpoch, alice.SessionEpoch+1)
	}
	// Idempotent: a retried request must not bump the epoch a second time.
	if err := f.st.SetUserApproval(ctx, "alice", false); err != nil {
		t.Fatalf("SetUserApproval (repeat): %v", err)
	}
	if got := f.mustUser("alice").SessionEpoch; got != pending.SessionEpoch {
		t.Fatalf("session_epoch = %d after a repeated call, want %d", got, pending.SessionEpoch)
	}
	if err := f.st.SetUserApproval(ctx, "alice", true); err != nil {
		t.Fatalf("SetUserApproval(true): %v", err)
	}
	if f.mustUser("alice").PendingApproval() {
		t.Fatal("SetUserApproval(true) did not admit the account")
	}

	if err := f.st.SetUserApproval(ctx, "nobody", true); err == nil {
		t.Fatal("SetUserApproval on an unknown username succeeded")
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetUserApproval on an unknown username = %v, want ErrNotFound", err)
	}
}
