// Regression tests for the security audit findings (todo/security-audit-findings.md).
// Each one fails against the pre-fix code: the resolve headers, the login and
// HTTP Basic rate limits, LFS object ownership, the CORS allowlist, session
// revocation, the signup input bounds, and the responses that used to leak
// whether something existed.

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// secFixture is a real Server over a real SQLite store, an on-disk git
// manager and the in-memory object store, driven over actual HTTP. The
// security properties under test live in middleware and handler wiring, so
// nothing short of the assembled stack proves them.
type secFixture struct {
	t   *testing.T
	s   *Server
	st  *store.Store
	git *gitrepo.Manager
	obj *memStore
	cfg *config.Config
}

func newSecFixture(t *testing.T) *secFixture {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
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
	return &secFixture{t: t, s: srv, st: st, git: gitMgr, obj: obj, cfg: cfg}
}

// user creates an account with a real bcrypt hash, so the login path exercises
// the same work production does.
func (f *secFixture) user(name, password string) *store.User {
	f.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		f.t.Fatalf("hash password: %v", err)
	}
	u, err := f.st.CreateUser(context.Background(), name, name+"@example.com", hash, false)
	if err != nil {
		f.t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func (f *secFixture) token(u *store.User, scope string) string {
	f.t.Helper()
	tok, hash, err := auth.NewToken()
	if err != nil {
		f.t.Fatalf("new token: %v", err)
	}
	if _, err := f.st.CreateToken(context.Background(), u.ID, "test", scope, hash, nil); err != nil {
		f.t.Fatalf("create token: %v", err)
	}
	return tok
}

func (f *secFixture) repo(ns, name, kind string) *store.Repo {
	f.t.Helper()
	ctx := context.Background()
	n, err := f.st.GetNamespace(ctx, ns)
	if err != nil {
		f.t.Fatalf("namespace %s: %v", ns, err)
	}
	sp := store.NewStoragePath()
	r, err := f.st.CreateRepo(ctx, n.ID, name, kind, "desc", "main", sp)
	if err != nil {
		f.t.Fatalf("create repo %s/%s: %v", ns, name, err)
	}
	if err := f.git.Init(sp, "main"); err != nil {
		f.t.Fatalf("git init: %v", err)
	}
	return r
}

// writeFile commits one blob onto the repository's default branch.
func (f *secFixture) writeFile(repo *store.Repo, path string, data []byte) {
	f.t.Helper()
	g, err := f.git.Open(repo.StoragePath)
	if err != nil {
		f.t.Fatalf("open git: %v", err)
	}
	_, _, err = g.Commit(gitrepo.CommitRequest{
		Branch:  "main",
		Message: "add " + path,
		Author:  gitrepo.Signature{Name: "t", Email: "t@example.com", When: time.Now()},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: data}},
	})
	if err != nil {
		f.t.Fatalf("commit %s: %v", path, err)
	}
}

// putLFSObject writes object bytes into the bucket without linking them to any
// repository -- the state an attacker exploits when they know an oid.
func (f *secFixture) putLFSObject(data []byte) string {
	f.t.Helper()
	sum := sha256.Sum256(data)
	oid := hex.EncodeToString(sum[:])
	if err := f.obj.Put(context.Background(), storage.LFSKey(oid), bytes.NewReader(data), "application/octet-stream"); err != nil {
		f.t.Fatalf("put lfs object: %v", err)
	}
	return oid
}

type secRequest struct {
	method, path string
	body         any
	rawBody      []byte
	headers      map[string]string
	cookies      []*http.Cookie
	remoteAddr   string
}

