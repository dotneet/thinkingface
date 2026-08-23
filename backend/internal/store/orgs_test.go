package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Organisation store tests (docs/organization-design.md §11). They run
// against every available backend, which is what proves the SQLite and
// Postgres spellings of the last-admin lock, the JSON details column, and the
// nullable timestamps behave identically.

// mustOrg creates an organisation founded by the given user.
func mustOrg(t *testing.T, s *Store, name string, founder *User, in OrgUpdate) *Org {
	t.Helper()
	org, err := s.CreateOrg(context.Background(), name, founder.ID, in)
	if err != nil {
		t.Fatalf("create org %s: %v", name, err)
	}
	return org
}

func TestIntegrationOrgCRUD(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{DisplayName: ptr("Acme Inc.")})
		if org.DisplayName != "Acme Inc." || org.MembersVisibility != "members" {
			t.Fatalf("new org = %+v, want the documented defaults", org)
		}
		if org.CreatedBy == nil || *org.CreatedBy != f.alice.ID {
			t.Fatalf("created_by = %v, want alice", org.CreatedBy)
		}
		if org.UpdatedAt.IsZero() {
			t.Fatalf("updated_at is zero")
		}

		// The founder is an admin member, and nothing else.
		members, err := s.ListOrgMembers(ctx, org.ID)
		if err != nil || len(members) != 1 || members[0].Username != "alice" || members[0].Role != "admin" {
			t.Fatalf("members = %+v, %v", members, err)
		}
		if members[0].AddedBy == nil || *members[0].AddedBy != f.alice.ID {
			t.Fatalf("added_by = %v, want alice", members[0].AddedBy)
		}
		if members[0].CreatedAt.IsZero() {
			t.Fatalf("member created_at is zero")
		}

		// GetOrg only answers for organisations, never personal namespaces.
		if got, err := s.GetOrg(ctx, "acme"); err != nil || got.ID != org.ID {
			t.Fatalf("GetOrg = %+v, %v", got, err)
		}
		if _, err := s.GetOrg(ctx, "alice"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetOrg on a user namespace err = %v, want ErrNotFound", err)
		}
		if _, err := s.GetOrg(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetOrg missing err = %v", err)
		}

		// Partial update: named fields change, the rest stay put.
		updated, err := s.UpdateOrg(ctx, org.ID, OrgUpdate{
			Description:       ptr("we make anvils"),
			MembersVisibility: ptr("public"),
		})
		if err != nil {
			t.Fatalf("UpdateOrg: %v", err)
		}
		if updated.Description != "we make anvils" || updated.DisplayName != "Acme Inc." ||
			updated.MembersVisibility != "public" {
			t.Fatalf("updated org = %+v", updated)
		}
		if _, err := s.UpdateOrg(ctx, 999999, OrgUpdate{Website: ptr("x")}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateOrg missing err = %v", err)
		}

		// Deletion is refused while a repository is still there.
		f.repo(t, "acme", "anvil", "model", nil)
		if err := s.DeleteOrg(ctx, org.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("DeleteOrg with a repository err = %v, want ErrConflict", err)
		}
		repo, err := s.GetRepo(ctx, "model", "acme", "anvil")
		if err != nil {
			t.Fatalf("get repo: %v", err)
		}
		if repo.NamespaceKind != "org" {
			t.Fatalf("repo.NamespaceKind = %q, want org", repo.NamespaceKind)
		}
		if err := s.DeleteRepo(ctx, repo.ID); err != nil {
			t.Fatalf("delete repo: %v", err)
		}
		if err := s.DeleteOrg(ctx, org.ID); err != nil {
			t.Fatalf("DeleteOrg: %v", err)
		}
		if _, err := s.GetOrg(ctx, "acme"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("org still present after delete: %v", err)
		}
		// The name is free again, memberships having cascaded away.
		if _, err := s.CreateOrg(ctx, "acme", f.bob.ID, OrgUpdate{}); err != nil {
			t.Fatalf("recreate org after delete: %v", err)
		}
		// A personal namespace is not an organisation and cannot be deleted here.
		if err := s.DeleteOrg(ctx, f.ns(t, "alice").ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteOrg on a user namespace err = %v, want ErrNotFound", err)
		}
	})
}

