// Tests for the machine-readable half of an error response: the X-Error-Code
// and X-Error-Message headers huggingface_hub reads before it ever looks at the
// body (hf_raise_for_status in huggingface_hub/utils/_http.py).
//
// The one that matters most is X-Error-Code: RepoNotFound. hf_raise_for_status
// raises RepositoryNotFoundError only when that header is present, or on a 401
// whose URL matches REPO_API_REGEX -- and that regex is anchored on
// `^https://`, so a self-hosted instance reached over plain HTTP never hits the
// second case. RepositoryNotFoundError is in turn the only exception
// HfApi.repo_exists / revision_exists catch, and one of the three file_exists
// catches, so a bare 404 makes all three raise HfHubHTTPError instead of
// answering False.
//
// The other half of the contract is what must *not* carry the header: a
// repository that exists but is archived, or that the caller may not write, is
// a 403/409, not a RepoNotFound. Claiming otherwise would make repo_exists()
// answer False for a repository the same client can list and download.
//
// Driven over real HTTP against a real Server, like revision_test.go and
// archive_test.go.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

type errSignalFixture struct {
	t     *testing.T
	s     *Server
	st    *store.Store
	git   *gitrepo.Manager
	alice *store.User
}

func newErrSignalFixture(t *testing.T) *errSignalFixture {
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
	cfg := &config.Config{
		PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret",
	}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  newMemStore(),
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})

	f := &errSignalFixture{t: t, s: srv, st: st, git: gitMgr}
	f.alice = f.user("alice")
	return f
}

func (f *errSignalFixture) user(name string) *store.User {
	f.t.Helper()
	u, err := f.st.CreateUser(context.Background(), name, name+"@example.com", "hash", false)
	if err != nil {
		f.t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

// repo creates a repository with one commit on main, so both the "this exists"
// and the "this file resolves" halves have something real to answer with.
func (f *errSignalFixture) repo(ns, name, kind string) *store.Repo {
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
		f.t.Fatalf("git init %s/%s: %v", ns, name, err)
	}
	gitRepo, err := f.git.Open(sp)
	if err != nil {
		f.t.Fatalf("open git repo: %v", err)
	}
	if _, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch:  "main",
		Message: "Add README",
		Author:  gitrepo.Signature{Name: ns, Email: ns + "@example.com", When: time.Now()},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "README.md", Data: []byte("hello")}},
	}); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	return r
}

func (f *errSignalFixture) token(u *store.User, scope string) string {
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

func (f *errSignalFixture) do(method, path, token string, body any) response {
	f.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return response{rec: rec}
}

// errSignalErrorBody decodes the JSON error envelope so a test can compare the
// header against the message the body carries.
func errSignalErrorBody(t *testing.T, r response) apitypes.ApiError {
	t.Helper()
	var body apitypes.ApiErrorBody
	if err := json.Unmarshal(r.rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", r.rec.Body.String(), err)
	}
	return body.Error
}

// errSignalRequest is one call huggingface_hub makes against a repository id.
type errSignalRequest struct {
	name   string
	method string
	path   string
	body   any
}

// errSignalRequestsAgainst names the HF-compatible calls that reach loadRepoForRead for
// a repository id, so no endpoint quietly keeps the old bare 404.
func errSignalRequestsAgainst(ns, name string) []errSignalRequest {
	base := "/api/models/" + ns + "/" + name
	return []errSignalRequest{
		// HfApi.repo_info -- what repo_exists() calls.
		{"repo-info", "GET", base, nil},
		// HfApi.repo_info(revision=...) -- what revision_exists() calls.
		{"repo-info at revision", "GET", base + "/revision/main", nil},
		// HfApi.list_repo_files / list_repo_tree.
		{"tree", "GET", base + "/tree/main", nil},
		{"paths-info", "POST", base + "/paths-info/main", map[string]any{"paths": []string{"README.md"}}},
		// get_hf_file_metadata -- what file_exists() calls.
		{"resolve", "GET", "/" + ns + "/" + name + "/resolve/main/README.md", nil},
		{"resolve HEAD", "HEAD", "/" + ns + "/" + name + "/resolve/main/README.md", nil},
	}
}

// ------------------------------------------------------- missing repository

// The headline case: every HF-compatible read of a repository that does not
// exist answers 404 with X-Error-Code: RepoNotFound, which is what makes
// repo_exists() return False instead of raising.
func TestMissingRepo_HFReadsCarryRepoNotFound(t *testing.T) {
	f := newErrSignalFixture(t)
	// A repository under the same namespace, so the miss is the repository and
	// not the namespace.
	f.repo("alice", "foo", "model")

	for _, tc := range errSignalRequestsAgainst("alice", "ghost") {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, "", tc.body)
			if resp.status() != 404 {
				t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "RepoNotFound" {
				t.Fatalf("X-Error-Code = %q, want RepoNotFound", got)
			}
		})
	}
}

