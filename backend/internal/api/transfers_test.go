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

// The auditor's reproduction, end to end: alice offers alice/thing to bob,
// bob never answers, and seven days pass. The request is then invisible
// (GET .../transfer 404) and uncancellable (DELETE .../transfer 404), yet it
// still held the repository's one pending slot -- so alice could not offer
// the repository to anybody, ever again. The TTL is a constant, so the
// expired row is planted through the store rather than by waiting a week.
func TestTransfer_ExpiredRequestDoesNotBlockANewOne(t *testing.T) {
	f := newTransferFixture(t)
	ctx := context.Background()
	r := f.repo("alice", "thing", "model")
	bobNS, err := f.st.GetNamespace(ctx, "bob")
	if err != nil {
		t.Fatalf("namespace bob: %v", err)
	}
	if _, err := f.st.CreateRepoTransfer(ctx, store.TransferSpec{
		RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID,
	}, -time.Hour); err != nil {
		t.Fatalf("plant an expired request: %v", err)
	}

	aliceTok := f.token(f.alice, "write")
	if got := f.do("GET", "/api/v1/repos/model/alice/thing/transfer", aliceTok, nil).status(); got != 404 {
		t.Fatalf("GET .../transfer on an expired request = %d, want 404", got)
	}
	if got := f.do("DELETE", "/api/v1/repos/model/alice/thing/transfer", aliceTok, nil).status(); got != 404 {
		t.Fatalf("DELETE .../transfer on an expired request = %d, want 404", got)
	}
	var mine apitypes.MyTransfersResponse
	f.do("GET", "/api/v1/me/transfers", aliceTok, nil).json(t, &mine)
	if len(mine.Incoming) != 0 || len(mine.Outgoing) != 0 {
		t.Fatalf("/me/transfers = %+v, want an expired request listed nowhere", mine)
	}

	// Nothing above could have cleared it, so this is the request that has to.
	resp := f.do("POST", "/api/v1/repos/model/alice/thing/transfer", aliceTok,
		map[string]any{"namespace": "bob"})
	if resp.status() != 202 {
		t.Fatalf("a fresh transfer request = %d, body = %s, want 202", resp.status(), resp.rec.Body.String())
	}
	if got := f.do("GET", "/api/v1/repos/model/alice/thing/transfer", aliceTok, nil).status(); got != 200 {
		t.Fatalf("GET .../transfer after the fresh request = %d, want 200", got)
	}
}

// A site admin may accept or reject any pending transfer by id -- roleIn
// answers RoleAdmin in every namespace -- but /me/transfers is an inbox, not
// a list of everything they are permitted to do. While the two shared one
// rule, an administrator's header badge counted every pending transfer on the
// instance, twice over, none of it addressed to them.
func TestMyTransfers_OmitsTransfersBetweenStrangersForASiteAdmin(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")

	aliceTok := f.token(f.alice, "write")
	resp := f.do("POST", "/api/repos/move", aliceTok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "bob/foo", "type": "model",
	})
	if resp.status() != 202 {
		t.Fatalf("move status = %d, want 202 (pending)", resp.status())
	}
	var move struct {
		TransferID int64 `json:"transfer_id"`
	}
	resp.json(t, &move)

	adminTok := f.token(f.admin, "write")
	var mine apitypes.MyTransfersResponse
	f.do("GET", "/api/v1/me/transfers", adminTok, nil).json(t, &mine)
	if len(mine.Incoming) != 0 || len(mine.Outgoing) != 0 {
		t.Fatalf("site admin /me/transfers = %+v, want a stranger's transfer listed on neither side", mine)
	}

	// bob, who was actually asked, still gets it -- the endpoint works, it
	// just is not addressed to the administrator.
	var bobs apitypes.MyTransfersResponse
	f.do("GET", "/api/v1/me/transfers", f.token(f.bob, "write"), nil).json(t, &bobs)
	if len(bobs.Incoming) != 1 || len(bobs.Outgoing) != 0 {
		t.Fatalf("bob /me/transfers = %+v, want the one pending transfer incoming", bobs)
	}

	// And the power the listing no longer advertises is still there: the
	// administrator decides it by id, which is how an unresponsive
	// destination gets unstuck.
	if got := f.do("POST", fmt.Sprintf("/api/v1/transfers/%d/accept", move.TransferID), adminTok, nil).status(); got != 200 {
		t.Fatalf("site admin accepting transfer %d = %d, want 200", move.TransferID, got)
	}
	if r, err := f.st.GetRepo(context.Background(), "model", "bob", "foo"); err != nil {
		t.Fatalf("repository after the admin accepted: %v", err)
	} else if r.Namespace != "bob" {
		t.Fatalf("repository namespace = %q, want bob", r.Namespace)
	}
}

