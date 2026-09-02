package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// Organisation store tests (docs/dev/organization-design.md §11). They run
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
		members, total, err := s.ListOrgMembers(ctx, org.ID, 0, 0)
		if err != nil || len(members) != 1 || members[0].Username != "alice" || members[0].Role != "admin" {
			t.Fatalf("members = %+v, %v", members, err)
		}
		if total != 1 {
			t.Fatalf("total = %d, want 1", total)
		}
		if members[0].AddedBy == nil || *members[0].AddedBy != f.alice.ID {
			t.Fatalf("added_by = %v, want alice", members[0].AddedBy)
		}
		if members[0].CreatedAt.IsZero() {
			t.Fatalf("member created_at is zero")
		}

		// An organisation must not depend on its founder
		// (docs/dev/organization-design.md §6.1): owner_user_id stays NULL and
		// the founder's authority comes from the admin membership above, so
		// removing them from the roster really removes their power and
		// deleting their account no longer cascades the organisation away.
		var owner *int64
		if err := s.db.QueryRow(ctx,
			`SELECT owner_user_id FROM namespaces WHERE id = $1`, org.ID,
		).Scan(&owner); err != nil {
			t.Fatalf("read namespace: %v", err)
		}
		if owner != nil {
			t.Fatalf("owner_user_id = %v, want NULL", *owner)
		}
		// Personal namespaces keep their owner: the rule is org-only.
		if alice := f.ns(t, "alice"); alice.OwnerUserID == nil || *alice.OwnerUserID != f.alice.ID {
			t.Fatalf("alice's namespace lost its owner: %+v", alice)
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
		members, _, err := s.ListOrgMembers(ctx, org.ID, 0, 0)
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

// TestIntegrationOrgMemberPaging pins the page window of the roster. The
// listing used to have none at all: every caller read every membership row,
// so a large organisation cost more to look at simply for being large.
//
// The clamp is the part worth a test of its own. A caller that asks for more
// than MaxOrgPageSize gets MaxOrgPageSize, and `total` keeps counting the
// whole membership, which is what lets the caller tell "a full page with more
// behind it" from "the end of the roster". Deriving that from the number it
// asked for instead is the bug the audit log had.
func TestIntegrationOrgMemberPaging(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})

		// Enough members that a clamped page leaves some behind. The names are
		// zero-padded so alphabetical order is also numerical order, which is
		// what makes the walk below able to name the row it expects.
		const extra = MaxOrgPageSize + 24
		for i := 0; i < extra; i++ {
			name := fmt.Sprintf("member%04d", i)
			u, err := s.CreateUser(ctx, name, name+"@example.com", "hash", false)
			if err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
			if _, err := s.AddOrgMember(ctx, org.ID, u.ID, "read", f.alice.ID); err != nil {
				t.Fatalf("add %s: %v", name, err)
			}
		}
		wantTotal := int64(extra + 1) // the founder is a member too

		// No limit is the default page, not the whole roster.
		page, total, err := s.ListOrgMembers(ctx, org.ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != defaultOrgPageSize || total != wantTotal {
			t.Fatalf("default page = %d rows, total %d; want %d rows, total %d",
				len(page), total, defaultOrgPageSize, wantTotal)
		}
		if page[0].Username != "alice" || page[0].Role != "admin" {
			t.Fatalf("first row = %+v, want the admin first", page[0])
		}

		// An oversized limit is clamped, and the total still describes the
		// whole roster -- so `offset + len(page) < total` still finds the rest.
		page, total, err = s.ListOrgMembers(ctx, org.ID, 500, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != MaxOrgPageSize {
			t.Fatalf("clamped page = %d rows, want %d", len(page), MaxOrgPageSize)
		}
		if total != wantTotal {
			t.Fatalf("total = %d, want %d", total, wantTotal)
		}
		if int64(len(page)) >= total {
			t.Fatalf("a clamped page of %d looks like the whole roster of %d", len(page), total)
		}

		// A negative offset is the first page rather than a database error.
		if first, _, err := s.ListOrgMembers(ctx, org.ID, 10, -5); err != nil || len(first) != 10 ||
			first[0].Username != "alice" {
			t.Fatalf("negative offset = %+v, %v", first, err)
		}

		// Walking the pages visits every member exactly once, in one order.
		seen := map[string]bool{}
		var order []string
		for offset := 0; ; offset += MaxOrgPageSize {
			p, tot, err := s.ListOrgMembers(ctx, org.ID, MaxOrgPageSize, offset)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range p {
				if seen[m.Username] {
					t.Fatalf("%s appeared on two pages", m.Username)
				}
				seen[m.Username] = true
				order = append(order, m.Username)
			}
			if int64(len(seen)) >= tot {
				break
			}
			if len(p) == 0 {
				t.Fatalf("paging stalled after %d of %d members", len(seen), tot)
			}
		}
		if int64(len(seen)) != wantTotal {
			t.Fatalf("walked %d members, want %d", len(seen), wantTotal)
		}
		if order[0] != "alice" || order[1] != "member0000" || order[len(order)-1] != fmt.Sprintf("member%04d", extra-1) {
			t.Fatalf("page order broke at the seams: %v ... %v", order[:2], order[len(order)-1])
		}
	})
}