func TestIntegrationOrgMembers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})

		m, err := s.AddOrgMember(ctx, org.ID, f.bob.ID, "write", f.alice.ID)
		if err != nil || m.Role != "write" || m.Username != "bob" || m.Email != "bob@example.com" {
			t.Fatalf("AddOrgMember = %+v, %v", m, err)
		}
		if _, err := s.AddOrgMember(ctx, org.ID, f.bob.ID, "read", f.alice.ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate member err = %v, want ErrConflict", err)
		}

		// Admins sort first, then write, then read; alphabetical inside each.
		if _, err := s.AddOrgMember(ctx, org.ID, f.admin.ID, "read", f.alice.ID); err != nil {
			t.Fatalf("add read member: %v", err)
		}
		members, err := s.ListOrgMembers(ctx, org.ID)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, mm := range members {
			order = append(order, mm.Username+":"+mm.Role)
		}
		if !equalStrings(order, []string{"alice:admin", "bob:write", "admin:read"}) {
			t.Fatalf("members = %v", order)
		}

		if _, err := s.GetOrgMember(ctx, org.ID, f.bob.ID); err != nil {
			t.Fatalf("GetOrgMember: %v", err)
		}

		// Promotion and demotion.
		if _, err := s.UpdateOrgMemberRole(ctx, org.ID, f.bob.ID, "admin"); err != nil {
			t.Fatalf("promote bob: %v", err)
		}
		if _, err := s.UpdateOrgMemberRole(ctx, org.ID, f.alice.ID, "read"); err != nil {
			t.Fatalf("demote alice with another admin present: %v", err)
		}
		// Bob is now the only admin: demoting or removing him is refused,
		// and so is his leaving.
		if _, err := s.UpdateOrgMemberRole(ctx, org.ID, f.bob.ID, "write"); !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("demote last admin err = %v, want ErrLastAdmin", err)
		}
		if err := s.RemoveOrgMember(ctx, org.ID, f.bob.ID); !errors.Is(err, ErrLastAdmin) {
			t.Fatalf("remove last admin err = %v, want ErrLastAdmin", err)
		}
		// A non-admin may always go.
		if err := s.RemoveOrgMember(ctx, org.ID, f.alice.ID); err != nil {
			t.Fatalf("remove alice: %v", err)
		}
		if _, err := s.GetOrgMember(ctx, org.ID, f.alice.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("alice still a member: %v", err)
		}
		if _, err := s.UpdateOrgMemberRole(ctx, org.ID, f.alice.ID, "read"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update non-member err = %v", err)
		}
		if err := s.RemoveOrgMember(ctx, org.ID, f.alice.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("remove non-member err = %v", err)
		}
	})
}

