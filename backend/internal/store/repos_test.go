package store

import (
	"reflect"
	"strings"
	"testing"
)

// ------------------------------------------------------------ buildRepoWhere

func TestBuildRepoWhereEmptyFilterIsUnrestricted(t *testing.T) {
	clause, args := buildRepoWhere(pgDialect{}, RepoFilter{}, repoFilterScopeAll)
	if clause != "" {
		t.Fatalf("clause = %q, want empty", clause)
	}
	if len(args) != 0 {
		t.Fatalf("args = %v, want none", args)
	}
}

func TestBuildRepoWhereKindAuthorAndLegacyQuery(t *testing.T) {
	f := RepoFilter{Kind: "dataset", Author: "alice", Query: "bert"}
	clause, args := buildRepoWhere(pgDialect{}, f, repoFilterScopeAll)

	for _, want := range []string{
		`r.kind = $1`,
		`LOWER(n.name) = LOWER($2)`,
		`(r.name ILIKE $3 ESCAPE '\' OR n.name ILIKE $3 ESCAPE '\' OR r.description ILIKE $3 ESCAPE '\')`,
	} {
		if !strings.Contains(clause, want) {
			t.Errorf("clause %q does not contain %q", clause, want)
		}
	}
	wantArgs := []any{"dataset", "alice", "%bert%"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v, want %#v", args, wantArgs)
	}
	// The legacy substring filter is a single ILIKE placeholder reused three
	// times (once per column) -- this is what keeps huggingface_hub's plain
	// "bert" matching "distilbert" behaviour untouched.
	if n := strings.Count(clause, "$3"); n != 3 {
		t.Errorf("ILIKE placeholder $3 appears %d times, want 3", n)
	}
}

func TestBuildRepoWhereSearchQueryUsesToTsquery(t *testing.T) {
	f := RepoFilter{Search: "bert base"}
	clause, args := buildRepoWhere(pgDialect{}, f, repoFilterScopeAll)
	if !strings.Contains(clause, `r.search_vector @@ to_tsquery('simple', $1)`) {
		t.Fatalf("clause = %q, missing tsquery match", clause)
	}
	if len(args) < 1 || args[0] != "bert:* & base:*" {
		t.Fatalf("args = %#v, want first arg to be the tsquery string", args)
	}
}

func TestBuildRepoWhereTagsIsSingleContainmentCheck(t *testing.T) {
	f := RepoFilter{Tags: []string{"nlp", "pytorch"}}
	clause, args := buildRepoWhere(pgDialect{}, f, repoFilterScopeAll)
	if !strings.Contains(clause, `r.card->'tags' @> $1::jsonb`) {
		t.Fatalf("clause = %q, want a single jsonb containment check", clause)
	}
	if len(args) != 1 || args[0] != `["nlp","pytorch"]` {
		t.Fatalf("args = %#v, want one JSON array argument", args)
	}
}

func TestBuildRepoWhereLicenseAndTask(t *testing.T) {
	f := RepoFilter{License: "mit", Task: "text-classification"}
	clause, args := buildRepoWhere(pgDialect{}, f, repoFilterScopeAll)
	if !strings.Contains(clause, `r.card->>'license' = $1`) {
		t.Errorf("clause = %q, missing license filter", clause)
	}
	if !strings.Contains(clause, `r.card->>'pipeline_tag' = $2`) ||
		!strings.Contains(clause, `r.card->'task_categories' @> to_jsonb($2::text)`) {
		t.Errorf("clause = %q, missing task filter across pipeline_tag/task_categories", clause)
	}
	wantArgs := []any{"mit", "text-classification"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v, want %#v", args, wantArgs)
	}
}

