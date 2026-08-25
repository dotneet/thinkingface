package store

import (
	"errors"
	"testing"
)

// Storage quota store tests. The recurring theme is the three-way
// distinction the column has to keep: NULL ("inherit the instance default"),
// 0 ("a quota of zero bytes") and a positive number. Collapsing the first two
// -- with a COALESCE, or by storing 0 for "cleared" -- is the mistake this
// file exists to catch, so most cases here are really about which of the
// three a round trip comes back as.

// linkObject registers an LFS object of the given size against a repository,
// the way an upload's verify does. The confirmPresent callback stands in for
// the storage round trip: these tests have no bucket, and what they are about
// is the ledger the quota is computed from.
func linkObject(t *testing.T, s *Store, repoID int64, oid string, size int64) {
	t.Helper()
	if err := s.RecordLFSObject(t.Context(), repoID, oid, size, func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("RecordLFSObject(%s): %v", oid, err)
	}
}

func TestEffectiveQuota(t *testing.T) {
	tests := []struct {
		name        string
		override    *int64
		defaultBy   int64
		want        *int64
		wantMessage string
	}{
		{name: "no override, no default: unlimited", override: nil, defaultBy: 0, want: nil},
		{name: "no override falls back to the default", override: nil, defaultBy: 100, want: ptr(int64(100))},
		{name: "an override wins over the default", override: ptr(int64(5)), defaultBy: 100, want: ptr(int64(5))},
		// The pair that must never be folded together: a stored zero is a
		// real quota, and it has to survive a default that would otherwise
		// look more permissive.
		{name: "an override of zero is a quota, not an absence", override: ptr(int64(0)), defaultBy: 100, want: ptr(int64(0))},
		// ... while a *configured* zero is how "unlimited" is spelled, so an
		// instance that never sets one refuses nothing.
		{name: "a default of zero is unlimited", override: nil, defaultBy: 0, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveQuota(tt.override, tt.defaultBy)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("EffectiveQuota() = %d, want nil (unlimited)", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("EffectiveQuota() = nil, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Fatalf("EffectiveQuota() = %d, want %d", *got, *tt.want)
			}
		})
	}
}

func TestIntegrationNamespaceQuotaRoundTrip(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		// A namespace nobody has touched carries no override at all, which is
		// what makes an instance without quotas behave exactly as before.
		got, err := s.GetNamespaceQuota(ctx, "alice")
		if err != nil {
			t.Fatalf("GetNamespaceQuota(alice): %v", err)
		}
		if got.QuotaBytes != nil {
			t.Fatalf("a fresh namespace has quota %d, want no override", *got.QuotaBytes)
		}
		if got.Kind != "user" || got.Namespace != "alice" {
			t.Fatalf("GetNamespaceQuota(alice) = %+v", got)
		}

		if err := s.SetNamespaceQuota(ctx, "alice", ptr(int64(4096))); err != nil {
			t.Fatalf("SetNamespaceQuota: %v", err)
		}
		if got, err = s.GetNamespaceQuota(ctx, "alice"); err != nil || got.QuotaBytes == nil || *got.QuotaBytes != 4096 {
			t.Fatalf("after set: %+v, %v", got, err)
		}

		// Zero is a quota, and reading it back as one is the whole point of
		// the column being nullable.
		if err := s.SetNamespaceQuota(ctx, "alice", ptr(int64(0))); err != nil {
			t.Fatalf("SetNamespaceQuota(0): %v", err)
		}
		if got, err = s.GetNamespaceQuota(ctx, "alice"); err != nil || got.QuotaBytes == nil || *got.QuotaBytes != 0 {
			t.Fatalf("after set to zero: %+v, %v", got, err)
		}

		// nil clears it, which is a different state from the zero above.
		if err := s.SetNamespaceQuota(ctx, "alice", nil); err != nil {
			t.Fatalf("SetNamespaceQuota(nil): %v", err)
		}
		if got, err = s.GetNamespaceQuota(ctx, "alice"); err != nil || got.QuotaBytes != nil {
			t.Fatalf("after clearing: %+v, %v", got, err)
		}

		// Organisations take a quota the same way accounts do: both are
		// namespaces, and the organisation is usually the one holding the
		// large datasets.
		mustOrg(t, s, "Acme", f.alice, OrgUpdate{})
		if err := s.SetNamespaceQuota(ctx, "acme", ptr(int64(9))); err != nil {
			t.Fatalf("SetNamespaceQuota(acme): %v", err)
		}
		// Names are matched case-insensitively everywhere else, so an
		// administrator typing the wrong case must not silently write nothing.
		if got, err = s.GetNamespaceQuota(ctx, "ACME"); err != nil || got.QuotaBytes == nil || *got.QuotaBytes != 9 {
			t.Fatalf("GetNamespaceQuota(ACME) = %+v, %v", got, err)
		}
		if got.Namespace != "Acme" || got.Kind != "org" {
			t.Fatalf("GetNamespaceQuota(ACME) = %+v, want the org's stored spelling", got)
		}

		if err := s.SetNamespaceQuota(ctx, "nobody", ptr(int64(1))); !errors.Is(err, ErrNotFound) {
			t.Fatalf("SetNamespaceQuota(missing) = %v, want ErrNotFound", err)
		}
		if _, err := s.GetNamespaceQuota(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetNamespaceQuota(missing) = %v, want ErrNotFound", err)
		}
	})
}

