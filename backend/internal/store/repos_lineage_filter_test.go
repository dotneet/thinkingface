// Lineage as a search axis: the base_model / relation / dataset / base_only
// filters on ListRepos, and the relation facet that goes with them. Both
// engines run this, since the filters are EXISTS subqueries whose SQL is
// shared but whose planners are not.

package store

import (
	"testing"
)

// lineageFixture builds a small model tree around one base model:
//
//	alice/base-model  (public, no base model of its own)
//	alice/base-2      (public, no base model of its own)
//	alice/ft          fine-tune of base-model, trained on bob/imdb
//	alice/gguf        quantisation of base-model            (relation "quantized")
//	alice/legacy      base-model edge with no relation      (reads as finetune)
//	bob/secret-ft     fine-tune of base-model, in another namespace
//	alice/merged      merge of base-model and base-2 (two edges, one relation)
//	alice/other-ft    fine-tune of base-2, trained on bob/imdb
type lineageFixture struct {
	*fixture
	base, base2                     *Repo
	ft, gguf, legacy, merged, other *Repo
	secret                          *Repo
}

func newLineageFixture(t *testing.T, s *Store) *lineageFixture {
	t.Helper()
	f := &lineageFixture{fixture: newFixture(t, s)}
	ctx := f.ctx

	f.base = f.repo(t, "alice", "base-model", "model", nil)
	f.base2 = f.repo(t, "alice", "base-2", "model", nil)
	f.ft = f.repo(t, "alice", "ft", "model", nil)
	f.gguf = f.repo(t, "alice", "gguf", "model", nil)
	f.legacy = f.repo(t, "alice", "legacy", "model", nil)
	f.merged = f.repo(t, "alice", "merged", "model", nil)
	f.other = f.repo(t, "alice", "other-ft", "model", nil)
	f.secret = f.repo(t, "bob", "secret-ft", "model", nil)
	_ = f.repo(t, "bob", "imdb", "dataset", nil)

	base := func(rev, relation string) LineageEdge {
		return LineageEdge{Kind: LineageKindBaseModel, Raw: "alice/base-model", Namespace: "alice",
			Name: "base-model", Rev: rev, Relation: relation}
	}
	imdb := LineageEdge{Kind: LineageKindDataset, Raw: "bob/imdb", Namespace: "bob", Name: "imdb"}

	set := func(r *Repo, edges ...LineageEdge) {
		t.Helper()
		if err := s.ReplaceRepoLineage(ctx, r.ID, edges); err != nil {
			t.Fatalf("ReplaceRepoLineage %s: %v", r.FullName(), err)
		}
	}
	// A revision-pinned edge still belongs to alice/base-model: the filter
	// must match it without the caller knowing the revision.
	set(f.ft, base("v1", "finetune"), imdb)
	set(f.gguf, base("", "quantized"))
	// Indexed before the relation column existed: no relation at all.
	set(f.legacy, base("", ""))
	set(f.secret, base("", "finetune"))
	set(f.merged,
		base("", "merge"),
		LineageEdge{Kind: LineageKindBaseModel, Raw: "alice/base-2", Namespace: "alice",
			Name: "base-2", Relation: "merge", Ordinal: 1})
	set(f.other,
		LineageEdge{Kind: LineageKindBaseModel, Raw: "alice/base-2", Namespace: "alice",
			Name: "base-2", Relation: "finetune"},
		imdb)
	return f
}