func TestBuildRepoWhereScopeExcludesOwnDimension(t *testing.T) {
	f := RepoFilter{Tags: []string{"nlp"}, License: "mit", Task: "summarization"}

	tagFacetClause, _ := buildRepoWhere(pgDialect{}, f, repoFilterScope{license: true, task: true})
	if strings.Contains(tagFacetClause, "card->'tags'") {
		t.Errorf("tag facet scope should drop the tags filter, got %q", tagFacetClause)
	}
	if !strings.Contains(tagFacetClause, "license") || !strings.Contains(tagFacetClause, "pipeline_tag") {
		t.Errorf("tag facet scope should keep license/task filters, got %q", tagFacetClause)
	}

	licenseFacetClause, _ := buildRepoWhere(pgDialect{}, f, repoFilterScope{tags: true, task: true})
	if strings.Contains(licenseFacetClause, "card->>'license'") {
		t.Errorf("license facet scope should drop the license filter, got %q", licenseFacetClause)
	}

	taskFacetClause, _ := buildRepoWhere(pgDialect{}, f, repoFilterScope{tags: true, license: true})
	if strings.Contains(taskFacetClause, "pipeline_tag") {
		t.Errorf("task facet scope should drop the task filter, got %q", taskFacetClause)
	}
}

// ------------------------------------------------------------ lineage filters