// The same for a namespace that has no rows at all: huggingface_hub cannot tell
// the two apart, and neither may the signal.
func TestMissingNamespace_HFReadsCarryRepoNotFound(t *testing.T) {
	f := newErrSignalFixture(t)

	for _, tc := range errSignalRequestsAgainst("nobody", "ghost") {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, "", tc.body)
			if resp.status() != 404 {
				t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "RepoNotFound" {
				t.Fatalf("X-Error-Code = %q, want RepoNotFound", got)
			}
		})
	}
}

// The write endpoints go through loadRepoForWrite, which layers onto
// loadRepoForRead -- so upload_file / create_commit against a repo id nobody
// created must raise RepositoryNotFoundError too, not a bare HfHubHTTPError.
func TestMissingRepo_HFWritesCarryRepoNotFound(t *testing.T) {
	f := newErrSignalFixture(t)
	tok := f.token(f.alice, "write")

	cases := []errSignalRequest{
		{"preupload", "POST", "/api/models/alice/ghost/preupload/main", map[string]any{"files": []any{}}},
		{"commit", "POST", "/api/models/alice/ghost/commit/main", nil},
		{"lfs batch", "POST", "/models/alice/ghost/info/lfs/objects/batch",
			map[string]any{"operation": "upload", "objects": []any{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, tok, tc.body)
			if resp.status() != 404 {
				t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "RepoNotFound" {
				t.Fatalf("X-Error-Code = %q, want RepoNotFound", got)
			}
		})
	}
}

// The Web UI's own routes share loadRepoForRead, so they carry the header too.
// That is harmless: apiFetch in frontend/lib/api.ts reads only the status and
// the JSON body, and X-Error-Code is not in the CORS Access-Control-Expose-
// Headers list, so browser JS cannot see it even in principle.
func TestMissingRepo_UIRoutesCarryRepoNotFoundAndStayJSON(t *testing.T) {
	f := newErrSignalFixture(t)

	resp := f.do("GET", "/api/v1/repos/model/alice/ghost", "", nil)
	if resp.status() != 404 {
		t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RepoNotFound" {
		t.Fatalf("X-Error-Code = %q, want RepoNotFound", got)
	}
	// The body shape the frontend is typed against is untouched.
	if got := errSignalErrorBody(t, resp).Type; got != "not_found" {
		t.Fatalf("error type = %q, want not_found; body = %s", got, resp.rec.Body.String())
	}
}

// A repository that has moved is answered with a redirect (or the repo_moved
// body) on every route but DELETE /api/repos/delete, where redirectNone makes
// it look exactly like one that never existed -- deliberately, so a client
// cannot delete the repository sitting at the new name by accident. "Exactly
// like" has to include the header, or the two identical bodies would still
// raise different exceptions on the client.
func TestMovedRepo_DeleteByOldNameCarriesRepoNotFoundAndNoLocation(t *testing.T) {
	f := newErrSignalFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/repos/move", tok,
		map[string]any{"fromRepo": "alice/foo", "toRepo": "alice/bar", "type": "model"})
	if resp.status() != 200 {
		t.Fatalf("move status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	// Sanity: a read of the old name still redirects, so the redirect row exists
	// and this test is really exercising the redirectNone branch below.
	if got := f.do("GET", "/api/models/alice/foo", tok, nil).status(); got != 308 {
		t.Fatalf("repo-info on the old name = %d, want 308", got)
	}

	resp = f.do("DELETE", "/api/repos/delete", tok, map[string]any{"name": "alice/foo", "type": "model"})
	if resp.status() != 404 {
		t.Fatalf("delete status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RepoNotFound" {
		t.Fatalf("X-Error-Code = %q, want RepoNotFound", got)
	}
	if got := resp.rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want none: redirectNone must not disclose the new name", got)
	}
	if got := errSignalErrorBody(t, resp).MovedTo; got != nil {
		t.Fatalf("moved_to = %+v, want nil", got)
	}
	// And the repository at the new name is still there.
	if got := f.do("GET", "/api/models/alice/bar", tok, nil).status(); got != 200 {
		t.Fatalf("repo-info on the new name = %d, want 200", got)
	}
}

// ------------------------------------------------- exists but out of reach

// An archived repository exists and is readable; only writes are refused. It
// must stay a 403 repository_archived, because a RepoNotFound here would make
// repo_exists() answer False for a repository the same client can download.
func TestArchivedRepo_WriteRefusalIsNotRepoNotFound(t *testing.T) {
	f := newErrSignalFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	if got := f.do("POST", "/api/v1/repos/model/alice/foo/archive", tok, nil).status(); got != 200 {
		t.Fatalf("archive status = %d, want 200", got)
	}

	resp := f.do("POST", "/api/models/alice/foo/preupload/main", tok, map[string]any{"files": []any{}})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "" {
		t.Fatalf("X-Error-Code = %q, want none on an archived repository", got)
	}
	if got := errSignalErrorBody(t, resp).Type; got != "repository_archived" {
		t.Fatalf("error type = %q, want repository_archived", got)
	}
	// The read side is unaffected: this is the state repo_exists() must call True.
	if got := f.do("GET", "/api/models/alice/foo", "", nil).status(); got != 200 {
		t.Fatalf("repo-info on an archived repository = %d, want 200", got)
	}
}

// Someone else's repository is visible but not writable. The refusal is about
// permission, so it stays 401/403 and carries no RepoNotFound either.
func TestNoWriteAccess_RefusalIsNotRepoNotFound(t *testing.T) {
	f := newErrSignalFixture(t)
	f.repo("alice", "foo", "model")
	bob := f.user("bob")

	cases := []struct {
		name   string
		token  string
		status int
	}{
		{"anonymous", "", 401},
		{"another user", f.token(bob, "write"), 403},
		{"read-only token", f.token(f.alice, "read"), 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("POST", "/api/models/alice/foo/preupload/main", tc.token,
				map[string]any{"files": []any{}})
			if resp.status() != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", resp.status(), tc.status, resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "" {
				t.Fatalf("X-Error-Code = %q, want none: the repository exists", got)
			}
		})
	}
}

// A name collision is a 409, and must not be reported as a missing repository
// either -- create_repo(exist_ok=False) needs to see the conflict. (This route
// keeps HF's own error body, so only the header is asserted here.)
func TestDuplicateRepo_ConflictIsNotRepoNotFound(t *testing.T) {
	f := newErrSignalFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/repos/create", tok,
		map[string]any{"name": "foo", "organization": "alice", "type": "model"})
	if resp.status() != 409 {
		t.Fatalf("status = %d, want 409; body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "" {
		t.Fatalf("X-Error-Code = %q, want none on a conflict", got)
	}
}

// -------------------------------------------------------- X-Error-Message

// Every error response carries X-Error-Message, and it says exactly what the
// body says. hf_raise_for_status reads the header first and only falls back to
// the body, where this API's `error` is an object rather than HF's plain
// string -- so without the header the user is shown a Python dict repr.
func TestErrorResponses_CarryXErrorMessageMatchingBody(t *testing.T) {
	f := newErrSignalFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")
	if got := f.do("POST", "/api/v1/repos/model/alice/foo/archive", tok, nil).status(); got != 200 {
		t.Fatalf("archive status = %d, want 200", got)
	}

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		body   any
		status int
	}{
		{"404 missing repo", "GET", "/api/models/alice/ghost", "", nil, 404},
		{"400 malformed body", "POST", "/api/v1/auth/login", "", "not-an-object", 400},
		{"401 anonymous write", "POST", "/api/models/alice/foo/preupload/main", "",
			map[string]any{"files": []any{}}, 401},
		{"403 archived", "POST", "/api/models/alice/foo/preupload/main", tok,
			map[string]any{"files": []any{}}, 403},
		// The Web UI's create, not the HF-compatible one: HF's answers a
		// duplicate with its own body shape (`{"error": "You already created
		// this model repo", ...}`, a plain string, which is exactly what
		// huggingface_hub expects there) without going through writeError, so
		// it has no X-Error-Message to check.
		{"409 duplicate", "POST", "/api/v1/repos", tok,
			map[string]any{"kind": "model", "namespace": "alice", "name": "foo"}, 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, tc.token, tc.body)
			if resp.status() != tc.status {
				t.Fatalf("status = %d, want %d; body = %s", resp.status(), tc.status, resp.rec.Body.String())
			}
			header := resp.rec.Header().Get("X-Error-Message")
			if header == "" {
				t.Fatalf("X-Error-Message missing; body = %s", resp.rec.Body.String())
			}
			if msg := errSignalErrorBody(t, resp).Message; header != msg {
				t.Fatalf("X-Error-Message = %q, body message = %q; they must agree", header, msg)
			}
		})
	}
}

