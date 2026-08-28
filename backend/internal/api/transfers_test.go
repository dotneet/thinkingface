package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// noopEnqueuer satisfies the Enqueuer interface without a real sync worker --
// the transfer tests never push commits, so nothing needs to be indexed.
type noopEnqueuer struct{}

func (noopEnqueuer) Enqueue(context.Context, int64, string, string, string) error { return nil }

// transferFixture wires a real Server -- SQLite store, on-disk git manager,
// in-memory object store -- so the transfer endpoints can be driven over
// actual HTTP requests end to end, the way they run in production.
type transferFixture struct {
	t   *testing.T
	s   *Server
	st  *store.Store
	git *gitrepo.Manager

	admin *store.User
	alice *store.User
	bob   *store.User
}

func newTransferFixture(t *testing.T) *transferFixture {
	t.Helper()
	return newFixtureWithConfig(t, nil)
}

// newFixtureWithConfig is newTransferFixture with a hook to adjust the
// server configuration before the Server is built, for the settings a test
// needs to vary (TF_ORG_CREATION and friends).
func newFixtureWithConfig(t *testing.T, tweak func(*config.Config)) *transferFixture {
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
		PublicURL: "http://test.local", WALMode: "off",
		SessionSecret: "test-secret-test-secret", OrgCreation: "anyone", AllowSignup: true,
	}
	if tweak != nil {
		tweak(cfg)
	}
	srv := NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  newMemStore(),
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})

	f := &transferFixture{t: t, s: srv, st: st, git: gitMgr}
	f.admin = f.mustUser(ctx, "admin", true)
	f.alice = f.mustUser(ctx, "alice", false)
	f.bob = f.mustUser(ctx, "bob", false)
	return f
}

func (f *transferFixture) mustUser(ctx context.Context, name string, isAdmin bool) *store.User {
	f.t.Helper()
	u, err := f.st.CreateUser(ctx, name, name+"@example.com", "hash", isAdmin)
	if err != nil {
		f.t.Fatalf("create user %s: %v", name, err)
	}
	return u
}

// org creates an organisation whose founder becomes its admin member.
func (f *transferFixture) org(name string, founder *store.User) *store.Org {
	f.t.Helper()
	n, err := f.st.CreateOrg(context.Background(), name, founder.ID, store.OrgUpdate{})
	if err != nil {
		f.t.Fatalf("create org %s: %v", name, err)
	}
	return n
}

// addOrgMember grants userID a role in the organisation. added_by is
// recorded as the site admin, which is informational only.
func (f *transferFixture) addOrgMember(namespaceID, userID int64, role string) {
	f.t.Helper()
	if _, err := f.st.AddOrgMember(context.Background(), namespaceID, userID, role, f.admin.ID); err != nil {
		f.t.Fatalf("add org member: %v", err)
	}
}