func TestSplitRepoRef(t *testing.T) {
	tests := []struct {
		in       string
		ns, name string
		ok       bool
	}{
		{"alice/bert", "alice", "bert", true},
		// A pinned revision names the same repository; lineage filters match
		// at repository granularity.
		{"alice/bert@v1", "alice", "bert", true},
		{"alice/bert@", "alice", "bert", true},
		{"bert", "", "", false},
		{"/bert", "", "", false},
		{"alice/", "", "", false},
		{"a/b/c", "", "", false},
		{"", "", "", false},
		{"@v1", "", "", false},
	}
	for _, tt := range tests {
		ns, name, ok := splitRepoRef(tt.in)
		if ns != tt.ns || name != tt.name || ok != tt.ok {
			t.Errorf("splitRepoRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, ns, name, ok, tt.ns, tt.name, tt.ok)
		}
	}
}

func TestBuildRepoWhereBaseModelAndRelationConstrainOneEdge(t *testing.T) {
	f := RepoFilter{BaseModel: "alice/bert-base@main", Relation: "quantized"}
	clause, args := buildRepoWhere(pgDialect{}, f, repoFilterScopeAll)

	// One EXISTS, not two: "the quantisations of alice/bert-base", never "a
	// quantisation of something that separately derives from alice/bert-base".
	if n := strings.Count(clause, "EXISTS (SELECT 1 FROM repo_lineage"); n != 1 {
		t.Fatalf("clause %q has %d lineage subqueries, want 1", clause, n)
	}
	for _, want := range []string{
		`l.edge_kind = 'base_model'`,
		`LOWER(l.target_namespace) = LOWER($1)`,
		`l.target_name = $2`,
		`COALESCE(NULLIF(l.relation, ''), 'finetune') = $3`,
	} {
		if !strings.Contains(clause, want) {
			t.Errorf("clause %q does not contain %q", clause, want)
		}
	}
	// The "@main" is dropped: the edge's own revision is irrelevant.
	wantArgs := []any{"alice", "bert-base", "quantized"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v, want %#v", args, wantArgs)
	}
}

// A base_model edge with no relation reads as a fine-tune, matching how the
// model tree groups one (docs/dev/api-contract.md §12) -- otherwise rows indexed
// before the relation column existed would be unreachable from the listing.
func TestBuildRepoWhereRelationFinetuneMatchesUnsetRelation(t *testing.T) {
	clause, args := buildRepoWhere(pgDialect{}, RepoFilter{Relation: "finetune"}, repoFilterScopeAll)
	if !strings.Contains(clause, `COALESCE(NULLIF(l.relation, ''), 'finetune') = $1`) {
		t.Fatalf("clause = %q, relation must be normalised before comparison", clause)
	}
	if len(args) != 1 || args[0] != "finetune" {
		t.Fatalf("args = %#v, want [finetune]", args)
	}
}

func TestBuildRepoWhereDatasetFilter(t *testing.T) {
	clause, args := buildRepoWhere(pgDialect{}, RepoFilter{Dataset: "bob/imdb"}, repoFilterScopeAll)
	if !strings.Contains(clause, `l.edge_kind = 'dataset'`) ||
		!strings.Contains(clause, `LOWER(l.target_namespace) = LOWER($1)`) ||
		!strings.Contains(clause, `l.target_name = $2`) {
		t.Fatalf("clause = %q, missing dataset edge filter", clause)
	}
	// Run edges are not included: logging a run into a dataset repository is
	// not the same as having been trained on it.
	if strings.Contains(clause, `'run'`) {
		t.Errorf("clause %q must not widen to run edges", clause)
	}
	if !reflect.DeepEqual(args, []any{"bob", "imdb"}) {
		t.Errorf("args = %#v", args)
	}
}

// A reference that does not parse must return nothing. Silently dropping the
// filter would answer `?base_model=garbage` with the whole hub.
func TestBuildRepoWhereMalformedRefMatchesNothing(t *testing.T) {
	for _, f := range []RepoFilter{{BaseModel: "garbage"}, {Dataset: "garbage"}} {
		clause, args := buildRepoWhere(pgDialect{}, f, repoFilterScopeAll)
		if !strings.Contains(clause, "1 = 0") {
			t.Errorf("filter %+v produced %q, want an unsatisfiable predicate", f, clause)
		}
		if len(args) != 0 {
			t.Errorf("filter %+v bound %#v, want nothing", f, args)
		}
	}
}

func TestBuildRepoWhereBaseOnly(t *testing.T) {
	clause, args := buildRepoWhere(pgDialect{}, RepoFilter{BaseOnly: true}, repoFilterScopeAll)
	if !strings.Contains(clause, `NOT EXISTS (SELECT 1 FROM repo_lineage l WHERE l.repo_id = r.id AND l.edge_kind = 'base_model')`) {
		t.Fatalf("clause = %q, missing the base-only exclusion", clause)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
}

// The relation facet drops the relation filter but keeps the base model one,
// so it can answer "of this base model, how many are quantisations?".
func TestBuildRepoWhereRelationScopeExcludesOnlyRelation(t *testing.T) {
	f := RepoFilter{BaseModel: "alice/bert-base", Relation: "quantized", Dataset: "bob/imdb"}
	clause, args := buildRepoWhere(pgDialect{}, f, repoFilterScope{tags: true, license: true, task: true})
	if strings.Contains(clause, "l.relation") {
		t.Errorf("relation facet scope should drop the relation filter, got %q", clause)
	}
	if !strings.Contains(clause, `l.edge_kind = 'base_model'`) || !strings.Contains(clause, `l.edge_kind = 'dataset'`) {
		t.Errorf("relation facet scope should keep base_model/dataset filters, got %q", clause)
	}
	if !reflect.DeepEqual(args, []any{"alice", "bert-base", "bob", "imdb"}) {
		t.Errorf("args = %#v", args)
	}
	// The other facets keep it, the same way they keep every dimension but
	// their own.
	tagFacetClause, _ := buildRepoWhere(pgDialect{}, f, repoFilterScope{license: true, task: true, relation: true})
	if !strings.Contains(tagFacetClause, "l.relation") {
		t.Errorf("tag facet scope should keep the relation filter, got %q", tagFacetClause)
	}
}

// Every lineage filter sits inside the same WHERE as the visibility predicate,
// so a private repository can never be surfaced by one.
func TestBuildRepoWhereIsExperiment(t *testing.T) {
	yes := true
	clause, args := buildRepoWhere(pgDialect{}, RepoFilter{IsExperiment: &yes}, repoFilterScopeAll)
	if !strings.Contains(clause, "r.is_experiment = $1") {
		t.Fatalf("clause = %q, missing is_experiment filter", clause)
	}
	if len(args) != 1 || args[0] != true {
		t.Fatalf("args = %#v, want [true]", args)
	}
}

// ------------------------------------------------------------ tagList / AND

func TestBuildRepoWhereEmptyTagsIsNoFilter(t *testing.T) {
	clause, _ := buildRepoWhere(pgDialect{}, RepoFilter{Tags: []string{}}, repoFilterScopeAll)
	if strings.Contains(clause, "tags") {
		t.Fatalf("clause = %q, an empty Tags slice should not add a filter", clause)
	}
}

// --------------------------------------------------------- BuildPrefixTSQuery

func TestBuildPrefixTSQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"single word", "bert", "bert:*"},
		{"two words AND together", "bert base", "bert:* & base:*"},
		{"hyphenated model name is kept", "gpt-2", "gpt-2:*"},
		{"trailing bangs are split off a hyphenated name", "gpt-2!!", "gpt-2:*"},
		{"license with hyphen and dot is kept", "apache-2.0", "apache-2.0:*"},
		{"hyphenated tag is kept", "text-classification", "text-classification:*"},
		{"tsquery syntax characters split tokens", "foo|bar&baz(qux)", "foo:* & bar:* & baz:* & qux:*"},
		{"leading/trailing whitespace ignored", "  hello world  ", "hello:* & world:*"},
		{"empty input yields empty query", "", ""},
		{"only punctuation yields empty query", "!!!---", ""},
		{"unicode letters pass through", "実験 モデル", "実験:* & モデル:*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildPrefixTSQuery(tt.input); got != tt.want {
				t.Errorf("BuildPrefixTSQuery(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// BuildPrefixTSQuery's output must never contain characters that would
// change to_tsquery's parse, since it lands straight into a query string
// passed as a bound parameter (safe from SQL injection) but is still parsed
// as tsquery syntax by Postgres itself.
func TestBuildPrefixTSQueryNeverEmitsControlCharacters(t *testing.T) {
	for _, r := range []rune{'&', '|', '!', '(', ')', ':', '\'', '<', '>', '*'} {
		q := BuildPrefixTSQuery("safe" + string(r) + "word")
		// Operators in input become token separators, so the query is
		// "safe:* & word:*". The join '&' is ours; individual tokens must
		// not contain operators that would change to_tsquery's parse.
		for _, tok := range strings.Split(q, " & ") {
			body := strings.TrimSuffix(tok, ":*")
			for _, bad := range []rune{'&', '|', '!', '(', ')', ':', '\'', '<', '>'} {
				if strings.ContainsRune(body, bad) {
					t.Errorf("BuildPrefixTSQuery(%q) = %q leaked control rune %q", string(r), q, bad)
				}
			}
		}
	}
}

// --------------------------------------------------- description ownership

// TestIntegrationRepoDescriptionSurvivesACardlessPush pins the split between
// the two writers of `description`: the settings form (SetRepoDescription)
// and the post-push indexer (UpdateRepoIndex). The indexer used to assign
// unconditionally, so a README without a `description:` key wiped a
// hand-written description on every push. An empty card description now means
// "the card said nothing" and leaves the column alone, while a card that does
// carry one still wins.
//
// Run against every available backend because the CASE expression reuses one
// placeholder, and pgx and SQLite bind that differently (pgx sends $5 once for
// both references; SQLite treats the repeated $5 as a single named parameter).
func TestIntegrationRepoDescriptionSurvivesACardlessPush(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "foo", "model", nil)

		updated, err := s.SetRepoDescription(ctx, r.ID, "hand written")
		if err != nil {
			t.Fatalf("SetRepoDescription: %v", err)
		}
		if updated.Description != "hand written" {
			t.Fatalf("description = %q, want %q", updated.Description, "hand written")
		}

		// A push whose README card has no description at all.
		if err := s.UpdateRepoIndex(ctx, r.ID, "abc", 10, map[string]any{"tags": []any{"nlp"}}, "", false); err != nil {
			t.Fatalf("UpdateRepoIndex: %v", err)
		}
		got, err := s.GetRepoByID(ctx, r.ID)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got.Description != "hand written" {
			t.Fatalf("description after a cardless push = %q, want it kept", got.Description)
		}
		// The rest of the index still landed.
		if got.HeadSHA != "abc" || got.TotalSize != 10 {
			t.Fatalf("index not applied: head_sha = %q, total_size = %d", got.HeadSHA, got.TotalSize)
		}

		// A card that does say something is still the source of truth.
		if err := s.UpdateRepoIndex(ctx, r.ID, "def", 20, map[string]any{}, "from the card", false); err != nil {
			t.Fatalf("UpdateRepoIndex: %v", err)
		}
		if got, err = s.GetRepoByID(ctx, r.ID); err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got.Description != "from the card" {
			t.Fatalf("description = %q, want the card's", got.Description)
		}

		// And the settings form can still clear it afterwards.
		if updated, err = s.SetRepoDescription(ctx, r.ID, ""); err != nil {
			t.Fatalf("SetRepoDescription(empty): %v", err)
		}
		if updated.Description != "" {
			t.Fatalf("description = %q, want it cleared", updated.Description)
		}
	})
}
