package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"

	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// TestHFDeleteRepoDefaultsNamespaceToCaller covers the same namespace
// fallback handleHFCreateRepo already has: huggingface_hub's
// HfApi.delete_repo("myrepo") (no "/" in the repo id) sends
// {"name": "myrepo", "organization": null}, and the caller's own namespace
// is implied. Before this fix handleHFDeleteRepo skipped the fallback that
// handleHFCreateRepo has, so every such delete looked up a repo in
// namespace "" and 404'd -- create worked but delete never did.
func TestHFDeleteRepoDefaultsNamespaceToCaller(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "myrepo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("DELETE", "/api/repos/delete", tok, map[string]any{
		"type": "model", "name": "myrepo", "organization": nil,
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	if r := f.do("GET", "/api/v1/repos/model/alice/myrepo", tok, nil); r.status() != 404 {
		t.Fatalf("repo still resolves after delete: status = %d, body = %s", r.status(), r.rec.Body.String())
	}
}

// TestHFDeleteRepoRejectsOtherNamespace makes sure the fallback above did not
// loosen the existing admin check: naming someone else's namespace
// explicitly must still be refused, exactly as before this fix.
func TestHFDeleteRepoRejectsOtherNamespace(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("bob", "other", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("DELETE", "/api/repos/delete", tok, map[string]any{
		"type": "model", "name": "bob/other",
	})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403, body = %s", resp.status(), resp.rec.Body.String())
	}

	// The repository must still be there.
	if r := f.do("GET", "/api/v1/repos/model/bob/other", "", nil); r.status() != 200 {
		t.Fatalf("repo missing after rejected delete: status = %d, body = %s", r.status(), r.rec.Body.String())
	}
}

