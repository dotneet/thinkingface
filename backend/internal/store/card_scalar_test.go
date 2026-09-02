package store

import (
	"sort"
	"testing"
)

// YAML front matter is not a string type. `license: yes` decodes to a
// boolean, `license: 2` and `license: 2.0` to numbers, and all three end up in
// the card as what they are. Postgres' jsonb `->>` renders every scalar as
// text, so filtering and faceting agreed; SQLite's `->>` hands the value back
// in its own storage type, so `r.card->>'license' = $1` never matched -- while
// the facet, which groups by the same expression, happily listed the value.
// The result was a sidebar entry that returned nothing when clicked, and it
// was different on the two backends.
func TestIntegrationCardScalarFiltersMatchTheFacetsTheyCameFrom(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		f.repo(t, "alice", "strlic", "model", map[string]any{"license": "mit", "pipeline_tag": "text-generation"})
		f.repo(t, "alice", "numlic", "model", map[string]any{"license": 2, "pipeline_tag": 7})
		f.repo(t, "alice", "boollic", "model", map[string]any{"license": true})

		cases := []struct {
			name   string
			filter RepoFilter
			want   string
		}{
			{"string license", RepoFilter{License: "mit"}, "alice/strlic"},
			{"numeric license", RepoFilter{License: "2"}, "alice/numlic"},
			{"boolean license", RepoFilter{License: "true"}, "alice/boollic"},
			{"string task", RepoFilter{Task: "text-generation"}, "alice/strlic"},
			{"numeric task", RepoFilter{Task: "7"}, "alice/numlic"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				got, _, _, err := s.ListRepos(ctx, c.filter)
				if err != nil {
					t.Fatalf("ListRepos: %v", err)
				}
				if len(got) != 1 || got[0].FullName() != c.want {
					t.Fatalf("ListRepos(%+v) = %v, want [%s]", c.filter, names(got), c.want)
				}
			})
		}

		// And the facets offer exactly those values, spelled the same way, on
		// both engines -- so every entry in the sidebar is one the filter
		// above can find.
		_, _, facets, err := s.ListRepos(ctx, RepoFilter{WithFacets: true})
		if err != nil {
			t.Fatalf("ListRepos with facets: %v", err)
		}
		if got, want := facetValues(facets.Licenses), []string{"2", "mit", "true"}; !equalStrings(got, want) {
			t.Errorf("license facet = %v, want %v", got, want)
		}
		if got, want := facetValues(facets.Tasks), []string{"7", "text-generation"}; !equalStrings(got, want) {
			t.Errorf("task facet = %v, want %v", got, want)
		}
	})
}

func facetValues(items []RepoFacetItem) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.Value)
	}
	sort.Strings(out)
	return out
}
