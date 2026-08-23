// Tests for the two disposal operations: archiving a repository (soft,
// reversible, read-only) and deleting one (hard, taking its git directory with
// it). Both are driven over real HTTP against a real Server, the same way
// transfers_test.go does.

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/storage"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// archiveFixture is transferFixture's sibling. It keeps the object store
// around as well, which the deletion test needs in order to assert that the
// shared content-addressed objects were left alone.
type archiveFixture struct {
	t      *testing.T
	s      *Server
	st     *store.Store
	git    *gitrepo.Manager
	obj    *memStore
	dbPath string

	admin *store.User
	alice *store.User
	bob   *store.User
}

func newArchiveFixture(t *testing.T) *archiveFixture {
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
	obj := newMemStore()
	cfg := &config.Config{PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret"}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  obj,
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})

	f := &archiveFixture{t: t, s: srv, st: st, git: gitMgr, obj: obj, dbPath: dbPath}
	f.admin = f.user("siteadmin", true)
	f.alice = f.user("alice", false)
	f.bob = f.user("bob", false)
	return f
}

func (f *archiveFixture) user(name string, isAdmin bool) *store.User {
	f.t.Helper()
	u, err := f.st.CreateUser(context.Background(), name, name+"@example.com", "hash", isAdmin)
	if err != nil {
		f.t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

func (f *archiveFixture) repo(ns, name, kind string) *store.Repo {
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

// addOrgMember inserts an org_members row directly: store.CreateOrg only ever
// creates the owner's 'admin' row, and the "a write member may not archive"
// case needs a non-admin member. Opened as a second raw connection to the
// same SQLite file rather than reaching into store's unexported db field.
func (f *archiveFixture) addOrgMember(namespaceID, userID int64, role string) {
	f.t.Helper()
	dsn := "file:" + f.dbPath + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		f.t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO org_members (namespace_id, user_id, role) VALUES (?, ?, ?)`,
		namespaceID, userID, role); err != nil {
		f.t.Fatalf("insert org_member: %v", err)
	}
}

func (f *archiveFixture) token(u *store.User, scope string) string {
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

func (f *archiveFixture) do(method, path, token string, body any) response {
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

// archive flips alice's repository into the archive and asserts it took.
func (f *archiveFixture) archive(kind, ns, name, token string) {
	f.t.Helper()
	resp := f.do("POST", "/api/v1/repos/"+kind+"/"+ns+"/"+name+"/archive", token, nil)
	if resp.status() != 200 {
		f.t.Fatalf("archive status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
}

// errorType pulls the machine-readable discriminator out of an error body, so
// a test can tell "archived" apart from a plain permission failure.
func errorType(t *testing.T, r response) string {
	t.Helper()
	var body apitypes.ApiErrorBody
	if err := json.Unmarshal(r.rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", r.rec.Body.String(), err)
	}
	return body.Error.Type
}

// ------------------------------------------------------------------ archive

func TestArchiveRepo_MarksReadOnlyAndIsReversible(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/repos/model/alice/foo/archive", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("archive status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if !body.Repo.Archived || body.Repo.ArchivedAt == nil {
		t.Fatalf("archived = %v, archived_at = %v, want true / non-nil", body.Repo.Archived, body.Repo.ArchivedAt)
	}
	// An archived repository offers no editing affordance, but its owner can
	// still act on it as an owner -- that is how it gets unarchived.
	if body.Repo.CanWrite {
		t.Fatalf("can_write = true on an archived repository")
	}
	if !body.Repo.CanAdmin {
		t.Fatalf("can_admin = false for the owner of an archived repository")
	}

	// Reads keep working.
	resp = f.do("GET", "/api/v1/repos/model/alice/foo", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("read status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	resp = f.do("DELETE", "/api/v1/repos/model/alice/foo/archive", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("unarchive status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	resp.json(t, &body)
	if body.Repo.Archived || body.Repo.ArchivedAt != nil {
		t.Fatalf("still archived after unarchive: %+v", body.Repo.ArchivedAt)
	}
	if !body.Repo.CanWrite {
		t.Fatalf("can_write = false after unarchiving")
	}
}

// The whole point of the flag: every write path refuses. Each case below is a
// different entry point into loadRepoForWrite -- the HF commit protocol, the
// web editor, git receive-pack, a transfer, and experiment ingest.
func TestArchivedRepo_RejectsEveryWritePath(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "foo", "model")
	f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	f.archive("model", "alice", "foo", tok)
	f.archive("dataset", "alice", "exp", tok)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"hf commit", "POST", "/api/models/alice/foo/commit/main", nil},
		{"hf preupload", "POST", "/api/models/alice/foo/preupload/main", map[string]any{"files": []any{}}},
		{"web edit", "PUT", "/api/v1/edit/model/alice/foo/main/README.md", map[string]any{"content": "hi"}},
		{"git receive-pack advertise", "GET", "/models/alice/foo/info/refs?service=git-receive-pack", nil},
		{"git receive-pack", "POST", "/models/alice/foo/git-receive-pack", nil},
		{"lfs upload batch", "POST", "/models/alice/foo/info/lfs/objects/batch",
			map[string]any{"operation": "upload", "objects": []any{}}},
		{"transfer", "POST", "/api/v1/repos/model/alice/foo/transfer", map[string]any{"namespace": "bob"}},
		{"hf move", "POST", "/api/repos/move",
			map[string]any{"fromRepo": "alice/foo", "toRepo": "bob/foo", "type": "model"}},
		{"experiment ingest", "POST", "/api/v1/experiments/alice/exp/p1/log",
			map[string]any{"run": "r1", "points": []any{}}},
		{"experiment finish", "POST", "/api/v1/experiments/alice/exp/p1/finish", map[string]any{"run": "r1"}},
		{"run annotation", "PATCH", "/api/v1/experiments/alice/exp/p1/runs/r1",
			map[string]any{"archived": true}},
		{"run delete", "DELETE", "/api/v1/experiments/alice/exp/p1/runs/r1", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do(tc.method, tc.path, tok, tc.body)
			if resp.status() != 403 {
				t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
			}
			if got := errorType(t, resp); got != "repository_archived" {
				t.Fatalf("error type = %q, want repository_archived; body = %s", got, resp.rec.Body.String())
			}
		})
	}
}

func TestArchivedRepo_ReadPathsStillWork(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")
	f.archive("model", "alice", "foo", tok)

	// Anonymous, since a public archived repository must stay downloadable.
	for _, path := range []string{
		"/api/models/alice/foo",
		"/models/alice/foo/info/refs?service=git-upload-pack",
		"/api/v1/repos?kind=model",
	} {
		resp := f.do("GET", path, "", nil)
		if resp.status() != 200 {
			t.Fatalf("GET %s = %d, body = %s", path, resp.status(), resp.rec.Body.String())
		}
	}
}

func TestArchiveRepo_RequiresNamespaceAdmin(t *testing.T) {
	f := newArchiveFixture(t)
	ns, err := f.st.CreateOrg(context.Background(), "acme", f.alice.ID, store.OrgUpdate{})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	f.repo("acme", "foo", "model")
	// bob is a write member: he may commit, but not dispose of the repository.
	f.addOrgMember(ns.ID, f.bob.ID, "write")

	resp := f.do("POST", "/api/v1/repos/model/acme/foo/archive", f.token(f.bob, "write"), nil)
	if resp.status() != 403 {
		t.Fatalf("write member archive status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}

	// The org admin may.
	if resp := f.do("POST", "/api/v1/repos/model/acme/foo/archive", f.token(f.alice, "write"), nil); resp.status() != 200 {
		t.Fatalf("org admin archive status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	// So may the site admin, who unarchives it again.
	if resp := f.do("DELETE", "/api/v1/repos/model/acme/foo/archive", f.token(f.admin, "write"), nil); resp.status() != 200 {
		t.Fatalf("site admin unarchive status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
}

func TestArchiveRepo_StrangerGets404Or403(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "foo", "model")

	// bob has no relationship to alice's namespace: the write gate rejects him
	// before the admin check, so this is the ordinary permission failure.
	resp := f.do("POST", "/api/v1/repos/model/alice/foo/archive", f.token(f.bob, "write"), nil)
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	// Anonymous callers are asked to authenticate.
	if resp := f.do("POST", "/api/v1/repos/model/alice/foo/archive", "", nil); resp.status() != 401 {
		t.Fatalf("anonymous status = %d, want 401", resp.status())
	}
}

func TestListRepos_ArchivedFilter(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "active", "model")
	f.repo("alice", "frozen", "model")
	tok := f.token(f.alice, "write")
	f.archive("model", "alice", "frozen", tok)

	names := func(query string) []string {
		t.Helper()
		resp := f.do("GET", "/api/v1/repos"+query, tok, nil)
		if resp.status() != 200 {
			t.Fatalf("list%s = %d, body = %s", query, resp.status(), resp.rec.Body.String())
		}
		var body apitypes.RepoListResponse
		resp.json(t, &body)
		out := make([]string, 0, len(body.Items))
		for _, item := range body.Items {
			out = append(out, item.Name)
		}
		return out
	}

	// Default: both, so an archived repository does not look deleted.
	if got := names(""); len(got) != 2 {
		t.Fatalf("unfiltered listing = %v, want both repositories", got)
	}
	if got := names("?archived=true"); len(got) != 1 || got[0] != "frozen" {
		t.Fatalf("archived=true listing = %v, want [frozen]", got)
	}
	if got := names("?archived=false"); len(got) != 1 || got[0] != "active" {
		t.Fatalf("archived=false listing = %v, want [active]", got)
	}
}

// ------------------------------------------------------------------- delete

// TestDeleteRepo_CleansUpEverything is the after-care check: the row and the
// bare git directory go synchronously, and object storage is left completely
// alone.
//
// Both storage layers are content-addressed and shared across repositories,
// so a delete can only drop references -- another repository may hold the very
// same bytes. The `gc` subcommand is what reclaims the objects nothing
// references any more.
func TestDeleteRepo_CleansUpEverything(t *testing.T) {
	f := newArchiveFixture(t)
	repo := f.repo("alice", "foo", "model")
	ctx := context.Background()

	sharedLFS := storage.LFSKey("abcd")
	sharedBlob := storage.BlobKey("beef")
	for _, key := range []string{sharedLFS, sharedBlob} {
		if err := f.obj.Put(ctx, key, strings.NewReader("x"), ""); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	if !f.git.Exists(repo.StoragePath) {
		t.Fatalf("bare repository was not created")
	}

	resp := f.do("DELETE", "/api/v1/repos/model/alice/foo", f.token(f.alice, "write"), nil)
	if resp.status() != 204 {
		t.Fatalf("delete status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	if _, err := f.st.GetRepo(ctx, "model", "alice", "foo"); err == nil {
		t.Fatalf("repository row still resolves after delete")
	}
	if f.git.Exists(repo.StoragePath) {
		t.Fatalf("bare git directory %s survived the delete", repo.StoragePath)
	}
	for _, key := range []string{sharedLFS, sharedBlob} {
		if _, err := f.obj.Stat(ctx, key); err != nil {
			t.Fatalf("shared object %s was removed by a repository delete: %v", key, err)
		}
	}
	// The name is free again.
	if resp := f.do("GET", "/api/v1/repos/model/alice/foo", "", nil); resp.status() != 404 {
		t.Fatalf("deleted repository still readable: %d", resp.status())
	}
}

func TestDeleteRepo_AllowedWhileArchived(t *testing.T) {
	f := newArchiveFixture(t)
	repo := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")
	f.archive("model", "alice", "foo", tok)

	if resp := f.do("DELETE", "/api/v1/repos/model/alice/foo", tok, nil); resp.status() != 204 {
		t.Fatalf("delete of an archived repository = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if f.git.Exists(repo.StoragePath) {
		t.Fatalf("bare git directory survived")
	}
}

// The HF-compatible delete is the same path with huggingface_hub's body shape.
func TestHFDeleteRepo_RemovesRepository(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "foo", "dataset")

	resp := f.do("DELETE", "/api/repos/delete", f.token(f.alice, "write"),
		map[string]any{"type": "dataset", "name": "alice/foo"})
	if resp.status() != 200 {
		t.Fatalf("hf delete status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	if _, err := f.st.GetRepo(context.Background(), "dataset", "alice", "foo"); err == nil {
		t.Fatalf("repository still resolves after HF delete")
	}
}

func TestDeleteRepo_ReadOnlyTokenRefused(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "foo", "model")

	if resp := f.do("DELETE", "/api/v1/repos/model/alice/foo", f.token(f.alice, "read"), nil); resp.status() != 403 {
		t.Fatalf("read-scoped delete = %d, want 403", resp.status())
	}
	if _, err := f.st.GetRepo(context.Background(), "model", "alice", "foo"); err != nil {
		t.Fatalf("repository was deleted by a read-only token: %v", err)
	}
}

// ------------------------------------------------------------- run deletion

func TestDeleteExperimentRun(t *testing.T) {
	f := newArchiveFixture(t)
	repo := f.repo("alice", "exp", "dataset")
	tok := f.token(f.alice, "write")
	ctx := context.Background()

	projectID, err := f.st.UpsertExpProject(ctx, repo.ID, "p1")
	if err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	for _, run := range []string{"keep", "drop"} {
		runID, err := f.st.UpsertExpRun(ctx, projectID, run, "finished", nil, nil, nil, 0, 0, nil)
		if err != nil {
			t.Fatalf("upsert run %s: %v", run, err)
		}
		if err := f.st.InsertPoints(ctx, runID, []store.MetricPoint{
			{Step: 1, TS: time.Now(), Metrics: map[string]float64{"loss": 1}},
		}); err != nil {
			t.Fatalf("insert points for %s: %v", run, err)
		}
	}

	resp := f.do("DELETE", "/api/v1/experiments/alice/exp/p1/runs/drop", tok, nil)
	if resp.status() != 204 {
		t.Fatalf("delete run status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	runs, err := f.st.ListExpRuns(ctx, projectID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].Name != "keep" {
		t.Fatalf("runs after delete = %+v, want just \"keep\"", runs)
	}

	// Deleting it again is a 404, not a silent success.
	if resp := f.do("DELETE", "/api/v1/experiments/alice/exp/p1/runs/drop", tok, nil); resp.status() != 404 {
		t.Fatalf("second delete = %d, want 404", resp.status())
	}
	// And a reader may not delete at all.
	if resp := f.do("DELETE", "/api/v1/experiments/alice/exp/p1/runs/keep", f.token(f.bob, "write"), nil); resp.status() != 403 {
		t.Fatalf("stranger delete = %d, want 403", resp.status())
	}
}

// Deleting is irreversible and takes the history, the LFS links and the
// exports with it, so it sits with archive and transfer behind namespace
// admin -- not with push behind plain write. Both delete paths (the UI one
// and HF's) must agree, or huggingface_hub becomes the way around the rule.
func TestDeleteRepo_RequiresNamespaceAdmin(t *testing.T) {
	f := newArchiveFixture(t)
	ns, err := f.st.CreateOrg(context.Background(), "acme", f.alice.ID, store.OrgUpdate{})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	f.addOrgMember(ns.ID, f.bob.ID, "write")

	f.repo("acme", "foo", "model")
	if resp := f.do("DELETE", "/api/v1/repos/model/acme/foo", f.token(f.bob, "write"), nil); resp.status() != 403 {
		t.Fatalf("write member delete status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}

	// The same rule on the HF-compatible path.
	hf := map[string]any{"name": "foo", "organization": "acme", "type": "model"}
	if resp := f.do("DELETE", "/api/repos/delete", f.token(f.bob, "write"), hf); resp.status() != 403 {
		t.Fatalf("write member HF delete status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}

	// The repository is still there.
	if _, err := f.st.GetRepo(context.Background(), "model", "acme", "foo"); err != nil {
		t.Fatalf("repository should have survived the refused deletes: %v", err)
	}

	// The org admin may delete it.
	if resp := f.do("DELETE", "/api/v1/repos/model/acme/foo", f.token(f.alice, "write"), nil); resp.status() != 204 {
		t.Fatalf("org admin delete status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	// And so may the site admin, via HF's path.
	f.repo("acme", "bar", "model")
	hfBar := map[string]any{"name": "bar", "organization": "acme", "type": "model"}
	if resp := f.do("DELETE", "/api/repos/delete", f.token(f.admin, "write"), hfBar); resp.status() != 200 {
		t.Fatalf("site admin HF delete status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
}