// TestSSHCloneURL pins the URL shape documented in docs/users/guides/git.md.
// The host comes from TF_PUBLIC_URL, the port from TF_SSH_PUBLIC_PORT when a
// deployment remaps the listener and from TF_SSH_ADDR otherwise, and the whole
// thing is empty when the listener is off, so the UI can tell "no SSH here"
// from "SSH on the default port".
func TestSSHCloneURL(t *testing.T) {
	repo := &store.Repo{Kind: "dataset", Namespace: "admin", Name: "imdb-reviews"}
	model := &store.Repo{Kind: "model", Namespace: "acme", Name: "sentiment-base"}

	cases := []struct {
		name       string
		enabled    bool
		publicURL  string
		sshAddr    string
		publicPort string
		repo       *store.Repo
		want       string
	}{
		{
			name: "disabled", enabled: false,
			publicURL: "http://localhost:8080", sshAddr: ":2222", repo: repo, want: "",
		},
		{
			name:      "listen address contributes only its port",
			enabled:   true,
			publicURL: "http://localhost:8080", sshAddr: "0.0.0.0:2222", repo: repo,
			want: "ssh://git@localhost:2222/datasets/admin/imdb-reviews.git",
		},
		{
			name: "models sit at the root", enabled: true,
			publicURL: "https://hub.example.com", sshAddr: ":2222", repo: model,
			want: "ssh://git@hub.example.com:2222/models/acme/sentiment-base.git",
		},
		{
			name: "port 22 stays implicit", enabled: true,
			publicURL: "https://hub.example.com", sshAddr: ":22", repo: repo,
			want: "ssh://git@hub.example.com/datasets/admin/imdb-reviews.git",
		},
		{
			name: "http port is dropped, not reused", enabled: true,
			publicURL: "http://192.168.1.10:8080", sshAddr: ":2222", repo: repo,
			want: "ssh://git@192.168.1.10:2222/datasets/admin/imdb-reviews.git",
		},
		{
			name: "address with no port falls back to the implicit 22", enabled: true,
			publicURL: "http://localhost:8080", sshAddr: "", repo: repo,
			want: "ssh://git@localhost/datasets/admin/imdb-reviews.git",
		},
		// A remapped listener: the container binds 2222, the world dials
		// 22022. Advertising the listen port here would hand out a URL that
		// does not connect.
		{
			name: "the advertised port wins over the listen port", enabled: true,
			publicURL: "https://hub.example.com", sshAddr: ":2222", publicPort: "22022",
			repo: repo,
			want: "ssh://git@hub.example.com:22022/datasets/admin/imdb-reviews.git",
		},
		{
			name: "an advertised port of 22 is still implicit", enabled: true,
			publicURL: "https://hub.example.com", sshAddr: ":2222", publicPort: "22",
			repo: repo,
			want: "ssh://git@hub.example.com/datasets/admin/imdb-reviews.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{
				PublicURL: tc.publicURL, SSHEnabled: tc.enabled, SSHAddr: tc.sshAddr,
				SSHPublicPort: tc.publicPort,
			}}
			if got := s.sshCloneURL(tc.repo); got != tc.want {
				t.Fatalf("sshCloneURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCreateRepoForeignNamespaceIsForbidden pins the status of the one
// createRepo failure that is not about the request body. Naming a namespace
// you may not write to used to be folded into inputError and answered 400
// bad_request, which reads as "fix your request" for something no request can
// fix; every other permission failure in this package (loadRepoForDelete,
// requireOrgRole, loadRepoForWriteAllowArchived) says 403, and
// docs/dev/api-contract.md distinguishes the two.
func TestCreateRepoForeignNamespaceIsForbidden(t *testing.T) {
	f := newTransferFixture(t)
	tok := f.token(f.alice, "write")

	for _, c := range []struct {
		what, path string
		body       any
	}{
		{"UI", "/api/v1/repos", map[string]any{"kind": "model", "namespace": "bob", "name": "x"}},
		{"HF", "/api/repos/create", map[string]any{"type": "model", "name": "bob/x"}},
	} {
		resp := f.do("POST", c.path, tok, c.body)
		if resp.status() != 403 {
			t.Fatalf("%s create in bob's namespace: status = %d, want 403, body = %s",
				c.what, resp.status(), resp.rec.Body.String())
		}
	}

	// A namespace that is not there at all stays a 400: its absence is a
	// fault in the request, and existence is public information anyway
	// (GET /api/v1/namespaces/{ns} answers it unauthenticated).
	resp := f.do("POST", "/api/v1/repos", tok, map[string]any{
		"kind": "model", "namespace": "nobody", "name": "x",
	})
	if resp.status() != 400 {
		t.Fatalf("create in a namespace that does not exist: status = %d, want 400, body = %s",
			resp.status(), resp.rec.Body.String())
	}
}

// failEnqueuer stands in for a syncer whose queue cannot be written to --
// the database is down, or the jobs table is locked.
type failEnqueuer struct {
	st          *store.Store
	storagePath string
	calls       int
}

func (e *failEnqueuer) Enqueue(ctx context.Context, repoID int64, _, _, _ string) error {
	e.calls++
	// Read the path while the row is still there, so the test can check the
	// bare repository was cleaned up too.
	if r, err := e.st.GetRepoByID(ctx, repoID); err == nil {
		e.storagePath = r.StoragePath
	}
	return errors.New("sync queue unavailable")
}

// TestCreateRepoRollsBackWhenTheIndexJobCannotBeQueued covers the third way
// createRepo can fail after it has started writing. `git init` and the initial
// commit both roll back; queuing the first index job did not, so a 500 left a
// repositories row, a bare directory and a commit behind -- and the client's
// retry then collided with the name it had just failed to create. The job is
// also the only one that will ever cover the initial commit: the next push
// enqueues a diff rooted at it (syncer.changedPaths), so the seeded README.md
// and .gitattributes would never be indexed.
func TestCreateRepoRollsBackWhenTheIndexJobCannotBeQueued(t *testing.T) {
	f := newTransferFixture(t)
	enq := &failEnqueuer{st: f.st}
	f.s.sync = enq
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/repos", tok, map[string]any{
		"kind": "model", "namespace": "alice", "name": "half-made",
	})
	if resp.status() != 500 {
		t.Fatalf("create with a broken queue: status = %d, want 500, body = %s",
			resp.status(), resp.rec.Body.String())
	}
	if enq.calls != 1 {
		t.Fatalf("enqueue called %d times, want 1", enq.calls)
	}

	ctx := context.Background()
	if _, err := f.st.GetRepo(ctx, "model", "alice", "half-made"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("repository row survived the failed create: err = %v", err)
	}
	if enq.storagePath == "" {
		t.Fatal("test could not observe the storage path")
	}
	if f.git.Exists(enq.storagePath) {
		t.Fatalf("bare repository %s survived the failed create", enq.storagePath)
	}

	// And the name is free again, so the obvious retry works.
	f.s.sync = noopEnqueuer{}
	if retry := f.do("POST", "/api/v1/repos", tok, map[string]any{
		"kind": "model", "namespace": "alice", "name": "half-made",
	}); retry.status() != 200 {
		t.Fatalf("retry after a failed create: status = %d, want 200, body = %s",
			retry.status(), retry.rec.Body.String())
	}
}

// cancellingEnqueuer fails the way a client hanging up mid-request does: the
// context is cancelled, and the queue call fails because of it.
type cancellingEnqueuer struct{ cancel context.CancelFunc }

func (e *cancellingEnqueuer) Enqueue(ctx context.Context, _ int64, _, _, _ string) error {
	e.cancel()
	return ctx.Err()
}

// TestCreateRepoRollsBackOnACancelledRequest is the other half of the rollback
// above. The most likely reason the queue call fails is that the request's
// context died, and a rollback running on that same context deletes nothing --
// while git.Remove, which takes no context, still succeeds. That combination
// is worse than no rollback at all: the name stays taken by a row whose bare
// repository is gone, so the retry gets the 409 the rollback exists to avoid
// and the repository it names can never be opened.
func TestCreateRepoRollsBackOnACancelledRequest(t *testing.T) {
	f := newTransferFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.s.sync = &cancellingEnqueuer{cancel: cancel}

	repo, err := f.s.createRepo(ctx, f.alice, "model", "alice", "cut-off", "")
	if err == nil {
		t.Fatalf("createRepo on a cancelled request returned %+v, want an error", repo)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("createRepo error = %v, want it to wrap context.Canceled", err)
	}

	if _, err := f.st.GetRepo(context.Background(), "model", "alice", "cut-off"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("repository row survived a cancelled create: err = %v", err)
	}

	// The name is free again, which is the whole point.
	f.s.sync = noopEnqueuer{}
	if _, err := f.s.createRepo(context.Background(), f.alice, "model", "alice", "cut-off", ""); err != nil {
		t.Fatalf("re-creating the rolled-back repository: %v", err)
	}
}

// --------------------------------------------- PATCH: rename / description
//
// The default_branch half of this endpoint is covered in default_branch_test.go;
// these are the two fields that were added next to it. They reuse that file's
// fixture (a real Server over real HTTP) rather than standing up a third one.

// TestUpdateRepo_RenameLeavesARedirect is the reason a rename may go through
// PATCH at all: it is the *same* move a transfer performs, minus the change of
// owner, so everything a transfer leaves behind has to be left behind here too
// -- above all the redirect, without which every existing clone URL, model
// card reference and bookmark 404s.
func TestUpdateRepo_RenameLeavesARedirect(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{Name: strPtr("bar")})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if body.Repo.Name != "bar" || body.Repo.FullName != "alice/bar" {
		t.Fatalf("renamed repo = %q / %q, want bar / alice/bar", body.Repo.Name, body.Repo.FullName)
	}

	if resp := f.do("GET", "/api/v1/repos/model/alice/bar", tok, nil); resp.status() != 200 {
		t.Fatalf("new name status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	// The old name answers with the repo_moved shape the UI follows.
	old := f.do("GET", "/api/v1/repos/model/alice/foo", tok, nil)
	if old.status() != 404 {
		t.Fatalf("old name status = %d, want 404; body = %s", old.status(), old.rec.Body.String())
	}
	var errBody apitypes.ApiErrorBody
	old.json(t, &errBody)
	if errBody.Error.Type != "repo_moved" {
		t.Fatalf("old name error type = %q, want repo_moved", errBody.Error.Type)
	}
	if errBody.Error.MovedTo == nil || errBody.Error.MovedTo.Namespace != "alice" || errBody.Error.MovedTo.Name != "bar" {
		t.Fatalf("moved_to = %+v, want alice/bar", errBody.Error.MovedTo)
	}
}

// A rename never becomes a *takeover*: the name has to be free, and a taken
// one is the same 409 a transfer into an occupied name answers with.
func TestUpdateRepo_RenameConflictIs409(t *testing.T) {
	f := newDefaultBranchFixture(t)
	f.repo("alice", "foo", "model")
	f.repo("alice", "bar", "model")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{Name: strPtr("bar")})
	if resp.status() != 409 {
		t.Fatalf("status = %d, want 409; body = %s", resp.status(), resp.rec.Body.String())
	}
	// Both repositories are still where they were.
	if resp := f.do("GET", "/api/v1/repos/model/alice/foo", tok, nil); resp.status() != 200 {
		t.Fatalf("source status = %d after a refused rename", resp.status())
	}
}

// Renaming to the name it already has is a no-op rather than the ErrConflict
// store.TransferRepo reports for a move that goes nowhere.
func TestUpdateRepo_RenameToTheSameNameIsANoop(t *testing.T) {
	f := newDefaultBranchFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{Name: strPtr("foo")})
	if resp.status() != 200 {
		t.Fatalf("status = %d, want 200; body = %s", resp.status(), resp.rec.Body.String())
	}
}

