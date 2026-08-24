// Regression tests for the offboarding switch: suspending an account
// (PATCH /api/v1/admin/users/{username} with `disabled`) and destroying its
// credentials (POST .../revoke-credentials).
//
// The point of the switch is that it stops *every* way in at once, so the
// tests are written the same way: one account is given all five credentials
// this server accepts -- a browser session, its password, HTTP Basic, an
// access token and a registered SSH key -- and each one is checked before the
// suspension, after it, and after the account is restored. A switch that
// covers four of the five is the failure mode worth a test at all.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/gitserver"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// credentialSet is every credential one account holds, so a test can ask the
// same question of all of them rather than of whichever ones it remembered.
type credentialSet struct {
	session     []*http.Cookie
	password    string
	token       string
	fingerprint string
}

// grantEveryCredential gives an existing account one of each and returns them.
func (f *secFixture) grantEveryCredential(username, password string) credentialSet {
	f.t.Helper()
	user := f.mustUser(username)
	if _, err := f.st.CreateSSHKey(context.Background(), user.ID, "laptop",
		"ssh-ed25519 AAAA"+username, "SHA256:"+username); err != nil {
		f.t.Fatalf("create ssh key for %s: %v", username, err)
	}
	return credentialSet{
		session:     f.session(username, password),
		password:    password,
		token:       f.token(user, "write"),
		fingerprint: "SHA256:" + username,
	}
}

// meStatus is the status GET /api/v1/me answers for one credential. It is the
// cheapest authenticated endpoint in the server, so it isolates the identity
// path from anything a handler might decide on its own.
func (f *secFixture) meStatus(req secRequest) int {
	f.t.Helper()
	req.method, req.path = "GET", "/api/v1/me"
	return f.do(req).Code
}

// sshResolves reports whether the SSH transport still finds an identity for
// the fingerprint. internal/sshserver authenticates before any of this package
// runs, so store.LookupSSHKey is the gate that path passes through.
func (f *secFixture) sshResolves(fingerprint string) bool {
	f.t.Helper()
	_, _, err := f.st.LookupSSHKey(context.Background(), fingerprint)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		f.t.Fatalf("LookupSSHKey %s: %v", fingerprint, err)
	}
	return err == nil
}

// serveGitError runs the git-over-SSH entry point for an already-authenticated
// user and returns its refusal. The repository name is deliberately one that
// does not exist: an active account gets as far as the lookup and is told so,
// which is how "the suspension gate let this through" is distinguished from
// "the suspension gate refused it".
func (f *secFixture) serveGitError(user *store.User, ns, name string) string {
	f.t.Helper()
	err := f.s.ServeGit(context.Background(), user, gitserver.UploadPack,
		"model", ns, name, "", gitserver.Streams{})
	if err == nil {
		f.t.Fatalf("ServeGit for %s/%s succeeded, want a refusal", ns, name)
	}
	return err.Error()
}

// setDisabled drives the administration endpoint the way the web UI does and
// insists on the status, so a test that meant to suspend somebody cannot
// quietly go on asserting against an account that was never touched.
func (f *secFixture) setDisabled(admin []*http.Cookie, username string, disabled bool) {
	f.t.Helper()
	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/" + username,
		cookies: admin, body: map[string]any{"disabled": disabled}})
	if rec.Code != http.StatusOK {
		f.t.Fatalf("set disabled=%v on %s: status %d, body %s",
			disabled, username, rec.Code, rec.Body.String())
	}
}

// ------------------------------------------------------- the switch itself