func (f *transferFixture) repo(ns, name, kind string) *store.Repo {
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

func (f *transferFixture) token(u *store.User, scope string) string {
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

type response struct {
	rec *httptest.ResponseRecorder
}

func (f *transferFixture) do(method, path, token string, body any) response {
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

func (r response) status() int { return r.rec.Code }

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response body %q: %v", r.rec.Body.String(), err)
	}
}

// ------------------------------------------------------------------- tests

func TestHFMoveRepo_ImmediateAcrossOwnNamespaces(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")
	f.org("acme", f.alice) // alice is admin of "acme" too

	tok := f.token(f.alice, "write")
	resp := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "acme/foo", "type": "model",
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body struct {
		URL string `json:"url"`
	}
	resp.json(t, &body)
	if !strings.HasSuffix(body.URL, "/acme/foo") {
		t.Fatalf("url = %q, want suffix /acme/foo", body.URL)
	}

	// The repository is reachable at its new name immediately.
	repo, err := f.st.GetRepo(context.Background(), "model", "acme", "foo")
	if err != nil {
		t.Fatalf("get moved repo: %v", err)
	}
	if repo.Namespace != "acme" || repo.Name != "foo" {
		t.Fatalf("repo = %s/%s, want acme/foo", repo.Namespace, repo.Name)
	}

	// The old name is gone.
	if _, err := f.st.GetRepo(context.Background(), "model", "alice", "foo"); err == nil {
		t.Fatalf("old name alice/foo still resolves directly")
	}
}

func TestHFMoveRepo_PendingThenAccept_ReachableAtNewNameAndOldNameRedirects(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")

	aliceTok := f.token(f.alice, "write")
	resp := f.do("POST", "/api/repos/move", aliceTok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "bob/foo", "type": "model",
	})
	if resp.status() != 202 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var pendingBody struct {
		URL        string `json:"url"`
		Pending    bool   `json:"pending"`
		TransferID int64  `json:"transfer_id"`
	}
	resp.json(t, &pendingBody)
	if !pendingBody.Pending || pendingBody.TransferID == 0 {
		t.Fatalf("pending body = %+v, want pending=true and a transfer_id", pendingBody)
	}

	// While pending, the repository still answers at the old name.
	if repo, err := f.st.GetRepo(context.Background(), "model", "alice", "foo"); err != nil || repo == nil {
		t.Fatalf("repo should still be at alice/foo while pending: %v", err)
	}

	// bob accepts.
	bobTok := f.token(f.bob, "write")
	acceptResp := f.do("POST", fmt.Sprintf("/api/v1/transfers/%d/accept", pendingBody.TransferID), bobTok, nil)
	if acceptResp.status() != 200 {
		t.Fatalf("accept status = %d, body = %s", acceptResp.status(), acceptResp.rec.Body.String())
	}
	var acceptBody apitypes.RepoTransferResponse
	acceptResp.json(t, &acceptBody)
	if acceptBody.Repo == nil || acceptBody.Repo.Namespace != "bob" || acceptBody.Repo.Name != "foo" {
		t.Fatalf("accept response repo = %+v, want bob/foo", acceptBody.Repo)
	}
	if acceptBody.Transfer.Status != apitypes.RepoTransferAccepted {
		t.Fatalf("transfer status = %s, want accepted", acceptBody.Transfer.Status)
	}

	// Repository now reachable at the new name.
	if _, err := f.st.GetRepo(context.Background(), "model", "bob", "foo"); err != nil {
		t.Fatalf("get repo at new name: %v", err)
	}

	// The old name's HF repo-info route answers 308 to the new name.
	infoResp := f.do("GET", "/api/models/alice/foo", "", nil)
	if infoResp.status() != 308 {
		t.Fatalf("HF repo-info at old name status = %d, want 308", infoResp.status())
	}
	loc := infoResp.rec.Header().Get("Location")
	if !strings.Contains(loc, "/bob/foo") {
		t.Fatalf("Location = %q, want it to contain /bob/foo", loc)
	}

	// The old name's UI API answers 404 with a repo_moved body.
	uiResp := f.do("GET", "/api/v1/repos/model/alice/foo", "", nil)
	if uiResp.status() != 404 {
		t.Fatalf("UI repo detail at old name status = %d, want 404", uiResp.status())
	}
	var errBody apitypes.ApiErrorBody
	uiResp.json(t, &errBody)
	if errBody.Error.Type != "repo_moved" || errBody.Error.MovedTo == nil ||
		errBody.Error.MovedTo.Namespace != "bob" || errBody.Error.MovedTo.Name != "foo" {
		t.Fatalf("error body = %+v, want repo_moved to bob/foo", errBody.Error)
	}
}

func TestHFMoveRepo_ForbiddenForOrgWriteMember(t *testing.T) {
	f := newTransferFixture(t)
	acme := f.org("acme", f.admin)
	f.addOrgMember(acme.ID, f.alice.ID, "write")
	f.repo("acme", "foo", "model")

	tok := f.token(f.alice, "write")
	resp := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "acme/foo", "toRepo": "alice/foo", "type": "model",
	})
	if resp.status() != 403 {
		t.Fatalf("status = %d, body = %s, want 403 (write member may not transfer an org repo)", resp.status(), resp.rec.Body.String())
	}

	// The repository never moved.
	if _, err := f.st.GetRepo(context.Background(), "model", "acme", "foo"); err != nil {
		t.Fatalf("repo should still be at acme/foo: %v", err)
	}
}

func TestHFMoveRepo_ConflictOnNameCollision(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")
	f.repo("bob", "foo", "model") // already occupies the destination name

	tok := f.token(f.alice, "write")
	resp := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "bob/foo", "type": "model",
	})
	if resp.status() != 409 {
		t.Fatalf("status = %d, body = %s, want 409", resp.status(), resp.rec.Body.String())
	}
}