func (f *secFixture) do(req secRequest) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader *bytes.Reader
	switch {
	case req.rawBody != nil:
		reader = bytes.NewReader(req.rawBody)
	case req.body != nil:
		b, err := json.Marshal(req.body)
		if err != nil {
			f.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	default:
		reader = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(req.method, req.path, reader)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range req.headers {
		r.Header.Set(k, v)
	}
	for _, c := range req.cookies {
		r.AddCookie(c)
	}
	if req.remoteAddr != "" {
		r.RemoteAddr = req.remoteAddr
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, r)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

// ------------------------------------------------------------------- [S1]

// A pushed .html must not come back as something the browser will render on
// the API origin: that origin holds the session cookie.
func TestResolve_ExecutableTypesAreNeutralised(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "poc", "model")

	tests := []struct {
		path            string
		wantContentType string
	}{
		{"poc.html", "application/octet-stream"},
		{"poc.xhtml", "application/octet-stream"},
		{"poc.xml", "application/octet-stream"},
		// Kept renderable on purpose: <img src> of an SVG runs no script and
		// the README image path depends on it. Content-Disposition covers the
		// top-level navigation case.
		{"poc.svg", "image/svg+xml"},
		// Ordinary downloads keep their type.
		{"config.json", "application/json"},
		{"plot.png", "image/png"},
	}
	for _, tt := range tests {
		f.writeFile(repo, tt.path, []byte("x"))
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := f.do(secRequest{method: "GET", path: "/alice/poc/resolve/main/" + tt.path})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			gotType, _, _ := strings.Cut(rec.Header().Get("Content-Type"), ";")
			if strings.TrimSpace(gotType) != tt.wantContentType {
				t.Errorf("Content-Type = %q, want %q", rec.Header().Get("Content-Type"), tt.wantContentType)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
			disp := rec.Header().Get("Content-Disposition")
			if !strings.HasPrefix(disp, "attachment;") {
				t.Errorf("Content-Disposition = %q, want an attachment", disp)
			}
			if !strings.Contains(disp, tt.path) {
				t.Errorf("Content-Disposition = %q, want the file name in it", disp)
			}
		})
	}
}

func TestAttachmentDisposition_NonASCIIAndQuotes(t *testing.T) {
	got := attachmentDisposition(`実験"ログ.txt`)
	if !strings.HasPrefix(got, "attachment; filename=\"") {
		t.Fatalf("disposition = %q, want an attachment with a quoted fallback", got)
	}
	// The quote must not escape the quoted-string, and the UTF-8 name must
	// survive in filename*.
	if strings.Count(got, `"`) != 2 {
		t.Errorf("disposition = %q, want exactly one quoted-string", got)
	}
	if !strings.Contains(got, "filename*=UTF-8''") {
		t.Errorf("disposition = %q, want an RFC 5987 filename*", got)
	}
}

