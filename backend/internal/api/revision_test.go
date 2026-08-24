// Tests for revision resolution on the read-only HuggingFace-compatible
// endpoints: repo-info, tree and paths-info (repotree.go, via
// Server.revisionOrEmpty in refs.go).
//
// The point of every assertion here is the difference between "this repository
// has nothing in it" and "this revision does not exist". Both used to answer
// 200 with an empty body, which made HfApi.revision_exists(repo_id, "typo")
// return True, list_repo_files(revision="typo") return [], and
// snapshot_download(revision="typo") write a silent zero-file snapshot. The
// empty-repository half of that must stay a 200: create_repo -> repo_info is
// an ordinary huggingface_hub flow.
//
// Driven over real HTTP against a real Server, the same way refs_test.go and
// archive_test.go are.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

type revisionFixture struct {
	t     *testing.T
	s     *Server
	st    *store.Store
	git   *gitrepo.Manager
	alice *store.User
}

func newRevisionFixture(t *testing.T) *revisionFixture {
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

	f := &revisionFixture{t: t, s: srv, st: st, git: gitMgr}
	u, err := st.CreateUser(ctx, "alice", "alice@example.com", "hash", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	f.alice = u
	return f
}

// emptyRepo creates a repository whose git directory exists but holds no
// commits at all -- the state create_repo leaves behind.
func (f *revisionFixture) emptyRepo(ns, name string) *store.Repo {
	f.t.Helper()
	ctx := context.Background()
	n, err := f.st.GetNamespace(ctx, ns)
	if err != nil {
		f.t.Fatalf("namespace %s: %v", ns, err)
	}
	sp := store.NewStoragePath()
	r, err := f.st.CreateRepo(ctx, n.ID, name, "model", "desc", "main", sp)
	if err != nil {
		f.t.Fatalf("create repo %s/%s: %v", ns, name, err)
	}
	if err := f.git.Init(sp, "main"); err != nil {
		f.t.Fatalf("git init %s/%s: %v", ns, name, err)
	}
	return r
}

// repo creates a repository with one commit on main, so there is a revision
// for the "this one resolves" half of every test.
func (f *revisionFixture) repo(ns, name string) *store.Repo {
	f.t.Helper()
	r := f.emptyRepo(ns, name)
	f.commit(r, "main", "Add README")
	return r
}

func (f *revisionFixture) commit(r *store.Repo, branch, message string) plumbing.Hash {
	f.t.Helper()
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		f.t.Fatalf("open git repo: %v", err)
	}
	h, _, err := gitRepo.Commit(gitrepo.CommitRequest{
		Branch:  branch,
		Message: message,
		Author:  gitrepo.Signature{Name: "alice", Email: "alice@example.com", When: time.Now()},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: "README.md", Data: []byte(message)}},
	})
	if err != nil {
		f.t.Fatalf("commit: %v", err)
	}
	return h
}

func (f *revisionFixture) token(u *store.User, scope string) string {
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

func (f *revisionFixture) do(method, path, token string, body any) response {
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

// readsOf names the three read endpoints for one revision, so each test can
// walk all of them and no case silently covers only one.
func readsOf(rev string) []struct {
	name   string
	method string
	path   string
	body   any
} {
	base := "/api/models/alice/foo"
	return []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"repo-info", "GET", base + "/revision/" + rev, nil},
		{"tree", "GET", base + "/tree/" + rev, nil},
		{"paths-info", "POST", base + "/paths-info/" + rev, map[string]any{"paths": []string{"README.md"}}},
	}
}

// ------------------------------------------------------- unknown revisions

// A revision that is not in a repository that *has* commits is a 404 carrying
// X-Error-Code: RevisionNotFound -- the header is what makes huggingface_hub
// raise RevisionNotFoundError instead of a bare HfHubHTTPError.
func TestHFReadEndpoints_UnknownRevisionIsRevisionNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	for _, tc := range readsOf("does-not-exist") {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, "", tc.body)
			if resp.status() != 404 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
				t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
			}
		})
	}
}

// A hash-shaped revision that names no object is just as unknown as a typo'd
// branch name; it must not fall through to an empty listing either.
func TestHFReadEndpoints_UnknownCommitSHAIsRevisionNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	missing := "0123456789012345678901234567890123456789"
	for _, tc := range readsOf(missing) {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, "", tc.body)
			if resp.status() != 404 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
				t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
			}
		})
	}
}

// ---------------------------------------------------------- known revisions

