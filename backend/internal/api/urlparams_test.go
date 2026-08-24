// Tests for URL path parameter decoding (urlparams.go, plus revParam/refParam
// in refs.go and wildcardPath in server.go).
//
// Two failures live here, and they are each other's opposite:
//
//   - Not decoding. huggingface_hub sends every revision as
//     `quote(revision, safe="")`, so the branch "feature/x" arrives as
//     "feature%2Fx". chi routes on the escaped path when there is one, so the
//     handler was handed "feature%2Fx" literally: create_branch("feature/x")
//     succeeded and every read and write naming the branch afterwards came
//     back 404, empty, or -- worse -- landed on a *new* branch literally named
//     "feature%2Fx".
//   - Decoding twice. That same "%2F" makes r.URL.RawPath non-empty for the
//     whole request, which is the only signal that anything is encoded. A
//     request without one carries parameters chi already decoded, so an
//     unconditional unescape mangles any file name holding a literal "%":
//     "a%b.txt" fails to unescape at all, and "a%2Fb.txt" turns into a path
//     with a directory separator in it.
//
// So every case below is run on both a plain revision and a slash-bearing one,
// and file names with "%" in them are checked on both. Driven over real HTTP
// against a real Server, the way revision_test.go and preupload_test.go are.

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/parquet-go/parquet-go"
	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/viewer"
)

// ------------------------------------------------------------------ fixture

type urlParamFixture struct {
	t     *testing.T
	s     *Server
	st    *store.Store
	git   *gitrepo.Manager
	write string
}

func newURLParamFixture(t *testing.T) *urlParamFixture {
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
	blobs := newMemStore()
	cfg := &config.Config{
		PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret",
	}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  blobs,
		Viewer:   viewer.New(blobs, 1<<20),
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})

	f := &urlParamFixture{t: t, s: srv, st: st, git: gitMgr}
	u, err := st.CreateUser(ctx, "alice", "alice@example.com", "hash", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	tok, hash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if _, err := st.CreateToken(ctx, u.ID, "test", "write", hash, nil); err != nil {
		t.Fatalf("create token: %v", err)
	}
	f.write = tok
	return f
}

// repo creates alice/{name} with one commit on main, so there is always a
// revision that needs no escaping to compare the escaped one against.
func (f *urlParamFixture) repo(name string) *store.Repo {
	f.t.Helper()
	ctx := context.Background()
	n, err := f.st.GetNamespace(ctx, "alice")
	if err != nil {
		f.t.Fatalf("namespace alice: %v", err)
	}
	sp := store.NewStoragePath()
	r, err := f.st.CreateRepo(ctx, n.ID, name, "model", "desc", "main", sp)
	if err != nil {
		f.t.Fatalf("create repo alice/%s: %v", name, err)
	}
	if err := f.git.Init(sp, "main"); err != nil {
		f.t.Fatalf("git init alice/%s: %v", name, err)
	}
	f.commit(r, "main", "seed", map[string][]byte{"README.md": []byte("# foo\n")})
	return r
}

func (f *urlParamFixture) commit(r *store.Repo, branch, message string, files map[string][]byte) plumbing.Hash {
	f.t.Helper()
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		f.t.Fatalf("open git repo: %v", err)
	}
	ops := make([]gitrepo.Op, 0, len(files))
	for p, data := range files {
		ops = append(ops, gitrepo.Op{Kind: gitrepo.OpAdd, Path: p, Data: data})
	}
	h, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch:  branch,
		Message: message,
		Author:  gitrepo.Signature{Name: "alice", Email: "alice@example.com", When: time.Now()},
		Ops:     ops,
	})
	if err != nil {
		f.t.Fatalf("commit to %s: %v", branch, err)
	}
	return h
}

// send drives one request through the real router. The path is passed through
// verbatim -- the percent-encoding *is* the thing under test, so nothing here
// may normalise it.
func (f *urlParamFixture) send(method, path, token, contentType string, body []byte) response {
	f.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return response{rec: rec}
}

func (f *urlParamFixture) get(path, token string) response {
	f.t.Helper()
	return f.send("GET", path, token, "", nil)
}

func (f *urlParamFixture) postJSON(path, token string, body any) response {
	f.t.Helper()
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal body: %v", err)
		}
		raw = b
	}
	return f.send("POST", path, token, "application/json", raw)
}