func TestCreateAtOldName_SucceedsAndClearsRedirect(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")

	tok := f.token(f.alice, "write")
	resp := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "bob/foo", "type": "model",
	})
	if resp.status() != 202 {
		t.Fatalf("move status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var pendingBody struct {
		TransferID int64 `json:"transfer_id"`
	}
	resp.json(t, &pendingBody)
	bobTok := f.token(f.bob, "write")
	acceptResp := f.do("POST", fmt.Sprintf("/api/v1/transfers/%d/accept", pendingBody.TransferID), bobTok, nil)
	if acceptResp.status() != 200 {
		t.Fatalf("accept status = %d, body = %s", acceptResp.status(), acceptResp.rec.Body.String())
	}

	// Old name now redirects.
	preCreate := f.do("GET", "/api/models/alice/foo", "", nil)
	if preCreate.status() != 308 {
		t.Fatalf("pre-create status = %d, want 308", preCreate.status())
	}

	// A fresh repository may be created at the old name.
	aliceTok := f.token(f.alice, "write")
	createResp := f.do("POST", "/api/repos/create", aliceTok, map[string]any{
		"type": "model", "name": "foo",
	})
	if createResp.status() != 200 {
		t.Fatalf("create at old name status = %d, body = %s", createResp.status(), createResp.rec.Body.String())
	}

	// The redirect is gone: the old name resolves directly again, no
	// Location header, no repo_moved body.
	postCreate := f.do("GET", "/api/models/alice/foo", "", nil)
	if postCreate.status() != 200 {
		t.Fatalf("post-create status = %d, want 200 (no more redirect)", postCreate.status())
	}
}

func TestHFDeleteRepo_AtOldName_PlainNotFound(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	// Same-namespace rename: alice/foo -> alice/bar. Same actor owns both
	// sides, so this completes immediately and leaves a redirect at
	// alice/foo pointing to alice/bar.
	renameResp := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "alice/bar", "type": "model",
	})
	if renameResp.status() != 200 {
		t.Fatalf("rename status = %d, body = %s", renameResp.status(), renameResp.rec.Body.String())
	}

	// The old name redirects for a read...
	info := f.do("GET", "/api/models/alice/foo", "", nil)
	if info.status() != 308 {
		t.Fatalf("repo-info at old name status = %d, want 308", info.status())
	}

	// ...but DELETE /api/repos/delete at the old name must not follow it: a
	// destructive operation on a stale name is refused outright rather than
	// risk deleting the repository sitting at the new name.
	del := f.do("DELETE", "/api/repos/delete", tok, map[string]any{
		"type": "model", "name": "foo", "organization": "alice",
	})
	if del.status() != 404 {
		t.Fatalf("delete at old name status = %d, body = %s, want 404", del.status(), del.rec.Body.String())
	}

	// The repository at the new name is unharmed.
	if _, err := f.st.GetRepo(context.Background(), "model", "alice", "bar"); err != nil {
		t.Fatalf("repo at new name should be untouched: %v", err)
	}
}

// TestTransferDecide_TellsANonDestinationNothing pins that accept/reject
// answer a caller who may not write the destination exactly as they answer
// one who named an id that does not exist -- same status, same body.
//
// Transfer ids are instance-wide serials, so anything that distinguishes the
// two lets a caller with any write-scoped credential walk the id space and
// read off which namespaces have a pending inbound transfer. A 403 does that
// twice over: it confirms the row, and names the destination in its message.
// A 404 whose body differs from a miss is still two answers.
func TestTransferDecide_TellsANonDestinationNothing(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")
	aliceTok := f.token(f.alice, "write")
	carol := f.mustUser(context.Background(), "carol", false)
	carolTok := f.token(carol, "write")

	resp := f.do("POST", "/api/repos/move", aliceTok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "bob/foo", "type": "model",
	})
	if resp.status() != 202 {
		t.Fatalf("move status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var pending struct {
		TransferID int64 `json:"transfer_id"`
	}
	resp.json(t, &pending)
	if pending.TransferID == 0 {
		t.Fatal("move returned no transfer_id")
	}

	for _, action := range []string{"accept", "reject"} {
		path := fmt.Sprintf("/api/v1/transfers/%d/%s", pending.TransferID, action)
		missingPath := fmt.Sprintf("/api/v1/transfers/%d/%s", pending.TransferID+9999, action)
		real := f.do("POST", path, carolTok, nil)
		missing := f.do("POST", missingPath, carolTok, nil)
		if real.status() != 404 {
			t.Fatalf("%s of an existing transfer: status = %d, want 404 (body %s)",
				action, real.status(), real.rec.Body.String())
		}
		if missing.status() != 404 {
			t.Fatalf("%s of a missing transfer: status = %d, want 404 (body %s)",
				action, missing.status(), missing.rec.Body.String())
		}
		if real.rec.Body.String() != missing.rec.Body.String() {
			t.Fatalf("%s: the two 404s differ, so the id is still distinguishable:\n existing: %s\n  missing: %s",
				action, real.rec.Body.String(), missing.rec.Body.String())
		}
	}

	// The refused calls must not have consumed the pending transfer: bob
	// can still accept it.
	bobTok := f.token(f.bob, "write")
	if got := f.do("POST", fmt.Sprintf("/api/v1/transfers/%d/accept", pending.TransferID), bobTok, nil); got.status() != 200 {
		t.Fatalf("destination accept after refused probes: status = %d, body = %s",
			got.status(), got.rec.Body.String())
	}
}