func TestUpdateRepo_RenameValidatesTheName(t *testing.T) {
	f := newDefaultBranchFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	for _, name := range []string{"", "bad name", "-leading", "has/slash", "repo.git", strings.Repeat("x", 97)} {
		resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{Name: strPtr(name)})
		if resp.status() != 400 {
			t.Errorf("name %q: status = %d, want 400; body = %s", name, resp.status(), resp.rec.Body.String())
		}
	}
	// Nothing was renamed along the way.
	if resp := f.do("GET", "/api/v1/repos/model/alice/foo", tok, nil); resp.status() != 200 {
		t.Fatalf("repo status = %d after refused renames", resp.status())
	}
}

// The same namespace-admin bar the rest of this endpoint applies: a write
// member may push, but not rename the repository out from under everyone's
// bookmarks.
func TestUpdateRepo_RenameRequiresNamespaceAdmin(t *testing.T) {
	f := newDefaultBranchFixture(t)
	ns, err := f.st.CreateOrg(context.Background(), "acme", f.alice.ID, store.OrgUpdate{})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	f.repo("acme", "foo", "model")
	f.addOrgMember(ns.ID, f.bob.ID, "write")

	resp := f.patch("model", "acme", "foo", f.token(f.bob, "write"), apitypes.RepoUpdateRequest{Name: strPtr("bar")})
	if resp.status() != 403 {
		t.Fatalf("write member status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
	resp = f.patch("model", "acme", "foo", f.token(f.alice, "write"), apitypes.RepoUpdateRequest{Name: strPtr("bar")})
	if resp.status() != 200 {
		t.Fatalf("org admin status = %d, want 200; body = %s", resp.status(), resp.rec.Body.String())
	}
}

func TestUpdateRepo_RenameRejectsWhenArchived(t *testing.T) {
	f := newDefaultBranchFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")
	if resp := f.do("POST", "/api/v1/repos/model/alice/foo/archive", tok, nil); resp.status() != 200 {
		t.Fatalf("archive status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{Name: strPtr("bar")})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
	}
}

// description is editable after creation, and clearable. What it is *not* is
// stronger than the README card: store.UpdateRepoIndex still overwrites it on
// the next push of a card that carries one (covered in
// internal/store/repos_test.go).
func TestUpdateRepo_SetsAndClearsDescription(t *testing.T) {
	f := newDefaultBranchFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{Description: strPtr("  a better summary  ")})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if body.Repo.Description != "a better summary" {
		t.Fatalf("description = %q, want it trimmed and stored", body.Repo.Description)
	}

	resp = f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{Description: strPtr("")})
	if resp.status() != 200 {
		t.Fatalf("clear status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	resp.json(t, &body)
	if body.Repo.Description != "" {
		t.Fatalf("description = %q, want it cleared", body.Repo.Description)
	}
}

func TestUpdateRepo_RejectsAnOversizedDescription(t *testing.T) {
	f := newDefaultBranchFixture(t)
	f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok,
		apitypes.RepoUpdateRequest{Description: strPtr(strings.Repeat("あ", maxDescriptionRunes+1))})
	if resp.status() != 400 {
		t.Fatalf("status = %d, want 400; body = %s", resp.status(), resp.rec.Body.String())
	}
}

// One request may carry all three fields, and the response describes the
// repository as it stands after every one of them landed.
func TestUpdateRepo_AppliesEveryFieldInOneRequest(t *testing.T) {
	f := newDefaultBranchFixture(t)
	r := f.repo("alice", "foo", "model")
	f.commit(r, "main", "README.md", "hello")
	f.commit(r, "release", "VERSION", "1.0")
	tok := f.token(f.alice, "write")

	resp := f.patch("model", "alice", "foo", tok, apitypes.RepoUpdateRequest{
		DefaultBranch: strPtr("release"), Name: strPtr("bar"), Description: strPtr("everything at once"),
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.RepoDetailResponse
	resp.json(t, &body)
	if body.Repo.Name != "bar" || body.Repo.DefaultBranch != "release" || body.Repo.Description != "everything at once" {
		t.Fatalf("repo = %+v, want bar / release / everything at once", body.Repo.RepoSummary)
	}
}

// PATCH /api/v1/repos/{kind}/{ns}/{name} applies its three fields as three
// separate writes. A rename to a name the namespace already holds used to be
// caught only inside resolveTransferTarget -- after the description had
// already been committed -- so the caller got a 409 *and* a change they had
// asked for as part of a request that failed.
func TestUpdateRepo_RefusedRenameLeavesTheOtherFieldsAlone(t *testing.T) {
	f := newTransferFixture(t)
	r := f.repo("alice", "foo", "model")
	f.repo("alice", "taken", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("PATCH", "/api/v1/repos/model/alice/foo", tok, map[string]any{
		"description": "a description nobody asked to keep",
		"name":        "taken",
	})
	if resp.status() != 409 {
		t.Fatalf("status = %d, body = %s, want 409", resp.status(), resp.rec.Body.String())
	}

	stored, err := f.st.GetRepoByID(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if stored.Description != "desc" {
		t.Errorf("description = %q, want the original %q: a refused request committed it anyway",
			stored.Description, "desc")
	}
	if stored.Name != "foo" {
		t.Errorf("name = %q, want foo", stored.Name)
	}
}

// The other half of the same guarantee: when nothing is in the way, both
// fields still land and the rename still leaves a redirect behind.
func TestUpdateRepo_RenameAndDescribeTogether(t *testing.T) {
	f := newTransferFixture(t)
	r := f.repo("alice", "foo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("PATCH", "/api/v1/repos/model/alice/foo", tok, map[string]any{
		"description": "now with a description",
		"name":        "bar",
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	stored, err := f.st.GetRepoByID(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if stored.Name != "bar" || stored.Description != "now with a description" {
		t.Fatalf("repo = %s/%s %q, want alice/bar with the new description",
			stored.Namespace, stored.Name, stored.Description)
	}
	if _, err := f.st.ResolveRepoRedirect(context.Background(), "model", "alice", "foo"); err != nil {
		t.Errorf("the old name should still redirect after a rename: %v", err)
	}
}