// TestIntegrationListOrgMembersAfterSurvivesAConcurrentRemoval is why the
// whole-roster walk is keyed on the username rather than counted with OFFSET.
//
// OFFSET means "skip the first N rows" of the set as it stands when the query
// runs, so a membership removed ahead of the cursor between two pages shifts
// everything after it back by one and the next page starts past a row that was
// there the entire time. Nothing about the result says so: the caller ends up
// with a list that is simply missing somebody. A username cursor asks for what
// sorts after a specific name instead, which no amount of churn elsewhere can
// move.
func TestIntegrationListOrgMembersAfterSurvivesAConcurrentRemoval(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})

		// Zero-padded so alphabetical order is numerical order.
		const members = 10
		for i := range members {
			name := fmt.Sprintf("member%04d", i)
			u, err := s.CreateUser(ctx, name, name+"@example.com", "hash", false)
			if err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
			if _, err := s.AddOrgMember(ctx, org.ID, u.ID, "read", f.alice.ID); err != nil {
				t.Fatalf("add %s: %v", name, err)
			}
		}

		// Read the first page of a walk, then remove somebody inside it --
		// the shape that makes an OFFSET walk skip a row.
		const pageSize = 4
		first, err := s.ListOrgMembersAfter(ctx, org.ID, "", pageSize)
		if err != nil {
			t.Fatalf("first page: %v", err)
		}
		if len(first) != pageSize {
			t.Fatalf("first page = %d rows, want %d", len(first), pageSize)
		}
		// Not first[0]: usernames sort "alice" before "member0000", and the
		// founder is the only admin, which the store refuses to remove.
		victim := first[1].Username
		removed, err := s.GetUserByUsername(ctx, victim)
		if err != nil {
			t.Fatalf("look up %s: %v", victim, err)
		}
		if err := s.RemoveOrgMember(ctx, org.ID, removed.ID); err != nil {
			t.Fatalf("remove %s: %v", victim, err)
		}

		// Finish the walk from where it was. Everything the first page
		// returned counts as seen -- those rows were members when it ran.
		seen := map[string]int{}
		for _, m := range first {
			seen[m.Username]++
		}
		after := first[len(first)-1].Username
		for {
			page, err := s.ListOrgMembersAfter(ctx, org.ID, after, pageSize)
			if err != nil {
				t.Fatalf("page after %q: %v", after, err)
			}
			for _, m := range page {
				seen[m.Username]++
			}
			if len(page) < pageSize {
				break
			}
			after = page[len(page)-1].Username
		}

		// Everyone who was a member throughout appears exactly once. With an
		// OFFSET walk the row just past the first page is the one that goes
		// missing, because the removal pulled it into the window already read.
		for i := range members {
			name := fmt.Sprintf("member%04d", i)
			if name == victim {
				continue // legitimately removed mid-walk
			}
			if seen[name] != 1 {
				t.Errorf("%s appeared %d times in the walk, want exactly 1", name, seen[name])
			}
		}
		if seen["alice"] != 1 {
			t.Errorf("the founder appeared %d times, want exactly 1", seen["alice"])
		}
	})
}

