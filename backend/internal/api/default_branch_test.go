// Tests for PATCH /api/v1/repos/{kind}/{ns}/{name}, which today only
// switches default_branch (docs/dev/api-contract.md "Changing the default
// branch"). Driven over real HTTP against a real Server, the same way
// archive_test.go and transfers_test.go do; the on-disk HEAD-symref switch
// itself is covered at the gitrepo level by
// TestRepo_SetHead_RepointsSymbolicRef (internal/gitrepo/repo_test.go).

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// enqueueCall is one recorded Enqueue invocation.
type enqueueCall struct {
	RepoID              int64
	Ref, OldSHA, NewSHA string
}

// recordingEnqueuer captures every Enqueue call instead of running a real
// sync worker, so a test can assert whether -- and with what ref/old/new -- a
// reindex was requested. It is the api package's one sync-job recorder;
// refs_test.go asserts against it too.
type recordingEnqueuer struct {
	mu    sync.Mutex
	calls []enqueueCall
	// fail, when set, is returned by every Enqueue after the call is
	// recorded: the queue write failing on a request whose other writes
	// already landed, which is the case the handler has to leave consistent
	// rather than half-undone.
	fail error
}

func (e *recordingEnqueuer) Enqueue(_ context.Context, repoID int64, ref, oldSHA, newSHA string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, enqueueCall{RepoID: repoID, Ref: ref, OldSHA: oldSHA, NewSHA: newSHA})
	return e.fail
}

func (e *recordingEnqueuer) snapshot() []enqueueCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]enqueueCall(nil), e.calls...)
}

// defaultBranchFixture is archiveFixture's sibling, swapping the no-op
// syncer for recordingEnqueuer so the reindex side effect can be asserted.
type defaultBranchFixture struct {
	t    *testing.T
	s    *Server
	st   *store.Store
	git  *gitrepo.Manager
	sync *recordingEnqueuer

	admin *store.User
	alice *store.User
	bob   *store.User
}

func newDefaultBranchFixture(t *testing.T) *defaultBranchFixture {
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
	cfg := &config.Config{PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret"}
	sync := &recordingEnqueuer{}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  newMemStore(),
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   sync,
	})

	f := &defaultBranchFixture{t: t, s: srv, st: st, git: gitMgr, sync: sync}
	f.admin = f.user("siteadmin", true)
	f.alice = f.user("alice", false)
	f.bob = f.user("bob", false)
	return f
}

func (f *defaultBranchFixture) user(name string, isAdmin bool) *store.User {
	f.t.Helper()
	u, err := f.st.CreateUser(context.Background(), name, name+"@example.com", "hash", isAdmin)
	if err != nil {
		f.t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func (f *defaultBranchFixture) repo(ns, name, kind string) *store.Repo {
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
	return r
}

// commit writes one file directly to the repository's git storage on
// branch, bypassing the HTTP commit/edit endpoints, and returns the new
// commit hash as a string.
func (f *defaultBranchFixture) commit(r *store.Repo, branch, path, content string) string {
	f.t.Helper()
	g, err := f.git.Open(r.StoragePath)
	if err != nil {
		f.t.Fatalf("open git repo: %v", err)
	}
	h, _, err := g.Commit(gitrepo.CommitRequest{
		Branch:  branch,
		Message: "add " + path,
		Author:  gitrepo.Signature{Name: "tester", Email: "tester@example.com"},
		Ops:     []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: []byte(content)}},
	})
	if err != nil {
		f.t.Fatalf("commit %s on %s: %v", path, branch, err)
	}
	return h.String()
}

func (f *defaultBranchFixture) addOrgMember(namespaceID, userID int64, role string) {
	f.t.Helper()
	if _, err := f.st.AddOrgMember(context.Background(), namespaceID, userID, role, f.admin.ID); err != nil {
		f.t.Fatalf("add org member: %v", err)
	}
}