func TestIntegrationNamespaceQuotaForRepoSumsTheWholeNamespace(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		one := f.repo(t, "alice", "one", "model", nil)
		two := f.repo(t, "alice", "two", "dataset", nil)
		theirs := f.repo(t, "bob", "theirs", "model", nil)

		linkObject(t, s, one.ID, "aaaa", 100)
		linkObject(t, s, two.ID, "bbbb", 30)
		linkObject(t, s, theirs.ID, "cccc", 7000)

		// The check the upload path makes is against the namespace, not the
		// repository it happens to be pushing to: filling one repository
		// after another is the obvious way around a per-repository limit.
		// someLimit stands for "this instance has a quota configured", which
		// is what makes usage worth aggregating at all -- with no limit
		// anywhere the call deliberately skips the aggregation and leaves
		// UsedBytes at 0 (see the dedicated test below).
		const someLimit = int64(1)

		q, err := s.NamespaceQuotaForRepo(ctx, one.ID, someLimit)
		if err != nil {
			t.Fatalf("NamespaceQuotaForRepo: %v", err)
		}
		if q.Namespace != "alice" || q.UsedBytes != 130 || q.NumRepos != 2 {
			t.Fatalf("NamespaceQuotaForRepo(one) = %+v, want alice using 130 bytes over 2 repos", q)
		}
		// ... and another namespace's bytes are not in it.
		if q, err = s.NamespaceQuotaForRepo(ctx, theirs.ID, someLimit); err != nil || q.UsedBytes != 7000 {
			t.Fatalf("NamespaceQuotaForRepo(theirs) = %+v, %v, want 7000", q, err)
		}

		if err := s.SetNamespaceQuota(ctx, "alice", ptr(int64(200))); err != nil {
			t.Fatalf("SetNamespaceQuota: %v", err)
		}
		if q, err = s.NamespaceQuotaForRepo(ctx, one.ID, 0); err != nil || q.QuotaBytes == nil || *q.QuotaBytes != 200 {
			t.Fatalf("NamespaceQuotaForRepo after set = %+v, %v", q, err)
		}

		if _, err := s.NamespaceQuotaForRepo(ctx, 999999, someLimit); !errors.Is(err, ErrNotFound) {
			t.Fatalf("NamespaceQuotaForRepo(missing) = %v, want ErrNotFound", err)
		}
	})
}

// With no override and no instance default there is no limit to compare
// against, and the usage aggregation -- which scans every repository in the
// namespace -- is skipped. This runs on the upload path of every push, so an
// instance that has never configured a quota must not pay for an answer
// nothing reads.
func TestIntegrationNamespaceQuotaForRepoSkipsUsageWhenUnlimited(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		one := f.repo(t, "alice", "one", "model", nil)
		linkObject(t, s, one.ID, "aaaa", 100)

		q, err := s.NamespaceQuotaForRepo(ctx, one.ID, 0)
		if err != nil {
			t.Fatalf("NamespaceQuotaForRepo: %v", err)
		}
		if q.Namespace != "alice" || q.QuotaBytes != nil {
			t.Fatalf("NamespaceQuotaForRepo = %+v, want alice with no override", q)
		}
		if q.UsedBytes != 0 {
			t.Errorf("UsedBytes = %d, want 0: usage must not be aggregated when nothing limits it", q.UsedBytes)
		}

		// An instance default is a limit, so the same call now does the work.
		if q, err = s.NamespaceQuotaForRepo(ctx, one.ID, 1000); err != nil {
			t.Fatalf("NamespaceQuotaForRepo with a default: %v", err)
		}
		if q.UsedBytes != 100 {
			t.Errorf("UsedBytes = %d, want 100 once a default applies", q.UsedBytes)
		}

		// So is an override of zero, which is a real quota and not "unset".
		if err := s.SetNamespaceQuota(ctx, "alice", ptr(int64(0))); err != nil {
			t.Fatalf("SetNamespaceQuota: %v", err)
		}
		if q, err = s.NamespaceQuotaForRepo(ctx, one.ID, 0); err != nil {
			t.Fatalf("NamespaceQuotaForRepo with a zero override: %v", err)
		}
		if q.UsedBytes != 100 {
			t.Errorf("UsedBytes = %d, want 100: an override of zero still limits", q.UsedBytes)
		}
	})
}