func TestDisabledAccount_EveryIdentityPathRefuses(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")
	alice := f.grantEveryCredential("alice", "forgotten forever")

	// Before: every one of the five works. Without this half, a test that
	// only checked the refusals would pass just as well against a fixture
	// whose credentials never worked in the first place.
	if got := f.meStatus(secRequest{cookies: alice.session}); got != http.StatusOK {
		t.Fatalf("session before suspension = %d, want 200", got)
	}
	if !f.canLogIn("alice", alice.password) {
		t.Fatal("password login does not work before the suspension")
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": basicAuth("alice", alice.password)}}); got != http.StatusOK {
		t.Fatalf("HTTP Basic before suspension = %d, want 200", got)
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + alice.token}}); got != http.StatusOK {
		t.Fatalf("token before suspension = %d, want 200", got)
	}
	if !f.sshResolves(alice.fingerprint) {
		t.Fatal("the SSH key does not resolve before the suspension")
	}
	if got := f.serveGitError(f.mustUser("alice"), "alice", "no-such-repo"); !strings.Contains(got, "not found") {
		t.Fatalf("ServeGit before suspension = %q, want the repository lookup to be reached", got)
	}

	f.setDisabled(root, "alice", true)

	// After: all five refused, and the session without waiting for its TTL --
	// SetUserDisabled bumps session_epoch in the same statement.
	if got := f.meStatus(secRequest{cookies: alice.session}); got != http.StatusUnauthorized {
		t.Fatalf("session after suspension = %d, want 401", got)
	}
	rec := f.do(secRequest{method: "POST", path: "/api/v1/auth/login",
		body: map[string]any{"username": "alice", "password": alice.password}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login after suspension = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	// Its own error type rather than a plain 401: only reachable with the
	// *correct* password, so it enumerates nothing, and it is the answer the
	// person on the other end needs instead of an endless reset loop.
	if got := recErrorType(t, rec); got != "account_disabled" {
		t.Fatalf("login error type = %q, want account_disabled", got)
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": basicAuth("alice", alice.password)}}); got != http.StatusUnauthorized {
		t.Fatalf("HTTP Basic after suspension = %d, want 401", got)
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + alice.token}}); got != http.StatusUnauthorized {
		t.Fatalf("token after suspension = %d, want 401", got)
	}
	if f.sshResolves(alice.fingerprint) {
		t.Fatal("the SSH key still resolves for a suspended account")
	}
	// And the second gate on the same path: ServeGit takes its user from
	// another package, so it refuses a suspended account itself rather than
	// trusting that the key lookup already did.
	// The row is re-read, the way internal/sshserver hands over whatever the
	// key lookup returned rather than a copy taken earlier.
	if got := f.serveGitError(f.mustUser("alice"), "alice", "no-such-repo"); !strings.Contains(got, "disabled") {
		t.Fatalf("ServeGit after suspension = %q, want a refusal naming the suspension", got)
	}
}

// Suspension destroys nothing: an account restored from it is the account
// that was suspended, credentials included. Only the sessions are gone, and
// deliberately so -- the epoch bump that cut them off is not undone.
func TestDisabledAccount_RestoreBringsBackWhatWasNotDestroyed(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")
	alice := f.grantEveryCredential("alice", "forgotten forever")

	f.setDisabled(root, "alice", true)
	f.setDisabled(root, "alice", false)

	if !f.canLogIn("alice", alice.password) {
		t.Fatal("the restored account cannot log in")
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + alice.token}}); got != http.StatusOK {
		t.Fatalf("token after restore = %d, want 200: suspension is not revocation", got)
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": basicAuth("alice", alice.password)}}); got != http.StatusOK {
		t.Fatalf("HTTP Basic after restore = %d, want 200", got)
	}
	if !f.sshResolves(alice.fingerprint) {
		t.Fatal("the SSH key did not come back with the account")
	}
	// The one thing that stays dead. Cutting a session off has to be
	// permanent, or "you are signed out now" would only mean "until somebody
	// restores the account".
	if got := f.meStatus(secRequest{cookies: alice.session}); got != http.StatusUnauthorized {
		t.Fatalf("the pre-suspension session works again (status %d)", got)
	}
	if f.mustUser("alice").Disabled() {
		t.Fatal("the account is still marked suspended after being restored")
	}
}

