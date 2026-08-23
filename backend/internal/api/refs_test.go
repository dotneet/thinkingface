// Tests for the HuggingFace-compatible branch / tag / commit-list endpoints
// (refs.go), driven over real HTTP against a real Server the way
// archive_test.go and transfers_test.go are.
//
// The status codes asserted here are not arbitrary: huggingface_hub decides
// what `exist_ok=True` swallows by looking at the number (409), and what kind
// of exception to raise from the X-Error-Code header, so each one is a
// compatibility contract rather than a style choice.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/wal"
)

// recordingEnqueuer captures the sync jobs a handler schedules, so a test can
// assert that creating a branch refreshes that ref's file index -- the index is
// keyed by (repo_id, ref, path), so a branch with no job has no index at all.
type recordingEnqueuer struct {
	jobs []syncJob
}

type syncJob struct {
	repoID         int64
	ref            string
	oldSHA, newSHA string
}

func (e *recordingEnqueuer) Enqueue(_ context.Context, repoID int64, ref, oldSHA, newSHA string) error {
	e.jobs = append(e.jobs, syncJob{repoID: repoID, ref: ref, oldSHA: oldSHA, newSHA: newSHA})
	return nil
}

type refsFixture struct {
	t     *testing.T
	s     *Server
	st    *store.Store
	git   *gitrepo.Manager
	sync  *recordingEnqueuer
	alice *store.User
	bob   *store.User
}

func newRefsFixture(t *testing.T) *refsFixture {
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
	enq := &recordingEnqueuer{}
	cfg := &config.Config{
		PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret",
	}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  newMemStore(),
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   enq,
	})

	f := &refsFixture{t: t, s: srv, st: st, git: gitMgr, sync: enq}
	f.alice = f.user("alice")
	f.bob = f.user("bob")
	return f
}

