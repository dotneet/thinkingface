package api

import (
	"context"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Namespace endpoints (docs/dev/namespace-design.md §12), driven over real HTTP
// against the same fixture the org and transfer tests use.

// getNamespace reads GET /api/v1/namespaces/{ns} as the given token (empty
// for an anonymous caller).
func getNamespace(t *testing.T, f *transferFixture, ns, token string) (int, apitypes.NamespaceProfile) {
	t.Helper()
	resp := f.do("GET", "/api/v1/namespaces/"+ns, token, nil)
	if resp.status() != 200 {
		return resp.status(), apitypes.NamespaceProfile{}
	}
	var body apitypes.NamespaceResponse
	resp.json(t, &body)
	return resp.status(), body.Namespace
}

// markExperiment flips a dataset repository's is_experiment flag, which is
// what the syncer does after indexing a trackio export.
func markExperiment(t *testing.T, f *transferFixture, repo *store.Repo) {
	t.Helper()
	if err := f.st.UpdateRepoIndex(context.Background(), repo.ID, "abc", 1, map[string]any{}, "d", true); err != nil {
		t.Fatalf("mark experiment: %v", err)
	}
}

func TestGetNamespace_UserOrgReservedAndMissing(t *testing.T) {
	f := newTransferFixture(t)
	f.org("acme", f.bob)

	// A freshly registered account has a page, with everything at zero: this
	// is the 404 the old namespace page produced for an empty namespace.
	status, ns := getNamespace(t, f, "alice", "")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if ns.Kind != apitypes.NamespaceKindUser || ns.Name != "alice" {
		t.Fatalf("namespace = %+v, want the user alice", ns)
	}
	if ns.NumModels != 0 || ns.NumDatasets != 0 || ns.NumExperiments != 0 {
		t.Fatalf("counts = %+v, want all zero", ns)
	}
	// The organisation-only fields stay zero for a user namespace.
	if ns.NumMembers != 0 || ns.MembersVisibility != "" {
		t.Fatalf("user namespace = %+v, want no member fields", ns)
	}

	// Counts follow the three tabs, and an experiment repository counts once.
	f.repo("alice", "m1", "model")
	f.repo("alice", "d1", "dataset")
	markExperiment(t, f, f.repo("alice", "runs", "dataset"))
	if _, ns := getNamespace(t, f, "alice", ""); ns.NumModels != 1 || ns.NumDatasets != 1 || ns.NumExperiments != 1 {
		t.Fatalf("counts = %+v, want 1/1/1", ns)
	}

	// An organisation answers on the same endpoint, with its member fields.
	if _, ns := getNamespace(t, f, "acme", ""); ns.Kind != apitypes.NamespaceKindOrg ||
		ns.NumMembers != 1 || ns.MembersVisibility != apitypes.MembersVisibilityMembers {
		t.Fatalf("org namespace = %+v", ns)
	}

	// Case-insensitive, answering with the registered spelling so the UI can
	// redirect to the canonical URL.
	if status, ns := getNamespace(t, f, "Alice", ""); status != 200 || ns.Name != "alice" {
		t.Fatalf("GET /namespaces/Alice = %d %+v, want 200 and name alice", status, ns)
	}

	if status, _ := getNamespace(t, f, "nobody", ""); status != 404 {
		t.Fatalf("missing namespace status = %d, want 404", status)
	}
	// A reserved name nobody holds is a 404 like any other free name (the
	// reserved list only guards creation, docs/dev/namespace-design.md §9).
	for _, name := range []string{"settings", "models", "new"} {
		if status, _ := getNamespace(t, f, name, ""); status != 404 {
			t.Fatalf("reserved name %q status = %d, want 404", name, status)
		}
	}
}

func TestGetNamespace_ViewerRoleAndCanEdit(t *testing.T) {
	f := newTransferFixture(t)
	org := f.org("acme", f.bob)
	f.addOrgMember(org.ID, f.alice.ID, "write")

	cases := []struct {
		name     string
		ns       string
		token    string
		wantRole apitypes.OrgRole
		wantEdit bool
	}{
		{"own user namespace", "alice", f.token(f.alice, "write"), apitypes.OrgRoleAdmin, true},
		{"someone else's user namespace", "bob", f.token(f.alice, "write"), "", false},
		{"org admin", "acme", f.token(f.bob, "write"), apitypes.OrgRoleAdmin, true},
		{"org write member", "acme", f.token(f.alice, "write"), apitypes.OrgRoleWrite, false},
		{"non-member", "acme", f.token(f.mustUser(context.Background(), "carol", false), "write"), "", false},
		// A site admin is an admin everywhere without an org_members row.
		{"site admin", "acme", f.token(f.admin, "write"), apitypes.OrgRoleAdmin, true},
		// ...but can_edit on somebody's *user* namespace stays false: the only
		// profile editor is PATCH /me/profile, which edits one's own row.
		{"site admin on another user", "alice", f.token(f.admin, "write"), apitypes.OrgRoleAdmin, false},
		{"anonymous", "acme", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, ns := getNamespace(t, f, tc.ns, tc.token)
			if status != 200 {
				t.Fatalf("status = %d, want 200", status)
			}
			if ns.ViewerRole != tc.wantRole || ns.CanEdit != tc.wantEdit {
				t.Fatalf("viewer_role = %q, can_edit = %v; want %q / %v",
					ns.ViewerRole, ns.CanEdit, tc.wantRole, tc.wantEdit)
			}
		})
	}
}