// Setting the state an account is already in must not bump the epoch a second
// time, so a retried request (or a double-clicked toggle) does not sign the
// account out of a session it acquired in between.
func TestDisabledAccount_RepeatedRestoreIsANoOp(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")

	f.setDisabled(root, "alice", true)
	f.setDisabled(root, "alice", false)
	fresh := f.session("alice", "forgotten forever")
	f.setDisabled(root, "alice", false)

	if got := f.meStatus(secRequest{cookies: fresh}); got != http.StatusOK {
		t.Fatalf("session after a repeated restore = %d, want 200", got)
	}
}

// The mirror of TestAdminUpdateUser_DemotingAnotherAdminAlwaysLeavesOne. The
// handler refuses self-suspension before the store is reached, and any *other*
// administrator the actor could suspend implies a second active one -- so the
// store's 409 is unreachable over HTTP, and the rule is asserted where it
// actually lives. If the self-suspension rule is ever relaxed, the store guard
// here is what still stands between the instance and having no administrator.
func TestDisabledAccount_TheLastAdministratorIsProtected(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.adminUser("second", "correct horse battery")
	f.user("alice", "correct horse battery")
	root := f.session("root", "correct horse battery")

	// Two administrators, so suspending one is allowed.
	f.setDisabled(root, "second", true)

	// root is now the only active administrator, and cannot suspend itself.
	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/users/root",
		cookies: root, body: map[string]any{"disabled": true}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self-suspension status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "self_disable" {
		t.Fatalf("error type = %q, want self_disable", got)
	}

	// The store's own guard, reached directly because the handler cannot get
	// there: a suspended administrator does not count towards the total, so
	// root really is the last one.
	err := f.st.SetUserDisabled(context.Background(), "root", true, f.mustUser("alice").ID)
	if !errors.Is(err, store.ErrLastSiteAdmin) {
		t.Fatalf("SetUserDisabled on the last administrator = %v, want ErrLastSiteAdmin", err)
	}
	if f.mustUser("root").Disabled() {
		t.Fatal("the instance lost its last administrator")
	}
}

// ------------------------------------------------------ revoke-credentials

func TestAdminRevokeCredentials_DestroysTokensAndKeys(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")
	alice := f.grantEveryCredential("alice", "forgotten forever")
	// A second token, so the endpoint is shown clearing the account rather
	// than deleting one row.
	second := f.token(f.mustUser("alice"), "read")

	rec := f.do(secRequest{method: "POST",
		path: "/api/v1/admin/users/alice/revoke-credentials", cookies: root})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("204 with a body: %q", body)
	}

	for name, tok := range map[string]string{"the write token": alice.token, "the read token": second} {
		if got := f.meStatus(secRequest{headers: map[string]string{
			"Authorization": "Bearer " + tok}}); got != http.StatusUnauthorized {
			t.Fatalf("%s still authenticates (status %d)", name, got)
		}
	}
	if f.sshResolves(alice.fingerprint) {
		t.Fatal("the SSH key survived the revocation")
	}
	keys, err := f.st.ListSSHKeys(context.Background(), f.mustUser("alice").ID)
	if err != nil || len(keys) != 0 {
		t.Fatalf("ListSSHKeys after the revocation = %v, %v; want none", keys, err)
	}
	if got := f.meStatus(secRequest{cookies: alice.session}); got != http.StatusUnauthorized {
		t.Fatalf("the session survived the revocation (status %d)", got)
	}

	// What it deliberately does *not* do. Revoking credentials is for the
	// case where the credentials themselves are suspected; the account is
	// meant to keep working once new ones are issued, so it is neither
	// suspended nor stripped of its password.
	if f.mustUser("alice").Disabled() {
		t.Fatal("revoking credentials also suspended the account")
	}
	if !f.canLogIn("alice", alice.password) {
		t.Fatal("revoking credentials also changed the password")
	}
	// And the account can be equipped again from the session it signs back in
	// with, which is the whole reason the two actions are separate.
	if got := f.meStatus(secRequest{
		cookies: f.session("alice", alice.password)}); got != http.StatusOK {
		t.Fatalf("a fresh session after the revocation = %d, want 200", got)
	}
}

