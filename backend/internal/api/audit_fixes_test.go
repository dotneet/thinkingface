// Regression tests for the second API audit. Each one fails against the code
// as it stood before the fix:
//
//   - a write endpoint answering 500 internal_error for input it has always
//     been able to reject (an invalid path, a name git cannot hold as a
//     branch);
//   - a moved repository's redirect Location losing the percent-encoding that
//     makes a slash-bearing revision -- or a "%" in a file name -- work at all;
//   - HTTP Basic password failures not spending the address failure budget
//     that /auth/login is metered against;
//   - a webhook id answering 403-with-the-owning-namespace instead of the 404
//     an id that does not exist gets;
//   - the parquet viewer answering 500 for a file it cannot read, and
//     comma-joined column names being unrepresentable;
//   - run and metric names with a comma in them selecting the wrong runs.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ------------------------------------------------------------------- [E-1]

// Every path a commit operation can name is checked before the commit runs, so
// a traversal attempt or a ".git" component is the caller's 400 rather than
// this server's 500. gitrepo.Commit has always refused these; the difference is
// only in how the refusal reaches the caller -- and it matters, because
// huggingface_hub retries a 5xx (http_backoff) until it gives up, and a 500
// carries no sentence saying what to fix.
func TestCommit_InvalidOperationPathIsBadRequest(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "weights", "model")
	tok := f.token(f.alice, "write")

	tests := []struct {
		name string
		line string
	}{
		{"add escapes the repository", `{"key":"file","value":{"path":"../escape.txt","content":"eA==","encoding":"base64"}}`},
		{"add writes inside .git", `{"key":"file","value":{"path":".git/config","content":"eA==","encoding":"base64"}}`},
		{"add has a dot segment", `{"key":"file","value":{"path":"a/./b","content":"eA==","encoding":"base64"}}`},
		{"add climbs out of a subdirectory", `{"key":"file","value":{"path":"dir/../../x","content":"eA==","encoding":"base64"}}`},
		{"add has an empty path", `{"key":"file","value":{"path":"","content":"eA==","encoding":"base64"}}`},
		{"add is absolute", `{"key":"file","value":{"path":"/etc/passwd","content":"eA==","encoding":"base64"}}`},
		{"delete escapes the repository", `{"key":"deletedFile","value":{"path":"../x"}}`},
		{"delete folder escapes the repository", `{"key":"deletedFolder","value":{"path":"../x"}}`},
		{"copy destination escapes the repository", `{"key":"copyFile","value":{"path":"../x","srcPath":"README.md"}}`},
		{"copy source escapes the repository", `{"key":"copyFile","value":{"path":"x","srcPath":"../README.md"}}`},
		{"lfs pointer escapes the repository", `{"key":"lfsFile","value":{"path":"../x","oid":"` +
			strings.Repeat("a", 64) + `","size":1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok, tt.line)
			if resp.status() != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := errorType(t, resp); got != "bad_request" {
				t.Errorf("error type = %q, want bad_request", got)
			}
			// The sentence a client shows its user comes out of this header.
			if resp.rec.Header().Get("X-Error-Message") == "" {
				t.Error("X-Error-Message is empty; the caller is told nothing about what to fix")
			}
		})
	}
}

// The same rule for {rev}: a name git could never hold as a branch is refused
// before the repository is consulted. ".." used to reach go-git's ref lookup
// and come back as 500 "read branch ref failed"; the rest reached
// gitrepo.Commit and came back as 500 "create commit failed".
func TestWriteEndpoints_InvalidRevisionIsBadRequest(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "weights", "model")
	tok := f.token(f.alice, "write")

	revs := []struct {
		name string
		rev  string
	}{
		{"whitespace", "bad name"},
		{"double dot", "a..b"},
		{"leading dash", "-lead"},
		{"caret", "main^"},
		{"reflog syntax", "x@{0}"},
		{"lock suffix", "tail.lock"},
		{"bare double dot", ".."},
		{"leading dot component", ".hidden"},
	}
	for _, tt := range revs {
		t.Run("commit "+tt.name, func(t *testing.T) {
			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/"+url.PathEscape(tt.rev), tok,
				`{"key":"file","value":{"path":"a.txt","content":"eA==","encoding":"base64"}}`)
			if resp.status() != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
			}
		})
		t.Run("edit "+tt.name, func(t *testing.T) {
			resp := f.do("PUT", "/api/v1/edit/model/alice/weights/"+url.PathEscape(tt.rev)+"/a.txt", tok,
				map[string]any{"content": "hello"})
			if resp.status() != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
			}
		})
	}
}

// The two revisions that are *not* input errors keep the 409 the contract
// fixes for them: they name something real in this repository that simply
// cannot be committed to (docs/dev/api-contract.md, "{rev} is a branch name").
func TestCommit_ResolvableNonBranchRevisionStaysConflict(t *testing.T) {
	f := newArchiveFixture(t)
	repo := f.repo("alice", "weights", "model")
	tok := f.token(f.alice, "write")
	// A commit has to exist for HEAD to resolve.
	commitNDJSON(t, f, "/api/models/alice/weights/commit/main", tok,
		`{"key":"file","value":{"path":"a.txt","content":"eA==","encoding":"base64"}}`)
	seedTag(t, f, repo, "v1")

	for _, rev := range []string{"HEAD", "v1"} {
		t.Run(rev, func(t *testing.T) {
			resp := commitNDJSON(t, f, "/api/models/alice/weights/commit/"+rev, tok,
				`{"key":"file","value":{"path":"b.txt","content":"eA==","encoding":"base64"}}`)
			if resp.status() != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", resp.status(), resp.rec.Body.String())
			}
		})
	}
}

// The Web UI's edit endpoint reaches the same check through a percent-encoded
// path segment.
func TestEditFile_TraversalPathIsBadRequest(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "weights", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("PUT", "/api/v1/edit/model/alice/weights/main/..%2Fescape.txt", tok,
		map[string]any{"content": "hello"})
	if resp.status() != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
	}
}

// ------------------------------------------------------------------- [E-2]

// A renamed repository's 308 must keep the request's percent-encoding, or the
// redirect it hands out points somewhere else.
//
// Two cases, and huggingface_hub produces the first on every single call: it
// quotes revisions with quote(rev, safe=""), so the branch "feature/x" travels
// as "feature%2Fx". Decoded into the Location, that becomes revision "feature"
// and the follow-up request is a 404 RevisionNotFound. The second is a file
// whose name contains a "%": decoded, the Location is not even a valid URL and
// net/http refuses to follow it.
func TestMovedRepo_RedirectLocationKeepsEscaping(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	repo := f.repo("alice", "old", "model")
	f.writeFile(repo, "a%b.txt", []byte("percent"))
	f.writeFileOnBranch(repo, "feature/x", "README.md", []byte("on a slashy branch"))
	tok := f.token(alice, "write")

	move := f.do(secRequest{
		method: "POST", path: "/api/repos/move",
		body:    map[string]any{"fromRepo": "alice/old", "toRepo": "alice/new", "type": "model"},
		headers: map[string]string{"Authorization": "Bearer " + tok},
	})
	if move.Code != http.StatusOK {
		t.Fatalf("move status = %d, body = %s", move.Code, move.Body.String())
	}

	tests := []struct {
		name     string
		path     string
		wantLoc  string
		wantBody string
	}{
		{
			name:     "slash-bearing revision",
			path:     "/alice/old/resolve/feature%2Fx/README.md",
			wantLoc:  "/alice/new/resolve/feature%2Fx/README.md",
			wantBody: "on a slashy branch",
		},
		{
			name:     "percent in the file name",
			path:     "/alice/old/resolve/main/a%25b.txt",
			wantLoc:  "/alice/new/resolve/main/a%25b.txt",
			wantBody: "percent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do(secRequest{method: "GET", path: tt.path})
			if rec.Code != http.StatusPermanentRedirect {
				t.Fatalf("status = %d, want 308; body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != tt.wantLoc {
				t.Fatalf("Location = %q, want %q", got, tt.wantLoc)
			}
			// Following it has to actually work: the whole point of the
			// redirect is that an old repository id keeps functioning.
			followed := f.do(secRequest{method: "GET", path: rec.Header().Get("Location")})
			if followed.Code != http.StatusOK {
				t.Fatalf("following the redirect: status = %d, body = %s", followed.Code, followed.Body.String())
			}
			if got := followed.Body.String(); got != tt.wantBody {
				t.Errorf("body after the redirect = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

// ------------------------------------------------------------------- [E-3]

// HTTP Basic is accepted on every route, and a failed password there has to
// cost the caller's address the same failure budget a failed /auth/login does.
// It used to charge only the username bucket, so a guessing run that varied
// the username -- which is what a real one does -- ran forever from one
// address and left the login endpoint completely unthrottled.
func TestBasicAuth_FailuresSpendTheAddressBudget(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")

	const addr = "10.9.9.9:4444"
	// A different username every time, so the username buckets (which are
	// per-name and refill at half the address rate) never run out and the
	// address bucket is the only thing that can stop this.
	for i := 0; i < 15; i++ {
		r := httptest.NewRequest("GET", "/api/whoami-v2", bytes.NewReader(nil))
		r.RemoteAddr = addr
		r.SetBasicAuth(fmt.Sprintf("victim%d", i), "wrong")
		f.s.Handler().ServeHTTP(httptest.NewRecorder(), r)
	}

	rec := f.do(secRequest{
		method: "POST", path: "/api/v1/auth/login",
		body:       map[string]string{"username": "alice", "password": "wrong"},
		remoteAddr: addr,
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("login after 15 failed Basic attempts: status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After is missing from the 429")
	}
}

// ...and the counting must not double: one failed sign-in is one failure.
// handleLogin used to penalize the address itself on top of what checkPassword
// charged, which after the fix would have halved the budget it advertises.
func TestLogin_OneFailureCostsOneToken(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")

	// AuthRateLimitPerMinute is 10 in the fixture, so the address bucket holds
	// ten tokens: nine failures must leave it open.
	const addr = "10.9.9.8:4444"
	for i := 0; i < 9; i++ {
		rec := f.do(secRequest{
			method: "POST", path: "/api/v1/auth/login",
			body:       map[string]string{"username": fmt.Sprintf("nobody%d", i), "password": "wrong"},
			remoteAddr: addr,
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, rec.Code)
		}
	}
}

// ------------------------------------------------------------------- [E-4]

// A webhook id the caller may not administer answers exactly like an id that
// does not exist. Ids are small sequential integers in the URL, so anything
// else lets any account walk 1..N and read back every webhook on the instance
// together with the namespace that owns it -- which the 403 used to name in
// its message.
func TestGetWebhook_ForeignIdIsIndistinguishableFromMissing(t *testing.T) {
	f := newTransferFixture(t)
	aliceTok := f.token(f.alice, "write")
	bobTok := f.token(f.bob, "write")

	created := f.do("POST", "/api/v1/namespaces/alice/webhooks", aliceTok, map[string]any{
		"url": "https://hooks.example.com/a", "events": []string{"repo.push"},
	})
	if created.status() != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", created.status(), created.rec.Body.String())
	}
	var createdBody apitypes.CreateWebhookResponse
	created.json(t, &createdBody)
	id := createdBody.ID
	if id == 0 {
		t.Fatal("created webhook has no id")
	}

	paths := []string{
		"/api/v1/webhooks/%d",
		"/api/v1/webhooks/%d/deliveries",
	}
	for _, p := range paths {
		existing := f.do("GET", fmt.Sprintf(p, id), bobTok, nil)
		missing := f.do("GET", fmt.Sprintf(p, 999999), bobTok, nil)
		if existing.status() != http.StatusNotFound {
			t.Fatalf("GET %s as a stranger: status = %d, want 404; body = %s",
				fmt.Sprintf(p, id), existing.status(), existing.rec.Body.String())
		}
		if existing.status() != missing.status() {
			t.Fatalf("existing id answers %d and a missing one %d; the two must be identical",
				existing.status(), missing.status())
		}
		if body := existing.rec.Body.String(); strings.Contains(body, "alice") {
			t.Errorf("body = %s, want no mention of the owning namespace", body)
		}
	}

	// The same for the write verbs, which reached the identical loader.
	del := f.do("DELETE", fmt.Sprintf("/api/v1/webhooks/%d", id), bobTok, nil)
	if del.status() != http.StatusNotFound {
		t.Fatalf("DELETE as a stranger: status = %d, want 404", del.status())
	}
	// ...and the owner is unaffected.
	own := f.do("GET", fmt.Sprintf("/api/v1/webhooks/%d", id), aliceTok, nil)
	if own.status() != http.StatusOK {
		t.Fatalf("owner GET: status = %d, body = %s", own.status(), own.rec.Body.String())
	}
}

// ------------------------------------------------------------------- [E-5]

// A file the viewer cannot read, and a column that is not in the file, are
// both the request's problem rather than the server's. They used to come back
// as 500 internal_error, which reads as an outage, is retried by
// huggingface_hub's http_backoff, and files the caller's typo under
// slog.Error.
func TestParquetViewer_InputErrorsAreBadRequest(t *testing.T) {
	f := newURLParamFixture(t)
	r := f.repo("data")
	f.commit(r, "main", "add files", map[string][]byte{
		"rows.parquet": buildTestParquet(t),
		// Bytes that are not parquet at all, under a name the viewer accepts:
		// the same error path a truncated or corrupt file takes.
		"broken.parquet": []byte("this is plain text, not a parquet file"),
	})

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "unknown column",
			path: "/api/v1/parquet/model/alice/data/rows/main/rows.parquet?column=nope",
			want: "unknown column",
		},
		{
			name: "unknown column via the legacy list",
			path: "/api/v1/parquet/model/alice/data/rows/main/rows.parquet?columns=nope",
			want: "unknown column",
		},
		{
			name: "not a parquet file, schema",
			path: "/api/v1/parquet/model/alice/data/schema/main/broken.parquet",
			want: "not a readable parquet file",
		},
		{
			name: "not a parquet file, rows",
			path: "/api/v1/parquet/model/alice/data/rows/main/broken.parquet",
			want: "not a readable parquet file",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := f.get(tt.path, f.write)
			if resp.status() != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
			}
			body := resp.rec.Body.String()
			if !strings.Contains(body, tt.want) {
				t.Errorf("body = %s, want it to mention %q", body, tt.want)
			}
			// Never the object-storage key the bytes were read from: that is
			// where a file lives, not anything the caller asked about.
			if strings.Contains(body, "blobs/") {
				t.Errorf("body = %s, want no storage key in it", body)
			}
		})
	}

	// A column that *is* there still works, through both spellings.
	for _, path := range []string{
		"/api/v1/parquet/model/alice/data/rows/main/rows.parquet?column=label",
		"/api/v1/parquet/model/alice/data/rows/main/rows.parquet?columns=label",
	} {
		resp := f.get(path, f.write)
		if resp.status() != http.StatusOK {
			t.Fatalf("GET %s: status = %d, body = %s", path, resp.status(), resp.rec.Body.String())
		}
		if body := resp.rec.Body.String(); !strings.Contains(body, "label") {
			t.Errorf("body = %s, want the projected column in it", body)
		}
	}
}

// The column projection, which the comma-joined `columns=` spelling cannot
// express: "height,cm" is an ordinary column name for a parquet written from a
// CSV with that header, and splitting it asks for two columns that do not
// exist. Repeated `column=` keys carry the name through untouched.
func TestRequestedColumns(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"nothing means every column", "", nil},
		{"legacy comma-joined", "columns=a,b", []string{"a", "b"}},
		{"legacy trims spaces", "columns=a, b", []string{"a", "b"}},
		{"legacy drops empties", "columns=a,,b,", []string{"a", "b"}},
		{"repeated keys are taken literally", "column=height,cm&column=+age", []string{"height,cm", " age"}},
		{"one repeated key", "column=only", []string{"only"}},
		{"repeated keys win over the legacy list", "column=a&columns=b,c", []string{"a"}},
		{"empty repeated values fall back", "column=&columns=b", []string{"b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("parse query %q: %v", tt.query, err)
			}
			got := requestedColumns(q)
			if len(got) != len(tt.want) {
				t.Fatalf("requestedColumns(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("requestedColumns(%q) = %#v, want %#v", tt.query, got, tt.want)
				}
			}
		})
	}
}

// ------------------------------------------------------------------- [E-6]

// Run and metric names may contain commas -- a sweep names its runs after the
// parameters it varies, so "lr=0.1,bs=32" is the ordinary case -- and the
// comma-joined `runs=` spelling splits them into names that select nothing, or
// worse, into a fragment that matches some other run and draws a line nobody
// asked for.
func TestSelectedNames(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"nothing selected", "", nil},
		{"legacy comma-joined", "runs=a,b", []string{"a", "b"}},
		{"legacy trims", "runs= a , b ", []string{"a", "b"}},
		{"repeated keys keep commas", "run=lr%3D0.1,bs%3D32&run=baseline", []string{"lr=0.1,bs=32", "baseline"}},
		{"repeated keys win", "run=a&runs=b,c", []string{"a"}},
		{"empty repeated values fall back", "run=&runs=b", []string{"b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatalf("parse query %q: %v", tt.query, err)
			}
			got := selectedNames(q, "run", "runs")
			if len(got) != len(tt.want) {
				t.Fatalf("selectedNames(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("selectedNames(%q) = %#v, want %#v", tt.query, got, tt.want)
				}
			}
		})
	}
}

// ------------------------------------------------------------------ [E-13]

// whoami-v2 reports the name of the token in use, which is what `hf auth
// whoami` prints and the only way somebody holding several tokens can tell
// which one a client picked up. It used to answer the constant "thinkingface"
// for every caller.
func TestWhoami_ReportsTheTokenName(t *testing.T) {
	f := newSecFixture(t)
	alice := f.user("alice", "correct horse battery")
	tok, hash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if _, err := f.st.CreateToken(context.Background(), alice.ID, "laptop", "write", hash, nil); err != nil {
		t.Fatalf("create token: %v", err)
	}

	rec := f.do(secRequest{
		method: "GET", path: "/api/whoami-v2",
		headers: map[string]string{"Authorization": "Bearer " + tok},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Auth struct {
			AccessToken struct {
				DisplayName string `json:"displayName"`
				Role        string `json:"role"`
			} `json:"accessToken"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	if body.Auth.AccessToken.DisplayName != "laptop" {
		t.Errorf("displayName = %q, want the token's own name %q",
			body.Auth.AccessToken.DisplayName, "laptop")
	}
	if body.Auth.AccessToken.Role != "write" {
		t.Errorf("role = %q, want write", body.Auth.AccessToken.Role)
	}
}

// ------------------------------------------------------------------- [E-11]

// The HF tree endpoint answers a one-element array for a path that names a
// file, the way the Hub does. gitrepo.Tree only walks directories, so this
// used to be a 404 EntryNotFound for a file that plainly exists, and
// HfApi.list_repo_tree(path_in_repo=<a file>) raised instead of describing it.
func TestHFTree_FilePathReturnsOneEntry(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	repo := f.repo("alice", "weights", "model")
	f.writeFile(repo, "data/train.parquet", []byte("rows"))

	rec := f.do(secRequest{method: "GET", path: "/api/models/alice/weights/tree/main/data/train.parquet"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var entries []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
		OID  string `json:"oid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want exactly one", entries)
	}
	if entries[0].Path != "data/train.parquet" || entries[0].Type != "file" {
		t.Errorf("entry = %+v, want the file itself", entries[0])
	}
	if entries[0].Size != int64(len("rows")) || entries[0].OID == "" {
		t.Errorf("entry = %+v, want its real size and blob oid", entries[0])
	}
	// A path that really is missing is still a 404 with the header
	// huggingface_hub branches on.
	missing := f.do(secRequest{method: "GET", path: "/api/models/alice/weights/tree/main/data/nope.parquet"})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing path status = %d, want 404", missing.Code)
	}
	if got := missing.Header().Get("X-Error-Code"); got != "EntryNotFound" {
		t.Errorf("X-Error-Code = %q, want EntryNotFound", got)
	}
}

// ---------------------------------------------------------------- fixtures

// writeFileOnBranch commits one blob onto a branch other than the default,
// which is the only way to get a revision whose name contains a "/" -- the
// shape huggingface_hub percent-encodes and the redirect test needs.
func (f *secFixture) writeFileOnBranch(repo *store.Repo, branch, path string, data []byte) {
	f.t.Helper()
	g, err := f.git.Open(repo.StoragePath)
	if err != nil {
		f.t.Fatalf("open git: %v", err)
	}
	_, _, err = g.Commit(gitrepo.CommitRequest{
		Branch:  branch,
		Message: "add " + path,
		Author:  gitrepo.Signature{Name: "t", Email: "t@example.com", When: time.Now()},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: data}},
	})
	if err != nil {
		f.t.Fatalf("commit %s on %s: %v", path, branch, err)
	}
}