// Regression guard: the branch, both flavours of tag, and a full commit SHA
// all still resolve, and repo-info reports the commit each one points at.
func TestHFReadEndpoints_KnownRevisionsStillResolve(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.repo("alice", "foo")
	head := f.commit(repo, "main", "Second commit")
	tok := f.token(f.alice, "write")

	// A lightweight tag and an annotated one (a message makes create_tag
	// write a real tag object, which Resolve has to peel).
	for _, tag := range []struct{ name, message string }{
		{"v1-light", ""},
		{"v1-annotated", "release 1"},
	} {
		body := map[string]any{"tag": tag.name}
		if tag.message != "" {
			body["message"] = tag.message
		}
		resp := f.do("POST", "/api/models/alice/foo/tag/main", tok, body)
		if resp.status() != 201 {
			t.Fatalf("create tag %s: status = %d, body = %s", tag.name, resp.status(), resp.rec.Body.String())
		}
	}

	for _, rev := range []string{"main", "v1-light", "v1-annotated", head.String()} {
		for _, tc := range readsOf(rev) {
			t.Run(rev+"/"+tc.name, func(t *testing.T) {
				resp := f.do(tc.method, tc.path, "", tc.body)
				if resp.status() != 200 {
					t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
				}
			})
		}
		t.Run(rev+"/sha", func(t *testing.T) {
			var body struct {
				SHA      string `json:"sha"`
				Siblings []struct {
					RFilename string `json:"rfilename"`
				} `json:"siblings"`
			}
			f.do("GET", "/api/models/alice/foo/revision/"+rev, "", nil).json(t, &body)
			if body.SHA != head.String() {
				t.Fatalf("sha = %q, want %q", body.SHA, head.String())
			}
			if len(body.Siblings) != 1 || body.Siblings[0].RFilename != "README.md" {
				t.Fatalf("siblings = %+v, want just README.md", body.Siblings)
			}
		})
	}
}

// repo-info with no revision in the path keeps falling back to the default
// branch rather than 404-ing on the empty rev parameter.
func TestHFRepoInfo_NoRevisionUsesTheDefaultBranch(t *testing.T) {
	f := newRevisionFixture(t)
	repo := f.repo("alice", "foo")
	head := f.commit(repo, "main", "Second commit")

	resp := f.do("GET", "/api/models/alice/foo", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body struct {
		SHA string `json:"sha"`
	}
	resp.json(t, &body)
	if body.SHA != head.String() {
		t.Fatalf("sha = %q, want %q", body.SHA, head.String())
	}
}

// A path that is missing on a revision that *does* resolve stays
// EntryNotFound: HfFileSystem.glob -- and so datasets.push_to_hub, which globs
// data/* before uploading -- depends on that header, and it must not be
// swallowed by the new revision check.
func TestHFTree_MissingPathOnAKnownRevisionIsEntryNotFound(t *testing.T) {
	f := newRevisionFixture(t)
	f.repo("alice", "foo")

	resp := f.do("GET", "/api/models/alice/foo/tree/main/no-such-dir", "", nil)
	if resp.status() != 404 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "EntryNotFound" {
		t.Fatalf("X-Error-Code = %q, want EntryNotFound", got)
	}
}

// -------------------------------------------------------- empty repository

// The compatibility floor: a repository with no commits answers 200 with
// nothing in it on all three endpoints. create_repo followed by repo_info is
// an ordinary flow, and a 404 there would break it.
func TestHFReadEndpoints_EmptyRepositoryAnswers200(t *testing.T) {
	f := newRevisionFixture(t)
	f.emptyRepo("alice", "foo")

	t.Run("repo-info without a revision", func(t *testing.T) {
		resp := f.do("GET", "/api/models/alice/foo", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var body struct {
			SHA      string `json:"sha"`
			Siblings []any  `json:"siblings"`
		}
		resp.json(t, &body)
		if body.SHA != "" {
			t.Fatalf("sha = %q, want the empty string", body.SHA)
		}
		if len(body.Siblings) != 0 {
			t.Fatalf("siblings = %+v, want none", body.Siblings)
		}
	})

	// The default branch is unborn, so naming it explicitly must behave the
	// same as naming nothing -- this is the case huggingface_hub actually
	// sends, since it fills the revision in for the caller.
	t.Run("repo-info at the unborn default branch", func(t *testing.T) {
		resp := f.do("GET", "/api/models/alice/foo/revision/main", "", nil)
		if resp.status() != 200 {
			t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
		}
		var body struct {
			SHA      string `json:"sha"`
			Siblings []any  `json:"siblings"`
		}
		resp.json(t, &body)
		if body.SHA != "" || len(body.Siblings) != 0 {
			t.Fatalf("body = %+v, want an empty sha and no siblings", body)
		}
	})

	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"tree", "GET", "/api/models/alice/foo/tree/main", nil},
		{"paths-info", "POST", "/api/models/alice/foo/paths-info/main",
			map[string]any{"paths": []string{"README.md"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, "", tc.body)
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			var entries []any
			resp.json(t, &entries)
			if len(entries) != 0 {
				t.Fatalf("entries = %+v, want none", entries)
			}
		})
	}
}
