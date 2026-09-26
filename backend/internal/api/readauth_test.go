package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// readAuthFixture is a server with one user, one repository holding a file,
// and TF_REQUIRE_AUTH_FOR_READ set as asked. The switch is read when
// Handler() assembles the chain, which secFixture.do does per request.
func readAuthFixture(t *testing.T, on bool) *secFixture {
	t.Helper()
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "poc", "model")
	f.writeFile(repo, "config.json", []byte(`{"a":1}`))
	f.cfg.RequireAuthForRead = on
	return f
}

// The reads a signed-out caller makes most: the Web UI's lists and pages,
// huggingface_hub's listings and downloads, git's ref advertisement, and the
// experiments tracker's views.
var readAuthAnonymousReads = []struct{ name, method, path string }{
	{"ui repo list", "GET", "/api/v1/repos"},
	{"ui repo detail", "GET", "/api/v1/repos/model/alice/poc"},
	{"ui tree", "GET", "/api/v1/repos/model/alice/poc/tree/main"},
	{"hf list", "GET", "/api/models"},
	{"hf repo info", "GET", "/api/models/alice/poc"},
	{"resolve", "GET", "/alice/poc/resolve/main/config.json"},
	{"git info/refs", "GET", "/alice/poc.git/info/refs?service=git-upload-pack"},
	{"experiments", "GET", "/api/v1/experiments"},
	{"parquet schema", "GET", "/api/v1/parquet/model/alice/poc/schema/main/x.parquet"},
	{"namespace", "GET", "/api/v1/namespaces/alice"},
	{"openapi", "GET", "/api/openapi.json"},
}

func TestReadAuth_OffLeavesAnonymousReadsAlone(t *testing.T) {
	f := readAuthFixture(t, false)
	for _, tc := range []struct{ name, path string }{
		{"ui repo list", "/api/v1/repos"},
		{"hf list", "/api/models"},
		{"resolve", "/alice/poc/resolve/main/config.json"},
		{"experiments", "/api/v1/experiments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(secRequest{method: "GET", path: tc.path})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s; want 200", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestReadAuth_OnRefusesAnonymousReads(t *testing.T) {
	f := readAuthFixture(t, true)
	for _, tc := range readAuthAnonymousReads {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(secRequest{method: tc.method, path: tc.path})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s; want 401", rec.Code, rec.Body.String())
			}
			if got := recErrorType(t, rec); got != "authentication_required" {
				t.Errorf("error type = %q, want authentication_required", got)
			}
			// What makes git and git-lfs prompt (or ask a credential helper)
			// and retry instead of failing outright.
			if got := rec.Header().Get("WWW-Authenticate"); got != `Basic realm="thinkingface"` {
				t.Errorf("WWW-Authenticate = %q", got)
			}
			if rec.Header().Get("X-Error-Message") == "" {
				t.Error("no X-Error-Message: huggingface_hub would show the caller nothing")
			}
		})
	}
}

// Writes were already refused to an anonymous caller; with the switch on they
// get the same answer as reads rather than each handler's own.
func TestReadAuth_OnRefusesAnonymousWritesUniformly(t *testing.T) {
	f := readAuthFixture(t, true)
	rec := f.do(secRequest{method: "POST", path: "/api/v1/repos",
		body: map[string]any{"kind": "model", "name": "x"}})
	if rec.Code != http.StatusUnauthorized || recErrorType(t, rec) != "authentication_required" {
		t.Fatalf("status = %d, body = %s; want 401 authentication_required", rec.Code, rec.Body.String())
	}
}

func TestReadAuth_OnKeepsTheAllowlistReachable(t *testing.T) {
	f := readAuthFixture(t, true)

	if rec := f.do(secRequest{method: "GET", path: "/healthz"}); rec.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200", rec.Code)
	}

	// server-info answers, and says the switch is on.
	rec := f.do(secRequest{method: "GET", path: "/api/v1/server-info"})
	if rec.Code != http.StatusOK {
		t.Fatalf("server-info = %d, body = %s; want 200", rec.Code, rec.Body.String())
	}
	var info apitypes.ServerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode server-info: %v", err)
	}
	if !info.RequireAuthForRead || !info.AllowSignup {
		t.Errorf("server-info = %+v, want both true", info)
	}

	// /me keeps its own answer for a signed-out caller, the one the Web UI
	// reads as "not logged in".
	rec = f.do(secRequest{method: "GET", path: "/api/v1/me"})
	if rec.Code != http.StatusUnauthorized || recErrorType(t, rec) != "unauthorized" {
		t.Errorf("me = %d %s, want the handler's own 401", rec.Code, rec.Body.String())
	}

	// Login works (and is how the cookie below is obtained).
	f.login("alice", "correct horse battery")

	// Sign-up is reachable: with approval required it answers its own 403,
	// not the gate's 401.
	f.cfg.SignupRequireApproval = true
	rec = f.signup("bob", "bob@example.com", "correct horse battery")
	if rec.Code != http.StatusForbidden || recErrorType(t, rec) != "approval_pending" {
		t.Errorf("signup = %d %s, want 403 approval_pending", rec.Code, rec.Body.String())
	}

	rec = f.do(secRequest{method: "POST", path: "/api/v1/auth/logout"})
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("logout = 401, want it reachable signed out")
	}

	// Only the listed method: a GET on the login path is not a way around it.
	rec = f.do(secRequest{method: "GET", path: "/api/v1/auth/login"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/auth/login = %d, want 401", rec.Code)
	}
}