// TestIntegrationOffsetWalkSkipsARowAfterARemoval is the contrast that makes
// the test above mean something. Reading a roster with LIMIT/OFFSET, which is
// the obvious way and what api.allOrgMembers used to do, loses a row when a
// membership ahead of the cursor goes away between two pages -- and the result
// looks exactly like a roster that never had it.
//
// Pinning the shortcoming here rather than only asserting the fix keeps the
// keyset walk from being quietly replaced with the simpler thing later.
func TestIntegrationOffsetWalkSkipsARowAfterARemoval(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})

		const members = 10
		for i := range members {
			name := fmt.Sprintf("member%04d", i)
			u, err := s.CreateUser(ctx, name, name+"@example.com", "hash", false)
			if err != nil {
				t.Fatalf("create %s: %v", name, err)
			}
			if _, err := s.AddOrgMember(ctx, org.ID, u.ID, "read", f.alice.ID); err != nil {
				t.Fatalf("add %s: %v", name, err)
			}
		}

		const pageSize = 4
		seen := map[string]int{}
		first, _, err := s.ListOrgMembers(ctx, org.ID, pageSize, 0)
		if err != nil {
			t.Fatalf("first page: %v", err)
		}
		for _, m := range first {
			seen[m.Username]++
		}

		// Remove somebody the first page already returned. Every later row
		// now sits one place earlier than the offsets below assume.
		victim := first[1].Username
		removed, err := s.GetUserByUsername(ctx, victim)
		if err != nil {
			t.Fatalf("look up %s: %v", victim, err)
		}
		if err := s.RemoveOrgMember(ctx, org.ID, removed.ID); err != nil {
			t.Fatalf("remove %s: %v", victim, err)
		}

		for offset := pageSize; ; offset += pageSize {
			page, _, err := s.ListOrgMembers(ctx, org.ID, pageSize, offset)
			if err != nil {
				t.Fatalf("page at offset %d: %v", offset, err)
			}
			for _, m := range page {
				seen[m.Username]++
			}
			if len(page) < pageSize {
				break
			}
		}

		var missing []string
		for i := range members {
			name := fmt.Sprintf("member%04d", i)
			if name != victim && seen[name] == 0 {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			t.Fatal("the offset walk lost nobody, so this test no longer demonstrates " +
				"what ListOrgMembersAfter is for -- check whether the paging changed")
		}
	})
}

// repo_redirects.from_namespace is a plain string -- it has to be, since it
// names a place that no longer holds the repository -- so nothing in the
// schema removes an organisation's redirects when the organisation goes.
// Left behind, they are inherited by whoever registers that name next:
// acme/x would 308 into the namespace the *previous* acme handed it to, a
// decision the new owner of the name never made.
func TestIntegrationDeleteOrgClearsItsRedirects(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})
		r := f.repo(t, "acme", "x", "model", nil)

		// acme hands its only repository to bob, which leaves the redirect,
		// and can then delete itself: it holds no repositories any more.
		if _, err := s.TransferRepo(ctx, TransferSpec{
			RepoID: r.ID, ToNamespaceID: f.ns(t, "bob").ID, ActorID: f.alice.ID,
		}); err != nil {
			t.Fatalf("TransferRepo: %v", err)
		}
		if _, err := s.ResolveRepoRedirect(ctx, "model", "acme", "x"); err != nil {
			t.Fatalf("the redirect should exist while acme does: %v", err)
		}
		if err := s.DeleteOrg(ctx, org.ID); err != nil {
			t.Fatalf("DeleteOrg: %v", err)
		}

		if _, err := s.ResolveRepoRedirect(ctx, "model", "acme", "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("acme/x still redirects after acme was deleted (err = %v); "+
				"the next namespace to claim the name would inherit it", err)
		}
	})
}