func (f *refsFixture) user(name string) *store.User {
	f.t.Helper()
	u, err := f.st.CreateUser(context.Background(), name, name+"@example.com", "hash", false)
	if err != nil {
		f.t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

// repo creates a repository with one commit on main, so there is something for
// a branch or a tag to point at.
func (f *refsFixture) repo(ns, name, kind string) *store.Repo {
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
	f.commit(r, "main", "Add README\n\nThe long form of the story.")
	return r
}

func (f *refsFixture) commit(r *store.Repo, branch, message string) plumbing.Hash {
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

func (f *refsFixture) token(u *store.User, scope string) string {
	f.t.Helper()
	tok, hash, err := auth.NewToken()
	if err != nil {
		f.t.Fatalf("new token: %v", err)
	}
	if _, err := f.st.CreateToken(context.Background(), u.ID, "test", scope, hash); err != nil {
		f.t.Fatalf("create token: %v", err)
	}
	return tok
}

func (f *refsFixture) do(method, path, token string, body any) response {
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

// refs reads the HF refs endpoint, which is how a test asserts what actually
// ended up in the repository.
func (f *refsFixture) refs(kind, ns, name string) (branches, tags map[string]string) {
	f.t.Helper()
	resp := f.do("GET", "/api/"+kind+"s/"+ns+"/"+name+"/refs", "", nil)
	if resp.status() != 200 {
		f.t.Fatalf("refs status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body struct {
		Branches []struct{ Name, TargetCommit string } `json:"branches"`
		Tags     []struct{ Name, TargetCommit string } `json:"tags"`
	}
	resp.json(f.t, &body)
	branches, tags = map[string]string{}, map[string]string{}
	for _, b := range body.Branches {
		branches[b.Name] = b.TargetCommit
	}
	for _, tg := range body.Tags {
		tags[tg.Name] = tg.TargetCommit
	}
	return branches, tags
}

// ------------------------------------------------------------- create branch

func TestHFCreateBranch_DefaultsToTheDefaultBranchAndSchedulesIndexing(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	// An empty body is what create_branch sends without a revision.
	resp := f.do("POST", "/api/models/alice/foo/branch/experiment", tok, map[string]any{})
	if resp.status() != 201 {
		t.Fatalf("create status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	branches, _ := f.refs("model", "alice", "foo")
	if branches["experiment"] == "" || branches["experiment"] != branches["main"] {
		t.Fatalf("branches = %v, want experiment at main's tip", branches)
	}

	// The file index is per-ref, so a new branch must schedule its own job.
	if len(f.sync.jobs) != 1 {
		t.Fatalf("sync jobs = %+v, want exactly one", f.sync.jobs)
	}
	job := f.sync.jobs[0]
	if job.repoID != repo.ID || job.ref != "experiment" || job.oldSHA != "" ||
		job.newSHA != branches["experiment"] {
		t.Fatalf("sync job = %+v, want the new branch at its tip", job)
	}
}

func TestHFCreateBranch_FromAnExplicitStartingPoint(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	branches, _ := f.refs("model", "alice", "foo")
	initial := branches["main"]

	second := f.commit(repo, "main", "Second commit")
	if second.String() == initial {
		t.Fatalf("the second commit did not move main")
	}

	resp := f.do("POST", "/api/models/alice/foo/branch/from-initial", tok,
		map[string]any{"startingPoint": initial})
	if resp.status() != 201 {
		t.Fatalf("create status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	branches, _ = f.refs("model", "alice", "foo")
	if branches["from-initial"] != initial {
		t.Fatalf("from-initial = %s, want the initial commit %s", branches["from-initial"], initial)
	}
}

// A slashed branch name arrives percent-encoded, because huggingface_hub
// quotes it with safe="" -- and chi routes on the escaped path.
func TestHFCreateBranch_PercentEncodedName(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/models/alice/foo/branch/feature%2Fnew-tokenizer", tok, map[string]any{})
	if resp.status() != 201 {
		t.Fatalf("create status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	branches, _ := f.refs("model", "alice", "foo")
	if _, ok := branches["feature/new-tokenizer"]; !ok {
		t.Fatalf("branches = %v, want the decoded name feature/new-tokenizer", branches)
	}
}

// 409 exactly: it is the status create_branch(exist_ok=True) swallows.
func TestHFCreateBranch_DuplicateIs409(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	if got := f.do("POST", "/api/models/alice/foo/branch/dup", tok, map[string]any{}).status(); got != 201 {
		t.Fatalf("first create status = %d", got)
	}
	resp := f.do("POST", "/api/models/alice/foo/branch/dup", tok, map[string]any{})
	if resp.status() != 409 {
		t.Fatalf("duplicate create status = %d, want 409 (exist_ok relies on it)", resp.status())
	}
}

func TestHFCreateBranch_UnknownStartingPointIs404WithRevisionNotFound(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/models/alice/foo/branch/nope", tok,
		map[string]any{"startingPoint": "no-such-revision"})
	if resp.status() != 404 {
		t.Fatalf("status = %d, want 404", resp.status())
	}
	// Without this header huggingface_hub raises a generic HfHubHTTPError
	// instead of RevisionNotFoundError.
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

func TestHFCreateBranch_RejectsInvalidNames(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	// Each of these would be a path under refs/heads/ if it were let through.
	for _, name := range []string{"a..b", "bad%20name", "HEAD", "main.lock", "..%2F..%2Fconfig"} {
		resp := f.do("POST", "/api/models/alice/foo/branch/"+name, tok, map[string]any{})
		if resp.status() != 400 {
			t.Fatalf("create %q status = %d, want 400 (body %s)", name, resp.status(), resp.rec.Body.String())
		}
	}
	branches, _ := f.refs("model", "alice", "foo")
	if len(branches) != 1 {
		t.Fatalf("branches = %v, want main alone", branches)
	}
}

func TestHFCreateBranch_RejectsUnauthorizedCallers(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"anonymous", "", 401},
		{"another user", f.token(f.bob, "write"), 403},
		{"read-only token", f.token(f.alice, "read"), 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("POST", "/api/models/alice/foo/branch/sneaky", tc.token, map[string]any{})
			if resp.status() != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", resp.status(), tc.want, resp.rec.Body.String())
			}
		})
	}
	branches, _ := f.refs("model", "alice", "foo")
	if len(branches) != 1 {
		t.Fatalf("branches = %v, want main alone", branches)
	}
}

// Archiving is a single switch that must stop every write path, this one
// included (docs/dev/api-contract.md §2 "archiving").
func TestHFRefWrites_RefusedOnAnArchivedRepository(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	// Make a branch and a tag first, so the delete cases have real targets.
	if got := f.do("POST", "/api/models/alice/foo/branch/topic", tok, map[string]any{}).status(); got != 201 {
		t.Fatalf("create branch status = %d", got)
	}
	if got := f.do("POST", "/api/models/alice/foo/tag/main", tok, map[string]any{"tag": "v1.0"}).status(); got != 201 {
		t.Fatalf("create tag status = %d", got)
	}
	if got := f.do("POST", "/api/v1/repos/model/alice/foo/archive", tok, nil).status(); got != 200 {
		t.Fatalf("archive status = %d", got)
	}

	cases := []struct{ method, path string }{
		{"POST", "/api/models/alice/foo/branch/other"},
		{"DELETE", "/api/models/alice/foo/branch/topic"},
		{"POST", "/api/models/alice/foo/tag/main"},
		{"DELETE", "/api/models/alice/foo/tag/v1.0"},
	}
	for _, tc := range cases {
		resp := f.do(tc.method, tc.path, tok, map[string]any{"tag": "v2.0"})
		if resp.status() != 403 {
			t.Fatalf("%s %s status = %d, want 403", tc.method, tc.path, resp.status())
		}
		if got := errorType(t, resp); got != "repository_archived" {
			t.Fatalf("%s %s error type = %q, want repository_archived", tc.method, tc.path, got)
		}
	}

	// Reads keep working on an archive.
	if got := f.do("GET", "/api/models/alice/foo/commits/main", "", nil).status(); got != 200 {
		t.Fatalf("commits on an archived repository = %d, want 200", got)
	}
}

// ------------------------------------------------------------- delete branch

func TestHFDeleteBranch_RemovesTheRefAndReportsMissingOnes(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	if got := f.do("POST", "/api/models/alice/foo/branch/topic", tok, map[string]any{}).status(); got != 201 {
		t.Fatalf("create status = %d", got)
	}
	if got := f.do("DELETE", "/api/models/alice/foo/branch/topic", tok, nil).status(); got != 200 {
		t.Fatalf("delete status = %d", got)
	}
	branches, _ := f.refs("model", "alice", "foo")
	if _, ok := branches["topic"]; ok {
		t.Fatalf("branches = %v, want topic gone", branches)
	}

	resp := f.do("DELETE", "/api/models/alice/foo/branch/topic", tok, nil)
	if resp.status() != 404 {
		t.Fatalf("delete of a missing branch = %d, want 404", resp.status())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

// The default branch is what HEAD, the metadata index and every revision-less
// read depend on, so it is not deletable at all.
func TestHFDeleteBranch_RefusesTheDefaultBranch(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("DELETE", "/api/models/alice/foo/branch/main", tok, nil)
	if resp.status() != 409 {
		t.Fatalf("status = %d, want 409 (body %s)", resp.status(), resp.rec.Body.String())
	}
	branches, _ := f.refs("model", "alice", "foo")
	if _, ok := branches["main"]; !ok {
		t.Fatalf("branches = %v, want main still there", branches)
	}
}

// --------------------------------------------------------------------- tags

func TestHFCreateTag_LightweightAndAnnotated(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "dataset")
	tok := f.token(f.alice, "write")

	branches, _ := f.refs("dataset", "alice", "foo")
	head := branches["main"]

	if got := f.do("POST", "/api/datasets/alice/foo/tag/main", tok,
		map[string]any{"tag": "v1.0"}).status(); got != 201 {
		t.Fatalf("create lightweight tag status = %d", got)
	}
	if got := f.do("POST", "/api/datasets/alice/foo/tag/main", tok,
		map[string]any{"tag": "v1.1", "message": "the second release"}).status(); got != 201 {
		t.Fatalf("create annotated tag status = %d", got)
	}

	_, tags := f.refs("dataset", "alice", "foo")
	if tags["v1.0"] != head {
		t.Fatalf("v1.0 = %s, want the commit %s", tags["v1.0"], head)
	}
	// An annotated tag's ref names the tag object, exactly as git does.
	if tags["v1.1"] == "" || tags["v1.1"] == head {
		t.Fatalf("v1.1 = %s, want a tag object distinct from the commit", tags["v1.1"])
	}
	// ...but it still resolves to the tagged commit everywhere a revision is
	// accepted.
	resp := f.do("GET", "/api/datasets/alice/foo/revision/v1.1", "", nil)
	if resp.status() != 200 {
		t.Fatalf("revision status = %d", resp.status())
	}
	var info struct {
		SHA string `json:"sha"`
	}
	resp.json(t, &info)
	if info.SHA != head {
		t.Fatalf("revision v1.1 sha = %s, want the tagged commit %s", info.SHA, head)
	}

	// Tags schedule no indexing, the same as `git push v1.0` does not.
	if len(f.sync.jobs) != 0 {
		t.Fatalf("sync jobs = %+v, want none for tags", f.sync.jobs)
	}
}

func TestHFCreateTag_DuplicateIs409AndBadNamesAre400(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	if got := f.do("POST", "/api/models/alice/foo/tag/main", tok,
		map[string]any{"tag": "v1.0"}).status(); got != 201 {
		t.Fatalf("first create status = %d", got)
	}
	resp := f.do("POST", "/api/models/alice/foo/tag/main", tok, map[string]any{"tag": "v1.0"})
	if resp.status() != 409 {
		t.Fatalf("duplicate tag status = %d, want 409 (exist_ok relies on it)", resp.status())
	}

	for _, name := range []string{"", "a..b", "v1 0", "../escape"} {
		got := f.do("POST", "/api/models/alice/foo/tag/main", tok, map[string]any{"tag": name}).status()
		if got != 400 {
			t.Fatalf("tag %q status = %d, want 400", name, got)
		}
	}
}

func TestHFCreateTag_UnknownRevisionIs404(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/models/alice/foo/tag/no-such-rev", tok, map[string]any{"tag": "v1.0"})
	if resp.status() != 404 {
		t.Fatalf("status = %d, want 404", resp.status())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

func TestHFDeleteTag(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	if got := f.do("POST", "/api/models/alice/foo/tag/main", tok,
		map[string]any{"tag": "v1.0"}).status(); got != 201 {
		t.Fatalf("create status = %d", got)
	}
	if got := f.do("DELETE", "/api/models/alice/foo/tag/v1.0", tok, nil).status(); got != 200 {
		t.Fatalf("delete status = %d", got)
	}
	_, tags := f.refs("model", "alice", "foo")
	if len(tags) != 0 {
		t.Fatalf("tags = %v, want none", tags)
	}

	resp := f.do("DELETE", "/api/models/alice/foo/tag/v1.0", tok, nil)
	if resp.status() != 404 {
		t.Fatalf("delete of a missing tag = %d, want 404", resp.status())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

// ------------------------------------------------------------------ commits

func TestHFCommits_ShapeMatchesGitCommitInfo(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")

	// A read, so no token at all.
	resp := f.do("GET", "/api/models/alice/foo/commits/main", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var commits []struct {
		ID      string `json:"id"`
		Authors []struct {
			User string `json:"user"`
		} `json:"authors"`
		Date    string `json:"date"`
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	resp.json(t, &commits)
	if len(commits) != 1 {
		t.Fatalf("len(commits) = %d, want 1", len(commits))
	}
	c := commits[0]
	if len(c.ID) != 40 {
		t.Fatalf("id = %q, want a full commit hash", c.ID)
	}
	// huggingface_hub reads author["user"] out of each element, so an author
	// must be an object and not a bare string.
	if len(c.Authors) != 1 || c.Authors[0].User != "alice" {
		t.Fatalf("authors = %+v, want one author object naming alice", c.Authors)
	}
	if c.Title != "Add README" || c.Message != "The long form of the story." {
		t.Fatalf("title/message = %q / %q, want the subject and body split", c.Title, c.Message)
	}
	// parse_datetime accepts "%Y-%m-%dT%H:%M:%S.%fZ" and nothing else -- a
	// "+00:00" offset raises ValueError there.
	if !strings.HasSuffix(c.Date, "Z") || !strings.Contains(c.Date, ".") {
		t.Fatalf("date = %q, want an ISO-8601 instant ending in Z with fractional seconds", c.Date)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", c.Date); err != nil {
		t.Fatalf("date %q is not in the format huggingface_hub parses: %v", c.Date, err)
	}
}

func TestHFCommits_PagesWithALinkHeader(t *testing.T) {
	f := newRefsFixture(t)
	repo := f.repo("alice", "foo", "model")
	for i := 0; i < 3; i++ {
		f.commit(repo, "main", "commit "+string(rune('a'+i)))
	}

	resp := f.do("GET", "/api/models/alice/foo/commits/main?limit=2", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d", resp.status())
	}
	var page []struct {
		ID string `json:"id"`
	}
	resp.json(t, &page)
	if len(page) != 2 {
		t.Fatalf("len(page) = %d, want 2", len(page))
	}
	// huggingface_hub's paginate() follows response.links["next"]["url"]
	// verbatim, so the URL has to be absolute.
	link := resp.rec.Header().Get("Link")
	if !strings.HasPrefix(link, "<http://test.local/api/models/alice/foo/commits/main?") ||
		!strings.HasSuffix(link, `>; rel="next"`) {
		t.Fatalf("Link = %q, want an absolute next-page URL", link)
	}
	if !strings.Contains(link, "after=") {
		t.Fatalf("Link = %q, want an after cursor", link)
	}

	// The last page carries no Link header, which is what ends the walk.
	last := f.do("GET", "/api/models/alice/foo/commits/main?limit=100", "", nil)
	if got := last.rec.Header().Get("Link"); got != "" {
		t.Fatalf("Link on the final page = %q, want empty", got)
	}
}

func TestHFCommits_UnknownRevisionIs404(t *testing.T) {
	f := newRefsFixture(t)
	f.repo("alice", "foo", "model")

	resp := f.do("GET", "/api/models/alice/foo/commits/no-such-rev", "", nil)
	if resp.status() != 404 {
		t.Fatalf("status = %d, want 404", resp.status())
	}
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RevisionNotFound" {
		t.Fatalf("X-Error-Code = %q, want RevisionNotFound", got)
	}
}

// ------------------------------------------------------- ref writes and the WAL

// A ref created through the API must land in the WAL index, not only on disk.
// In authoritative mode the index is what a fresh instance rebuilds refs from
// (docs/dev/continuity-design.md §9), so a branch missing from it simply
// disappears the next time the local copy is materialised.
func TestCreateRefThroughWAL_AuthoritativeRecordsTheRefInTheIndex(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	head, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	if err := s.createRefThroughWAL(context.Background(), repo, gitrepo.BranchRef("topic"), head); err != nil {
		t.Fatalf("createRefThroughWAL: %v", err)
	}
	ix, _, err := wal.ReadIndex(context.Background(), st, repo.StoragePath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if ix.Refs["refs/heads/topic"] != head.String() {
		t.Fatalf("index refs = %v, want refs/heads/topic at %s", ix.Refs, head)
	}
	// The ref points at history the WAL already carries, so the update needs
	// no entry pack at all -- only the index revision.
	gitRepo, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, _ := gitRepo.RefTarget(gitrepo.BranchRef("topic")); got != head {
		t.Fatalf("on-disk topic = %s, want %s", got, head)
	}
}

func TestDeleteRefThroughWAL_AuthoritativeRemovesTheRefFromTheIndex(t *testing.T) {
	s, st, repo, _ := newWALCommitFixture(t, "authoritative")
	head, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if err := s.createRefThroughWAL(context.Background(), repo, gitrepo.TagRef("v1.0"), head); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.deleteRefThroughWAL(context.Background(), repo, gitrepo.TagRef("v1.0")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	ix, _, err := wal.ReadIndex(context.Background(), st, repo.StoragePath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if _, ok := ix.Refs["refs/tags/v1.0"]; ok {
		t.Fatalf("index refs = %v, want refs/tags/v1.0 gone", ix.Refs)
	}
}

// The rollback rule commitThroughWAL follows applies here for the same reason:
// a ref the WAL refused must not survive on disk, or this instance serves a
// branch no other instance will ever see and no materialisation will repair.
func TestCreateRefThroughWAL_UnreachableWALLeavesNoLocalRef(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	head, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	st.failPut = true
	err = s.createRefThroughWAL(context.Background(), repo, gitrepo.BranchRef("ghost"), head)
	if err == nil {
		t.Fatal("ref creation with the WAL down must fail")
	}
	if errors.Is(err, errWALConflict) {
		t.Fatalf("an outage must not masquerade as a conflict: %v", err)
	}
	gitRepo, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	branches, err := gitRepo.Branches()
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	for _, b := range branches {
		if b == "ghost" {
			t.Fatalf("branches = %v, want the rejected ref rolled back", branches)
		}
	}

	// And the name is free again once the WAL is back.
	st.failPut = false
	if err := s.createRefThroughWAL(context.Background(), repo, gitrepo.BranchRef("ghost"), head); err != nil {
		t.Fatalf("create after recovery: %v", err)
	}
}

func TestDeleteRefThroughWAL_UnreachableWALRestoresTheLocalRef(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "authoritative")
	head, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if err := s.createRefThroughWAL(context.Background(), repo, gitrepo.TagRef("v1.0"), head); err != nil {
		t.Fatalf("create: %v", err)
	}

	st.failPut = true
	if _, err := s.deleteRefThroughWAL(context.Background(), repo, gitrepo.TagRef("v1.0")); err == nil {
		t.Fatal("ref deletion with the WAL down must fail")
	}
	gitRepo, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, err := gitRepo.RefTarget(gitrepo.TagRef("v1.0")); err != nil || got != head {
		t.Fatalf("refs/tags/v1.0 = %s (%v), want it restored at %s", got, err, head)
	}
}

// A lost CAS is a genuine conflict for a ref write -- unlike a commit, there is
// nothing to rebuild -- so it must surface as one rather than be retried.
func TestCreateRefThroughWAL_ConcurrentCreationIsAConflict(t *testing.T) {
	s, st, repo, _ := newWALCommitFixture(t, "authoritative")
	head, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	// Another instance creates the same ref first.
	ix, gen, err := wal.ReadIndex(context.Background(), st, repo.StoragePath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	ix.Refs["refs/heads/topic"] = strings.Repeat("b", 40)
	if _, err := wal.PutIndex(context.Background(), st, repo.StoragePath, gen, ix); err != nil {
		t.Fatalf("put index: %v", err)
	}

	err = s.createRefThroughWAL(context.Background(), repo, gitrepo.BranchRef("topic"), head)
	if !errors.Is(err, errWALConflict) {
		t.Fatalf("err = %v, want errWALConflict", err)
	}
}

func TestRefWritesThroughWAL_OffModeNeverTouchesStorage(t *testing.T) {
	s, st, repo, _ := newWALCommitFixture(t, "off")
	head, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	st.failPut = true // would fail loudly if anything wrote
	if err := s.createRefThroughWAL(context.Background(), repo, gitrepo.BranchRef("topic"), head); err != nil {
		t.Fatalf("off mode create: %v", err)
	}
	if _, err := s.deleteRefThroughWAL(context.Background(), repo, gitrepo.BranchRef("topic")); err != nil {
		t.Fatalf("off mode delete: %v", err)
	}
}

// Shadow mode keeps disk authoritative: the WAL is a follower, so its failure
// is logged and the ref is still created.
func TestCreateRefThroughWAL_ShadowFailureStillCreatesTheRef(t *testing.T) {
	s, st, repo, git := newWALCommitFixture(t, "shadow")
	head, _, err := s.commitThroughWAL(context.Background(), repo, commitOps("one"), true)
	if err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	st.failPut = true
	if err := s.createRefThroughWAL(context.Background(), repo, gitrepo.BranchRef("topic"), head); err != nil {
		t.Fatalf("shadow mode must never surface WAL failures: %v", err)
	}
	gitRepo, err := git.Open(repo.StoragePath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, _ := gitRepo.RefTarget(gitrepo.BranchRef("topic")); got != head {
		t.Fatalf("on-disk topic = %s, want %s", got, head)
	}
}