// branches lists the repository's branch names through the public refs
// endpoint, which is how a test tells "the write landed on feature/x" apart
// from "the write invented a branch called feature%2Fx".
func (f *urlParamFixture) branches(repoPath string) []string {
	f.t.Helper()
	resp := f.get("/api/models/"+repoPath+"/refs", "")
	if resp.status() != 200 {
		f.t.Fatalf("refs: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
	var out struct {
		Branches []struct {
			Name string `json:"name"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(resp.rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("decode refs: %v (body %s)", err, resp.rec.Body.String())
	}
	names := make([]string, 0, len(out.Branches))
	for _, b := range out.Branches {
		names = append(names, b.Name)
	}
	return names
}

func (f *urlParamFixture) fileAt(r *store.Repo, rev, path string) (string, bool) {
	f.t.Helper()
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		f.t.Fatalf("open git repo: %v", err)
	}
	data, err := gitRepo.ReadFile(rev, path, 1<<20)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func containsName(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// ------------------------------------------- the chi behaviour being relied on
//
// Everything above rests on one property of chi v5: it routes on
// r.URL.RawPath when net/url set one and on r.URL.Path otherwise, and the
// parameters it extracts are slices of whichever string it routed on. If a chi
// upgrade ever changes that, this test fails first and says so, instead of the
// breakage surfacing as a mangled file name.

func TestChiURLParamEncodingContract(t *testing.T) {
	type seen struct {
		rawPath string
		rev     string
		star    string
	}
	var got seen
	r := chi.NewRouter()
	r.Get("/resolve/{rev}/*", func(_ http.ResponseWriter, req *http.Request) {
		got = seen{rawPath: req.URL.RawPath, rev: chi.URLParam(req, "rev"), star: chi.URLParam(req, "*")}
	})

	cases := []struct {
		name        string
		url         string
		wantEscaped bool // is RawPath set, i.e. are the parameters still encoded?
		wantRev     string
		wantStar    string
	}{
		{
			name: "plain path: parameters arrive already decoded",
			url:  "/resolve/main/dir/a%20b.txt", wantEscaped: false,
			wantRev: "main", wantStar: "dir/a b.txt",
		},
		{
			// The one that makes unconditional unescaping a bug: the file is
			// named "a%b.txt" and chi already handed over the decoded form.
			name: "literal percent in the file name, plain revision",
			url:  "/resolve/main/a%25b.txt", wantEscaped: false,
			wantRev: "main", wantStar: "a%b.txt",
		},
		{
			name: "slashed revision: every parameter stays encoded",
			url:  "/resolve/feature%2Fx/a.txt", wantEscaped: true,
			wantRev: "feature%2Fx", wantStar: "a.txt",
		},
		{
			// Both halves of the same request are affected at once, which is
			// why the fix has to cover the wildcard and not only the revision.
			name: "slashed revision drags the file path into the encoded form",
			url:  "/resolve/feature%2Fx/a%25b.txt", wantEscaped: true,
			wantRev: "feature%2Fx", wantStar: "a%25b.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got = seen{}
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", tc.url, nil))
			if escaped := got.rawPath != ""; escaped != tc.wantEscaped {
				t.Fatalf("RawPath = %q, want escaped=%v", got.rawPath, tc.wantEscaped)
			}
			if got.rev != tc.wantRev {
				t.Errorf("rev = %q, want %q", got.rev, tc.wantRev)
			}
			if got.star != tc.wantStar {
				t.Errorf("* = %q, want %q", got.star, tc.wantStar)
			}
		})
	}
}

// ------------------------------------------------------ slashed branch: reads

// The branch is created through the public API exactly the way
// HfApi.create_branch does it, so the test covers the round trip rather than a
// branch a fixture wrote behind the API's back.
func (f *urlParamFixture) createBranch(repoPath, encodedBranch, from string) {
	f.t.Helper()
	resp := f.postJSON("/api/models/"+repoPath+"/branch/"+encodedBranch, f.write,
		map[string]any{"startingPoint": from})
	if resp.status() != 201 {
		f.t.Fatalf("create_branch %s: status %d, body %s", encodedBranch, resp.status(), resp.rec.Body.String())
	}
}

func TestSlashedBranch_ReadEndpoints(t *testing.T) {
	f := newURLParamFixture(t)
	r := f.repo("foo")
	f.createBranch("alice/foo", "feature%2Fx", "main")
	f.commit(r, "feature/x", "branch work", map[string][]byte{
		"only-on-branch.txt": []byte("from the branch\n"),
		"data.parquet":       buildTestParquet(t),
		"model.safetensors":  buildTestSafetensors(t),
	})

	// Sanity: the branch really is "feature/x" and not "feature%2Fx".
	if names := f.branches("alice/foo"); !containsName(names, "feature/x") || containsName(names, "feature%2Fx") {
		t.Fatalf("branches = %v, want feature/x and no feature%%2Fx", names)
	}

	cases := []struct {
		name string
		path string
		// substring the successful body must contain, proving the response
		// came from the branch rather than from main
		want string
	}{
		{"hf resolve", "/models/alice/foo/resolve/feature%2Fx/only-on-branch.txt", "from the branch"},
		{"hf repo-info", "/api/models/alice/foo/revision/feature%2Fx", "only-on-branch.txt"},
		{"hf tree", "/api/models/alice/foo/tree/feature%2Fx", "only-on-branch.txt"},
		{"hf tree with a subpath", "/api/models/alice/foo/tree/feature%2Fx/", "only-on-branch.txt"},
		{"hf commits", "/api/models/alice/foo/commits/feature%2Fx", "branch work"},
		{"ui tree", "/api/v1/repos/model/alice/foo/tree/feature%2Fx", "only-on-branch.txt"},
		{"ui raw", "/api/v1/raw/model/alice/foo/feature%2Fx/only-on-branch.txt", "from the branch"},
		{"ui commits", "/api/v1/repos/model/alice/foo/commits/feature%2Fx", "branch work"},
		{"ui gcs script", "/api/v1/repos/model/alice/foo/gcs/feature%2Fx", "gcloud"},
		{"parquet schema", "/api/v1/parquet/model/alice/foo/schema/feature%2Fx/data.parquet", "label"},
		{"model metadata", "/api/v1/model-meta/model/alice/foo/feature%2Fx/model.safetensors", "safetensors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.get(tc.path, f.write)
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			if body := resp.rec.Body.String(); !strings.Contains(body, tc.want) {
				t.Fatalf("body does not mention %q: %s", tc.want, body)
			}
		})
	}

	// paths-info takes its paths in the body, so it gets its own assertion.
	resp := f.postJSON("/api/models/alice/foo/paths-info/feature%2Fx", f.write,
		map[string]any{"paths": []string{"only-on-branch.txt"}})
	if resp.status() != 200 {
		t.Fatalf("paths-info: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
	if !strings.Contains(resp.rec.Body.String(), "only-on-branch.txt") {
		t.Fatalf("paths-info did not find the file on the branch: %s", resp.rec.Body.String())
	}
}

// ----------------------------------------------------- slashed branch: writes

func TestSlashedBranch_WriteEndpoints(t *testing.T) {
	f := newURLParamFixture(t)
	r := f.repo("foo")
	f.createBranch("alice/foo", "feature%2Fx", "main")

	mainBefore, _ := f.fileAt(r, "main", "README.md")

	// preupload: the step huggingface_hub runs before every commit.
	resp := f.postJSON("/api/models/alice/foo/preupload/feature%2Fx", f.write, map[string]any{
		"files": []map[string]any{{"path": "notes.md", "size": 4}},
	})
	if resp.status() != 200 {
		t.Fatalf("preupload: status %d, body %s", resp.status(), resp.rec.Body.String())
	}

	// commit (the NDJSON protocol create_commit speaks).
	body := `{"key":"header","value":{"summary":"hf commit"}}` + "\n" +
		`{"key":"file","value":{"path":"hf.txt","content":"` +
		base64.StdEncoding.EncodeToString([]byte("hf\n")) + `","encoding":"base64"}}` + "\n"
	resp = f.send("POST", "/api/models/alice/foo/commit/feature%2Fx", f.write,
		"application/x-ndjson", []byte(body))
	if resp.status() != 200 {
		t.Fatalf("commit: status %d, body %s", resp.status(), resp.rec.Body.String())
	}

	// the Web UI's single-file editor.
	resp = f.send("PUT", "/api/v1/edit/model/alice/foo/feature%2Fx/notes.md", f.write,
		"application/json", []byte(`{"content":"edited\n","message":"edit"}`))
	if resp.status() != 200 {
		t.Fatalf("edit: status %d, body %s", resp.status(), resp.rec.Body.String())
	}

	// the Web UI's multipart upload.
	multi, ctype := singleFileMultipart(t, "uploaded.txt", []byte("uploaded\n"))
	resp = f.send("POST", "/api/v1/upload/model/alice/foo/feature%2Fx", f.write, ctype, multi)
	if resp.status() != 200 && resp.status() != 201 {
		t.Fatalf("upload: status %d, body %s", resp.status(), resp.rec.Body.String())
	}

	// Every write landed on the real branch...
	for _, path := range []string{"hf.txt", "notes.md", "uploaded.txt"} {
		if _, ok := f.fileAt(r, "feature/x", path); !ok {
			t.Errorf("%s is missing from feature/x", path)
		}
	}
	// ...and nowhere else. A write that took the revision literally would have
	// created a branch named "feature%2Fx" out of a parentless root commit and
	// reported success, which is the silent half of this bug.
	if names := f.branches("alice/foo"); containsName(names, "feature%2Fx") {
		t.Errorf("a branch named feature%%2Fx was created: %v", names)
	}
	if after, _ := f.fileAt(r, "main", "README.md"); after != mainBefore {
		t.Errorf("main changed: %q -> %q", mainBefore, after)
	}
	if _, ok := f.fileAt(r, "main", "hf.txt"); ok {
		t.Errorf("the commit landed on main instead of feature/x")
	}

	// The delete endpoint closes the loop on the same branch.
	resp = f.send("DELETE", "/api/v1/edit/model/alice/foo/feature%2Fx/notes.md", f.write,
		"application/json", nil)
	if resp.status() != 200 {
		t.Fatalf("delete: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
	if _, ok := f.fileAt(r, "feature/x", "notes.md"); ok {
		t.Errorf("notes.md survived the delete on feature/x")
	}
}

// ------------------------------------------------- literal "%" in file names

// The double-decode regression. Each name is committed as-is and requested in
// its percent-encoded form; the response has to be the file that was actually
// committed, on a plain revision (where chi already decoded the parameter) and
// on a slashed one (where it did not) alike.
func TestPercentInFileNameIsNotDecodedTwice(t *testing.T) {
	f := newURLParamFixture(t)
	r := f.repo("foo")
	f.createBranch("alice/foo", "feature%2Fx", "main")

	files := map[string][]byte{
		"a%b.txt":     []byte("literal percent\n"),
		"a%2Fb.txt":   []byte("percent two f\n"),
		"100%25.txt":  []byte("percent twenty five\n"),
		"sub/c%d.txt": []byte("nested percent\n"),
	}
	f.commit(r, "feature/x", "percent names", files)
	f.commit(r, "main", "percent names", files)

	// What the URL for each name looks like: every "%" becomes "%25".
	encoded := map[string]string{
		"a%b.txt":     "a%25b.txt",
		"a%2Fb.txt":   "a%252Fb.txt",
		"100%25.txt":  "100%2525.txt",
		"sub/c%d.txt": "sub/c%25d.txt",
	}

	for name, want := range files {
		for _, rev := range []struct{ label, encoded string }{
			{"plain revision", "main"},
			{"slashed revision", "feature%2Fx"},
		} {
			t.Run(name+" / "+rev.label, func(t *testing.T) {
				resp := f.get("/models/alice/foo/resolve/"+rev.encoded+"/"+encoded[name], f.write)
				if resp.status() != 200 {
					t.Fatalf("resolve: status %d, body %s", resp.status(), resp.rec.Body.String())
				}
				if got := resp.rec.Body.String(); got != string(want) {
					t.Fatalf("body = %q, want %q", got, string(want))
				}
			})
		}
	}

	// The Web UI reads the same names through its own route, which resolves
	// the wildcard the same way.
	resp := f.get("/api/v1/raw/model/alice/foo/main/a%252Fb.txt", f.write)
	if resp.status() != 200 || !strings.Contains(resp.rec.Body.String(), "percent two f") {
		t.Fatalf("ui raw of a%%2Fb.txt: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
}

// A ref name may contain "%" -- ValidateRefName does not forbid it -- so
// refParam has the same double-decode hazard as the wildcard. "50%done"
// travels as "50%25done", which needs no path normalisation, so it arrives
// already decoded.
func TestPercentInBranchName(t *testing.T) {
	f := newURLParamFixture(t)
	f.repo("foo")
	f.createBranch("alice/foo", "50%25done", "main")

	if names := f.branches("alice/foo"); !containsName(names, "50%done") {
		t.Fatalf("branches = %v, want 50%%done", names)
	}
	if resp := f.get("/api/models/alice/foo/tree/50%25done", f.write); resp.status() != 200 {
		t.Fatalf("tree of 50%%done: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
}

// ------------------------------------------------------------------ create_pr

// huggingface_hub asks for a pull request with `?create_pr=1` on preupload and
// on commit. thinkingface has none, and used to read neither parameter: the
// commit went straight onto the target branch and answered 200, so the caller
// believed a reviewable PR existed while the branch had already moved.
func TestCreatePRIsRefused(t *testing.T) {
	f := newURLParamFixture(t)
	r := f.repo("foo")
	before, _ := f.fileAt(r, "main", "README.md")

	body := `{"key":"header","value":{"summary":"via a PR"}}` + "\n" +
		`{"key":"file","value":{"path":"pr.txt","content":"` +
		base64.StdEncoding.EncodeToString([]byte("pr\n")) + `","encoding":"base64"}}` + "\n"

	for _, value := range []string{"1", "true", "True"} {
		t.Run("commit?create_pr="+value, func(t *testing.T) {
			resp := f.send("POST", "/api/models/alice/foo/commit/main?create_pr="+value, f.write,
				"application/x-ndjson", []byte(body))
			if resp.status() != 400 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			if !strings.Contains(resp.rec.Body.String(), "pull request") {
				t.Errorf("message does not mention pull requests: %s", resp.rec.Body.String())
			}
		})
	}

	t.Run("preupload?create_pr=1", func(t *testing.T) {
		resp := f.postJSON("/api/models/alice/foo/preupload/main?create_pr=1", f.write,
			map[string]any{"files": []map[string]any{{"path": "pr.txt", "size": 3}}})
		if resp.status() != 400 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
	})

	// Nothing was written by any of the refused calls.
	if _, ok := f.fileAt(r, "main", "pr.txt"); ok {
		t.Fatalf("a refused create_pr commit still wrote to main")
	}
	if after, _ := f.fileAt(r, "main", "README.md"); after != before {
		t.Fatalf("main changed: %q -> %q", before, after)
	}

	// A client that is *not* asking for a PR must be unaffected, whether it
	// omits the parameter or spells out the falsey value.
	for _, suffix := range []string{"", "?create_pr=0", "?create_pr=false"} {
		t.Run("commit"+suffix, func(t *testing.T) {
			resp := f.send("POST", "/api/models/alice/foo/commit/main"+suffix, f.write,
				"application/x-ndjson", []byte(body))
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
		})
	}
}

// --------------------------------------------------------- resolve: 404 shape

// huggingface_hub raises EntryNotFoundError -- the error hf_hub_download
// documents for a file that is not in the repository -- only when the 404
// carries this header. Without it a missing file came back as a generic
// HfHubHTTPError, indistinguishable from the repository itself being gone.
func TestResolveMissingFileIsEntryNotFound(t *testing.T) {
	f := newURLParamFixture(t)
	f.repo("foo")
	f.createBranch("alice/foo", "feature%2Fx", "main")

	for _, rev := range []string{"main", "feature%2Fx"} {
		t.Run(rev, func(t *testing.T) {
			resp := f.get("/models/alice/foo/resolve/"+rev+"/nope.txt", f.write)
			if resp.status() != 404 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "EntryNotFound" {
				t.Fatalf("X-Error-Code = %q, want EntryNotFound", got)
			}
		})
	}

	// A revision that does not resolve at all is a different failure and keeps
	// its own answer: it must not claim the entry is what is missing.
	resp := f.get("/models/alice/foo/resolve/no-such-branch/README.md", f.write)
	if resp.status() != 404 {
		t.Fatalf("unknown revision: status = %d", resp.status())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got == "EntryNotFound" {
		t.Fatalf("an unresolvable revision was reported as EntryNotFound")
	}
}

// ------------------------------------------------------------------ builders

// singleFileMultipart builds the body the Web UI's uploader sends: a message
// field followed by one file part.
func singleFileMultipart(t *testing.T, name string, data []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("message", "upload"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	w, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

type urlParamParquetRow struct {
	Label string `parquet:"label"`
	Value int64  `parquet:"value"`
}

// buildTestParquet writes a real parquet file, so the viewer endpoint answers
// 200 rather than failing on the bytes and hiding whether the revision
// resolved.
func buildTestParquet(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[urlParamParquetRow](&buf)
	if _, err := w.Write([]urlParamParquetRow{{Label: "a", Value: 1}, {Label: "b", Value: 2}}); err != nil {
		t.Fatalf("write parquet rows: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close parquet writer: %v", err)
	}
	return buf.Bytes()
}

// buildTestSafetensors writes the smallest valid safetensors file: an 8-byte
// little-endian header length, the JSON header, then the tensor bytes.
func buildTestSafetensors(t *testing.T) []byte {
	t.Helper()
	header, err := json.Marshal(map[string]any{
		"weight": map[string]any{
			"dtype":        "F32",
			"shape":        []int64{2},
			"data_offsets": []int64{0, 8},
		},
	})
	if err != nil {
		t.Fatalf("marshal safetensors header: %v", err)
	}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, uint64(len(header))); err != nil {
		t.Fatalf("write header length: %v", err)
	}
	buf.Write(header)
	buf.Write(make([]byte, 8))
	return buf.Bytes()
}