func TestSecurityHeaders_OnEveryResponse(t *testing.T) {
	f := newSecFixture(t)
	rec := f.do(secRequest{method: "GET", path: "/healthz"})
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// ------------------------------------------------------------------- [S2]

func TestLogin_RateLimitedAfterRepeatedFailures(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")

	bad := func() *httptest.ResponseRecorder {
		return f.do(secRequest{
			method: "POST", path: "/api/v1/auth/login",
			body:       map[string]string{"username": "alice", "password": "wrong"},
			remoteAddr: "10.0.0.1:1234",
		})
	}
	// The username bucket (half the per-IP rate) is the tighter of the two.
	var limited bool
	for i := 0; i < 12; i++ {
		rec := bad()
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			if rec.Header().Get("Retry-After") == "" {
				t.Errorf("attempt %d: 429 without a Retry-After header", i+1)
			}
			var body struct {
				Error struct{ Type string } `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode 429 body %q: %v", rec.Body.String(), err)
			}
			if body.Error.Type != "rate_limited" {
				t.Errorf("error type = %q, want rate_limited", body.Error.Type)
			}
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, body = %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if !limited {
		t.Fatalf("12 consecutive wrong passwords never produced a 429")
	}
}

// Successful logins must not consume the budget: the e2e suite and CI drive
// many of them from one address.
func TestLogin_SuccessIsNeverRateLimited(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	for i := 0; i < 30; i++ {
		rec := f.do(secRequest{
			method: "POST", path: "/api/v1/auth/login",
			body:       map[string]string{"username": "alice", "password": "correct horse battery"},
			remoteAddr: "10.0.0.1:1234",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("login %d: status = %d, body = %s", i+1, rec.Code, rec.Body.String())
		}
	}
}

// A correct password must clear the failure count, so a person who mistypes
// and then succeeds is not locked out afterwards.
func TestLogin_SuccessResetsFailureCount(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	for i := 0; i < 4; i++ {
		f.do(secRequest{
			method: "POST", path: "/api/v1/auth/login",
			body:       map[string]string{"username": "alice", "password": "wrong"},
			remoteAddr: "10.0.0.1:1234",
		})
	}
	ok := f.do(secRequest{
		method: "POST", path: "/api/v1/auth/login",
		body:       map[string]string{"username": "alice", "password": "correct horse battery"},
		remoteAddr: "10.0.0.1:1234",
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("login after failures: status = %d, body = %s", ok.Code, ok.Body.String())
	}
	// The next wrong password must still be answered normally, not throttled.
	again := f.do(secRequest{
		method: "POST", path: "/api/v1/auth/login",
		body:       map[string]string{"username": "alice", "password": "wrong"},
		remoteAddr: "10.0.0.1:1234",
	})
	if again.Code != http.StatusUnauthorized {
		t.Fatalf("status after reset = %d, want 401", again.Code)
	}
}

// HTTP Basic is accepted on every route, which makes it the cheapest way to
// force unauthenticated bcrypt work. Once throttled it must stop hashing.
func TestBasicAuth_StopsHashingOnceThrottled(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")

	basic := func() *httptest.ResponseRecorder {
		req := secRequest{method: "GET", path: "/api/whoami-v2", remoteAddr: "10.0.0.2:5555"}
		r := httptest.NewRequest(req.method, req.path, bytes.NewReader(nil))
		r.RemoteAddr = req.remoteAddr
		r.SetBasicAuth("alice", "wrong")
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, r)
		return rec
	}
	for i := 0; i < 8; i++ {
		if rec := basic(); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
	// Throttled now: the username bucket is empty, so checkPassword must
	// return before touching bcrypt. Measure it -- a bcrypt round is tens of
	// milliseconds, a short-circuit is microseconds.
	start := time.Now()
	for i := 0; i < 5; i++ {
		basic()
	}
	elapsed := time.Since(start)
	if elapsed > 20*time.Millisecond {
		t.Errorf("5 throttled Basic attempts took %v; bcrypt is still running for them", elapsed)
	}
	// The correct password still works: throttling must not lock the account
	// out of the credential it actually holds... except that it does, by
	// design, for the duration of the window. Assert the shape we chose: the
	// request is simply anonymous, never a 500 or a hang.
	r := httptest.NewRequest("GET", "/api/whoami-v2", bytes.NewReader(nil))
	r.RemoteAddr = "10.0.0.2:5555"
	r.SetBasicAuth("alice", "correct horse battery")
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("throttled correct password: status = %d, want 401 (anonymous)", rec.Code)
	}
}

// A personal access token must never enter the password path: it is a single
// SHA-256, and throttling it would break git and huggingface_hub under load.
func TestBearerAndTokenBasic_AreNotRateLimited(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	tok := f.token(alice, "write")

	for i := 0; i < 40; i++ {
		r := httptest.NewRequest("GET", "/api/whoami-v2", bytes.NewReader(nil))
		r.RemoteAddr = "10.0.0.3:9999"
		r.SetBasicAuth("git", tok)
		rec := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("token basic %d: status = %d, body = %s", i+1, rec.Code, rec.Body.String())
		}
	}
}

// ------------------------------------------------------------------- [S3]

// The whole point: knowing an oid must not be enough to pull the bytes
// through a repository of one's own.
func TestLFSBatchDownload_RefusesForeignObject(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	mallory := f.user("mallory", "correct horse battery")
	victim := f.repo("alice", "secrets", "model")
	f.repo("mallory", "x", "model")

	// alice's private object, recorded against her repository the way a real
	// upload would.
	oid := f.putLFSObject([]byte("weights for alice only"))
	if err := f.st.RecordLFSObject(context.Background(), victim.ID, oid, 22, func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	malloryTok := f.token(mallory, "write")
	rec := f.do(secRequest{
		method: "POST", path: "/mallory/x/info/lfs/objects/batch",
		body: map[string]any{
			"operation": "download",
			"objects":   []map[string]any{{"oid": oid, "size": 22}},
		},
		headers: map[string]string{"Authorization": "Bearer " + malloryTok},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("batch status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Objects []struct {
			Actions map[string]any `json:"actions"`
			Error   *struct {
				Code int `json:"code"`
			} `json:"error"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode batch body %q: %v", rec.Body.String(), err)
	}
	if len(resp.Objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(resp.Objects))
	}
	if resp.Objects[0].Actions != nil {
		t.Fatalf("actions = %v, want none: mallory's repo does not own this oid", resp.Objects[0].Actions)
	}
	if resp.Objects[0].Error == nil || resp.Objects[0].Error.Code != 404 {
		t.Fatalf("error = %+v, want a per-object 404", resp.Objects[0].Error)
	}
}

// The owner's own download must keep working, or the fix would be a
// regression in the only flow that matters.
func TestLFSBatchDownload_AllowsOwnObject(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")
	oid := f.putLFSObject([]byte("hello lfs"))
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, 9, func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	rec := f.do(secRequest{
		method: "POST", path: "/alice/weights/info/lfs/objects/batch",
		body: map[string]any{
			"operation": "download",
			"objects":   []map[string]any{{"oid": oid, "size": 9}},
		},
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("batch status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Objects []struct {
			Actions map[string]any `json:"actions"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode batch body: %v", err)
	}
	if _, ok := resp.Objects[0].Actions["download"]; !ok {
		t.Fatalf("actions = %v, want a download action", resp.Objects[0].Actions)
	}
}

// The reverse direction: committing a pointer to someone else's oid would
// make the object permanently fetchable through resolve.
func TestCommit_RefusesLFSPointerToForeignObject(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	mallory := f.user("mallory", "correct horse battery")
	victim := f.repo("alice", "secrets", "model")
	f.repo("mallory", "x", "model")

	oid := f.putLFSObject([]byte("weights for alice only"))
	if err := f.st.RecordLFSObject(context.Background(), victim.ID, oid, 22, func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	ndjson := strings.Join([]string{
		`{"key":"header","value":{"summary":"steal"}}`,
		fmt.Sprintf(`{"key":"lfsFile","value":{"path":"w.bin","algo":"sha256","oid":%q,"size":22}}`, oid),
	}, "\n")
	rec := f.do(secRequest{
		method: "POST", path: "/api/models/mallory/x/commit/main",
		rawBody: []byte(ndjson),
		headers: map[string]string{"Authorization": "Bearer " + f.token(mallory, "write")},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s; want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "has not been uploaded") {
		t.Errorf("body = %s, want the generic not-uploaded message", rec.Body.String())
	}
}

// The emulator proxy download is the other way out of the bucket.
func TestLFSProxyDownload_RefusesForeignObject(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	mallory := f.user("mallory", "correct horse battery")
	victim := f.repo("alice", "secrets", "model")
	attacker := f.repo("mallory", "x", "model")

	oid := f.putLFSObject([]byte("weights for alice only"))
	if err := f.st.RecordLFSObject(context.Background(), victim.ID, oid, 22, func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	rec := f.do(secRequest{
		method:  "GET",
		path:    "/api/v1/lfs/" + strconv.FormatInt(attacker.ID, 10) + "/" + oid,
		headers: map[string]string{"Authorization": "Bearer " + f.token(mallory, "write")},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "alice only") {
		t.Fatalf("the object body leaked through: %s", rec.Body.String())
	}
}

// ------------------------------------------------------------------- [S4]

func TestCORS_OnlyAllowlistedOriginsGetCredentialedHeaders(t *testing.T) {
	f := newSecFixture(t)
	tests := []struct {
		origin    string
		wantAllow bool
	}{
		{"http://web.test.local", true},
		{"https://evil.example", false},
		{"http://web.test.local.evil.example", false},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			rec := f.do(secRequest{
				method: "OPTIONS", path: "/api/v1/me",
				headers: map[string]string{"Origin": tt.origin},
			})
			got := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.wantAllow && got != tt.origin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tt.origin)
			}
			if !tt.wantAllow {
				if got != "" {
					t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
				}
				if cred := rec.Header().Get("Access-Control-Allow-Credentials"); cred != "" {
					t.Errorf("Access-Control-Allow-Credentials = %q, want none", cred)
				}
			}
			if v := rec.Header().Get("Vary"); !strings.Contains(v, "Origin") {
				t.Errorf("Vary = %q, want it to include Origin", v)
			}
		})
	}
}

func TestCSRF_CookieStateChangeFromForeignOriginIsRefused(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	login := f.do(secRequest{
		method: "POST", path: "/api/v1/auth/login",
		body: map[string]string{"username": "alice", "password": "correct horse battery"},
	})
	cookie := sessionCookie(login)
	if cookie == nil {
		t.Fatalf("login set no session cookie: %s", login.Body.String())
	}

	// The attack: a page on evil.example makes the browser mint a write token.
	evil := f.do(secRequest{
		method: "POST", path: "/api/v1/tokens",
		body:    map[string]string{"name": "x", "scope": "write"},
		headers: map[string]string{"Origin": "https://evil.example"},
		cookies: []*http.Cookie{cookie},
	})
	if evil.Code != http.StatusForbidden {
		t.Fatalf("cross-origin token mint: status = %d, body = %s; want 403", evil.Code, evil.Body.String())
	}

	// The web UI's own origin still works.
	good := f.do(secRequest{
		method: "POST", path: "/api/v1/tokens",
		body:    map[string]string{"name": "x", "scope": "write"},
		headers: map[string]string{"Origin": "http://web.test.local"},
		cookies: []*http.Cookie{cookie},
	})
	if good.Code != http.StatusOK {
		t.Fatalf("same-origin token mint: status = %d, body = %s", good.Code, good.Body.String())
	}

	// And so does a client that sends no Origin at all: curl, the e2e suite,
	// the Next.js server forwarding a cookie from a Server Component.
	noOrigin := f.do(secRequest{
		method: "POST", path: "/api/v1/tokens",
		body:    map[string]string{"name": "x", "scope": "write"},
		cookies: []*http.Cookie{cookie},
	})
	if noOrigin.Code != http.StatusOK {
		t.Fatalf("no-Origin token mint: status = %d, body = %s", noOrigin.Code, noOrigin.Body.String())
	}
}

// A Bearer-authenticated call is not riding an ambient credential, so the
// origin check must not touch it -- huggingface_hub sets no Origin, but a
// browser-based tool could.
func TestCSRF_BearerRequestsAreExempt(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	rec := f.do(secRequest{
		method: "POST", path: "/api/v1/tokens",
		body: map[string]string{"name": "x", "scope": "read"},
		headers: map[string]string{
			"Authorization": "Bearer " + f.token(alice, "write"),
			"Origin":        "https://evil.example",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ------------------------------------------------------------------- [S5]

func TestLogout_RevokesTheCookieServerSide(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	login := f.do(secRequest{
		method: "POST", path: "/api/v1/auth/login",
		body: map[string]string{"username": "alice", "password": "correct horse battery"},
	})
	cookie := sessionCookie(login)
	if cookie == nil {
		t.Fatalf("login set no session cookie")
	}
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/me", cookies: []*http.Cookie{cookie}}); rec.Code != http.StatusOK {
		t.Fatalf("me before logout: status = %d", rec.Code)
	}

	if rec := f.do(secRequest{method: "POST", path: "/api/v1/auth/logout", cookies: []*http.Cookie{cookie}}); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// The value the client kept is now worthless, which is the whole point:
	// clearing the cookie only affects the browser that obeyed the header.
	if rec := f.do(secRequest{method: "GET", path: "/api/v1/me", cookies: []*http.Cookie{cookie}}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: status = %d, want 401", rec.Code)
	}
}

// ------------------------------------------------------------------- [S6]

func TestSessionCookie_SecureFollowsConfig(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	yes := true
	f.cfg.CookieSecure = &yes

	login := f.do(secRequest{
		method: "POST", path: "/api/v1/auth/login",
		body: map[string]string{"username": "alice", "password": "correct horse battery"},
	})
	cookie := sessionCookie(login)
	if cookie == nil {
		t.Fatalf("login set no session cookie")
	}
	if !cookie.Secure {
		t.Errorf("Secure = false with TF_COOKIE_SECURE=true and an http public URL")
	}
}

// ------------------------------------------------------------------- [S7]

func TestSignup_PasswordAndEmailBounds(t *testing.T) {
	f := newSecFixture(t)
	tests := []struct {
		name     string
		username string
		email    string
		password string
		want     int
	}{
		{"73 bytes is rejected", "u1", "u1@example.com", strings.Repeat("a", 73), http.StatusBadRequest},
		{"exactly 72 bytes is accepted", "u2", "u2@example.com", strings.Repeat("a", 72), http.StatusOK},
		{"under 8 characters is rejected", "u3", "u3@example.com", "short", http.StatusBadRequest},
		{"empty email is rejected", "u4", "", "correct horse battery", http.StatusBadRequest},
		{"non-address email is rejected", "u5", "not-an-address", "correct horse battery", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(secRequest{
				method: "POST", path: "/api/v1/auth/signup",
				body: map[string]string{"username": tt.username, "email": tt.email, "password": tt.password},
			})
			if rec.Code != tt.want {
				t.Fatalf("status = %d, body = %s; want %d", rec.Code, rec.Body.String(), tt.want)
			}
			if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "hash password failed") {
				t.Errorf("bad input surfaced as an internal error: %s", rec.Body.String())
			}
		})
	}
}

// ------------------------------------------------------------------- [S8]

func TestEditFile_OversizedBodyIsPayloadTooLarge(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	f.repo("alice", "docs", "model")

	body := append([]byte(`{"content":"`), bytes.Repeat([]byte("a"), maxEditBytes+1)...)
	body = append(body, []byte(`"}`)...)
	rec := f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/edit/model/alice/docs/main/README.md",
		rawBody: body,
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s; want 413", rec.Code, rec.Body.String())
	}
	var parsed struct {
		Error struct{ Type string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if parsed.Error.Type != "payload_too_large" {
		t.Errorf("error type = %q, want payload_too_large", parsed.Error.Type)
	}
}

func TestEditFile_MalformedJSONDoesNotEchoTheDecoder(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	f.repo("alice", "docs", "model")

	rec := f.do(secRequest{
		method:  "PUT",
		path:    "/api/v1/edit/model/alice/docs/main/README.md",
		rawBody: []byte(`{"content":`),
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	for _, leak := range []string{"unexpected EOF", "invalid character", "json:"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("body %q leaks the decoder's own message (%q)", rec.Body.String(), leak)
		}
	}
}

// ------------------------------------------------------------------- [S9]

func TestPathsInfo_BoundsTheBatch(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "ds", "dataset")
	f.writeFile(repo, "a.txt", []byte("a"))

	paths := make([]string, maxPathsInfoPaths+1)
	for i := range paths {
		paths[i] = "a.txt"
	}
	rec := f.do(secRequest{
		method: "POST", path: "/api/datasets/alice/ds/paths-info/main",
		body: map[string]any{"paths": paths},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s; want 400", rec.Code, rec.Body.String())
	}

	// The ceiling itself is fine.
	ok := f.do(secRequest{
		method: "POST", path: "/api/datasets/alice/ds/paths-info/main",
		body: map[string]any{"paths": paths[:maxPathsInfoPaths]},
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("status at the limit = %d, body = %s; want 200", ok.Code, ok.Body.String())
	}

	// A form body is still tolerated as "no paths": huggingface_hub's
	// get_paths_info posts one.
	form := f.do(secRequest{
		method: "POST", path: "/api/datasets/alice/ds/paths-info/main",
		rawBody: []byte("paths=a.txt&expand=False"),
		headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
	})
	if form.Code != http.StatusOK {
		t.Fatalf("form body status = %d, body = %s; want 200", form.Code, form.Body.String())
	}
}

// ------------------------------------------------------------------ [S18]

// A repository id that exists and one that does not must be indistinguishable
// to a caller with no rights to either.
func TestLFSProxy_NoRepositoryExistenceOracle(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	private := f.repo("alice", "secrets", "model")
	oid := strings.Repeat("a", 64)

	missingID := private.ID + 10_000
	for _, tc := range []struct {
		name         string
		method, path string
	}{
		{"upload", "PUT", "/api/v1/lfs/%d/" + oid},
		{"download", "GET", "/api/v1/lfs/%d/" + oid},
		{"verify", "POST", "/api/v1/lfs/%d/verify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := f.do(secRequest{method: tc.method, path: fmt.Sprintf(tc.path, private.ID)})
			absent := f.do(secRequest{method: tc.method, path: fmt.Sprintf(tc.path, missingID)})
			if existing.Code != absent.Code {
				t.Errorf("existing repo -> %d, absent repo -> %d; the ids are distinguishable",
					existing.Code, absent.Code)
			}
			if existing.Body.String() != absent.Body.String() {
				t.Errorf("bodies differ:\n existing: %s\n absent:   %s", existing.Body.String(), absent.Body.String())
			}
			if existing.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", existing.Code)
			}
		})
	}
}

// ------------------------------------------------------------------ [S19]

func TestValidateYAML_RequiresAuthentication(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")

	anon := f.do(secRequest{
		method: "POST", path: "/api/validate-yaml",
		body: map[string]string{"content": "---\ntags: [a]\n---\n", "repoType": "model"},
	})
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, body = %s; want 401", anon.Code, anon.Body.String())
	}

	authed := f.do(secRequest{
		method: "POST", path: "/api/validate-yaml",
		body:    map[string]string{"content": "---\ntags: [a]\n---\n", "repoType": "model"},
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if authed.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, body = %s; want 200", authed.Code, authed.Body.String())
	}
}

// ------------------------------------------------------------------ [S17]

// Deciding a transfer looks the transfer up by numeric id before checking
// permission, so an answer that distinguishes "not yours" from "no such id"
// lets anyone enumerate every pending destination namespace.
func TestDecideTransfer_HidesTheDestinationFromOutsiders(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	f.user("bob", "correct horse battery")
	mallory := f.user("mallory", "correct horse battery")
	f.repo("alice", "foo", "model")

	start := f.do(secRequest{
		method: "POST", path: "/api/repos/move",
		body:    map[string]any{"fromRepo": "alice/foo", "toRepo": "bob/foo", "type": "model"},
		headers: map[string]string{"Authorization": "Bearer " + f.token(alice, "write")},
	})
	if start.Code != http.StatusAccepted {
		t.Fatalf("move status = %d, body = %s; want 202", start.Code, start.Body.String())
	}
	var pending struct {
		TransferID int64 `json:"transfer_id"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &pending); err != nil {
		t.Fatalf("decode move body %q: %v", start.Body.String(), err)
	}

	malloryTok := f.token(mallory, "write")
	for _, action := range []string{"accept", "reject"} {
		t.Run(action, func(t *testing.T) {
			real := f.do(secRequest{
				method:  "POST",
				path:    fmt.Sprintf("/api/v1/transfers/%d/%s", pending.TransferID, action),
				headers: map[string]string{"Authorization": "Bearer " + malloryTok},
			})
			if real.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s; want 404", real.Code, real.Body.String())
			}
			if strings.Contains(real.Body.String(), "bob") {
				t.Errorf("body %q names the destination namespace", real.Body.String())
			}
			absent := f.do(secRequest{
				method:  "POST",
				path:    fmt.Sprintf("/api/v1/transfers/%d/%s", pending.TransferID+9999, action),
				headers: map[string]string{"Authorization": "Bearer " + malloryTok},
			})
			if absent.Code != real.Code {
				t.Errorf("existing transfer -> %d, absent one -> %d; the ids are distinguishable", real.Code, absent.Code)
			}
		})
	}
}

// The batch API is not the only door to the bytes. A pointer file is just
// text, so a writer can commit one naming someone else's oid and then read
// the object through resolve or the raw preview -- both of which used to
// dereference the pointer on the oid alone. Reported by Cursor Bugbot on #13.
func TestResolveAndRaw_RefuseForeignLFSPointer(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	mallory := f.user("mallory", "correct horse battery")
	victim := f.repo("alice", "secrets", "model")
	attacker := f.repo("mallory", "x", "model")

	secret := []byte("weights for alice only")
	oid := f.putLFSObject(secret)
	if err := f.st.RecordLFSObject(context.Background(), victim.ID, oid, int64(len(secret)), func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}

	// mallory commits a pointer naming alice's object into her own public
	// repository. Nothing links the oid to it.
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(len(secret)) + "\n")
	f.writeFile(attacker, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(attacker, "stolen.bin", pointer)

	malloryTok := f.token(mallory, "write")
	auth := map[string]string{"Authorization": "Bearer " + malloryTok}

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"resolve GET", "GET", "/mallory/x/resolve/main/stolen.bin"},
		{"resolve HEAD", "HEAD", "/mallory/x/resolve/main/stolen.bin"},
		{"raw preview", "GET", "/api/v1/raw/model/mallory/x/main/stolen.bin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(secRequest{method: tc.method, path: tc.path, headers: auth})
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 for an oid this repository does not own; body = %s",
					rec.Code, rec.Body.String())
			}
			if bytes.Contains(rec.Body.Bytes(), secret) {
				t.Fatalf("response leaked the foreign object's bytes: %s", rec.Body.String())
			}
			// A HEAD leaks through headers rather than a body.
			if got := rec.Header().Get("X-Linked-Etag"); got != "" {
				t.Fatalf("X-Linked-Etag = %q, want empty: the pointer must not be dereferenced at all", got)
			}
		})
	}
}

// The owner's own pointer must still resolve, or the fix breaks the only
// flow that matters (hf_hub_download and git-lfs both go through here).
func TestResolveAndRaw_AllowOwnLFSPointer(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")

	body := []byte("hello lfs")
	oid := f.putLFSObject(body)
	if err := f.st.RecordLFSObject(context.Background(), repo.ID, oid, int64(len(body)), func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("record lfs object: %v", err)
	}
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + oid +
		"\nsize " + strconv.Itoa(len(body)) + "\n")
	f.writeFile(repo, ".gitattributes", []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"))
	f.writeFile(repo, "model.bin", pointer)

	auth := map[string]string{"Authorization": "Bearer " + f.token(alice, "read")}

	rec := f.do(secRequest{method: "GET", path: "/alice/weights/resolve/main/model.bin", headers: auth})
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("resolve body = %q, want %q", rec.Body.String(), body)
	}
	if rec2 := f.do(secRequest{method: "GET", path: "/api/v1/raw/model/alice/weights/main/model.bin", headers: auth}); rec2.Code != http.StatusOK {
		t.Fatalf("raw status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
}

// Running out of bcrypt slots says nothing about the password -- the hash is
// never compared. Answering it like a wrong password would tell a legitimate
// caller their credentials are bad and spend their failure budget on the
// server's own load. Reported by Cursor Bugbot on #13.
func TestLogin_OverloadIsNotACredentialFailure(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")

	// Occupy every bcrypt slot, so the next attempt can only time out.
	guard := f.s.authGuard
	for i := 0; i < cap(guard.sem); i++ {
		guard.sem <- struct{}{}
	}

	body := map[string]any{"username": "alice", "password": "correct horse battery"}
	rec := f.do(secRequest{method: "POST", path: "/api/v1/auth/login", body: body, remoteAddr: "10.9.9.9:1234"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing: an honest client has nothing to go on")
	}
	if strings.Contains(rec.Body.String(), "incorrect") {
		t.Errorf("body blames the credentials: %s", rec.Body.String())
	}

	// The address must not have been penalized for the server's saturation.
	if wait := guard.retryAfter("addr:10.9.9.9"); wait > 0 {
		t.Errorf("address bucket penalized by %v; overload is not a failed attempt", wait)
	}

	// Once a slot frees up the same credentials sign in normally.
	<-guard.sem
	rec2 := f.do(secRequest{method: "POST", path: "/api/v1/auth/login", body: body, remoteAddr: "10.9.9.9:1234"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("status after a slot freed = %d, body = %s", rec2.Code, rec2.Body.String())
	}
}
