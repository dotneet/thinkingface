// Tests for repository-restricted access tokens (docs/dev/agent-features.md §3):
// minting one, and what it may and may not do once minted. Driven over real
// HTTP against a real Server, with archiveFixture's plumbing.

package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// mintToken creates a token over POST /api/v1/tokens with the given
// credential and returns the response.
func (f *archiveFixture) mintToken(auth string, body map[string]any) (apitypes.CreateTokenResponse, response) {
	f.t.Helper()
	resp := f.do("POST", "/api/v1/tokens", auth, body)
	var out apitypes.CreateTokenResponse
	if resp.status() == http.StatusOK {
		resp.json(f.t, &out)
	}
	return out, resp
}

// restrictedToken mints a write token for alice restricted to repos, through
// the HTTP handler, and fails the test if that does not work.
func (f *archiveFixture) restrictedToken(auth string, repos ...string) apitypes.CreateTokenResponse {
	f.t.Helper()
	out, resp := f.mintToken(auth, map[string]any{"name": "agent", "scope": "write", "repos": repos})
	if resp.status() != http.StatusOK {
		f.t.Fatalf("mint restricted token: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
	return out
}

// commitFile puts one file on main so branch, tag and squash have a commit to
// work from.
func (f *archiveFixture) commitFile(repo *store.Repo, path string) {
	f.t.Helper()
	g, err := f.git.Open(repo.StoragePath)
	if err != nil {
		f.t.Fatalf("open git: %v", err)
	}
	if _, _, err := g.Commit(gitrepo.CommitRequest{
		Branch: "main", Message: "add " + path,
		Author: gitrepo.Signature{Name: "t", Email: "t@example.com", When: time.Now()},
		Ops:    []gitrepo.Op{{Kind: gitrepo.OpAdd, Path: path, Data: []byte("hello\n")}},
	}); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
}

type restrictCase struct {
	name   string
	method string
	// path has {model} and {dataset} placeholders for the repository under
	// test, and {model_id} for the model's numeric id.
	path string
	body any
}

// repoWriteCases are the repository-scoped content writes: allowed on a
// listed repository, token_restricted on any other.
var repoWriteCases = []restrictCase{
	{"hf preupload", "POST", "/api/models/alice/{model}/preupload/main", map[string]any{"files": []any{}}},
	{"hf commit", "POST", "/api/models/alice/{model}/commit/main", nil},
	{"hf create branch", "POST", "/api/models/alice/{model}/branch/dev", nil},
	{"hf create tag", "POST", "/api/models/alice/{model}/tag/main", map[string]any{"tag": "v1"}},
	{"hf super-squash", "POST", "/api/models/alice/{model}/super-squash/main", nil},
	{"web edit", "PUT", "/api/v1/edit/model/alice/{model}/main/notes.txt", map[string]any{"content": "hi\n"}},
	{"web delete file", "DELETE", "/api/v1/edit/model/alice/{model}/main/README.md", nil},
	{"web rename file", "POST", "/api/v1/rename/model/alice/{model}/main/README.md", map[string]any{"to": "README2.md"}},
	{"git receive-pack advertise", "GET", "/models/alice/{model}/info/refs?service=git-receive-pack", nil},
	{"lfs upload batch", "POST", "/models/alice/{model}/info/lfs/objects/batch",
		map[string]any{"operation": "upload", "objects": []any{}}},
	{"lfs verify", "POST", "/models/alice/{model}/info/lfs/objects/verify", "not an object"},
	{"experiment ingest", "POST", "/api/v1/experiments/alice/{dataset}/p1/log",
		map[string]any{"run": "r1", "points": []any{}}},
	{"experiment finish", "POST", "/api/v1/experiments/alice/{dataset}/p1/finish", map[string]any{"run": "r1"}},
	{"run annotation", "PATCH", "/api/v1/experiments/alice/{dataset}/p1/runs/r1", map[string]any{"archived": true}},
	{"run delete", "DELETE", "/api/v1/experiments/alice/{dataset}/p1/runs/r1", nil},
	{"project notes", "PUT", "/api/v1/experiments/alice/{dataset}/p1/notes", map[string]any{"content": "# notes\n"}},
	{"metric goals", "PATCH", "/api/v1/experiments/alice/{dataset}/p1", map[string]any{"metric_goals": map[string]any{"loss": "min"}}},
}

// repoAdminCases are the administrative repository operations: refused to a
// restricted token even on a repository it lists.
var repoAdminCases = []restrictCase{
	{"delete", "DELETE", "/api/v1/repos/model/alice/{model}", nil},
	{"hf delete", "DELETE", "/api/repos/delete", map[string]any{"name": "alice/{model}", "type": "model"}},
	{"settings", "PATCH", "/api/v1/repos/model/alice/{model}", map[string]any{"description": "x"}},
	{"rename", "PATCH", "/api/v1/repos/model/alice/{model}", map[string]any{"name": "renamed"}},
	{"archive", "POST", "/api/v1/repos/model/alice/{model}/archive", nil},
	{"transfer", "POST", "/api/v1/repos/model/alice/{model}/transfer", map[string]any{"namespace": "alice", "name": "moved"}},
	{"cancel transfer", "DELETE", "/api/v1/repos/model/alice/{model}/transfer", nil},
	{"hf move", "POST", "/api/repos/move",
		map[string]any{"fromRepo": "alice/{model}", "toRepo": "alice/moved", "type": "model"}},
}

// accountCases are account-level writes: refused to a restricted token
// outright.
var accountCases = []restrictCase{
	{"create repo", "POST", "/api/v1/repos", map[string]any{"kind": "model", "namespace": "alice", "name": "fresh"}},
	{"hf create repo", "POST", "/api/repos/create", map[string]any{"name": "fresh", "type": "model"}},
	{"mint token", "POST", "/api/v1/tokens", map[string]any{"name": "more", "scope": "write"}},
	{"revoke token", "DELETE", "/api/v1/tokens/1", nil},
	{"add ssh key", "POST", "/api/v1/me/ssh-keys", map[string]any{"title": "k", "key": "ssh-ed25519 AAAA"}},
	{"profile", "PATCH", "/api/v1/me/profile", map[string]any{"display_name": "Alice"}},
	{"password", "PATCH", "/api/v1/me/password", map[string]any{"current_password": "x", "new_password": "yyyyyyyyyy"}},
	{"create org", "POST", "/api/v1/orgs", map[string]any{"name": "acme"}},
	{"create webhook", "POST", "/api/v1/namespaces/alice/webhooks", map[string]any{"url": "http://example.com/hook"}},
	{"accept transfer", "POST", "/api/v1/transfers/1/accept", nil},
	{"reject transfer", "POST", "/api/v1/transfers/1/reject", nil},
	{"admin create user", "POST", "/api/v1/admin/users", map[string]any{"username": "zed"}},
}

func (c restrictCase) resolve(model, dataset string, modelID int64) (string, any) {
	rep := strings.NewReplacer("{model}", model, "{dataset}", dataset, "{model_id}", strconv.FormatInt(modelID, 10))
	body := c.body
	if m, ok := c.body.(map[string]any); ok {
		copied := make(map[string]any, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				v = rep.Replace(s)
			}
			copied[k] = v
		}
		body = copied
	}
	return rep.Replace(c.path), body
}

func TestRestrictedToken_WritePaths(t *testing.T) {
	f := newArchiveFixture(t)
	allowed := f.repo("alice", "allowed", "model")
	other := f.repo("alice", "other", "model")
	f.repo("alice", "exp", "dataset")
	f.repo("alice", "exp-other", "dataset")
	f.commitFile(allowed, "README.md")
	f.commitFile(other, "README.md")
	plain := f.token(f.alice, "write")

	minted := f.restrictedToken(plain, "models/alice/allowed", "datasets/alice/exp")
	tok := minted.Token
	if want := []string{"models/alice/allowed", "datasets/alice/exp"}; strings.Join(minted.Repos, ",") != strings.Join(want, ",") {
		t.Fatalf("minted repos = %v, want %v", minted.Repos, want)
	}

	isRefusal := func(resp response) bool {
		return resp.status() == http.StatusUnauthorized || resp.status() == http.StatusForbidden
	}

	for _, tc := range repoWriteCases {
		t.Run("listed/"+tc.name, func(t *testing.T) {
			path, body := tc.resolve("allowed", "exp", allowed.ID)
			resp := f.do(tc.method, path, tok, body)
			t.Logf("%s %s -> %d", tc.method, path, resp.status())
			if isRefusal(resp) {
				t.Fatalf("status = %d on a listed repository; body = %s", resp.status(), resp.rec.Body.String())
			}
		})
		t.Run("unlisted/"+tc.name, func(t *testing.T) {
			path, body := tc.resolve("other", "exp-other", other.ID)
			resp := f.do(tc.method, path, tok, body)
			if resp.status() != http.StatusForbidden || errorType(t, resp) != "token_restricted" {
				t.Fatalf("status = %d, want 403 token_restricted; body = %s", resp.status(), resp.rec.Body.String())
			}
		})
	}

	for _, tc := range repoAdminCases {
		t.Run("admin/"+tc.name, func(t *testing.T) {
			path, body := tc.resolve("allowed", "exp", allowed.ID)
			resp := f.do(tc.method, path, tok, body)
			if resp.status() != http.StatusForbidden || errorType(t, resp) != "token_restricted" {
				t.Fatalf("status = %d, want 403 token_restricted; body = %s", resp.status(), resp.rec.Body.String())
			}
		})
	}

	for _, tc := range accountCases {
		t.Run("account/"+tc.name, func(t *testing.T) {
			path, body := tc.resolve("allowed", "exp", allowed.ID)
			resp := f.do(tc.method, path, tok, body)
			if resp.status() != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body = %s", resp.status(), resp.rec.Body.String())
			}
			// The admin endpoints refuse every token as session_required
			// before anything else; every other route names the restriction.
			if got := errorType(t, resp); got != "token_restricted" && !(strings.HasPrefix(path, "/api/v1/admin/") && got == "forbidden") {
				t.Fatalf("error type = %q, want token_restricted; body = %s", got, resp.rec.Body.String())
			}
		})
	}

	// The LFS proxy routes answer 404 to anyone who may not write, so the
	// id space cannot be counted; the restriction must not change that.
	t.Run("lfs verify by id", func(t *testing.T) {
		if resp := f.do("POST", "/api/v1/lfs/"+strconv.FormatInt(other.ID, 10)+"/verify", tok, "x"); resp.status() != http.StatusNotFound {
			t.Fatalf("unlisted: status = %d, want 404; body = %s", resp.status(), resp.rec.Body.String())
		}
		if resp := f.do("POST", "/api/v1/lfs/"+strconv.FormatInt(allowed.ID, 10)+"/verify", tok, "x"); resp.status() != http.StatusBadRequest {
			t.Fatalf("listed: status = %d, want 400 (past the gate, refused on the body); body = %s", resp.status(), resp.rec.Body.String())
		}
	})

	// Reads are exactly an unrestricted token's.
	t.Run("reads", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/repos/model/alice/other",
			"/api/models/alice/other",
			"/api/whoami-v2",
			"/api/v1/tokens",
			"/api/v1/me",
		} {
			if resp := f.do("GET", path, tok, nil); resp.status() != http.StatusOK {
				t.Errorf("GET %s = %d; body = %s", path, resp.status(), resp.rec.Body.String())
			}
		}
		var detail apitypes.RepoDetailResponse
		f.do("GET", "/api/v1/repos/model/alice/other", tok, nil).json(t, &detail)
		if detail.Repo.CanWrite || detail.Repo.CanAdmin {
			t.Errorf("unlisted repo detail: can_write=%v can_admin=%v, want both false", detail.Repo.CanWrite, detail.Repo.CanAdmin)
		}
		f.do("GET", "/api/v1/repos/model/alice/allowed", tok, nil).json(t, &detail)
		if !detail.Repo.CanWrite || detail.Repo.CanAdmin {
			t.Errorf("listed repo detail: can_write=%v can_admin=%v, want true/false", detail.Repo.CanWrite, detail.Repo.CanAdmin)
		}
	})

	// create_repo(exist_ok=True) on a listed repository is the one "create"
	// that answers as it would for anyone: 409 with the url, which
	// huggingface_hub swallows. On an unlisted one it is refused like a
	// genuine create, so the answer says nothing about repositories outside
	// the list.
	t.Run("hf create_repo exist_ok", func(t *testing.T) {
		resp := f.do("POST", "/api/repos/create", tok, map[string]any{"name": "allowed", "type": "model"})
		if resp.status() != http.StatusConflict || !strings.Contains(resp.rec.Body.String(), "alice/allowed") {
			t.Fatalf("listed: status = %d, want 409 with the url; body = %s", resp.status(), resp.rec.Body.String())
		}
		resp = f.do("POST", "/api/repos/create", tok, map[string]any{"name": "other", "type": "model"})
		if resp.status() != http.StatusForbidden || errorType(t, resp) != "token_restricted" {
			t.Fatalf("unlisted: status = %d, want 403 token_restricted; body = %s", resp.status(), resp.rec.Body.String())
		}
	})

	// The listing shows the restriction; the unrestricted token's list is [].
	t.Run("list", func(t *testing.T) {
		var list apitypes.TokenListResponse
		f.do("GET", "/api/v1/tokens", plain, nil).json(t, &list)
		if !strings.Contains(f.do("GET", "/api/v1/tokens", plain, nil).rec.Body.String(), `"repos":[]`) {
			t.Errorf("an unrestricted token must list repos as [], got %s", f.do("GET", "/api/v1/tokens", plain, nil).rec.Body.String())
		}
		found := false
		for _, it := range list.Items {
			if it.ID == minted.ID {
				found = true
				if len(it.Repos) != 2 {
					t.Errorf("restricted token listed with repos %v", it.Repos)
				}
			}
		}
		if !found {
			t.Error("restricted token missing from the list")
		}
	})
}