func TestIntegrationOrgListings(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		acme := mustOrg(t, s, "acme", f.alice, OrgUpdate{DisplayName: ptr("Acme Inc.")})
		widgets := mustOrg(t, s, "widgets", f.bob, OrgUpdate{})
		if _, err := s.AddOrgMember(ctx, acme.ID, f.bob.ID, "read", f.alice.ID); err != nil {
			t.Fatal(err)
		}
		f.repo(t, "acme", "first-model", "model", nil)
		f.repo(t, "acme", "second-model", "model", nil)

		// Membership listing carries the viewer's role and both counts.
		mine, err := s.ListOrgsForUser(ctx, f.bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(mine) != 2 {
			t.Fatalf("ListOrgsForUser = %+v, want acme and widgets", mine)
		}
		if mine[0].Name != "acme" || mine[0].Role != "read" ||
			mine[0].NumMembers != 2 || mine[0].NumRepos != 2 {
			t.Fatalf("acme summary = %+v, want role=read members=2 repos=2", mine[0])
		}
		if mine[1].Name != "widgets" || mine[1].Role != "admin" {
			t.Fatalf("widgets summary = %+v", mine[1])
		}
		if alone, err := s.ListOrgsForUser(ctx, f.admin.ID); err != nil || len(alone) != 0 {
			t.Fatalf("site admin memberships = %+v, %v, want none", alone, err)
		}
		_ = widgets

		// Directory listing. Repositories have no visibility of their own, so
		// the count is the same for everyone; only the viewer's role differs.
		orgs, total, err := s.ListOrgs(ctx, "", nil, 0, 0)
		if err != nil || total != 2 || len(orgs) != 2 {
			t.Fatalf("ListOrgs = %+v, total %d, %v", orgs, total, err)
		}
		if orgs[0].Name != "acme" || orgs[0].NumRepos != 2 || orgs[0].Role != "" {
			t.Fatalf("anonymous acme = %+v, want both repos and no role", orgs[0])
		}
		orgs, _, err = s.ListOrgs(ctx, "", &f.bob.ID, 0, 0)
		if err != nil || orgs[0].NumRepos != 2 || orgs[0].Role != "read" {
			t.Fatalf("member acme = %+v, %v", orgs[0], err)
		}
		if orgs[1].Name != "widgets" || orgs[1].Role != "admin" {
			t.Fatalf("member widgets = %+v", orgs[1])
		}
		orgs, _, err = s.ListOrgs(ctx, "", nil, 0, 0)
		if err != nil || orgs[0].NumRepos != 2 {
			t.Fatalf("site-admin acme = %+v, %v", orgs[0], err)
		}

		// Search matches name and display name.
		orgs, total, err = s.ListOrgs(ctx, "acme", nil, 0, 0)
		if err != nil || total != 1 || len(orgs) != 1 || orgs[0].Name != "acme" {
			t.Fatalf("search acme = %+v, total %d, %v", orgs, total, err)
		}
		if orgs, total, err = s.ListOrgs(ctx, "Inc.", nil, 0, 0); err != nil || total != 1 ||
			len(orgs) != 1 || orgs[0].Name != "acme" {
			t.Fatalf("search display name = %+v, total %d, %v", orgs, total, err)
		}
		if orgs, total, err = s.ListOrgs(ctx, "nothing", nil, 0, 0); err != nil ||
			total != 0 || len(orgs) != 0 {
			t.Fatalf("search miss = %+v, total %d, %v", orgs, total, err)
		}
		// Paging.
		if orgs, _, err = s.ListOrgs(ctx, "", nil, 1, 1); err != nil ||
			len(orgs) != 1 || orgs[0].Name != "widgets" {
			t.Fatalf("second page = %+v, %v", orgs, err)
		}

		// CountOrgRepos answers the same for every caller.
		if n, err := s.CountOrgRepos(ctx, acme.ID); err != nil || n != 2 {
			t.Fatalf("repo count = %d, %v", n, err)
		}
	})
}

func TestIntegrationNamespaceRoleFor(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})
		if _, err := s.AddOrgMember(ctx, org.ID, f.bob.ID, "read", f.alice.ID); err != nil {
			t.Fatal(err)
		}

		cases := []struct {
			user     *User
			ns       string
			wantKind string
			wantRole string
		}{
			{f.alice, "alice", "user", "admin"},
			{f.bob, "alice", "user", ""},
			{f.alice, "acme", "org", "admin"},
			{f.bob, "acme", "org", "read"},
			{f.admin, "acme", "org", ""}, // site admin is not a member
		}
		for _, c := range cases {
			got, err := s.NamespaceRoleFor(ctx, c.user.ID, c.ns)
			if err != nil {
				t.Fatalf("NamespaceRoleFor(%s, %s): %v", c.user.Username, c.ns, err)
			}
			if got.Kind != c.wantKind || got.Role != c.wantRole {
				t.Fatalf("NamespaceRoleFor(%s, %s) = %+v, want kind %q role %q",
					c.user.Username, c.ns, got, c.wantKind, c.wantRole)
			}
			role, err := s.RoleInNamespace(ctx, c.user.ID, c.ns)
			if err != nil || role != c.wantRole {
				t.Fatalf("RoleInNamespace(%s, %s) = %q, %v", c.user.Username, c.ns, role, err)
			}
		}
		if _, err := s.NamespaceRoleFor(ctx, f.alice.ID, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing namespace err = %v", err)
		}
	})
}