func (f *defaultBranchFixture) token(u *store.User, scope string) string {
	f.t.Helper()
	tok, hash, err := auth.NewToken()
	if err != nil {
		f.t.Fatalf("new token: %v", err)
	}
	// nil expiry: these fixtures care about scope, not about the token
	// ageing out mid-test.
	if _, err := f.st.CreateToken(context.Background(), u.ID, "test", scope, hash, nil); err != nil {
		f.t.Fatalf("create token: %v", err)
	}
	return tok
}

func (f *defaultBranchFixture) do(method, path, token string, body any) response {
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

func (f *defaultBranchFixture) patch(kind, ns, name, token string, req apitypes.RepoUpdateRequest) response {
	f.t.Helper()
	return f.do("PATCH", "/api/v1/repos/"+kind+"/"+ns+"/"+name, token, req)
}

func strPtr(s string) *string { return &s }

// ------------------------------------------------------------------- happy

func TestUpdateRepoDefaultBranch_SwitchesAndReindexesTargetRef(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	releaseTip := f.commit(r, "release", "VERSION", "1.0")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if body.Repo.DefaultBranch != "release" {
		t.Fatalf("default_branch = %q, want release", body.Repo.DefaultBranch)
	}

	// The row itself agrees, independent of what buildDetail chose to render.
	stored, err := f.st.GetRepoByID(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if stored.DefaultBranch != "release" {
		t.Fatalf("stored default_branch = %q, want release", stored.DefaultBranch)
	}

	// A reindex of the new default branch was queued, old==new==its current
	// tip: nothing was actually pushed, so a downstream repo.push consumer
	// can tell this apart from a real push on the same ref.
	calls := f.sync.snapshot()
	if len(calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.RepoID != r.ID || got.Ref != "release" || got.OldSHA != releaseTip || got.NewSHA != releaseTip {
		t.Fatalf("enqueue call = %+v, want {RepoID:%d Ref:release OldSHA:%s NewSHA:%s}", got, r.ID, releaseTip, releaseTip)
	}
}

func TestUpdateRepoDefaultBranch_SameValueStillQueuesTheReindex(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{DefaultBranch: strPtr("main")})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if body.Repo.DefaultBranch != "main" {
		t.Fatalf("default_branch = %q, want main", body.Repo.DefaultBranch)
	}
	// Setting the branch that is already the default writes nothing, but it
	// does queue the reindex -- that is what makes retrying the request a
	// repair after an earlier attempt updated the row and then failed to
	// queue the job. Skipping it here would make such a retry a silent no-op.
	calls := f.sync.snapshot()
	if len(calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1 even for an already-default branch: %+v", len(calls), calls)
	}
	if calls[0].Ref != "main" {
		t.Fatalf("enqueue call = %+v, want a reindex of main", calls[0])
	}
}

// TestUpdateRepoDefaultBranch_LeavesTheSwitchInPlaceWhenTheReindexCannotBeQueued
// pins the failure shape the handler deliberately chooses. HEAD and the row
// are already consistent by the time the enqueue runs, so a failure there
// leaves a working repository with a stale index -- not a half-applied one.
// Undoing the switch would mean writing to the store that just refused a
// write, and the outage that failed the enqueue fails the undo too, which is
// how HEAD and the row end up naming different branches. The retry below is
// what actually repairs the index.
func TestUpdateRepoDefaultBranch_LeavesTheSwitchInPlaceWhenTheReindexCannotBeQueued(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	f.commit(r, "release", "VERSION", "1.0")
	tok := f.token(f.alice, "write")

	f.sync.fail = errors.New("sync_jobs insert failed")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")})
	if resp.status() != 500 {
		t.Fatalf("status = %d, want 500, body = %s", resp.status(), resp.rec.Body.String())
	}

	// The row moved and stayed moved.
	stored, err := f.st.GetRepoByID(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if stored.DefaultBranch != "release" {
		t.Fatalf("stored default_branch = %q, want release to stay applied", stored.DefaultBranch)
	}

	// And HEAD agrees with it -- the invariant that matters, since a clone
	// follows HEAD while every listing reads the row.
	gitRepo, err := f.git.Open(r.StoragePath)
	if err != nil {
		t.Fatalf("open git repository: %v", err)
	}
	head, err := os.ReadFile(filepath.Join(gitRepo.Dir(), "HEAD"))
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(head)); got != "ref: refs/heads/release" {
		t.Fatalf("HEAD = %q, want it to agree with the row on release", got)
	}

	// Retrying now takes the already-default path, which still queues the
	// reindex: the request repairs itself rather than answering 200 with
	// nothing done.
	f.sync.fail = nil
	if resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")}); resp.status() != 200 {
		t.Fatalf("retry status = %d, want 200, body = %s", resp.status(), resp.rec.Body.String())
	}
	calls := f.sync.snapshot()
	if len(calls) != 2 || calls[1].Ref != "release" {
		t.Fatalf("enqueue calls = %+v, want the retry to have queued a release reindex", calls)
	}
}

// ---------------------------------------------------------------- rejected

func TestUpdateRepoDefaultBranch_NonexistentBranch(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{DefaultBranch: strPtr("does-not-exist")})
	if resp.status() != 404 {
		t.Fatalf("status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
	}

	stored, err := f.st.GetRepoByID(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if stored.DefaultBranch != "main" {
		t.Fatalf("default_branch changed to %q despite the rejected request", stored.DefaultBranch)
	}
	if calls := f.sync.snapshot(); len(calls) != 0 {
		t.Fatalf("enqueue calls = %d, want 0 for a rejected request: %+v", len(calls), calls)
	}
}

func TestUpdateRepoDefaultBranch_MissingFieldIsBadRequest(t *testing.T) {
	f := newDefaultBranchFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{})
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
	}
}

