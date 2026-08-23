package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Organisation endpoints and the permission matrix of
// docs/organization-design.md §4, driven over real HTTP against the same
// fixture the transfer tests use.

// orgRoles are the columns of the permission matrix. "site-admin" is the
// users.is_admin flag rather than a membership, which is why it is a role
// here but never appears in a member list.
var orgRoles = []string{"none", "read", "write", "admin", "site-admin"}

// orgFixture is a transferFixture with one organisation ("acme"), one public
// and one private repository in it, and an acting user standing in the role
// under test. bob is always acme's founding admin, so demoting or removing
// the actor never runs into the last-admin rule by accident.
type orgFixture struct {
	*transferFixture
	org   *store.Org
	actor *store.User
	carol *store.User
}

func newOrgFixture(t *testing.T, role string) *orgFixture {
	t.Helper()
	return newOrgFixtureWithConfig(t, role, nil)
}

func newOrgFixtureWithConfig(t *testing.T, role string, tweak func(*config.Config)) *orgFixture {
	t.Helper()
	f := newFixtureWithConfig(t, tweak)
	ctx := context.Background()
	carol := f.mustUser(ctx, "carol", false)
	org := f.org("acme", f.bob)

	actor := f.alice
	switch role {
	case "none":
	case "read", "write", "admin":
		f.addOrgMember(org.ID, f.alice.ID, role)
	case "site-admin":
		actor = f.admin
	default:
		t.Fatalf("unknown role %q", role)
	}
	f.repo("acme", "pub", "model")
	f.repo("acme", "secret", "model")
	return &orgFixture{transferFixture: f, org: org, actor: actor, carol: carol}
}

// call runs one request as the fixture's acting user with a write-scoped
// token.
func (f *orgFixture) call(method, path string, body any) response {
	return f.do(method, path, f.token(f.actor, "write"), body)
}

// TestOrgPermissionMatrix walks docs/organization-design.md §4 operation by
// operation. Each cell gets its own fixture so the destructive rows (delete a
// repository, add a member) cannot leak into the next one.
func TestOrgPermissionMatrix(t *testing.T) {
	cases := []struct {
		op   string
		want map[string]int
		call func(f *orgFixture) response
	}{
		{
			op:   "view the organisation",
			want: map[string]int{"none": 200, "read": 200, "write": 200, "admin": 200, "site-admin": 200},
			call: func(f *orgFixture) response { return f.call("GET", "/api/v1/orgs/acme", nil) },
		},
		{
			// members_visibility defaults to "members".
			op:   "list members",
			want: map[string]int{"none": 403, "read": 200, "write": 200, "admin": 200, "site-admin": 200},
			call: func(f *orgFixture) response { return f.call("GET", "/api/v1/orgs/acme/members", nil) },
		},
		{
			// Repositories carry no visibility of their own, so reading one is
			// open to everybody -- including a caller with no role at all.
			op:   "read a repository",
			want: map[string]int{"none": 200, "read": 200, "write": 200, "admin": 200, "site-admin": 200},
			call: func(f *orgFixture) response { return f.call("GET", "/api/v1/repos/model/acme/secret", nil) },
		},
		{
			op:   "create a repository",
			want: map[string]int{"none": 400, "read": 400, "write": 200, "admin": 200, "site-admin": 200},
			call: func(f *orgFixture) response {
				return f.call("POST", "/api/v1/repos", map[string]any{
					"kind": "model", "namespace": "acme", "name": "fresh",
				})
			},
		},
		{
			// Narrowed from write to admin by this design.
			op:   "delete a repository",
			want: map[string]int{"none": 403, "read": 403, "write": 403, "admin": 204, "site-admin": 204},
			call: func(f *orgFixture) response { return f.call("DELETE", "/api/v1/repos/model/acme/pub", nil) },
		},
		{
			// Narrowed from write to admin by this design.
			op:   "manage webhooks",
			want: map[string]int{"none": 403, "read": 403, "write": 403, "admin": 200, "site-admin": 200},
			call: func(f *orgFixture) response {
				return f.call("GET", "/api/v1/namespaces/acme/webhooks", nil)
			},
		},
		{
			op:   "update the profile and policies",
			want: map[string]int{"none": 403, "read": 403, "write": 403, "admin": 200, "site-admin": 200},
			call: func(f *orgFixture) response {
				return f.call("PATCH", "/api/v1/orgs/acme", map[string]any{"display_name": "Acme"})
			},
		},
		{
			op:   "add a member",
			want: map[string]int{"none": 403, "read": 403, "write": 403, "admin": 201, "site-admin": 201},
			call: func(f *orgFixture) response {
				return f.call("POST", "/api/v1/orgs/acme/members", map[string]any{
					"username": "carol", "role": "write",
				})
			},
		},
		{
			op:   "read the audit log",
			want: map[string]int{"none": 403, "read": 403, "write": 403, "admin": 200, "site-admin": 200},
			call: func(f *orgFixture) response { return f.call("GET", "/api/v1/orgs/acme/audit-log", nil) },
		},
	}

	for _, c := range cases {
		for _, role := range orgRoles {
			t.Run(fmt.Sprintf("%s/%s", strings.ReplaceAll(c.op, " ", "_"), role), func(t *testing.T) {
				f := newOrgFixture(t, role)
				resp := c.call(f)
				if resp.status() != c.want[role] {
					t.Fatalf("%s as %s: status = %d, want %d, body = %s",
						c.op, role, resp.status(), c.want[role], resp.rec.Body.String())
				}
			})
		}
	}
}

