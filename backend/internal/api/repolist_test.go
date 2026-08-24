// The query-string surface of the two repository listings
// (docs/dev/api-contract.md §2): the Web UI's GET /api/v1/repos and the
// HF-compatible GET /api/models / /api/datasets. Both map their query string
// onto a store filter with a pure function, so the mapping is checked here
// without a database; what the filters then mean is store's own integration
// suite.

package api

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	return q
}

func TestRepoFilterFromQueryLineageParams(t *testing.T) {
	f := repoFilterFromQuery(mustQuery(t,
		"base_model=alice%2Fbert-base%40main&relation=quantized&dataset=bob%2Fimdb&base_only=true"))

	if f.BaseModel != "alice/bert-base@main" {
		t.Errorf("BaseModel = %q", f.BaseModel)
	}
	if f.Relation != "quantized" {
		t.Errorf("Relation = %q", f.Relation)
	}
	if f.Dataset != "bob/imdb" {
		t.Errorf("Dataset = %q", f.Dataset)
	}
	if !f.BaseOnly {
		t.Error("BaseOnly = false, want true")
	}
}

func TestRepoFilterFromQueryDefaults(t *testing.T) {
	f := repoFilterFromQuery(url.Values{})
	if f.BaseModel != "" || f.Relation != "" || f.Dataset != "" || f.BaseOnly {
		t.Errorf("empty query produced lineage filters: %+v", f)
	}
	// Absent tri-states stay nil: "both" for archived, "either" for
	// experiment. Only an explicit value narrows the listing.
	if f.Archived != nil || f.IsExperiment != nil {
		t.Errorf("empty query set a tri-state: archived=%v experiment=%v", f.Archived, f.IsExperiment)
	}
	if f.WithFacets {
		t.Error("WithFacets must be set by the handler, not the parser")
	}
}

func TestRepoFilterFromQueryFlagsAcceptTrueAndOne(t *testing.T) {
	for _, raw := range []string{"base_only=true", "base_only=1"} {
		if !repoFilterFromQuery(mustQuery(t, raw)).BaseOnly {
			t.Errorf("%q did not set BaseOnly", raw)
		}
	}
	for _, raw := range []string{"base_only=false", "base_only=0", "base_only=yes", "base_only="} {
		if repoFilterFromQuery(mustQuery(t, raw)).BaseOnly {
			t.Errorf("%q set BaseOnly", raw)
		}
	}
}