func TestIntegrationOrgAuditLog(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})

		for i := 0; i < 5; i++ {
			e := AuditEntry{
				ActorUserID: &f.alice.ID,
				ActorName:   "alice",
				Action:      "member.added",
				TargetName:  "bob",
				Details:     json.RawMessage(`{"role":"write"}`),
			}
			if i == 0 {
				// Details are optional; the column defaults to an empty object.
				e.Details = nil
				e.Action = "org.created"
				e.TargetName = "acme"
			}
			if err := s.AppendOrgAudit(ctx, org.ID, e); err != nil {
				t.Fatalf("AppendOrgAudit: %v", err)
			}
		}

		// Newest first, paged with `before`.
		page, err := s.ListOrgAudit(ctx, org.ID, 0, 2)
		if err != nil || len(page) != 2 {
			t.Fatalf("first page = %+v, %v", page, err)
		}
		if page[0].ID <= page[1].ID {
			t.Fatalf("page is not newest-first: %+v", page)
		}
		if page[0].ActorName != "alice" || page[0].Action != "member.added" || page[0].TargetName != "bob" {
			t.Fatalf("entry = %+v", page[0])
		}
		var details map[string]any
		if err := json.Unmarshal(page[0].Details, &details); err != nil || details["role"] != "write" {
			t.Fatalf("details = %s, %v", page[0].Details, err)
		}

		next, err := s.ListOrgAudit(ctx, org.ID, page[1].ID, 10)
		if err != nil || len(next) != 3 {
			t.Fatalf("second page = %+v, %v", next, err)
		}
		last := next[len(next)-1]
		if last.Action != "org.created" || string(last.Details) != "{}" {
			t.Fatalf("oldest entry = %+v, details %s", last, last.Details)
		}
		if rest, err := s.ListOrgAudit(ctx, org.ID, last.ID, 10); err != nil || len(rest) != 0 {
			t.Fatalf("past the end = %+v, %v", rest, err)
		}

		// The log cascades away with the organisation.
		if err := s.DeleteOrg(ctx, org.ID); err != nil {
			t.Fatalf("delete org: %v", err)
		}
		if rest, err := s.ListOrgAudit(ctx, org.ID, 0, 10); err != nil || len(rest) != 0 {
			t.Fatalf("audit log after delete = %+v, %v", rest, err)
		}
	})
}

// TestIntegrationOrgFounderBackfill exercises the part of the organisations
// migration that detaches an organisation from its founder: a legacy row
// carrying owner_user_id and no org_members entry must come out with an
// admin membership and a NULL owner (docs/organization-design.md §6.1).
//
// The legacy shape is recreated by hand and the migration's own backfill
// statements are replayed from the embedded file, so the assertions are
// against the SQL that actually ships rather than a copy of it.
func TestIntegrationOrgFounderBackfill(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})

		// Wind the row back to how 0009/0003 left it.
		if _, err := s.db.Exec(ctx, `DELETE FROM org_members WHERE namespace_id = $1`, org.ID); err != nil {
			t.Fatalf("clear members: %v", err)
		}
		if _, err := s.db.Exec(ctx,
			`UPDATE namespaces SET owner_user_id = $2, created_by = NULL WHERE id = $1`,
			org.ID, f.alice.ID); err != nil {
			t.Fatalf("restore owner: %v", err)
		}

		if _, err := s.db.Exec(ctx, orgFounderBackfillSQL(t, s.d.name())); err != nil {
			t.Fatalf("replay backfill: %v", err)
		}

		members, err := s.ListOrgMembers(ctx, org.ID)
		if err != nil || len(members) != 1 || members[0].Username != "alice" || members[0].Role != "admin" {
			t.Fatalf("backfilled members = %+v, %v", members, err)
		}
		var owner *int64
		var createdBy *int64
		if err := s.db.QueryRow(ctx,
			`SELECT owner_user_id, created_by FROM namespaces WHERE id = $1`, org.ID,
		).Scan(&owner, &createdBy); err != nil {
			t.Fatalf("read namespace: %v", err)
		}
		if owner != nil {
			t.Fatalf("owner_user_id = %v, want NULL", *owner)
		}
		if createdBy == nil || *createdBy != f.alice.ID {
			t.Fatalf("created_by = %v, want alice", createdBy)
		}
		// Personal namespaces keep their owner: the rewrite is org-only.
		alice := f.ns(t, "alice")
		if alice.OwnerUserID == nil || *alice.OwnerUserID != f.alice.ID {
			t.Fatalf("alice's namespace lost its owner: %+v", alice)
		}
	})
}

// orgFounderBackfillSQL slices the founder-detaching statements out of the
// organisations migration for the given engine. The markers are the comment
// that introduces the block and the statement that follows it.
func orgFounderBackfillSQL(t *testing.T, engine string) string {
	t.Helper()
	name := "migrations/postgres/0010_organizations.sql"
	if engine == "sqlite" {
		name = "migrations/sqlite/0004_organizations.sql"
	}
	raw, err := migrationsFS.ReadFile(name)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	const startMarker = "-- An organisation must not depend on its founder"
	const endMarker = "CREATE TABLE IF NOT EXISTS org_audit_log"
	start := strings.Index(string(raw), startMarker)
	end := strings.Index(string(raw), endMarker)
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("could not locate the backfill block in %s", name)
	}
	return string(raw)[start:end]
}