func TestUpdateMyProfile(t *testing.T) {
	f := newTransferFixture(t)
	write := f.token(f.alice, "write")

	// Partial update: named fields change, the rest stay empty.
	resp := f.do("PATCH", "/api/v1/me/profile", write, map[string]any{
		"display_name": "Alice A.",
		"website":      "https://alice.example",
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.NamespaceResponse
	resp.json(t, &body)
	if body.Namespace.DisplayName != "Alice A." || body.Namespace.Website != "https://alice.example" {
		t.Fatalf("profile = %+v", body.Namespace)
	}
	if body.Namespace.Name != "alice" || body.Namespace.Description != "" {
		t.Fatalf("update touched more than it was given: %+v", body.Namespace)
	}

	// A second call leaves the first one's fields alone.
	resp = f.do("PATCH", "/api/v1/me/profile", write, map[string]any{"description": "hello"})
	resp.json(t, &body)
	if resp.status() != 200 || body.Namespace.DisplayName != "Alice A." || body.Namespace.Description != "hello" {
		t.Fatalf("second update = %d %+v", resp.status(), body.Namespace)
	}

	// /me reflects it...
	meResp := f.do("GET", "/api/v1/me", write, nil)
	var me apitypes.UserResponse
	meResp.json(t, &me)
	if me.User.DisplayName != "Alice A." || me.User.AvatarURL != "" {
		t.Fatalf("/me user = %+v", me.User)
	}

	// ... and so does whoami-v2's fullname, which is what `hf auth whoami`
	// prints.
	whoResp := f.do("GET", "/api/whoami-v2", write, nil)
	var who map[string]any
	whoResp.json(t, &who)
	if who["fullname"] != "Alice A." || who["name"] != "alice" {
		t.Fatalf("whoami = %+v", who)
	}
	if who["avatarUrl"] != "" {
		t.Fatalf("whoami avatarUrl = %v, want empty", who["avatarUrl"])
	}
	if resp := f.do("PATCH", "/api/v1/me/profile", write,
		map[string]any{"avatar_url": "https://cdn.example/a.png"}); resp.status() != 200 {
		t.Fatalf("set avatar status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	whoResp = f.do("GET", "/api/whoami-v2", write, nil)
	whoResp.json(t, &who)
	if who["avatarUrl"] != "https://cdn.example/a.png" {
		t.Fatalf("whoami avatarUrl = %v", who["avatarUrl"])
	}
}

// Schemes are matched case-insensitively: a pasted "HTTPS://" URL is fine.
func TestUpdateMyProfile_AcceptsUpperCaseScheme(t *testing.T) {
	f := newTransferFixture(t)
	resp := f.do("PATCH", "/api/v1/me/profile", f.token(f.alice, "write"),
		map[string]any{"website": "HTTPS://Alice.Example/Home"})
	if resp.status() != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", resp.status(), resp.rec.Body.String())
	}
}

func TestUpdateMyProfile_Validation(t *testing.T) {
	f := newTransferFixture(t)
	write := f.token(f.alice, "write")

	cases := []struct {
		name string
		body map[string]any
	}{
		// The reason this validation exists: the value would land in an
		// <a href> (docs/dev/namespace-design.md §10).
		{"javascript website", map[string]any{"website": "javascript:alert(1)"}},
		{"javascript avatar", map[string]any{"avatar_url": "javascript:alert(1)"}},
		{"data avatar", map[string]any{"avatar_url": "data:text/html,<script>"}},
		{"scheme-relative website", map[string]any{"website": "//evil.example"}},
		{"display_name too long", map[string]any{"display_name": strings.Repeat("あ", 97)}},
		{"description too long", map[string]any{"description": strings.Repeat("a", 1025)}},
		{"website too long", map[string]any{"website": "https://x.example/" + strings.Repeat("a", 2048)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := f.do("PATCH", "/api/v1/me/profile", write, tc.body)
			if resp.status() != 400 {
				t.Fatalf("status = %d, want 400 (body %s)", resp.status(), resp.rec.Body.String())
			}
		})
	}

	// The limits are inclusive, and counted in runes for the prose fields.
	ok := f.do("PATCH", "/api/v1/me/profile", write, map[string]any{
		"display_name": strings.Repeat("あ", 96),
		"description":  strings.Repeat("a", 1024),
		"website":      "http://plain.example",
	})
	if ok.status() != 200 {
		t.Fatalf("at-the-limit update status = %d, body = %s", ok.status(), ok.rec.Body.String())
	}
	// Clearing a URL is not a scheme violation.
	if cleared := f.do("PATCH", "/api/v1/me/profile", write, map[string]any{"website": ""}); cleared.status() != 200 {
		t.Fatalf("clearing website status = %d", cleared.status())
	}
}

func TestUpdateMyProfile_Authorization(t *testing.T) {
	f := newTransferFixture(t)
	body := map[string]any{"display_name": "nope"}

	if resp := f.do("PATCH", "/api/v1/me/profile", "", body); resp.status() != 401 {
		t.Fatalf("anonymous status = %d, want 401", resp.status())
	}
	// A read-scoped token may read the profile but not write it.
	if resp := f.do("PATCH", "/api/v1/me/profile", f.token(f.alice, "read"), body); resp.status() != 403 {
		t.Fatalf("read token status = %d, want 403", resp.status())
	}
	if _, ns := getNamespace(t, f, "alice", ""); ns.DisplayName != "" {
		t.Fatalf("profile changed despite a refused request: %+v", ns)
	}
}

// TestUpdateOrg_RejectsHostileURLs is the same validation on the
// organisation path, which had none before (docs/dev/namespace-design.md §10).
func TestUpdateOrg_RejectsHostileURLs(t *testing.T) {
	f := newTransferFixture(t)
	f.org("acme", f.alice)
	tok := f.token(f.alice, "write")

	for _, body := range []map[string]any{
		{"website": "javascript:alert(1)"},
		{"avatar_url": "javascript:alert(1)"},
		{"display_name": strings.Repeat("x", 97)},
	} {
		resp := f.do("PATCH", "/api/v1/orgs/acme", tok, body)
		if resp.status() != 400 {
			t.Fatalf("PATCH %v status = %d, want 400 (body %s)", body, resp.status(), resp.rec.Body.String())
		}
	}
	// Creation is validated the same way.
	if resp := f.do("POST", "/api/v1/orgs", tok, map[string]any{
		"name": "toolong", "display_name": strings.Repeat("x", 97),
	}); resp.status() != 400 {
		t.Fatalf("create with an over-long display name = %d", resp.status())
	}
	// A legitimate update still goes through.
	if resp := f.do("PATCH", "/api/v1/orgs/acme", tok, map[string]any{
		"website": "https://acme.example", "avatar_url": "http://acme.example/logo.png",
	}); resp.status() != 200 {
		t.Fatalf("valid update status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
}

func TestListExperiments_AuthorAndTotal(t *testing.T) {
	f := newTransferFixture(t)
	markExperiment(t, f, f.repo("alice", "r1", "dataset"))
	markExperiment(t, f, f.repo("alice", "r2", "dataset"))
	markExperiment(t, f, f.repo("bob", "r3", "dataset"))
	// A plain dataset is not an experiment and must not show up.
	f.repo("alice", "plain", "dataset")

	list := func(query string) apitypes.ExpProjectListResponse {
		t.Helper()
		resp := f.do("GET", "/api/v1/experiments"+query, "", nil)
		if resp.status() != 200 {
			t.Fatalf("GET /experiments%s = %d", query, resp.status())
		}
		var body apitypes.ExpProjectListResponse
		resp.json(t, &body)
		return body
	}

	// No arguments: the previous behaviour, plus total.
	if all := list(""); len(all.Items) != 3 || all.Total != 3 {
		t.Fatalf("unfiltered = %d items, total %d, want 3/3", len(all.Items), all.Total)
	}
	// author filters by namespace, case-insensitively like every other
	// namespace lookup.
	for _, author := range []string{"alice", "ALICE"} {
		got := list("?author=" + author)
		if len(got.Items) != 2 || got.Total != 2 {
			t.Fatalf("author=%s = %d items, total %d, want 2/2", author, len(got.Items), got.Total)
		}
		for _, item := range got.Items {
			if item.Namespace != "alice" {
				t.Fatalf("author=%s returned %s", author, item.FullName)
			}
		}
	}
	// total is the number of matches regardless of the page.
	page := list("?author=alice&limit=1")
	if len(page.Items) != 1 || page.Total != 2 {
		t.Fatalf("limit=1 = %d items, total %d, want 1/2", len(page.Items), page.Total)
	}
	if second := list("?author=alice&limit=1&offset=1"); len(second.Items) != 1 ||
		second.Items[0].FullName == page.Items[0].FullName {
		t.Fatalf("offset=1 returned %+v, want the other repository", second.Items)
	}
	if none := list("?author=nobody"); len(none.Items) != 0 || none.Total != 0 {
		t.Fatalf("author=nobody = %+v", none)
	}
}

func TestHFOverviewEndpoints(t *testing.T) {
	f := newTransferFixture(t)
	org := f.org("acme", f.alice)
	f.addOrgMember(org.ID, f.bob.ID, "read")
	f.repo("alice", "m1", "model")
	f.repo("alice", "d1", "dataset")
	markExperiment(t, f, f.repo("alice", "runs", "dataset"))
	f.repo("acme", "shared", "model")

	tok := f.token(f.alice, "write")
	if resp := f.do("PATCH", "/api/v1/me/profile", tok, map[string]any{
		"display_name": "Alice A.", "description": "hi", "avatar_url": "https://cdn.example/a.png",
	}); resp.status() != 200 {
		t.Fatalf("set profile: %d", resp.status())
	}
	// acme's roster is made public so the unauthenticated overview below can
	// still show the membership. With the default "members" it would not, and
	// deliberately so -- that is TestHFUserOverviewOrgsVisibility's subject;
	// here the point is the shape of the response, not who may see it.
	if resp := f.do("PATCH", "/api/v1/orgs/acme", tok, map[string]any{
		"members_visibility": "public",
	}); resp.status() != 200 {
		t.Fatalf("make acme's roster public: %d", resp.status())
	}

	resp := f.do("GET", "/api/users/alice/overview", "", nil)
	if resp.status() != 200 {
		t.Fatalf("user overview status = %d", resp.status())
	}
	var user map[string]any
	resp.json(t, &user)
	for key, want := range map[string]any{
		"user": "alice", "fullname": "Alice A.", "avatarUrl": "https://cdn.example/a.png",
		"details": "hi", "type": "user", "numModels": 1.0,
		// Experiment repositories are datasets to huggingface_hub, and
		// GET /api/datasets lists them, so the overview counts them too.
		"numDatasets": 2.0, "numSpaces": 0.0, "numLikes": 0.0,
		"numFollowers": 0.0, "numFollowing": 0.0, "isPro": false,
	} {
		if user[key] != want {
			t.Fatalf("user overview %s = %#v, want %#v", key, user[key], want)
		}
	}
	orgs, _ := user["orgs"].([]any)
	if len(orgs) != 1 {
		t.Fatalf("orgs = %#v, want the one membership", user["orgs"])
	}
	if first, _ := orgs[0].(map[string]any); first["name"] != "acme" || first["roleInOrg"] != "admin" {
		t.Fatalf("orgs[0] = %#v", orgs[0])
	}

	resp = f.do("GET", "/api/organizations/acme/overview", "", nil)
	if resp.status() != 200 {
		t.Fatalf("org overview status = %d", resp.status())
	}
	var o map[string]any
	resp.json(t, &o)
	for key, want := range map[string]any{
		"name": "acme", "fullname": "acme", "avatarUrl": "", "details": "",
		"numUsers": 2.0, "numModels": 1.0, "numDatasets": 0.0,
		"numSpaces": 0.0, "numFollowers": 0.0, "isVerified": false,
	} {
		if o[key] != want {
			t.Fatalf("org overview %s = %#v, want %#v", key, o[key], want)
		}
	}

	// The two kinds share one name space, and each endpoint answers only for
	// its own kind.
	if resp := f.do("GET", "/api/users/acme/overview", "", nil); resp.status() != 404 {
		t.Fatalf("user overview of an organisation = %d, want 404", resp.status())
	}
	if resp := f.do("GET", "/api/organizations/alice/overview", "", nil); resp.status() != 404 {
		t.Fatalf("org overview of a user = %d, want 404", resp.status())
	}
	if resp := f.do("GET", "/api/users/nobody/overview", "", nil); resp.status() != 404 {
		t.Fatalf("user overview of a missing account = %d, want 404", resp.status())
	}
	if resp := f.do("GET", "/api/organizations/nobody/overview", "", nil); resp.status() != 404 {
		t.Fatalf("org overview of a missing organisation = %d, want 404", resp.status())
	}
}
