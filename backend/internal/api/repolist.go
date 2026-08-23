// Repository listing and search: the UI's paginated browse endpoint, the
// dashboard counters, and the HuggingFace list_models/list_datasets APIs.

package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(r.Context())
	if err != nil {
		internalError(w, "load stats", err)
		return
	}
	writeJSON(w, http.StatusOK, apitypes.StatsResponse{
		Datasets: stats.Datasets, Models: stats.Models,
		Experiments: stats.Experiments, TotalSize: stats.TotalSize,
	})
}

// listRepoTags merges the singular `tag=` param (kept for old links, e.g.
// the tag badges on a repository page) with the repeatable `tags=` param the
// facet sidebar sends, deduplicating case-sensitively.
func listRepoTags(q url.Values) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(q["tags"])+1)
	add := func(tag string) {
		if tag == "" || seen[tag] {
			return
		}
		seen[tag] = true
		out = append(out, tag)
	}
	add(q.Get("tag"))
	for _, tag := range q["tags"] {
		add(tag)
	}
	return out
}

// queryFlag reads a boolean query parameter the way the Web UI spells one.
func queryFlag(v string) bool { return v == "true" || v == "1" }

// repoFilterFromQuery maps the listing query string onto a store filter. It is
// pure so the parameter surface documented in docs/api-contract.md §2 can be
// tested without a database (repolist_test.go); the viewer scope is layered on
// by the handler, since only it knows who is asking.
func repoFilterFromQuery(q url.Values) store.RepoFilter {
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	filter := store.RepoFilter{
		Kind:      q.Get("kind"),
		Query:     q.Get("q"),
		Search:    q.Get("search"),
		Author:    q.Get("author"),
		Tags:      listRepoTags(q),
		License:   q.Get("license"),
		Task:      q.Get("task"),
		BaseModel: q.Get("base_model"),
		Relation:  q.Get("relation"),
		Dataset:   q.Get("dataset"),
		BaseOnly:  queryFlag(q.Get("base_only")),
		Sort:      q.Get("sort"),
		Limit:     limit,
		Offset:    offset,
	}
	if v := q.Get("experiment"); v != "" {
		b := queryFlag(v)
		filter.IsExperiment = &b
	}
	// Absent means "both": an archived repository stays in the listing with a
	// badge rather than vanishing, since disappearing would read as deleted.
	if v := q.Get("archived"); v != "" {
		b := queryFlag(v)
		filter.Archived = &b
	}
	return filter
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	filter := repoFilterFromQuery(r.URL.Query())
	filter.WithFacets = true

	repos, total, facets, err := s.store.ListRepos(r.Context(), filter)
	if err != nil {
		internalError(w, "list repositories", err)
		return
	}
	items := make([]apitypes.RepoSummary, 0, len(repos))
	for i := range repos {
		items = append(items, toSummary(&repos[i]))
	}
	writeJSON(w, http.StatusOK, apitypes.RepoListResponse{
		Items:  items,
		Total:  total,
		Facets: toFacets(facets),
	})
}

func toFacets(f store.RepoFacets) apitypes.RepoFacets {
	return apitypes.RepoFacets{
		Tags:      toFacetItems(f.Tags),
		Licenses:  toFacetItems(f.Licenses),
		Tasks:     toFacetItems(f.Tasks),
		Relations: toFacetItems(f.Relations),
	}
}

func toFacetItems(items []store.RepoFacetItem) []apitypes.RepoFacetItem {
	out := make([]apitypes.RepoFacetItem, 0, len(items))
	for _, item := range items {
		out = append(out, apitypes.RepoFacetItem{Value: item.Value, Count: item.Count})
	}
	return out
}

// handleHFList backs HfApi.list_models / list_datasets.
func (s *Server) handleHFList(w http.ResponseWriter, r *http.Request) {
	kind := "model"
	if strings.HasSuffix(r.URL.Path, "/datasets") {
		kind = "dataset"
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	filter := store.RepoFilter{Kind: kind, Query: q.Get("search"), Author: q.Get("author"), Limit: limit}
	repos, _, _, err := s.store.ListRepos(r.Context(), filter)
	if err != nil {
		internalError(w, "list repositories", err)
		return
	}
	out := make([]map[string]any, 0, len(repos))
	for i := range repos {
		repo := &repos[i]
		item := map[string]any{
			"_id":          strconv.FormatInt(repo.ID, 10),
			"id":           repo.FullName(),
			"author":       repo.Namespace,
			"sha":          repo.HeadSHA,
			"lastModified": repo.UpdatedAt.UTC().Format(time.RFC3339),
			// Kept for huggingface_hub, which reads this field off every
			// listing entry. There is no visibility concept here, so it is
			// always false.
			"private":   false,
			"tags":      repo.Tags(),
			"downloads": repo.Downloads,
			"likes":     0,
		}
		if kind == "model" {
			item["modelId"] = repo.FullName()
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}