// move_repo() called on a name the repository has already left must answer
// "no such repository", not a redirect.
//
// This route names both repositories in its *body*: the request path is a
// constant "/api/repos/move" with no {ns}/{name} in it for movedLocation to
// rewrite, so a 308 here carries a Location equal to the URL just requested.
// requests replays a 308 with the same body, so the client walks that circle
// until it gives up with TooManyRedirects -- and if it ever did land, it would
// be moving the repository that now sits at the *new* name, which the caller
// never asked for. DELETE /api/repos/delete answers a moved repository the
// same way, for the same two reasons.
func TestHFMoveRepo_OldNameIsNotFoundRatherThanARedirect(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "foo", "model")
	f.org("acme", f.alice) // alice is admin of "acme" too, so this completes at once
	tok := f.token(f.alice, "write")

	if got := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "acme/foo", "type": "model",
	}).status(); got != 200 {
		t.Fatalf("first move status = %d, want 200", got)
	}

	// The same call again, still naming the old location.
	resp := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "alice/foo", "toRepo": "acme/bar", "type": "model",
	})
	if resp.status() != 404 {
		t.Fatalf("move from the old name = %d, body = %s, want 404",
			resp.status(), resp.rec.Body.String())
	}
	if loc := resp.rec.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want none: a redirect on this route points at itself", loc)
	}
	// The header huggingface_hub needs to raise RepositoryNotFoundError rather
	// than a bare HfHubHTTPError.
	if got := resp.rec.Header().Get("X-Error-Code"); got != "RepoNotFound" {
		t.Fatalf("X-Error-Code = %q, want RepoNotFound", got)
	}

	// Nothing moved on the strength of the old name: the repository is where
	// the first call put it, and the second call's destination was never made.
	ctx := context.Background()
	if r, err := f.st.GetRepo(ctx, "model", "acme", "foo"); err != nil {
		t.Fatalf("repository should still be at acme/foo: %v", err)
	} else if r.Name != "foo" {
		t.Fatalf("repository name = %q, want foo", r.Name)
	}
	if _, err := f.st.GetRepo(ctx, "model", "acme", "bar"); err == nil {
		t.Fatal("the second move went through against the old name")
	}
}

// Cancelling a transfer is the same authority as starting one
// (docs/dev/repo-transfer-design.md §7): admin on the source namespace.
//
// It used to stop at write access, which under an organisation is a strictly
// larger set -- startTransfer refuses a `write` member outright
// (TestHFMoveRepo_ForbiddenForOrgWriteMember), so a member who could not have
// filed the request could still withdraw the admin's.
func TestCancelTransfer_RequiresAdminOnTheSourceNamespace(t *testing.T) {
	f := newTransferFixture(t)
	acme := f.org("acme", f.alice) // alice: org admin
	f.addOrgMember(acme.ID, f.bob.ID, "write")
	f.repo("acme", "thing", "model")

	aliceTok, bobTok := f.token(f.alice, "write"), f.token(f.bob, "write")

	// alice files a request nobody has accepted yet: she holds no role in
	// bob's personal namespace, so it waits for him.
	resp := f.do("POST", "/api/v1/repos/model/acme/thing/transfer", aliceTok,
		apitypes.RepoTransferRequest{Namespace: "bob"})
	if resp.status() != 202 {
		t.Fatalf("start transfer = %d, body = %s, want 202 (pending)",
			resp.status(), resp.rec.Body.String())
	}

	// bob may write to acme, and that is not enough.
	cancel := f.do("DELETE", "/api/v1/repos/model/acme/thing/transfer", bobTok, nil)
	if cancel.status() != 403 {
		t.Fatalf("write member cancelling = %d, body = %s, want 403",
			cancel.status(), cancel.rec.Body.String())
	}
	if _, err := f.st.PendingRepoTransfer(context.Background(), f.mustRepo("model", "acme", "thing").ID); err != nil {
		t.Fatalf("the request should still be pending after the refused cancel: %v", err)
	}

	// The person who filed it still can.
	if got := f.do("DELETE", "/api/v1/repos/model/acme/thing/transfer", aliceTok, nil).status(); got != 204 {
		t.Fatalf("org admin cancelling = %d, want 204", got)
	}
	if _, err := f.st.PendingRepoTransfer(context.Background(), f.mustRepo("model", "acme", "thing").ID); err == nil {
		t.Fatal("the transfer is still pending after the admin cancelled it")
	}
}

// mustRepo reads a repository straight out of the store, for the assertions
// that are about stored state rather than about a response.
func (f *transferFixture) mustRepo(kind, ns, name string) *store.Repo {
	f.t.Helper()
	r, err := f.st.GetRepo(context.Background(), kind, ns, name)
	if err != nil {
		f.t.Fatalf("get repo %s/%s: %v", ns, name, err)
	}
	return r
}