func TestServerInfo_ReflectsConfiguration(t *testing.T) {
	f := readAuthFixture(t, false)
	f.cfg.AllowSignup = false
	rec := f.do(secRequest{method: "GET", path: "/api/v1/server-info"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var info apitypes.ServerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.RequireAuthForRead || info.AllowSignup {
		t.Errorf("server-info = %+v, want both false", info)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestReadAuth_OnAdmitsEveryCredentialKind(t *testing.T) {
	f := readAuthFixture(t, true)
	alice := f.mustUser("alice")
	token := f.token(alice, "read")
	cookie := f.login("alice", "correct horse battery")

	for _, tc := range []struct {
		name string
		req  secRequest
	}{
		{"bearer token", secRequest{headers: map[string]string{"Authorization": "Bearer " + token}}},
		{"basic with token", secRequest{headers: map[string]string{"Authorization": basicAuth("x", token)}}},
		{"basic with password", secRequest{headers: map[string]string{
			"Authorization": basicAuth("alice", "correct horse battery")}}},
		{"session cookie", secRequest{cookies: []*http.Cookie{cookie}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/v1/repos", "/alice/poc/resolve/main/config.json", "/api/v1/experiments"} {
				req := tc.req
				req.method, req.path = "GET", path
				if rec := f.do(req); rec.Code != http.StatusOK {
					t.Errorf("%s = %d, body = %s; want 200", path, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// An account identify refuses is nobody, so it is refused like nobody.
func TestReadAuth_OnRefusesAPendingAccount(t *testing.T) {
	f := readAuthFixture(t, true)
	f.cfg.SignupRequireApproval = true
	if rec := f.signup("bob", "bob@example.com", "correct horse battery"); rec.Code != http.StatusForbidden {
		t.Fatalf("signup = %d, want 403", rec.Code)
	}
	rec := f.do(secRequest{method: "GET", path: "/api/v1/repos",
		headers: map[string]string{"Authorization": basicAuth("bob", "correct horse battery")}})
	if rec.Code != http.StatusUnauthorized || recErrorType(t, rec) != "authentication_required" {
		t.Fatalf("status = %d, body = %s; want 401 authentication_required", rec.Code, rec.Body.String())
	}
}

// The emulator transfer proxy's URLs carry their own signature and are used
// with no Authorization header (git-lfs and huggingface_hub both assume a
// pre-signed href), so they must keep working with the switch on -- they were
// issued by a batch call the caller had to authenticate for. An unsigned or
// tampered request to the same route is refused by the gate.
func TestReadAuth_OnKeepsSignedLFSTransfersWorking(t *testing.T) {
	f := readAuthFixture(t, true)
	alice := f.mustUser("alice")
	repo := f.repo("alice", "weights", "model")
	body := []byte("hello lfs")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)),
		func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	rec := f.do(secRequest{
		method: "POST", path: "/alice/weights/info/lfs/objects/batch",
		body: map[string]any{
			"operation": "download",
			"objects":   []map[string]any{{"oid": oid, "size": len(body)}},
		},
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "read")},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("batch = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Objects []struct {
			Actions map[string]struct {
				Href string `json:"href"`
			} `json:"actions"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if len(resp.Objects) != 1 || resp.Objects[0].Actions["download"].Href == "" {
		t.Fatalf("batch body = %s, want a download href", rec.Body.String())
	}
	href, err := url.Parse(resp.Objects[0].Actions["download"].Href)
	if err != nil {
		t.Fatalf("parse href: %v", err)
	}

	// Signed, no credentials: admitted.
	rec = f.do(secRequest{method: "GET", path: href.RequestURI()})
	if rec.Code != http.StatusOK || rec.Body.String() != string(body) {
		t.Fatalf("signed download = %d %q, want 200 with the object", rec.Code, rec.Body.String())
	}

	// Unsigned: refused by the gate, before the handler's anonymous fallback.
	rec = f.do(secRequest{method: "GET", path: href.Path})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unsigned download = %d, want 401", rec.Code)
	}

	// A download signature does not open the upload route.
	q := href.Query()
	q.Set("op", "upload")
	rec = f.do(secRequest{method: "PUT", path: href.Path + "?" + q.Encode(), rawBody: body})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("upload with a download signature = %d, want 401", rec.Code)
	}

	// Tampered signature: refused.
	q = href.Query()
	q.Set("sig", "AAAA"+q.Get("sig"))
	rec = f.do(secRequest{method: "GET", path: href.Path + "?" + q.Encode()})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered download = %d, want 401", rec.Code)
	}
}
