// Pagination and facet arithmetic for the repository listing: the two places
// where a listing can be self-inconsistent without any single query looking
// wrong -- a page boundary that lands inside a run of tied rows, and a facet
// count that does not survive being clicked.

package store

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Every sort must end in a unique column. This is the cheap half of the
// guarantee the integration test below exercises: it fails deterministically
// on both engines, where a lost or duplicated row only shows up when a
// planner happens to reorder the tie.
func TestRepoOrderByIsTotal(t *testing.T) {
	for _, sort := range []string{"", "created", "downloads", "name", "size", "nonsense"} {
		order := repoOrderBy(sort)
		last := order[strings.LastIndex(order, ",")+1:]
		if got := strings.TrimSpace(last); got != "r.id DESC" && got != "r.id ASC" {
			t.Errorf("repoOrderBy(%q) = %q; the last key must be r.id so the order is total", sort, order)
		}
	}
}

// Every offset-paginated listing resolves its page size the same way: unset
// means the default, too large means the maximum -- never fewer rows than an
// unset limit would have returned.
func TestPageWindowClampsRatherThanFallsBack(t *testing.T) {
	cases := []struct{ limit, offset, wantLimit, wantOffset int }{
		{0, 0, 30, 0},
		{-1, 0, 30, 0},
		{10, 5, 10, 5},
		{100, 0, 100, 0},
		{101, 0, 100, 0},
		{5000, 0, 100, 0},
		{10, -5, 10, 0},
	}
	for _, c := range cases {
		limit, offset := pageWindow(c.limit, c.offset, 30, 100)
		if limit != c.wantLimit || offset != c.wantOffset {
			t.Errorf("pageWindow(%d, %d, 30, 100) = %d, %d; want %d, %d",
				c.limit, c.offset, limit, offset, c.wantLimit, c.wantOffset)
		}
	}
}

// A listing whose rows all tie on the sort key must still hand out every
// repository exactly once across its pages. Ties are ordinary: a bulk import
// shares a timestamp, nothing has been downloaded yet, and total_size is 0
// until the first push is indexed.
func TestIntegrationRepoListPaginationIsStableUnderTies(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		const nModels = 35
		ids := map[int64]bool{}
		for i := 0; i < nModels; i++ {
			ids[f.repo(t, "alice", fmt.Sprintf("r%02d", i), "model", nil).ID] = true
		}
		// A dataset sharing a model's name: UNIQUE is (namespace_id, name,
		// kind), so sort=name has a genuine tie even before the timestamps do.
		ids[f.repo(t, "alice", "r00", "dataset", nil).ID] = true
		total := int64(len(ids))

		// Flatten every sort key, so each ORDER BY is decided entirely by its
		// tie-breaker rather than by data that happens to differ.
		tie := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
		if _, err := s.db.Exec(ctx,
			`UPDATE repositories SET created_at = $1, updated_at = $1, total_size = 0, downloads = 0`,
			tie); err != nil {
			t.Fatalf("flatten sort keys: %v", err)
		}

		for _, sort := range []string{"", "created", "downloads", "name", "size"} {
			t.Run("sort="+sort, func(t *testing.T) {
				const page = 10
				seen := map[int64]int{}
				for offset := 0; int64(offset) < total; offset += page {
					repos, got, _, err := s.ListRepos(ctx, RepoFilter{Sort: sort, Limit: page, Offset: offset})
					if err != nil {
						t.Fatalf("ListRepos(offset %d): %v", offset, err)
					}
					if got != total {
						t.Fatalf("total = %d, want %d", got, total)
					}
					for _, r := range repos {
						seen[r.ID]++
					}
				}
				for id := range ids {
					if seen[id] != 1 {
						t.Errorf("repository %d appeared %d times across the pages, want exactly once", id, seen[id])
					}
				}
				if len(seen) != len(ids) {
					t.Errorf("pages covered %d repositories, want %d", len(seen), len(ids))
				}
			})
		}

		// "max 100" means a bigger request is served at 100, not dropped back
		// to the default 30 -- which would silently return fewer rows the
		// more the caller asked for.
		t.Run("an over-large limit is clamped, not reset to the default", func(t *testing.T) {
			repos, _, _, err := s.ListRepos(ctx, RepoFilter{Limit: 200})
			if err != nil {
				t.Fatalf("ListRepos: %v", err)
			}
			if int64(len(repos)) != total {
				t.Fatalf("limit=200 returned %d of %d repositories", len(repos), total)
			}
		})

		t.Run("no limit is the documented default", func(t *testing.T) {
			repos, _, _, err := s.ListRepos(ctx, RepoFilter{})
			if err != nil {
				t.Fatalf("ListRepos: %v", err)
			}
			if len(repos) != defaultRepoPageSize {
				t.Fatalf("unset limit returned %d rows, want %d", len(repos), defaultRepoPageSize)
			}
		})
	})
}

// A facet count is the size of the result set the facet leads to, so a value
// a card states twice -- or states in both of the two keys a task can live in
// -- is still one repository.
func TestIntegrationRepoFacetsCountRepositoriesNotMentions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		f.repo(t, "alice", "m", "model", map[string]any{
			// pipeline_tag and task_categories are the two places a task
			// name lives; the facet unions them.
			"pipeline_tag":    "text-classification",
			"task_categories": []any{"text-classification", "summarization"},
			"tags":            []any{"nlp", "nlp"},
		})

		_, _, facets, err := s.ListRepos(ctx, RepoFilter{WithFacets: true})
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}
		facet := func(items []RepoFacetItem) map[string]int64 {
			m := map[string]int64{}
			for _, it := range items {
				m[it.Value] = it.Count
			}
			return m
		}
		if got := facet(facets.Tasks); got["text-classification"] != 1 || got["summarization"] != 1 {
			t.Errorf("task facet = %v, want one repository per task", got)
		}
		if got := facet(facets.Tags); got["nlp"] != 1 {
			t.Errorf("tag facet = %v, want one repository for a tag its card repeats", got)
		}
		// The number the sidebar shows is the number the filter returns.
		for _, task := range []string{"text-classification", "summarization"} {
			if _, total, _, err := s.ListRepos(ctx, RepoFilter{Task: task}); err != nil || total != 1 {
				t.Errorf("filtering by task %q = %d repositories, %v", task, total, err)
			}
		}
	})
}