func TestRepoFilterFromQueryArchivedTriState(t *testing.T) {
	tests := []struct {
		raw  string
		want *bool
	}{
		{"", nil},
		{"archived=true", boolPtr(true)},
		{"archived=1", boolPtr(true)},
		{"archived=false", boolPtr(false)},
		{"archived=0", boolPtr(false)},
	}
	for _, tt := range tests {
		got := repoFilterFromQuery(mustQuery(t, tt.raw)).Archived
		switch {
		case tt.want == nil && got != nil:
			t.Errorf("%q: Archived = %v, want nil", tt.raw, *got)
		case tt.want != nil && (got == nil || *got != *tt.want):
			t.Errorf("%q: Archived = %v, want %v", tt.raw, got, *tt.want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// ---------------------------------------------------- HF-compatible listings

// The parameter surface of GET /api/models / /api/datasets, which
// HfApi.list_models and list_datasets drive. hfListFilter is pure for the same
// reason repoFilterFromQuery is.

func TestHFListFilterMapsTheHFParameters(t *testing.T) {
	f, msg := hfListFilter("model", mustQuery(t,
		"search=bert&author=alice&filter=pytorch&filter=text-classification&tags=en&pipeline_tag=fill-mask&limit=7&offset=14"))
	if msg != "" {
		t.Fatalf("unexpected refusal: %s", msg)
	}
	if f.Kind != "model" || f.Query != "bert" || f.Author != "alice" {
		t.Errorf("kind/search/author = %q/%q/%q", f.Kind, f.Query, f.Author)
	}
	// `search` is HF's substring match, never the full-text one.
	if f.Search != "" {
		t.Errorf("Search = %q, want the full-text filter left alone", f.Search)
	}
	want := []string{"pytorch", "text-classification", "en"}
	if len(f.Tags) != len(want) {
		t.Fatalf("Tags = %v, want %v", f.Tags, want)
	}
	for i := range want {
		if f.Tags[i] != want[i] {
			t.Fatalf("Tags = %v, want %v", f.Tags, want)
		}
	}
	if f.Task != "fill-mask" {
		t.Errorf("Task = %q", f.Task)
	}
	if f.Limit != 7 || f.Offset != 14 {
		t.Errorf("limit/offset = %d/%d, want 7/14", f.Limit, f.Offset)
	}
}

func TestHFListFilterSortMapping(t *testing.T) {
	tests := []struct {
		raw     string
		want    string
		refused string // substring of the refusal, "" when it must be accepted
	}{
		{"sort=downloads&direction=-1", "downloads", ""},
		{"sort=lastModified&direction=-1", "", ""},
		{"sort=last_modified&direction=-1", "", ""},
		{"sort=createdAt&direction=-1", "created", ""},
		{"sort=id", "name", ""},
		{"sort=author", "name", ""},
		// No sort at all keeps the default ordering, whatever direction says.
		{"direction=-1", "", ""},
		// An absent direction takes the ordering's natural one. `sort=downloads`
		// on its own is how essentially everybody asks for "most downloaded",
		// and refusing it would have been a compatibility regression dressed
		// up as strictness.
		{"sort=downloads", "downloads", ""},
		{"sort=createdAt", "created", ""},
		{"sort=id", "name", ""},
		// An *explicit* direction the store cannot produce is still refused,
		// which is the case worth refusing: the caller named an order, and
		// answering with its reverse would be a lie.
		{"sort=downloads&direction=1", "", "direction=-1"},
		{"sort=createdAt&direction=1", "", "direction=-1"},
		{"sort=id&direction=-1", "", "ascending"},
		// Metrics this server does not have must never come back as the
		// default order dressed up as a ranking.
		{"sort=likes&direction=-1", "", "does not track"},
		{"sort=trending_score&direction=-1", "", "does not track"},
		{"sort=nonsense&direction=-1", "", "not supported"},
	}
	for _, tt := range tests {
		f, msg := hfListFilter("model", mustQuery(t, tt.raw))
		switch {
		case tt.refused == "" && msg != "":
			t.Errorf("%q: refused with %q", tt.raw, msg)
		case tt.refused == "" && f.Sort != tt.want:
			t.Errorf("%q: Sort = %q, want %q", tt.raw, f.Sort, tt.want)
		case tt.refused != "" && msg == "":
			t.Errorf("%q: accepted, want a refusal mentioning %q", tt.raw, tt.refused)
		case tt.refused != "" && !strings.Contains(msg, tt.refused):
			t.Errorf("%q: refusal = %q, want it to mention %q", tt.raw, msg, tt.refused)
		}
	}
}

// Parameters that would narrow the result set are refused rather than ignored:
// answering with a superset of what was asked for is indistinguishable from
// data.
func TestHFListFilterRefusesUnsupportedNarrowingParameters(t *testing.T) {
	for _, raw := range []string{"expand=downloads", "gated=True", "inference=warm", "emissions_thresholds=10"} {
		if _, msg := hfListFilter("model", mustQuery(t, raw)); msg == "" {
			t.Errorf("%q was accepted and silently ignored", raw)
		}
	}
	for _, raw := range []string{"limit=0", "limit=-3", "limit=many", "offset=-1", "offset=x"} {
		if _, msg := hfListFilter("model", mustQuery(t, raw)); msg == "" {
			t.Errorf("%q was accepted", raw)
		}
	}
	// gated=False asks for exactly what this server returns anyway, so it is
	// honoured. Only the True side can be answered with a superset.
	for _, raw := range []string{"gated=False", "gated=false", "gated=0"} {
		if _, msg := hfListFilter("model", mustQuery(t, raw)); msg != "" {
			t.Errorf("%q was refused with %q, but it asks for the default result set", raw, msg)
		}
	}
}

func TestHFListWantsCard(t *testing.T) {
	// huggingface_hub spells its booleans Python-style.
	for _, raw := range []string{"full=True", "full=true", "full=1", "cardData=True", "cardData=1"} {
		if !hfListWantsCard(mustQuery(t, raw)) {
			t.Errorf("%q did not ask for cardData", raw)
		}
	}
	for _, raw := range []string{"", "full=False", "cardData=False", "full=0"} {
		if hfListWantsCard(mustQuery(t, raw)) {
			t.Errorf("%q asked for cardData", raw)
		}
	}
}

// Over real HTTP, because the two things below are the handler's and not the
// filter's: the `Link` header a paginating client follows, and whether the
// card is in the response.

// HfApi.list_models walks pages by following `Link: rel="next"`. Without one,
// the listing simply stopped at the store's page size and every repository
// past it looked as though it did not exist.
func TestHFList_LinkHeaderWalksPastTheFirstPage(t *testing.T) {
	f := newArchiveFixture(t)
	const total = 32
	for i := 0; i < total; i++ {
		f.repo("alice", fmt.Sprintf("m%02d", i), "model")
	}

	resp := f.do("GET", "/api/models", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var page []map[string]any
	resp.json(t, &page)
	if len(page) != 30 {
		t.Fatalf("first page = %d items, want the store's page size of 30", len(page))
	}
	link := resp.rec.Header().Get("Link")
	if want := `<http://test.local/api/models?offset=30>; rel="next"`; link != want {
		t.Fatalf("Link = %q, want %q", link, want)
	}

	// Follow it exactly as a client would.
	next, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(link, "<"), `>; rel="next"`))
	if err != nil {
		t.Fatalf("parse next page url: %v", err)
	}
	resp = f.do("GET", next.RequestURI(), "", nil)
	if resp.status() != 200 {
		t.Fatalf("next page status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var rest []map[string]any
	resp.json(t, &rest)
	if len(rest) != total-30 {
		t.Errorf("last page = %d items, want %d", len(rest), total-30)
	}
	// No link off the end: a client that kept following one would loop.
	if got := resp.rec.Header().Get("Link"); got != "" {
		t.Errorf("Link on the last page = %q, want none", got)
	}

	seen := map[string]bool{}
	for _, item := range append(page, rest...) {
		id, _ := item["id"].(string)
		if seen[id] {
			t.Fatalf("%s appeared on both pages", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Errorf("walked %d repositories, want %d", len(seen), total)
	}
}

// `full` / `cardData` are what huggingface_hub sends to get ModelInfo.card_data
// populated, and `filter` is its tag filter.
func TestHFList_CardDataAndTagFilter(t *testing.T) {
	f := newArchiveFixture(t)
	r := f.repo("alice", "bert", "model")
	f.repo("alice", "plain", "model")
	card := map[string]any{"tags": []any{"nlp", "pytorch"}, "license": "mit"}
	if err := f.st.UpdateRepoIndex(context.Background(), r.ID, "abc", 10, card, "a card", false); err != nil {
		t.Fatalf("index card: %v", err)
	}

	resp := f.do("GET", "/api/models?filter=nlp", "", nil)
	var page []map[string]any
	resp.json(t, &page)
	if len(page) != 1 || page[0]["id"] != "alice/bert" {
		t.Fatalf("filter=nlp returned %v, want just alice/bert", page)
	}
	if _, ok := page[0]["cardData"]; ok {
		t.Error("cardData came back without being asked for")
	}

	resp = f.do("GET", "/api/models?filter=nlp&filter=pytorch&full=True", "", nil)
	resp.json(t, &page)
	if len(page) != 1 {
		t.Fatalf("filter=nlp&filter=pytorch returned %d items, want 1 (tags are ANDed)", len(page))
	}
	got, ok := page[0]["cardData"].(map[string]any)
	if !ok {
		t.Fatalf("cardData = %v, want the repository card", page[0]["cardData"])
	}
	if got["license"] != "mit" {
		t.Errorf("cardData.license = %v, want mit", got["license"])
	}

	resp = f.do("GET", "/api/models?filter=nlp&filter=vision", "", nil)
	resp.json(t, &page)
	if len(page) != 0 {
		t.Errorf("a tag no repository carries returned %v, want nothing", page)
	}
}

// The refusals reach the wire as 400s rather than being swallowed by the
// handler.
func TestHFList_RefusesASortItCannotServe(t *testing.T) {
	f := newArchiveFixture(t)
	f.repo("alice", "bert", "model")

	resp := f.do("GET", "/api/models?sort=likes&direction=-1", "", nil)
	if resp.status() != 400 {
		t.Fatalf("status = %d, body = %s; want 400", resp.status(), resp.rec.Body.String())
	}
	if !strings.Contains(resp.rec.Body.String(), "does not track") {
		t.Errorf("body = %s, want it to say likes are not tracked", resp.rec.Body.String())
	}

	resp = f.do("GET", "/api/models?sort=downloads&direction=-1", "", nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s; want 200", resp.status(), resp.rec.Body.String())
	}
}