// Suspension and revocation are independent in both directions: restoring a
// suspended account brings back nothing that was separately destroyed.
func TestAdminRevokeCredentials_SurvivesARestore(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "forgotten forever")
	root := f.session("root", "correct horse battery")
	alice := f.grantEveryCredential("alice", "forgotten forever")

	f.setDisabled(root, "alice", true)
	if rec := f.do(secRequest{method: "POST",
		path: "/api/v1/admin/users/alice/revoke-credentials", cookies: root}); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d, body %s", rec.Code, rec.Body.String())
	}
	f.setDisabled(root, "alice", false)

	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + alice.token}}); got != http.StatusUnauthorized {
		t.Fatalf("a revoked token came back with the account (status %d)", got)
	}
	if f.sshResolves(alice.fingerprint) {
		t.Fatal("a deleted SSH key came back with the account")
	}
	// The account itself is usable again, so this is revocation outliving a
	// restore rather than the restore having failed.
	if !f.canLogIn("alice", alice.password) {
		t.Fatal("the restored account cannot log in")
	}
}

func TestAdminRevokeCredentials_Rejections(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct horse battery")
	f.user("alice", "correct horse battery")
	root := f.session("root", "correct horse battery")
	rootToken := f.token(f.mustUser("root"), "write")
	alice := f.grantEveryCredential("alice", "correct horse battery")

	// Your own account: refused, because the session revocation would include
	// the cookie the request arrived on. Your own tokens and keys are managed
	// from /settings, where the browser keeps itself signed in.
	rec := f.do(secRequest{method: "POST",
		path: "/api/v1/admin/users/root/revoke-credentials", cookies: root})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self-revocation status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "self_revoke" {
		t.Fatalf("error type = %q, want self_revoke", got)
	}
	// Refused means nothing happened, including to the actor's own session.
	if got := f.meStatus(secRequest{cookies: root}); got != http.StatusOK {
		t.Fatalf("the refused self-revocation signed the administrator out (status %d)", got)
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + rootToken}}); got != http.StatusOK {
		t.Fatalf("the refused self-revocation revoked the actor's token (status %d)", got)
	}

	// An account that does not exist is a 404, and nobody else's credentials
	// are touched on the way to it.
	rec = f.do(secRequest{method: "POST",
		path: "/api/v1/admin/users/nobody/revoke-credentials", cookies: root})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown username status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if got := recErrorType(t, rec); got != "not_found" {
		t.Fatalf("error type = %q, want not_found", got)
	}
	if got := f.meStatus(secRequest{headers: map[string]string{
		"Authorization": "Bearer " + alice.token}}); got != http.StatusOK {
		t.Fatalf("a 404 revocation took alice's token with it (status %d)", got)
	}
	if !f.sshResolves(alice.fingerprint) {
		t.Fatal("a 404 revocation deleted alice's SSH key")
	}
}

// A last check that the fixture's own account really does hold what the tests
// above assume: auth.HashToken is what LookupToken matches on, so a token the
// fixture minted must resolve to its owner before any of this means anything.
func TestGrantEveryCredential_IsARealAccount(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "forgotten forever")
	alice := f.grantEveryCredential("alice", "forgotten forever")

	user, _, err := f.st.LookupToken(context.Background(), auth.HashToken(alice.token))
	if err != nil || user.Username != "alice" {
		t.Fatalf("LookupToken = %v, %v; want alice", user, err)
	}
	if !f.sshResolves(alice.fingerprint) {
		t.Fatal("the fingerprint the fixture registered does not resolve")
	}
}