func TestIntegrationListNamespaceQuotas(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		mustOrg(t, s, "acme", f.alice, OrgUpdate{})

		repo := f.repo(t, "alice", "one", "model", nil)
		linkObject(t, s, repo.ID, "aaaa", 42)
		if err := s.SetNamespaceQuota(ctx, "alice", ptr(int64(1000))); err != nil {
			t.Fatalf("SetNamespaceQuota: %v", err)
		}

		rows, total, err := s.ListNamespaceQuotas(ctx, "", 0, 0)
		if err != nil {
			t.Fatalf("ListNamespaceQuotas: %v", err)
		}
		// admin, alice, bob and the organisation, ordered by name.
		if total != 4 || len(rows) != 4 {
			t.Fatalf("ListNamespaceQuotas() = %d rows, total %d, want 4/4", len(rows), total)
		}
		if rows[0].Namespace != "acme" || rows[1].Namespace != "admin" {
			t.Fatalf("order = %s, %s, want acme, admin", rows[0].Namespace, rows[1].Namespace)
		}
		var alice NamespaceQuota
		for _, r := range rows {
			if r.Namespace == "alice" {
				alice = r
			}
		}
		if alice.UsedBytes != 42 || alice.NumRepos != 1 || alice.QuotaBytes == nil || *alice.QuotaBytes != 1000 {
			t.Fatalf("alice = %+v, want 42 bytes over 1 repo with a 1000 byte quota", alice)
		}
		// A namespace holding nothing is listed with zeroes rather than
		// dropped: an administrator setting a quota ahead of the first push
		// has to be able to find it.
		for _, r := range rows {
			if r.Namespace == "acme" && (r.UsedBytes != 0 || r.NumRepos != 0 || r.QuotaBytes != nil) {
				t.Fatalf("acme = %+v, want an empty namespace with no override", r)
			}
		}

		// Search is a case-insensitive substring of the name, as it is for
		// the account directory next door.
		rows, total, err = s.ListNamespaceQuotas(ctx, "LIC", 0, 0)
		if err != nil || total != 1 || len(rows) != 1 || rows[0].Namespace != "alice" {
			t.Fatalf("ListNamespaceQuotas(LIC) = %+v, total %d, %v", rows, total, err)
		}

		// Paging: total ignores the window, so a page-2 read still knows how
		// many there are.
		rows, total, err = s.ListNamespaceQuotas(ctx, "", 2, 2)
		if err != nil || total != 4 || len(rows) != 2 {
			t.Fatalf("ListNamespaceQuotas(limit 2, offset 2) = %d rows, total %d, %v", len(rows), total, err)
		}
		if rows[0].Namespace != "alice" || rows[1].Namespace != "bob" {
			t.Fatalf("page 2 = %s, %s, want alice, bob", rows[0].Namespace, rows[1].Namespace)
		}
	})
}

func TestIntegrationNamespaceQuotaOverrides(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		if err := s.SetNamespaceQuota(ctx, "alice", ptr(int64(0))); err != nil {
			t.Fatalf("SetNamespaceQuota: %v", err)
		}

		got, err := s.NamespaceQuotaOverrides(ctx, []string{"alice", "bob"})
		if err != nil {
			t.Fatalf("NamespaceQuotaOverrides: %v", err)
		}
		// bob has no override, so he is absent rather than present-and-zero:
		// a caller reading a zero out of the map would enforce a quota
		// nobody set.
		if v, ok := got["alice"]; !ok || v != 0 {
			t.Fatalf("overrides[alice] = %d, %v, want 0, true", v, ok)
		}
		if _, ok := got["bob"]; ok {
			t.Fatalf("overrides[bob] is present; a namespace without an override must be absent")
		}
		if got, err := s.NamespaceQuotaOverrides(ctx, nil); err != nil || len(got) != 0 {
			t.Fatalf("NamespaceQuotaOverrides(nil) = %+v, %v, want an empty map", got, err)
		}
	})
}
