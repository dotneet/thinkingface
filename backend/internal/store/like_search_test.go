package store

import (
	"sort"
	"testing"
)

// Every substring filter in this package runs through the same escaping
// (like.go), and the reason it has to is only visible against a live database:
// unescaped, "%" and "_" turn into wildcards, and a backslash means opposite
// things on the two engines -- PostgreSQL's LIKE takes it as the escape
// character by default, SQLite's takes it literally unless ESCAPE says
// otherwise. So these cases run on every available backend and assert the same
// answer from both. A regression that reaches only one engine is the expensive
// kind: the listing still works locally on SQLite and silently mis-answers in
// production, or the reverse.

func TestIntegrationListUsersSearchIsALiteralSubstring(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		if _, err := s.CreateUser(ctx, "a_b", "a_b@example.com", "hash", false); err != nil {
			t.Fatalf("create a_b: %v", err)
		}
		if _, err := s.CreateUser(ctx, "axb", "axb@example.com", "hash", false); err != nil {
			t.Fatalf("create axb: %v", err)
		}
		// The backslash cases carry it in the email, which is the free-form
		// half of this listing's search.
		if _, err := s.CreateUser(ctx, "slashy", `a\b@example.com`, "hash", false); err != nil {
			t.Fatalf("create slashy: %v", err)
		}
		if _, err := s.CreateUser(ctx, "plain", "ab@example.com", "hash", false); err != nil {
			t.Fatalf("create plain: %v", err)
		}

		cases := []struct {
			search string
			want   []string
		}{
			// "_" is a single-character wildcard unescaped, so this used to
			// drag "axb" in with it.
			{"a_b", []string{"a_b"}},
			// "%" unescaped matches everything, which reads to the caller as
			// "the filter was ignored".
			{"%", nil},
			{"a%b", nil},
			// The engines disagreed here: Postgres matched "ab@example.com"
			// (backslash as its default escape) and SQLite matched
			// `a\b@example.com` (backslash as an ordinary character).
			{`a\b`, []string{"slashy"}},
			// Nothing was broken about the ordinary case, and it stays that
			// way: plain substrings still match plainly.
			{"ali", []string{"alice"}},
		}
		for _, c := range cases {
			got, total, err := s.ListUsers(ctx, c.search, 0, 0)
			if err != nil {
				t.Fatalf("search %q: %v", c.search, err)
			}
			names := make([]string, 0, len(got))
			for _, u := range got {
				names = append(names, u.Username)
			}
			if !equalStrings(names, c.want) {
				t.Errorf("search %q = %v, want %v", c.search, names, c.want)
			}
			if total != int64(len(c.want)) {
				t.Errorf("search %q total = %d, want %d", c.search, total, len(c.want))
			}
		}
	})
}

func TestIntegrationListOrgsSearchIsALiteralSubstring(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		orgs := []struct{ name, display string }{
			{"cotton", "100% Cotton"},
			{"blend", "100 Cotton"},
			{"under", "a_b Labs"},
			{"exes", "axb Labs"},
			{"slashy", `a\b Labs`},
			{"plain", "ab Labs"},
		}
		for _, o := range orgs {
			display := o.display
			if _, err := s.CreateOrg(ctx, o.name, f.alice.ID, OrgUpdate{DisplayName: &display}); err != nil {
				t.Fatalf("create org %s: %v", o.name, err)
			}
		}

		cases := []struct {
			search string
			want   []string
		}{
			{"100%", []string{"cotton"}},
			{"a_b", []string{"under"}},
			{`a\b`, []string{"slashy"}},
			// A lone "%" is a search for that character, not a wildcard that
			// quietly returns the whole directory -- so it finds the one
			// display name that really contains one.
			{"%", []string{"cotton"}},
			{"Labs", []string{"exes", "plain", "slashy", "under"}},
		}
		for _, c := range cases {
			got, total, err := s.ListOrgs(ctx, c.search, nil, 0, 0)
			if err != nil {
				t.Fatalf("search %q: %v", c.search, err)
			}
			names := make([]string, 0, len(got))
			for _, o := range got {
				names = append(names, o.Name)
			}
			sort.Strings(names)
			if !equalStrings(names, c.want) {
				t.Errorf("search %q = %v, want %v", c.search, names, c.want)
			}
			if total != int64(len(c.want)) {
				t.Errorf("search %q total = %d, want %d", c.search, total, len(c.want))
			}
		}
	})
}

// RepoFilter.Query is the HF-compatible `search=` / `q=`: huggingface_hub
// passes whatever the caller typed and expects a plain substring, so a "%" in
// it must narrow the listing rather than widen it to everything.
func TestIntegrationListReposQueryIsALiteralSubstring(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		for _, name := range []string{"a_b", "axb", `a\b`, "ab"} {
			f.repo(t, "alice", name, "model", nil)
		}

		cases := []struct {
			query string
			want  []string
		}{
			{"a_b", []string{"alice/a_b"}},
			{`a\b`, []string{`alice/a\b`}},
			{"%", nil},
			// Untouched: the substring match huggingface_hub depends on
			// ("bert" finding "distilbert") is the whole point of this filter.
			{"xb", []string{"alice/axb"}},
		}
		for _, c := range cases {
			got, total, _, err := s.ListRepos(ctx, RepoFilter{Query: c.query})
			if err != nil {
				t.Fatalf("query %q: %v", c.query, err)
			}
			full := names(got)
			sort.Strings(full)
			if !equalStrings(full, c.want) {
				t.Errorf("query %q = %v, want %v", c.query, full, c.want)
			}
			if total != int64(len(c.want)) {
				t.Errorf("query %q total = %d, want %d", c.query, total, len(c.want))
			}
		}
	})
}