// TestOrgRepoIsReadableByAnyone is the inverse of what this used to assert.
// Repositories have no visibility of their own, so an organisation's
// repository answers the whole read surface for a non-member and for an
// anonymous caller alike. Membership governs writing, not reading.
func TestOrgRepoIsReadableByAnyone(t *testing.T) {
	f := newOrgFixture(t, "none")
	tok := f.token(f.alice, "write")
	for _, path := range []string{
		"/api/v1/repos/model/acme/secret",
		"/api/models/acme/secret",
	} {
		if resp := f.do("GET", path, tok, nil); resp.status() != 200 {
			t.Fatalf("GET %s as a non-member = %d, want 200", path, resp.status())
		}
		if resp := f.do("GET", path, "", nil); resp.status() != 200 {
			t.Fatalf("GET %s anonymously = %d, want 200", path, resp.status())
		}
	}
}

func TestOrgReadOnlyTokenCannotWrite(t *testing.T) {
	f := newOrgFixture(t, "admin")
	readTok := f.token(f.alice, "read")

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/orgs", map[string]any{"name": "another"}},
		{"PATCH", "/api/v1/orgs/acme", map[string]any{"display_name": "x"}},
		{"DELETE", "/api/v1/orgs/acme", nil},
		{"POST", "/api/v1/orgs/acme/members", map[string]any{"username": "carol"}},
		{"PATCH", "/api/v1/orgs/acme/members/alice", map[string]any{"role": "read"}},
		{"DELETE", "/api/v1/orgs/acme/members/alice", nil},
	} {
		resp := f.do(c.method, c.path, readTok, c.body)
		if resp.status() != 403 {
			t.Fatalf("%s %s with a read token = %d, want 403", c.method, c.path, resp.status())
		}
	}
	// Reading is still fine with a read token.
	if resp := f.do("GET", "/api/v1/orgs/acme/members", readTok, nil); resp.status() != 200 {
		t.Fatalf("GET members with a read token = %d, want 200", resp.status())
	}
}