// An unrestricted write token is untouched by all of this: the same
// administrative and account-level operations still work.
func TestRestrictedToken_UnrestrictedTokenUnaffected(t *testing.T) {
	f := newArchiveFixture(t)
	repo := f.repo("alice", "allowed", "model")
	f.commitFile(repo, "README.md")
	f.repo("alice", "doomed", "model")
	tok := f.token(f.alice, "write")

	for _, tc := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"PUT", "/api/v1/edit/model/alice/allowed/main/notes.txt", map[string]any{"content": "hi\n"}, http.StatusOK},
		{"PATCH", "/api/v1/repos/model/alice/allowed", map[string]any{"description": "x"}, http.StatusOK},
		{"POST", "/api/v1/repos/model/alice/allowed/archive", nil, http.StatusOK},
		{"DELETE", "/api/v1/repos/model/alice/allowed/archive", nil, http.StatusOK},
		{"DELETE", "/api/v1/repos/model/alice/doomed", nil, http.StatusNoContent},
		{"POST", "/api/v1/repos", map[string]any{"kind": "model", "namespace": "alice", "name": "fresh"}, http.StatusOK},
		{"POST", "/api/v1/tokens", map[string]any{"name": "more", "scope": "write"}, http.StatusOK},
		{"PATCH", "/api/v1/me/profile", map[string]any{"display_name": "Alice"}, http.StatusOK},
	} {
		resp := f.do(tc.method, tc.path, tok, tc.body)
		if resp.status() != tc.want {
			t.Errorf("%s %s = %d, want %d; body = %s", tc.method, tc.path, resp.status(), tc.want, resp.rec.Body.String())
		}
	}
	var detail apitypes.RepoDetailResponse
	f.do("GET", "/api/v1/repos/model/alice/allowed", tok, nil).json(t, &detail)
	if !detail.Repo.CanWrite || !detail.Repo.CanAdmin {
		t.Errorf("unrestricted: can_write=%v can_admin=%v, want both true", detail.Repo.CanWrite, detail.Repo.CanAdmin)
	}
}