func TestUpdateRepoDefaultBranch_RequiresNamespaceAdmin(t *testing.T) {
	f := newDefaultBranchFixture(t)
	ns, err := f.st.CreateOrg(context.Background(), "acme", f.alice.ID, store.OrgUpdate{})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	r := f.repo("acme", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	f.commit(r, "release", "VERSION", "1.0")
	// bob is a write member: he may commit, but not reconfigure the repository.
	f.addOrgMember(ns.ID, f.bob.ID, "write")

	resp := f.patch("model", "acme", "foo", f.token(f.bob, "write"), apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")})
	if resp.status() != 403 {
		t.Fatalf("write member status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}

	// The org admin may.
	resp = f.patch("model", "acme", "foo", f.token(f.alice, "write"), apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")})
	if resp.status() != 200 {
		t.Fatalf("org admin status = %d, want 200; body = %s", resp.status(), resp.rec.Body.String())
	}
}

func TestUpdateRepoDefaultBranch_StrangerGets401Or403(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	f.commit(r, "release", "VERSION", "1.0")

	// bob has no relationship to alice's namespace: the write gate rejects
	// him before the admin check, so this is the ordinary permission failure.
	resp := f.patch("model", "alice", "foo", f.token(f.bob, "write"), apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	// Anonymous callers are asked to authenticate.
	if resp := f.patch("model", "alice", "foo", "", apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")}); resp.status() != 401 {
		t.Fatalf("anonymous status = %d, want 401", resp.status())
	}
}

func TestUpdateRepoDefaultBranch_RejectsWhenArchived(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	f.commit(r, "release", "VERSION", "1.0")
	tok := f.token(f.alice, "write")

	if resp := f.do("POST", "/api/v1/repos/model/alice/foo/archive", tok, nil); resp.status() != 200 {
		t.Fatalf("archive status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	// Even the owner, who still holds admin over an archived repository (and
	// could unarchive it), is refused: this is a content-adjacent change, not
	// the unarchive escape hatch.
	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{DefaultBranch: strPtr("release")})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	if got := errorType(t, resp); got != "repository_archived" {
		t.Fatalf("error type = %q, want repository_archived; body = %s", got, resp.rec.Body.String())
	}
}