func TestOrgCreateAndReservedNames(t *testing.T) {
	f := newOrgFixture(t, "none")
	tok := f.token(f.alice, "write")

	resp := f.do("POST", "/api/v1/orgs", tok, map[string]any{
		"name": "widgets", "display_name": "Widgets Ltd.", "description": "we widget",
	})
	if resp.status() != 201 {
		t.Fatalf("create org status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var created apitypes.OrgResponse
	resp.json(t, &created)
	if created.Org.Name != "widgets" || created.Org.DisplayName != "Widgets Ltd." ||
		created.Org.ViewerRole != apitypes.OrgRoleAdmin || created.Org.NumMembers != 1 {
		t.Fatalf("created org = %+v", created.Org)
	}
	if created.Org.MembersVisibility != apitypes.MembersVisibilityMembers {
		t.Fatalf("created org policies = %+v", created.Org)
	}
	// Creation opens the organisation's audit log.
	logResp := f.do("GET", "/api/v1/orgs/widgets/audit-log", tok, nil)
	var logBody apitypes.OrgAuditLogResponse
	logResp.json(t, &logBody)
	if len(logBody.Items) != 1 || logBody.Items[0].Action != "org.created" ||
		logBody.Items[0].Actor != "alice" || logBody.Items[0].Target != "widgets" {
		t.Fatalf("audit log after create = %+v", logBody.Items)
	}

	// The name is shared with accounts and with other organisations.
	if resp := f.do("POST", "/api/v1/orgs", tok, map[string]any{"name": "widgets"}); resp.status() != 409 {
		t.Fatalf("duplicate org status = %d", resp.status())
	}
	if resp := f.do("POST", "/api/v1/orgs", tok, map[string]any{"name": "bob"}); resp.status() != 409 {
		t.Fatalf("org over a username status = %d", resp.status())
	}

	// Reserved namespace names are refused with their own error type, on
	// organisation creation and on sign-up alike.
	for _, name := range []string{"datasets", "settings", "orgs", "API"} {
		resp := f.do("POST", "/api/v1/orgs", tok, map[string]any{"name": name})
		if resp.status() != 400 {
			t.Fatalf("reserved name %q status = %d", name, resp.status())
		}
		var body apitypes.ApiErrorBody
		resp.json(t, &body)
		if body.Error.Type != "reserved_name" {
			t.Fatalf("reserved name %q error type = %q", name, body.Error.Type)
		}
	}
	signup := f.do("POST", "/api/v1/auth/signup", "", map[string]any{
		"username": "models", "email": "m@example.com", "password": "password123",
	})
	if signup.status() != 400 {
		t.Fatalf("reserved username signup status = %d", signup.status())
	}
	var signupBody apitypes.ApiErrorBody
	signup.json(t, &signupBody)
	if signupBody.Error.Type != "reserved_name" {
		t.Fatalf("reserved username error type = %q", signupBody.Error.Type)
	}
}

func TestOrgCreationRestrictedToSiteAdmins(t *testing.T) {
	f := newOrgFixtureWithConfig(t, "none", func(c *config.Config) { c.OrgCreation = "admin" })

	resp := f.do("POST", "/api/v1/orgs", f.token(f.alice, "write"), map[string]any{"name": "widgets"})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403", resp.status())
	}
	var body apitypes.ApiErrorBody
	resp.json(t, &body)
	if body.Error.Type != "org_creation_disabled" {
		t.Fatalf("error type = %q", body.Error.Type)
	}

	if resp := f.do("POST", "/api/v1/orgs", f.token(f.admin, "write"),
		map[string]any{"name": "widgets"}); resp.status() != 201 {
		t.Fatalf("site admin create status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
}

func TestOrgViewerRoleAndMembersVisibility(t *testing.T) {
	f := newOrgFixture(t, "none")
	aliceTok := f.token(f.alice, "write")

	// A non-member sees the organisation but no role and only public repos.
	resp := f.do("GET", "/api/v1/orgs/acme", aliceTok, nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d", resp.status())
	}
	var body apitypes.OrgResponse
	resp.json(t, &body)
	if body.Org.ViewerRole != "" {
		t.Fatalf("viewer_role = %q, want empty for a non-member", body.Org.ViewerRole)
	}
	if body.Org.NumRepos != 2 || body.Org.NumMembers != 1 {
		t.Fatalf("counts = repos %d members %d, want both repos and 1 member",
			body.Org.NumRepos, body.Org.NumMembers)
	}

	// Anonymous callers get the same public view.
	if resp := f.do("GET", "/api/v1/orgs/acme", "", nil); resp.status() != 200 {
		t.Fatalf("anonymous org view = %d", resp.status())
	}
	// A user namespace is not an organisation.
	if resp := f.do("GET", "/api/v1/orgs/alice", aliceTok, nil); resp.status() != 404 {
		t.Fatalf("GET /orgs/alice = %d, want 404", resp.status())
	}

	// Opening the roster up lets non-members read it, without addresses.
	bobTok := f.token(f.bob, "write")
	if resp := f.do("PATCH", "/api/v1/orgs/acme", bobTok,
		map[string]any{"members_visibility": "public"}); resp.status() != 200 {
		t.Fatalf("open members status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	resp = f.do("GET", "/api/v1/orgs/acme/members", aliceTok, nil)
	if resp.status() != 200 {
		t.Fatalf("public members status = %d", resp.status())
	}
	var members apitypes.OrgMembersResponse
	resp.json(t, &members)
	if len(members.Items) != 1 || members.Items[0].Username != "bob" {
		t.Fatalf("members = %+v", members.Items)
	}
	if members.Items[0].Email != "" {
		t.Fatalf("email leaked to a non-member: %q", members.Items[0].Email)
	}
	// Members still see addresses.
	resp = f.do("GET", "/api/v1/orgs/acme/members", bobTok, nil)
	resp.json(t, &members)
	if members.Items[0].Email != "bob@example.com" {
		t.Fatalf("member email = %q", members.Items[0].Email)
	}

	// An invalid policy value is refused.
	if resp := f.do("PATCH", "/api/v1/orgs/acme", bobTok,
		map[string]any{"members_visibility": "everyone"}); resp.status() != 400 {
		t.Fatalf("invalid members_visibility = %d", resp.status())
	}
}

func TestOrgMembershipLifecycle(t *testing.T) {
	f := newOrgFixture(t, "none")
	bobTok := f.token(f.bob, "write") // acme's founding admin
	aliceTok := f.token(f.alice, "write")

	// Add, then fail to add twice.
	resp := f.do("POST", "/api/v1/orgs/acme/members", bobTok, map[string]any{"username": "alice"})
	if resp.status() != 201 {
		t.Fatalf("add member status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var added apitypes.OrgMemberResponse
	resp.json(t, &added)
	if added.Member.Username != "alice" || added.Member.Role != apitypes.OrgRoleRead {
		t.Fatalf("added member = %+v, want alice with the default read role", added.Member)
	}
	dup := f.do("POST", "/api/v1/orgs/acme/members", bobTok, map[string]any{"username": "alice"})
	if dup.status() != 409 {
		t.Fatalf("duplicate member status = %d", dup.status())
	}
	var dupBody apitypes.ApiErrorBody
	dup.json(t, &dupBody)
	if dupBody.Error.Type != "already_member" {
		t.Fatalf("duplicate error type = %q", dupBody.Error.Type)
	}
	if miss := f.do("POST", "/api/v1/orgs/acme/members", bobTok,
		map[string]any{"username": "nobody"}); miss.status() != 404 {
		t.Fatalf("unknown user status = %d", miss.status())
	}
	if bad := f.do("POST", "/api/v1/orgs/acme/members", bobTok,
		map[string]any{"username": "carol", "role": "owner"}); bad.status() != 400 {
		t.Fatalf("invalid role status = %d", bad.status())
	}

	// The membership shows up in the new member's own account immediately.
	me := f.do("GET", "/api/v1/me", aliceTok, nil)
	var meBody apitypes.UserResponse
	me.json(t, &meBody)
	var roles []string
	for _, n := range meBody.User.Namespaces {
		roles = append(roles, n.Name+":"+string(n.Kind)+":"+n.Role)
	}
	if !containsString(roles, "acme:org:read") || !containsString(roles, "alice:user:admin") {
		t.Fatalf("namespaces = %v", roles)
	}
	myOrgs := f.do("GET", "/api/v1/me/orgs", aliceTok, nil)
	var myOrgsBody apitypes.OrgListResponse
	myOrgs.json(t, &myOrgsBody)
	if len(myOrgsBody.Items) != 1 || myOrgsBody.Items[0].ViewerRole != apitypes.OrgRoleRead {
		t.Fatalf("me/orgs = %+v", myOrgsBody.Items)
	}

	// Promotion.
	if resp := f.do("PATCH", "/api/v1/orgs/acme/members/alice", bobTok,
		map[string]any{"role": "admin"}); resp.status() != 200 {
		t.Fatalf("promote status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	// Leaving is the same endpoint pointed at yourself, and needs no admin
	// role -- but the last admin may not go.
	if resp := f.do("DELETE", "/api/v1/orgs/acme/members/bob", bobTok, nil); resp.status() != 204 {
		t.Fatalf("bob leaving status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	last := f.do("DELETE", "/api/v1/orgs/acme/members/alice", aliceTok, nil)
	if last.status() != 409 {
		t.Fatalf("last admin leaving status = %d", last.status())
	}
	var lastBody apitypes.ApiErrorBody
	last.json(t, &lastBody)
	if lastBody.Error.Type != "last_admin" {
		t.Fatalf("last admin error type = %q", lastBody.Error.Type)
	}
	demote := f.do("PATCH", "/api/v1/orgs/acme/members/alice", aliceTok, map[string]any{"role": "write"})
	if demote.status() != 409 {
		t.Fatalf("last admin demotion status = %d", demote.status())
	}
	demote.json(t, &lastBody)
	if lastBody.Error.Type != "last_admin" {
		t.Fatalf("last admin demotion error type = %q", lastBody.Error.Type)
	}

	// A user who is not a member is a 404 against the membership URL,
	// whether or not the account exists.
	for _, name := range []string{"carol", "nobody"} {
		if resp := f.do("DELETE", "/api/v1/orgs/acme/members/"+name, aliceTok, nil); resp.status() != 404 {
			t.Fatalf("remove %s status = %d, want 404", name, resp.status())
		}
	}
}

// TestOrgLeaveIsSelfServiceOnly covers the one row of the matrix where a
// non-admin may change a membership: their own (§4 "leaving on your own").
func TestOrgLeaveIsSelfServiceOnly(t *testing.T) {
	f := newOrgFixture(t, "read")
	aliceTok := f.token(f.alice, "write")

	// A read member cannot evict anyone else...
	if r := f.do("DELETE", "/api/v1/orgs/acme/members/bob", aliceTok, nil); r.status() != 403 {
		t.Fatalf("read member removing bob = %d, want 403", r.status())
	}
	if r := f.do("PATCH", "/api/v1/orgs/acme/members/alice", aliceTok,
		map[string]any{"role": "admin"}); r.status() != 403 {
		t.Fatalf("read member promoting herself = %d, want 403", r.status())
	}
	// ...but may walk out.
	if r := f.do("DELETE", "/api/v1/orgs/acme/members/alice", aliceTok, nil); r.status() != 204 {
		t.Fatalf("read member leaving = %d, body = %s", r.status(), r.rec.Body.String())
	}
	// Reading is unaffected -- it never depended on membership -- but the
	// role is gone, which is what the next assertions rest on.
	if r := f.do("GET", "/api/v1/repos/model/acme/secret", aliceTok, nil); r.status() != 200 {
		t.Fatalf("repo after leaving = %d, want 200", r.status())
	}
	// Leaving twice is a 404, not a second departure.
	if r := f.do("DELETE", "/api/v1/orgs/acme/members/alice", aliceTok, nil); r.status() != 404 {
		t.Fatalf("leaving twice = %d, want 404", r.status())
	}

	// The departure is on the record.
	logResp := f.do("GET", "/api/v1/orgs/acme/audit-log", f.token(f.bob, "write"), nil)
	var logBody apitypes.OrgAuditLogResponse
	logResp.json(t, &logBody)
	if len(logBody.Items) == 0 || logBody.Items[0].Action != "member.left" ||
		logBody.Items[0].Actor != "alice" || logBody.Items[0].Target != "alice" {
		t.Fatalf("audit log = %+v, want member.left", logBody.Items)
	}
}

func TestOrgDeleteRequiresNoRepositories(t *testing.T) {
	f := newOrgFixture(t, "admin")
	tok := f.token(f.alice, "write")

	resp := f.do("DELETE", "/api/v1/orgs/acme", tok, nil)
	if resp.status() != 409 {
		t.Fatalf("status = %d, want 409", resp.status())
	}
	var body apitypes.ApiErrorBody
	resp.json(t, &body)
	if body.Error.Type != "has_repositories" {
		t.Fatalf("error type = %q", body.Error.Type)
	}

	for _, name := range []string{"pub", "secret"} {
		if r := f.do("DELETE", "/api/v1/repos/model/acme/"+name, tok, nil); r.status() != 204 {
			t.Fatalf("delete %s status = %d", name, r.status())
		}
	}
	if r := f.do("DELETE", "/api/v1/orgs/acme", tok, nil); r.status() != 204 {
		t.Fatalf("delete org status = %d, body = %s", r.status(), r.rec.Body.String())
	}
	if r := f.do("GET", "/api/v1/orgs/acme", tok, nil); r.status() != 404 {
		t.Fatalf("deleted org still readable: %d", r.status())
	}
}

// TestCreateRepoAcceptsAndIgnoresVisibilityFields pins the HF compatibility
// half of dropping repository visibility: huggingface_hub keeps sending
// "private" (< 1.0) or "visibility" (1.x) on every create_repo, and a client
// that does must still succeed. The fields are decoded and discarded -- there
// is nothing here for them to set.
func TestCreateRepoAcceptsAndIgnoresVisibilityFields(t *testing.T) {
	f := newOrgFixture(t, "write")
	tok := f.token(f.alice, "write")

	for i, payload := range []map[string]any{
		{"type": "model", "name": "acme/hf-unset"},
		{"type": "model", "name": "acme/hf-public", "visibility": "public"},
		{"type": "model", "name": "acme/hf-private", "visibility": "private"},
		{"type": "model", "name": "acme/hf-legacy-public", "private": false},
		{"type": "model", "name": "acme/hf-legacy-private", "private": true},
	} {
		if r := f.do("POST", "/api/repos/create", tok, payload); r.status() != 200 {
			t.Fatalf("HF create #%d %v status = %d, body = %s", i, payload, r.status(), r.rec.Body.String())
		}
	}

	// The UI endpoint has no visibility field at all; an extra key is ignored
	// rather than rejected.
	if r := f.do("POST", "/api/v1/repos", tok, map[string]any{
		"kind": "model", "namespace": "acme", "name": "ui-model", "private": true,
	}); r.status() != 200 {
		t.Fatalf("UI create status = %d, body = %s", r.status(), r.rec.Body.String())
	}
	// And every one of them is readable by an anonymous caller.
	if r := f.do("GET", "/api/v1/repos/model/acme/hf-private", "", nil); r.status() != 200 {
		t.Fatalf("anonymous read status = %d, want 200", r.status())
	}
}

func TestOrgWhoamiAndHFMembers(t *testing.T) {
	f := newOrgFixture(t, "write")
	tok := f.token(f.alice, "write")

	resp := f.do("GET", "/api/whoami-v2", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("whoami status = %d", resp.status())
	}
	var whoami struct {
		Name string `json:"name"`
		Orgs []struct {
			Type      string  `json:"type"`
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			Fullname  string  `json:"fullname"`
			RoleInOrg string  `json:"roleInOrg"`
			Email     *string `json:"email"`
		} `json:"orgs"`
	}
	resp.json(t, &whoami)
	if whoami.Name != "alice" || len(whoami.Orgs) != 1 {
		t.Fatalf("whoami = %+v", whoami)
	}
	o := whoami.Orgs[0]
	if o.Type != "org" || o.Name != "acme" || o.RoleInOrg != "write" || o.ID == "" || o.Fullname != "acme" {
		t.Fatalf("whoami org = %+v", o)
	}
	if o.Email != nil {
		t.Fatalf("whoami org email = %v, want null", *o.Email)
	}

	// A site admin's blanket authority is not a membership.
	adminResp := f.do("GET", "/api/whoami-v2", f.token(f.admin, "write"), nil)
	adminResp.json(t, &whoami)
	if len(whoami.Orgs) != 0 {
		t.Fatalf("site admin orgs = %+v, want none", whoami.Orgs)
	}

	// HF's member list: same authorization, HF's field names.
	membersResp := f.do("GET", "/api/organizations/acme/members", tok, nil)
	if membersResp.status() != 200 {
		t.Fatalf("HF members status = %d", membersResp.status())
	}
	var hfMembers []map[string]any
	membersResp.json(t, &hfMembers)
	if len(hfMembers) != 2 {
		t.Fatalf("HF members = %+v, want bob and alice", hfMembers)
	}
	first := hfMembers[0]
	if first["user"] != "bob" || first["type"] != "user" || first["isPro"] != false {
		t.Fatalf("HF member = %+v", first)
	}
	// Non-members are refused while members_visibility is "members".
	if r := f.do("GET", "/api/organizations/acme/members", f.token(f.carol, "write"), nil); r.status() != 403 {
		t.Fatalf("HF members as a non-member = %d, want 403", r.status())
	}
	if r := f.do("GET", "/api/organizations/nope/members", tok, nil); r.status() != 404 {
		t.Fatalf("HF members for a missing org = %d", r.status())
	}
}

func TestOrgDirectoryListing(t *testing.T) {
	f := newOrgFixture(t, "read")
	tok := f.token(f.alice, "write")
	if r := f.do("POST", "/api/v1/orgs", tok, map[string]any{"name": "widgets"}); r.status() != 201 {
		t.Fatalf("create widgets status = %d", r.status())
	}

	resp := f.do("GET", "/api/v1/orgs", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d", resp.status())
	}
	var body apitypes.OrgListResponse
	resp.json(t, &body)
	if body.Total != 2 || len(body.Items) != 2 {
		t.Fatalf("listing = %+v (total %d)", body.Items, body.Total)
	}
	if body.Items[0].Name != "acme" || body.Items[0].ViewerRole != apitypes.OrgRoleRead {
		t.Fatalf("acme entry = %+v", body.Items[0])
	}
	if body.Items[0].NumRepos != 2 {
		t.Fatalf("acme repos = %d, want both (a read member sees the private one)", body.Items[0].NumRepos)
	}

	// Search narrows by name.
	searchResp := f.do("GET", "/api/v1/orgs?search=widg", tok, nil)
	searchResp.json(t, &body)
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].Name != "widgets" {
		t.Fatalf("search = %+v (total %d)", body.Items, body.Total)
	}

	// Anonymous callers see the same directory and the same repository counts;
	// only the viewer's own role is absent.
	anon := f.do("GET", "/api/v1/orgs", "", nil)
	anon.json(t, &body)
	if body.Total != 2 || body.Items[0].NumRepos != 2 || body.Items[0].ViewerRole != "" {
		t.Fatalf("anonymous listing = %+v", body.Items)
	}
}

func TestOrgAuditLogRecordsAdministrativeActions(t *testing.T) {
	f := newOrgFixture(t, "admin")
	tok := f.token(f.alice, "write")

	if r := f.do("POST", "/api/v1/orgs/acme/members", tok,
		map[string]any{"username": "carol", "role": "write"}); r.status() != 201 {
		t.Fatalf("add member status = %d", r.status())
	}
	if r := f.do("PATCH", "/api/v1/orgs/acme/members/carol", tok,
		map[string]any{"role": "read"}); r.status() != 200 {
		t.Fatalf("change role status = %d", r.status())
	}
	if r := f.do("DELETE", "/api/v1/orgs/acme/members/carol", tok, nil); r.status() != 204 {
		t.Fatalf("remove member status = %d", r.status())
	}
	if r := f.do("PATCH", "/api/v1/orgs/acme", tok,
		map[string]any{"description": "anvils"}); r.status() != 200 {
		t.Fatalf("update org status = %d", r.status())
	}
	if r := f.do("POST", "/api/v1/repos", tok, map[string]any{
		"kind": "model", "namespace": "acme", "name": "fresh",
	}); r.status() != 200 {
		t.Fatalf("create repo status = %d", r.status())
	}
	if r := f.do("DELETE", "/api/v1/repos/model/acme/fresh", tok, nil); r.status() != 204 {
		t.Fatalf("delete repo status = %d", r.status())
	}

	resp := f.do("GET", "/api/v1/orgs/acme/audit-log", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("audit log status = %d", resp.status())
	}
	var body apitypes.OrgAuditLogResponse
	resp.json(t, &body)
	var actions []string
	for _, e := range body.Items {
		actions = append(actions, e.Action)
		if e.Actor == "" {
			t.Fatalf("entry with no actor: %+v", e)
		}
	}
	for _, want := range []string{
		"member.added", "member.role_changed", "member.removed",
		"org.updated", "repo.created", "repo.deleted",
	} {
		if !containsString(actions, want) {
			t.Fatalf("audit actions = %v, missing %q", actions, want)
		}
	}
	// Newest first. (org.created is absent because this fixture builds the
	// organisation through the store rather than the API.)
	if body.Items[0].Action != "repo.deleted" {
		t.Fatalf("newest entry = %q, want repo.deleted", body.Items[0].Action)
	}
	if body.Items[len(body.Items)-1].Action != "member.added" {
		t.Fatalf("oldest entry = %+v, want member.added", body.Items[len(body.Items)-1])
	}
	// Details travel with the entry.
	for _, e := range body.Items {
		if e.Action == "member.role_changed" && (e.Details["from"] != "write" || e.Details["to"] != "read") {
			t.Fatalf("role change details = %+v", e.Details)
		}
	}

	// Paging: a full page reports a cursor, the last page does not.
	first := f.do("GET", "/api/v1/orgs/acme/audit-log?limit=2", tok, nil)
	first.json(t, &body)
	if len(body.Items) != 2 || body.NextBefore == 0 {
		t.Fatalf("first page = %+v, next %d", body.Items, body.NextBefore)
	}
	rest := f.do("GET", fmt.Sprintf("/api/v1/orgs/acme/audit-log?before=%d", body.NextBefore), tok, nil)
	rest.json(t, &body)
	if len(body.Items) == 0 || body.NextBefore != 0 {
		t.Fatalf("second page = %+v, next %d", body.Items, body.NextBefore)
	}
}

func TestOrgAuditLogRecordsTransfers(t *testing.T) {
	f := newOrgFixture(t, "admin")
	tok := f.token(f.alice, "write")
	f.repo("alice", "moving", "model")

	if r := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "alice/moving", "toRepo": "acme/moving", "type": "model",
	}); r.status() != 200 {
		t.Fatalf("move status = %d, body = %s", r.status(), r.rec.Body.String())
	}

	resp := f.do("GET", "/api/v1/orgs/acme/audit-log", tok, nil)
	var body apitypes.OrgAuditLogResponse
	resp.json(t, &body)
	if body.Items[0].Action != "repo.transferred_in" || body.Items[0].Target != "acme/moving" {
		t.Fatalf("newest entry = %+v, want repo.transferred_in", body.Items[0])
	}

	// And out again, which the organisation records as a departure.
	if r := f.do("POST", "/api/repos/move", tok, map[string]any{
		"fromRepo": "acme/moving", "toRepo": "alice/moving", "type": "model",
	}); r.status() != 200 {
		t.Fatalf("move back status = %d, body = %s", r.status(), r.rec.Body.String())
	}
	resp = f.do("GET", "/api/v1/orgs/acme/audit-log", tok, nil)
	resp.json(t, &body)
	if body.Items[0].Action != "repo.transferred_out" || body.Items[0].Target != "acme/moving" {
		t.Fatalf("newest entry = %+v, want repo.transferred_out", body.Items[0])
	}
}

func TestOrgRepoSummaryCarriesNamespaceKind(t *testing.T) {
	f := newOrgFixture(t, "read")
	tok := f.token(f.alice, "write")
	f.repo("alice", "personal", "model")

	kinds := map[string]apitypes.NamespaceKind{}
	resp := f.do("GET", "/api/v1/repos?limit=50", tok, nil)
	if resp.status() != 200 {
		t.Fatalf("list status = %d", resp.status())
	}
	var body apitypes.RepoListResponse
	resp.json(t, &body)
	for _, r := range body.Items {
		kinds[r.FullName] = r.NamespaceKind
	}
	if kinds["acme/pub"] != apitypes.NamespaceKindOrg {
		t.Fatalf("acme/pub namespace_kind = %q", kinds["acme/pub"])
	}
	if kinds["alice/personal"] != apitypes.NamespaceKindUser {
		t.Fatalf("alice/personal namespace_kind = %q", kinds["alice/personal"])
	}

	detail := f.do("GET", "/api/v1/repos/model/acme/secret", tok, nil)
	var detailBody apitypes.RepoDetailResponse
	detail.json(t, &detailBody)
	if detailBody.Repo.NamespaceKind != apitypes.NamespaceKindOrg {
		t.Fatalf("detail namespace_kind = %q", detailBody.Repo.NamespaceKind)
	}
	// A read member sees the repository but is told they cannot write to it.
	if detailBody.Repo.CanWrite {
		t.Fatalf("read member reported as able to write")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
