package store

import (
	"errors"
	"testing"
)

// Namespace store tests (docs/dev/namespace-design.md §12). They run against
// every available backend: the counting query uses SUM(CASE ...) rather than
// Postgres' FILTER precisely so both engines can run it, and that claim is
// only worth anything if both are exercised.

func TestIntegrationNamespaceProfile(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		// A user namespace answers, with the profile columns empty.
		p, err := s.GetNamespaceProfile(ctx, "alice")
		if err != nil {
			t.Fatalf("GetNamespaceProfile(alice): %v", err)
		}
		if p.Kind != "user" || p.Name != "alice" {
			t.Fatalf("profile = %+v, want the user namespace alice", p)
		}
		if p.DisplayName != "" || p.Description != "" || p.Website != "" || p.AvatarURL != "" {
			t.Fatalf("fresh profile = %+v, want the profile columns empty", p)
		}
		if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
			t.Fatalf("timestamps = %v / %v, want both set", p.CreatedAt, p.UpdatedAt)
		}

		// Case-insensitive lookup answers with the registered spelling.
		if got, err := s.GetNamespaceProfile(ctx, "ALICE"); err != nil || got.Name != "alice" {
			t.Fatalf("GetNamespaceProfile(ALICE) = %+v, %v, want name alice", got, err)
		}

		// An organisation answers from the same method, kind 'org'.
		org := mustOrg(t, s, "Acme", f.alice, OrgUpdate{DisplayName: ptr("Acme Inc.")})
		got, err := s.GetNamespaceProfile(ctx, "acme")
		if err != nil {
			t.Fatalf("GetNamespaceProfile(acme): %v", err)
		}
		if got.Kind != "org" || got.Name != "Acme" || got.ID != org.ID {
			t.Fatalf("profile = %+v, want the org namespace Acme", got)
		}
		if got.DisplayName != "Acme Inc." || got.MembersVisibility != "members" {
			t.Fatalf("org profile = %+v", got)
		}

		if _, err := s.GetNamespaceProfile(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetNamespaceProfile(nope) err = %v, want ErrNotFound", err)
		}
		if _, err := s.GetNamespaceProfileByID(ctx, 999999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetNamespaceProfileByID(missing) err = %v, want ErrNotFound", err)
		}
	})
}

func TestIntegrationCountNamespaceResources(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		// An empty namespace counts zero everywhere rather than 404ing: a
		// freshly registered account has a page (docs/dev/namespace-design.md §5.5).
		bob := f.ns(t, "bob")
		if c, err := s.CountNamespaceResources(ctx, bob.ID); err != nil || c != (NamespaceCounts{}) {
			t.Fatalf("counts of an empty namespace = %+v, %v, want all zero", c, err)
		}

		f.repo(t, "alice", "m1", "model", nil)
		f.repo(t, "alice", "m2", "model", nil)
		f.repo(t, "alice", "d1", "dataset", nil)
		exp := f.repo(t, "alice", "runs", "dataset", nil)
		if err := s.UpdateRepoIndex(ctx, exp.ID, "abc", 1, map[string]any{}, "runs", true); err != nil {
			t.Fatalf("mark experiment: %v", err)
		}

		alice := f.ns(t, "alice")
		c, err := s.CountNamespaceResources(ctx, alice.ID)
		if err != nil {
			t.Fatalf("CountNamespaceResources: %v", err)
		}
		// The experiment repository is a dataset on disk but counts only
		// once, under Experiments.
		want := NamespaceCounts{Models: 2, Datasets: 1, Experiments: 1, Members: 0}
		if c != want {
			t.Fatalf("counts = %+v, want %+v", c, want)
		}

		// Members counts org_members, and only organisations have any.
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})
		if _, err := s.AddOrgMember(ctx, org.ID, f.bob.ID, "read", f.alice.ID); err != nil {
			t.Fatalf("add member: %v", err)
		}
		f.repo(t, "acme", "shared", "model", nil)
		c, err = s.CountNamespaceResources(ctx, org.ID)
		if err != nil {
			t.Fatalf("CountNamespaceResources(org): %v", err)
		}
		if want := (NamespaceCounts{Models: 1, Members: 2}); c != want {
			t.Fatalf("org counts = %+v, want %+v", c, want)
		}
	})
}

func TestIntegrationUpdateNamespaceProfile(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		alice := f.ns(t, "alice")
		before, err := s.GetNamespaceProfileByID(ctx, alice.ID)
		if err != nil {
			t.Fatalf("read before: %v", err)
		}

		updated, err := s.UpdateNamespaceProfile(ctx, alice.ID, NamespaceUpdate{
			DisplayName: ptr("Alice A."),
			Website:     ptr("https://alice.example"),
		})
		if err != nil {
			t.Fatalf("UpdateNamespaceProfile: %v", err)
		}
		if updated.DisplayName != "Alice A." || updated.Website != "https://alice.example" {
			t.Fatalf("updated = %+v", updated)
		}
		// A partial update leaves the untouched columns and the name alone.
		if updated.Description != "" || updated.AvatarURL != "" || updated.Name != "alice" {
			t.Fatalf("partial update touched more than it was given: %+v", updated)
		}
		if updated.UpdatedAt.Before(before.UpdatedAt) {
			t.Fatalf("updated_at went backwards: %v -> %v", before.UpdatedAt, updated.UpdatedAt)
		}

		// An empty string clears a field.
		cleared, err := s.UpdateNamespaceProfile(ctx, alice.ID, NamespaceUpdate{Website: ptr("")})
		if err != nil || cleared.Website != "" || cleared.DisplayName != "Alice A." {
			t.Fatalf("clear = %+v, %v", cleared, err)
		}

		// The same method edits an organisation's row.
		org := mustOrg(t, s, "acme", f.alice, OrgUpdate{})
		if got, err := s.UpdateNamespaceProfile(ctx, org.ID, NamespaceUpdate{Description: ptr("anvils")}); err != nil ||
			got.Description != "anvils" || got.Kind != "org" {
			t.Fatalf("update org profile = %+v, %v", got, err)
		}
		// ... and UpdateOrg, now a wrapper over it, still refuses a user
		// namespace and still carries members_visibility.
		if _, err := s.UpdateOrg(ctx, alice.ID, OrgUpdate{Description: ptr("x")}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateOrg on a user namespace err = %v, want ErrNotFound", err)
		}

		if _, err := s.UpdateNamespaceProfile(ctx, 999999, NamespaceUpdate{Website: ptr("x")}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("UpdateNamespaceProfile(missing) err = %v, want ErrNotFound", err)
		}
	})
}