// ------------------------------------------------------------- regression

// The success path carries neither header. A stale X-Error-Code on a 200 would
// be read by huggingface_hub only on a later failure, but a stale
// X-Error-Message is worse: it would surface as the reason for an unrelated
// error, and a `X-Error-Code: RepoNotFound` leaking onto a 2xx would be a lie
// about a repository that is right there in the response body.
func TestSuccessfulResponses_CarryNoErrorHeaders(t *testing.T) {
	f := newErrSignalFixture(t)
	f.repo("alice", "foo", "model")

	for _, tc := range errSignalRequestsAgainst("alice", "foo") {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, "", tc.body)
			if resp.status() != 200 {
				t.Fatalf("status = %d, want 200; body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "" {
				t.Fatalf("X-Error-Code = %q on a 200", got)
			}
			if got := resp.rec.Header().Get("X-Error-Message"); got != "" {
				t.Fatalf("X-Error-Message = %q on a 200", got)
			}
		})
	}
}

// A revision that does not exist in a repository that does keeps its own
// signal: RevisionNotFound, not RepoNotFound. revision_exists() catches both,
// but hf_hub_download and file_exists rely on the distinction to report which
// half of the coordinates was wrong.
func TestUnknownRevision_StaysRevisionNotFound(t *testing.T) {
	f := newErrSignalFixture(t)
	f.repo("alice", "foo", "model")

	resp := f.do("GET", "/api/models/alice/foo/revision/does-not-exist", "", nil)
	if resp.status() != 404 {
		t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}
