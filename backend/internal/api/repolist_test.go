// The query-string surface of GET /api/v1/repos (docs/api-contract.md §2).
// repoFilterFromQuery is pure, so the mapping is checked here without a
// database; what the filters then mean is store's own integration suite.

package api

import (
	"net/url"
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