// The validation POST /api/v1/tokens applies to a repository list.
func TestRestrictedToken_MintValidation(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "exp", "dataset")
	f.repo("bob", "theirs", "dataset")
	tok := f.token(f.alice, "write")
	readTok := f.token(f.alice, "read")

	many := make([]string, MaxTokenRepos+1)
	for i := range many {
		many[i] = "datasets/alice/exp"
	}
	for _, tc := range []struct {
		name  string
		auth  string
		body  map[string]any
		want  int
		wType string
	}{
		{"bad spelling", tok, map[string]any{"scope": "write", "repos": []string{"alice/exp"}}, 400, "bad_request"},
		{"singular kind", tok, map[string]any{"scope": "write", "repos": []string{"dataset/alice/exp"}}, 400, "bad_request"},
		{"extra segment", tok, map[string]any{"scope": "write", "repos": []string{"datasets/alice/exp/x"}}, 400, "bad_request"},
		{"missing repo", tok, map[string]any{"scope": "write", "repos": []string{"datasets/alice/nope"}}, 400, "bad_request"},
		{"wrong kind", tok, map[string]any{"scope": "write", "repos": []string{"models/alice/exp"}}, 400, "bad_request"},
		{"not writable", tok, map[string]any{"scope": "write", "repos": []string{"datasets/bob/theirs"}}, 403, "forbidden"},
		{"too many", tok, map[string]any{"scope": "write", "repos": many}, 400, "bad_request"},
		{"read scope", tok, map[string]any{"scope": "read", "repos": []string{"datasets/alice/exp"}}, 400, "bad_request"},
		{"read token cannot mint", readTok, map[string]any{"scope": "write", "repos": []string{"datasets/alice/exp"}}, 403, "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, resp := f.mintToken(tc.auth, tc.body)
			if resp.status() != tc.want || errorType(t, resp) != tc.wType {
				t.Fatalf("status = %d (%s), want %d %s; body = %s", resp.status(), errorType(t, resp), tc.want, tc.wType, resp.rec.Body.String())
			}
		})
	}

	// Duplicates collapse; an empty list is unrestricted and answers [].
	out, resp := f.mintToken(tok, map[string]any{"scope": "write", "repos": []string{"datasets/alice/exp", "datasets/alice/exp"}})
	if resp.status() != http.StatusOK || len(out.Repos) != 1 {
		t.Fatalf("duplicate entries: status %d, repos %v", resp.status(), out.Repos)
	}
	_, resp = f.mintToken(tok, map[string]any{"scope": "write", "repos": []string{}})
	if resp.status() != http.StatusOK || !strings.Contains(resp.rec.Body.String(), `"repos":[]`) {
		t.Fatalf("empty list: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
	_, resp = f.mintToken(tok, map[string]any{"scope": "write"})
	if resp.status() != http.StatusOK || !strings.Contains(resp.rec.Body.String(), `"repos":[]`) {
		t.Fatalf("no list: status %d, body %s", resp.status(), resp.rec.Body.String())
	}
}

// The restriction is bound to the repository, not its name: it follows a
// rename, it does not pass to a new repository created at a deleted one's
// name, and a token whose repositories are all gone stays restricted -- to
// nothing -- rather than becoming unrestricted.
func TestRestrictedToken_BoundToRepositoryIdentity(t *testing.T) {
	f := newArchiveFixture(t)
	repo := f.repo("alice", "exp", "model")
	f.commitFile(repo, "README.md")
	plain := f.token(f.alice, "write")
	tok := f.restrictedToken(plain, "models/alice/exp").Token

	edit := func(name string) response {
		return f.do("PUT", "/api/v1/edit/model/alice/"+name+"/main/notes.txt", tok, map[string]any{"content": "hi\n"})
	}
	if resp := edit("exp"); resp.status() != http.StatusOK {
		t.Fatalf("edit before rename: %d %s", resp.status(), resp.rec.Body.String())
	}

	// Renamed by its owner (with an unrestricted token): the grant follows.
	if resp := f.do("PATCH", "/api/v1/repos/model/alice/exp", plain, map[string]any{"name": "exp2"}); resp.status() != http.StatusOK {
		t.Fatalf("rename: %d %s", resp.status(), resp.rec.Body.String())
	}
	if resp := edit("exp2"); resp.status() != http.StatusOK {
		t.Fatalf("edit after rename: %d %s", resp.status(), resp.rec.Body.String())
	}
	var list apitypes.TokenListResponse
	f.do("GET", "/api/v1/tokens", plain, nil).json(t, &list)
	for _, it := range list.Items {
		if it.Name == "agent" && strings.Join(it.Repos, ",") != "models/alice/exp2" {
			t.Errorf("listed repos after rename = %v, want the current name", it.Repos)
		}
	}

	// Transferred to bob: the grant is not authority of its own, and alice
	// can no longer write there, so neither can her token.
	if _, err := f.st.TransferRepo(context.Background(), store.TransferSpec{
		RepoID: repo.ID, ToNamespaceID: mustNamespaceID(t, f.st, "bob"), ActorID: f.admin.ID,
	}); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	resp := f.do("PUT", "/api/v1/edit/model/bob/exp2/main/notes.txt", tok, map[string]any{"content": "hi\n"})
	if resp.status() != http.StatusForbidden || errorType(t, resp) != "forbidden" {
		t.Fatalf("edit after transfer away: %d %s; want 403 forbidden", resp.status(), resp.rec.Body.String())
	}

	// Deleted, and a new repository created at the old name: not listed.
	if err := f.st.DeleteRepo(context.Background(), repo.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	again := f.repo("alice", "exp2", "model")
	f.commitFile(again, "README.md")
	if resp := edit("exp2"); resp.status() != http.StatusForbidden || errorType(t, resp) != "token_restricted" {
		t.Fatalf("edit of a re-created repository: %d %s; want 403 token_restricted", resp.status(), resp.rec.Body.String())
	}
	// And the token is still restricted everywhere else.
	resp = f.do("POST", "/api/v1/repos", tok, map[string]any{"kind": "model", "namespace": "alice", "name": "fresh"})
	if resp.status() != http.StatusForbidden || errorType(t, resp) != "token_restricted" {
		t.Fatalf("create with a token whose repositories are gone: %d %s", resp.status(), resp.rec.Body.String())
	}
	f.do("GET", "/api/v1/tokens", plain, nil).json(t, &list)
	for _, it := range list.Items {
		if it.Name == "agent" && len(it.Repos) == 0 {
			t.Error("a token whose repositories were deleted is listed as unrestricted")
		}
	}
}

func mustNamespaceID(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	n, err := st.GetNamespace(context.Background(), name)
	if err != nil {
		t.Fatalf("namespace %s: %v", name, err)
	}
	return n.ID
}