func TestIntegrationRepoLineageFilters(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newLineageFixture(t, s)
		ctx := f.ctx

		cases := []struct {
			name   string
			filter RepoFilter
			want   []string
		}{
			{
				"base model lists every derivative, revision-pinned edges included",
				RepoFilter{BaseModel: "alice/base-model", Sort: "name"},
				[]string{"alice/ft", "alice/gguf", "alice/legacy", "alice/merged", "bob/secret-ft"},
			},
			{
				"a revision on the filter itself is ignored too",
				RepoFilter{BaseModel: "alice/base-model@v1", Sort: "name"},
				[]string{"alice/ft", "alice/gguf", "alice/legacy", "alice/merged", "bob/secret-ft"},
			},
			{
				"relation alone spans base models",
				RepoFilter{Relation: "merge", Sort: "name"},
				[]string{"alice/merged"},
			},
			{
				"relation narrows the same edge as base_model",
				RepoFilter{BaseModel: "alice/base-model", Relation: "quantized"},
				[]string{"alice/gguf"},
			},
			{
				"an unset relation reads as finetune",
				RepoFilter{BaseModel: "alice/base-model", Relation: "finetune", Sort: "name"},
				[]string{"alice/ft", "alice/legacy", "bob/secret-ft"},
			},
			{
				"base_model and relation must hold on one edge",
				// alice/merged merges base-model and base-2, and alice/other-ft
				// fine-tunes base-2 -- neither is a fine-tune of base-2 plus a
				// merge, so combining the two must not match alice/merged.
				RepoFilter{BaseModel: "alice/base-2", Relation: "finetune"},
				[]string{"alice/other-ft"},
			},
			{
				"dataset lists what was trained on it",
				RepoFilter{Dataset: "bob/imdb", Sort: "name"},
				[]string{"alice/ft", "alice/other-ft"},
			},
			{
				"base only excludes everything with a base model",
				RepoFilter{BaseOnly: true, Kind: "model", Sort: "name"},
				[]string{"alice/base-2", "alice/base-model"},
			},
			{
				"base only and base model are complements",
				RepoFilter{BaseOnly: true, BaseModel: "alice/base-model"},
				[]string{},
			},
			{
				"a malformed reference matches nothing rather than everything",
				RepoFilter{BaseModel: "garbage"},
				[]string{},
			},
			{
				"a malformed dataset reference matches nothing either",
				RepoFilter{Dataset: "garbage"},
				[]string{},
			},
			{
				"an unknown relation a card wrote is matched verbatim",
				RepoFilter{Relation: "distillation"},
				[]string{},
			},
			// Dangling edges (raw text that never parsed) store "" for both
			// halves; an empty-ish filter must not collide with them.
			{
				"lineage filters compose with the rest",
				RepoFilter{BaseModel: "alice/base-model", Dataset: "bob/imdb"},
				[]string{"alice/ft"},
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				repos, total, _, err := s.ListRepos(ctx, c.filter)
				if err != nil {
					t.Fatalf("ListRepos: %v", err)
				}
				if !equalStrings(names(repos), c.want) || total != int64(len(c.want)) {
					t.Fatalf("ListRepos = %v (total %d), want %v", names(repos), total, c.want)
				}
			})
		}

		t.Run("the relation facet counts every derivative", func(t *testing.T) {
			_, _, facets, err := s.ListRepos(ctx, RepoFilter{
				BaseModel: "alice/base-model", WithFacets: true,
			})
			if err != nil {
				t.Fatalf("ListRepos: %v", err)
			}
			got := map[string]int64{}
			for _, it := range facets.Relations {
				got[it.Value] = it.Count
			}
			// finetune = ft + legacy + secret-ft (the unset relation is
			// normalised into this bucket, exactly as the model tree groups it).
			want := map[string]int64{"finetune": 3, "quantized": 1, "merge": 1}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("relation facet[%q] = %d, want %d (got %v)", k, got[k], v, got)
				}
			}
			if len(got) != len(want) {
				t.Errorf("relation facet = %v, want exactly %v", got, want)
			}
		})

		t.Run("the relation facet drops its own dimension but keeps the base model", func(t *testing.T) {
			_, _, facets, err := s.ListRepos(ctx, RepoFilter{
				BaseModel: "alice/base-model", Relation: "quantized", WithFacets: true,
			})
			if err != nil {
				t.Fatalf("ListRepos: %v", err)
			}
			got := map[string]int64{}
			for _, it := range facets.Relations {
				got[it.Value] = it.Count
			}
			// "how many if I picked finetune instead" -- still scoped to this
			// base model, so alice/other-ft (a fine-tune of base-2) is absent.
			if got["finetune"] != 3 || got["quantized"] != 1 || got["merge"] != 1 || len(got) != 3 {
				t.Fatalf("relation facet under a selected relation = %v", got)
			}
		})

		// A merge naming two base models is one merge, not two: the facet
		// counts repositories, not edges.
		t.Run("the relation facet counts repositories, not edges", func(t *testing.T) {
			_, _, facets, err := s.ListRepos(ctx, RepoFilter{Relation: "merge", WithFacets: true})
			if err != nil {
				t.Fatalf("ListRepos: %v", err)
			}
			for _, it := range facets.Relations {
				if it.Value == "merge" && it.Count != 1 {
					t.Fatalf("merge facet count = %d, want 1", it.Count)
				}
			}
		})

		// Nothing about lineage should reach the card facets, and the base
		// model filter must still narrow them.
		t.Run("card facets stay scoped to the lineage filter", func(t *testing.T) {
			if err := s.UpdateRepoIndex(ctx, f.gguf.ID, "abc", 1,
				map[string]any{"license": "mit"}, "", false); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateRepoIndex(ctx, f.other.ID, "abc", 1,
				map[string]any{"license": "apache-2.0"}, "", false); err != nil {
				t.Fatal(err)
			}
			_, _, facets, err := s.ListRepos(ctx, RepoFilter{
				BaseModel: "alice/base-model", WithFacets: true,
			})
			if err != nil {
				t.Fatalf("ListRepos: %v", err)
			}
			got := map[string]int64{}
			for _, it := range facets.Licenses {
				got[it.Value] = it.Count
			}
			if got["mit"] != 1 || len(got) != 1 {
				t.Fatalf("license facet under base_model = %v, want only mit:1", got)
			}
		})
	})
}
