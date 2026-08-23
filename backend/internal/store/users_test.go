package store

import (
	"errors"
	"testing"
)

// Account administration at the store layer: the listing behind the site
// admin's user directory, the password write, and the last-site-admin rule.
// They run against every available backend, which is what proves the
// ILIKE-to-LIKE rewrite and the advisory lock behave the same on SQLite and
// Postgres.

func TestIntegrationListUsers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		users, total, err := s.ListUsers(ctx, "", 0, 0)
		if err != nil {
			t.Fatalf("list users: %v", err)
		}
		if total != 3 || len(users) != 3 {
			t.Fatalf("listed %d of %d, want 3 of 3", len(users), total)
		}
		// Ordered by username, so the page window is stable between calls.
		if users[0].Username != "admin" || users[1].Username != "alice" || users[2].Username != "bob" {
			t.Fatalf("order = %s/%s/%s, want admin/alice/bob",
				users[0].Username, users[1].Username, users[2].Username)
		}
		// The hash is never selected: a listing has no use for it.
		for _, u := range users {
			if u.PasswordHash != "" {
				t.Fatalf("%s carries a password hash into the listing", u.Username)
			}
		}
		if !users[0].IsAdmin || users[1].IsAdmin {
			t.Fatalf("is_admin = %v/%v, want true/false", users[0].IsAdmin, users[1].IsAdmin)
		}

		// Search matches the username or the email, case-insensitively.
		for _, q := range []string{"ALI", "alice@", "Alice"} {
			got, n, err := s.ListUsers(ctx, q, 0, 0)
			if err != nil {
				t.Fatalf("search %q: %v", q, err)
			}
			if n != 1 || len(got) != 1 || got[0].Username != "alice" {
				t.Fatalf("search %q = %d rows (total %d), want just alice", q, len(got), n)
			}
		}
		if _, n, err := s.ListUsers(ctx, "nobody", 0, 0); err != nil || n != 0 {
			t.Fatalf("search miss = %d, %v, want 0, nil", n, err)
		}

		// Total ignores the page window; the window itself is honoured.
		page, total, err := s.ListUsers(ctx, "", 2, 1)
		if err != nil {
			t.Fatalf("paged list: %v", err)
		}
		if total != 3 || len(page) != 2 || page[0].Username != "alice" {
			t.Fatalf("page = %d rows of %d starting %q, want 2 of 3 starting alice",
				len(page), total, page[0].Username)
		}
		// An out-of-range limit falls back to the default rather than erroring.
		if _, _, err := s.ListUsers(ctx, "", 100000, -5); err != nil {
			t.Fatalf("clamped list: %v", err)
		}
	})
}

func TestIntegrationUpdateUserPassword(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		if err := s.UpdateUserPassword(ctx, f.alice.ID, "new-hash"); err != nil {
			t.Fatalf("update password: %v", err)
		}
		got, err := s.GetUserByUsername(ctx, "alice")
		if err != nil {
			t.Fatalf("reload alice: %v", err)
		}
		if got.PasswordHash != "new-hash" {
			t.Fatalf("hash = %q, want new-hash", got.PasswordHash)
		}
		// Revoking sessions is the caller's decision, not a side effect.
		if got.SessionEpoch != f.alice.SessionEpoch {
			t.Fatalf("session_epoch moved to %d on its own", got.SessionEpoch)
		}
		if err := s.UpdateUserPassword(ctx, 999999, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update on a missing user = %v, want ErrNotFound", err)
		}
	})
}

func TestIntegrationSetUserAdmin(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		// admin is the only administrator, so revoking its flag would leave
		// the instance with nobody able to hand it back.
		if err := s.SetUserAdmin(ctx, f.admin.ID, false); !errors.Is(err, ErrLastSiteAdmin) {
			t.Fatalf("demote the last admin = %v, want ErrLastSiteAdmin", err)
		}
		if got, _ := s.GetUserByID(ctx, f.admin.ID); !got.IsAdmin {
			t.Fatalf("the refused demotion still cleared is_admin")
		}

		// With a second administrator appointed, the first may step down.
		if err := s.SetUserAdmin(ctx, f.alice.ID, true); err != nil {
			t.Fatalf("promote alice: %v", err)
		}
		if got, _ := s.GetUserByID(ctx, f.alice.ID); !got.IsAdmin {
			t.Fatalf("alice was not promoted")
		}
		if err := s.SetUserAdmin(ctx, f.admin.ID, false); err != nil {
			t.Fatalf("demote admin with alice appointed: %v", err)
		}
		if got, _ := s.GetUserByID(ctx, f.admin.ID); got.IsAdmin {
			t.Fatalf("admin still carries is_admin")
		}
		// And now alice is the last one, so she is the one who is stuck.
		if err := s.SetUserAdmin(ctx, f.alice.ID, false); !errors.Is(err, ErrLastSiteAdmin) {
			t.Fatalf("demote the new last admin = %v, want ErrLastSiteAdmin", err)
		}

		// Revoking from someone who never had it is a no-op, not a
		// last-admin error: the count cannot drop.
		if err := s.SetUserAdmin(ctx, f.bob.ID, false); err != nil {
			t.Fatalf("clear is_admin on a non-admin: %v", err)
		}
		if err := s.SetUserAdmin(ctx, 999999, true); !errors.Is(err, ErrNotFound) {
			t.Fatalf("promote a missing user = %v, want ErrNotFound", err)
		}
	})
}
