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
// pure so the parameter surface documented in docs/dev/api-contract.md §2 can be
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

// hfSortOrder is one HuggingFace `sort=` value expressed in this store's
// terms: which ordering answers it, and which direction that ordering runs in.
//
// The direction is recorded rather than applied because the store's orderings
// are fixed (store.repoOrderBy): every metric sort is descending and the name
// sort is ascending. A request for the other direction therefore cannot be
// served, and is refused instead of being answered with the wrong order --
// "most downloaded" and "least downloaded" are different questions, and a
// listing silently sorted the wrong way looks like data, not like an error.
type hfSortOrder struct {
	sort       string
	descending bool
}

// hfSorts maps HuggingFace's sort keys onto store orderings, under a
// normalised key (lowercased, "_" removed) so `lastModified`, `last_modified`
// and `lastmodified` are one entry.
//
// `likes` and `trending_score` are deliberately absent: this server tracks
// neither, and the Hub's own listing is routinely sorted by them, so silently
// falling back to the default order would hand a caller a page of results that
// look ranked and are not. They get their own message in hfListFilter.
var hfSorts = map[string]hfSortOrder{
	"downloads":    {"downloads", true},
	"lastmodified": {"", true}, // the default ordering: updated_at DESC
	"createdat":    {"created", true},
	// Ascending by (namespace, name) -- which is also ascending by author,
	// since that is the leading key.
	"id":     {"name", false},
	"name":   {"name", false},
	"author": {"name", false},
}

func normalizeHFSort(v string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(v)), "_", "")
}

// hfListUnsupported are the HuggingFace listing parameters that narrow a
// result set in ways this server cannot reproduce. Accepting one and ignoring
// it answers with a *superset* of what was asked for, which is the failure
// mode that reads as data: `gated=True` would come back full of repositories
// that are not gated, `expand=[...]` would come back missing exactly the
// fields the caller asked to be given. Each is a 400 naming itself.
var hfListUnsupported = []string{"expand", "gated", "inference", "emissions_thresholds"}

// hfListFilter maps the HuggingFace listing query string onto a store filter,
// returning a non-empty message when the request cannot be honoured as asked.
// Pure, like repoFilterFromQuery, so repolist_test.go can cover the mapping
// without a database.
func hfListFilter(kind string, q url.Values) (store.RepoFilter, string) {
	filter := store.RepoFilter{
		Kind: kind,
		// `search` is HF's substring search, which is what Query is (see the
		// field's own comment) -- not the full-text Search the Web UI uses.
		Query:  q.Get("search"),
		Author: q.Get("author"),
	}

	for _, name := range hfListUnsupported {
		if q.Get(name) != "" {
			return filter, name + " is not supported by this instance; drop it and filter the results yourself"
		}
	}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return filter, "limit must be a positive integer"
		}
		filter.Limit = n
	}
	// offset is not part of the HuggingFace protocol; it is how the `Link`
	// header below points at the next page, and clients reach it only by
	// following that link.
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return filter, "offset must be a non-negative integer"
		}
		filter.Offset = n
	}

	// `filter=` repeats and is ANDed, which is exactly the store's tag
	// filter. huggingface_hub folds `library` / `language` / `task_categories`
	// / `size_categories` and the rest into it as prefixed tags
	// ("task_categories:summarization"), so there is nothing else to map.
	filter.Tags = dedupeStrings(append(append([]string{}, q["filter"]...), q["tags"]...))
	// pipeline_tag travels on its own rather than inside `filter`.
	filter.Task = q.Get("pipeline_tag")

	if v := q.Get("sort"); v != "" {
		key := normalizeHFSort(v)
		order, ok := hfSorts[key]
		if !ok {
			if key == "likes" || key == "trendingscore" {
				return filter, "this instance does not track " + v +
					"; sort by downloads, lastModified or createdAt instead"
			}
			return filter, "sort=" + v + " is not supported; use downloads, lastModified, createdAt or id"
		}
		// The Hub reads any direction other than -1 as ascending, so an
		// absent direction asks for ascending too.
		if wantDescending := q.Get("direction") == "-1"; wantDescending != order.descending {
			if order.descending {
				return filter, "sort=" + v + " is only available in descending order here; add direction=-1"
			}
			return filter, "sort=" + v + " is only available in ascending order here; drop direction=-1"
		}
		filter.Sort = order.sort
	}
	return filter, ""
}

// dedupeStrings keeps the first occurrence of each non-empty value, in order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// hfListWantsCard reports whether the caller asked for repository cards.
// `full` and `cardData` are separate parameters on the Hub and either one
// brings cardData with it; huggingface_hub spells their values Python-style
// ("True"), hence the fold.
func hfListWantsCard(q url.Values) bool {
	return queryFlag(strings.ToLower(q.Get("full"))) || queryFlag(strings.ToLower(q.Get("cardData")))
}

// hfListPageURL rebuilds this request's URL at the next offset, the same way
// commitsPageURL does for the commit listing -- on the configured public URL
// rather than the Host header, so a client behind a proxy is never sent to an
// internal address.
func (s *Server) hfListPageURL(r *http.Request, offset int) string {
	q := r.URL.Query()
	q.Set("offset", strconv.Itoa(offset))
	return strings.TrimSuffix(s.cfg.PublicURL, "/") + r.URL.EscapedPath() + "?" + q.Encode()
}

// handleHFList backs HfApi.list_models / list_datasets.
func (s *Server) handleHFList(w http.ResponseWriter, r *http.Request) {
	kind := "model"
	if strings.HasSuffix(r.URL.Path, "/datasets") {
		kind = "dataset"
	}
	q := r.URL.Query()
	filter, badMsg := hfListFilter(kind, q)
	if badMsg != "" {
		badRequest(w, badMsg)
		return
	}
	repos, total, _, err := s.store.ListRepos(r.Context(), filter)
	if err != nil {
		internalError(w, "list repositories", err)
		return
	}
	withCard := hfListWantsCard(q)
	out := make([]map[string]any, 0, len(repos))
	for i := range repos {
		repo := &repos[i]
		item := map[string]any{
			"_id":          strconv.FormatInt(repo.ID, 10),
			"id":           repo.FullName(),
			"author":       repo.Namespace,
			"sha":          repo.HeadSHA,
			"lastModified": repo.UpdatedAt.UTC().Format(time.RFC3339),
			"createdAt":    repo.CreatedAt.UTC().Format(time.RFC3339),
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
		if withCard {
			item["cardData"] = repo.Card
		}
		out = append(out, item)
	}

	// The page size is the store's, not the caller's (default 30, max 100),
	// so every listing longer than that used to simply end at 30 with nothing
	// to say there was more -- and HfApi.list_models, which walks pages by
	// following `Link`, reported the 31st model as not existing. The cursor is
	// an offset because that is what the listing query already takes; it is
	// the client's only way in, since the parameter is not part of HF's own
	// protocol.
	if next := filter.Offset + len(repos); len(repos) > 0 && int64(next) < total {
		w.Header().Set("Link", `<`+s.hfListPageURL(r, next)+`>; rel="next"`)
	}
	writeJSON(w, http.StatusOK, out)
}
