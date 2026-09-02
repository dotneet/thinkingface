package store

import (
	"context"
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
	if !strings.Contains(clause, `(r.card->>'license') = $1`) {
		t.Errorf("clause = %q, missing license filter", clause)
	}
	if !strings.Contains(clause, `(r.card->>'pipeline_tag') = $2`) ||
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
		// A leading "@" is not a separator: there is no namespace/name in
		// front of it, so this is not a reference at all.
		{"@v1", "", "", false},
		// The last "@" is the revision separator, so the name keeps the
		// first one. What matters is not which half wins but that the
		// syncer, which wrote the row, cut it in exactly the same place --
		// this used to split at the *first* "@" here and the *last* one
		// there, and the repository dropped out of its own lineage listing.
		{"a/b@x@y", "a", "b@x", true},
		// A hand-copied URL path: surrounding slashes and spaces are
		// tolerated, blank segments are not (splitSegments).
		{"/alice/bert/", "alice", "bert", true},
		{" alice / bert ", "alice", "bert", true},
		{"alice//bert", "", "", false},
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
//
// Every scope here is the variable the corresponding facet query passes, not
// a hand-built copy of it: the earlier version of this test asserted its own
// literal and so kept passing while tagFacet/licenseFacet/taskFacet were
// dropping Relation from the counts they showed beside the listing.
func TestBuildRepoWhereFacetScopesExcludeOnlyTheirOwnDimension(t *testing.T) {
	f := RepoFilter{BaseModel: "alice/bert-base", Relation: "quantized", Dataset: "bob/imdb"}
	clause, args := buildRepoWhere(pgDialect{}, f, relationFacetScope)
	if strings.Contains(clause, "l.relation") {
		t.Errorf("relation facet scope should drop the relation filter, got %q", clause)
	}
	if !strings.Contains(clause, `l.edge_kind = 'base_model'`) || !strings.Contains(clause, `l.edge_kind = 'dataset'`) {
		t.Errorf("relation facet scope should keep base_model/dataset filters, got %q", clause)
	}
	if !reflect.DeepEqual(args, []any{"alice", "bert-base", "bob", "imdb"}) {
		t.Errorf("args = %#v", args)
	}
	// The card facets keep it, the same way they keep every dimension but
	// their own. A facet that counted over the unfiltered relation set
	// offered a tag whose count came from a repository the listing beside it
	// does not contain, so clicking the tag returned nothing.
	for name, scope := range map[string]repoFilterScope{
		"tag":     tagFacetScope,
		"license": licenseFacetScope,
		"task":    taskFacetScope,
	} {
		got, _ := buildRepoWhere(pgDialect{}, f, scope)
		if !strings.Contains(got, "l.relation") {
			t.Errorf("%s facet scope should keep the relation filter, got %q", name, got)
		}
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

// ------------------------------------------------- search index maintenance

// clearSearchIndex blanks a repository's full-text index entry behind the
// trigger's back, so a later search tells us whether anything rebuilt it.
// Neither statement touches a column the search triggers watch, so neither
// re-fills what it just emptied.
func clearSearchIndex(t *testing.T, s *Store, repoID int64) {
	t.Helper()
	var q string
	switch s.d.name() {
	case "postgres":
		q = `UPDATE repositories SET search_vector = ''::tsvector WHERE id = $1`
	default:
		q = `DELETE FROM repositories_fts WHERE rowid = $1`
	}
	if _, err := s.db.Exec(context.Background(), q, repoID); err != nil {
		t.Fatalf("clear search index: %v", err)
	}
}

// searchFinds reports whether the free-text listing filter matches repoID.
func searchFinds(t *testing.T, s *Store, term string, repoID int64) bool {
	t.Helper()
	repos, _, _, err := s.ListRepos(context.Background(), RepoFilter{Search: term})
	if err != nil {
		t.Fatalf("ListRepos(search %q): %v", term, err)
	}
	for _, r := range repos {
		if r.ID == repoID {
			return true
		}
	}
	return false
}

// The search index is maintained by a database trigger, and 0001_init.sql
// attached it to every UPDATE of the repositories row. The row's hottest
// write by far is IncrementDownloads -- one `SET downloads = downloads + 1`
// per resolved file, from a detached goroutine -- which cannot change the
// index by construction: on Postgres it re-ran nine to_tsvector() calls over
// the card to store the tsvector already there, and on SQLite it ran a
// DELETE + INSERT into FTS5 on the single writer connection every other write
// in the process queues behind.
//
// 0004 narrowed both triggers to name/description/card. This test pins both
// halves of that: the download must leave the index alone, and every write
// that does feed it must still rebuild it. The index is deliberately emptied
// first, because "unchanged" is only observable against a value that would be
// wrong if anything had run.
func TestIntegrationSearchIndexOnlyFollowsTheColumnsItReads(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		r := f.repo(t, "alice", "bert", "model", map[string]any{"tags": []any{"alphatoken"}})
		if !searchFinds(t, s, "alphatoken", r.ID) {
			t.Fatalf("the card's tag is not searchable to begin with")
		}

		clearSearchIndex(t, s, r.ID)
		if searchFinds(t, s, "alphatoken", r.ID) {
			t.Fatalf("clearing the index did not make the repository unsearchable")
		}

		// The download-only write must not rebuild it.
		s.IncrementDownloads(ctx, r.ID)
		got, err := s.GetRepoByID(ctx, r.ID)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got.Downloads != 1 {
			t.Fatalf("downloads = %d, want 1 -- the counter itself must still move", got.Downloads)
		}
		if searchFinds(t, s, "alphatoken", r.ID) {
			t.Errorf("a download rebuilt the search index")
		}

		// ... and neither must the other writes that leave the indexed
		// columns alone: a push whose README could not be read, and the
		// archive flag.
		if err := s.UpdateRepoIndexKeepingCard(ctx, r.ID, "deadbeef", 99); err != nil {
			t.Fatalf("UpdateRepoIndexKeepingCard: %v", err)
		}
		if searchFinds(t, s, "alphatoken", r.ID) {
			t.Errorf("a head_sha/total_size update rebuilt the search index")
		}

		// A card change does rebuild it.
		if err := s.UpdateRepoIndex(ctx, r.ID, "abc", 10,
			map[string]any{"tags": []any{"betatoken"}, "license": "gammatoken"}, "", false); err != nil {
			t.Fatalf("UpdateRepoIndex: %v", err)
		}
		if !searchFinds(t, s, "betatoken", r.ID) {
			t.Errorf("a card update did not rebuild the search index (tags)")
		}
		if !searchFinds(t, s, "gammatoken", r.ID) {
			t.Errorf("a card update did not rebuild the search index (license)")
		}

		// So does a description change...
		clearSearchIndex(t, s, r.ID)
		if _, err := s.SetRepoDescription(ctx, r.ID, "deltatoken"); err != nil {
			t.Fatalf("SetRepoDescription: %v", err)
		}
		if !searchFinds(t, s, "deltatoken", r.ID) {
			t.Errorf("a description update did not rebuild the search index")
		}

		// ... and a rename. The statement is the one transferMove issues;
		// calling it directly keeps this test on the trigger rather than on
		// the transfer workflow around it.
		clearSearchIndex(t, s, r.ID)
		if _, err := s.db.Exec(ctx,
			`UPDATE repositories SET name = $2 WHERE id = $1`, r.ID, "epsilontoken"); err != nil {
			t.Fatalf("rename: %v", err)
		}
		if !searchFinds(t, s, "epsilontoken", r.ID) {
			t.Errorf("a rename did not rebuild the search index")
		}
	})
}

// ------------------------------------------------------------- card facets

// A facet's count and the listing behind it have to describe the same set.
// The three card facets dropped Relation from their WHERE -- not just from
// their own dimension, the way the design intends, but entirely -- so with
// ?base_model=...&relation=... selected the sidebar counted over every
// relation and offered tags, licenses and tasks that only exist on
// repositories the listing does not contain. Clicking one returned nothing.
func TestIntegrationCardFacetsRespectTheRelationFilter(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newLineageFixture(t, s)
		ctx := f.ctx

		// Two children of the same base model, with disjoint card metadata,
		// distinguished only by their relation to it.
		if err := s.UpdateRepoIndex(ctx, f.ft.ID, "abc", 10, map[string]any{
			"tags": []any{"ft-only"}, "license": "ft-license", "pipeline_tag": "ft-task",
		}, "", false); err != nil {
			t.Fatalf("UpdateRepoIndex(ft): %v", err)
		}
		if err := s.UpdateRepoIndex(ctx, f.gguf.ID, "abc", 10, map[string]any{
			"tags": []any{"gguf-only"}, "license": "gguf-license", "pipeline_tag": "gguf-task",
		}, "", false); err != nil {
			t.Fatalf("UpdateRepoIndex(gguf): %v", err)
		}

		base := RepoFilter{BaseModel: "alice/base-model", Relation: "quantized"}
		withFacets := base
		withFacets.WithFacets = true
		_, _, facets, err := s.ListRepos(ctx, withFacets)
		if err != nil {
			t.Fatalf("ListRepos: %v", err)
		}

		// Each facet offers exactly what the quantisation carries, and
		// nothing the fine-tune does.
		for _, c := range []struct {
			name    string
			items   []RepoFacetItem
			want    string
			refuted string
			apply   func(*RepoFilter, string)
		}{
			{"tag", facets.Tags, "gguf-only", "ft-only",
				func(f *RepoFilter, v string) { f.Tags = []string{v} }},
			{"license", facets.Licenses, "gguf-license", "ft-license",
				func(f *RepoFilter, v string) { f.License = v }},
			{"task", facets.Tasks, "gguf-task", "ft-task",
				func(f *RepoFilter, v string) { f.Task = v }},
		} {
			got := map[string]int64{}
			for _, it := range c.items {
				got[it.Value] = it.Count
			}
			if got[c.want] != 1 {
				t.Errorf("%s facet[%q] = %d, want 1 (got %v)", c.name, c.want, got[c.want], got)
			}
			if _, ok := got[c.refuted]; ok {
				t.Errorf("%s facet offers %q, which only exists on a repository the listing excludes (got %v)",
					c.name, c.refuted, got)
			}
			// Whatever it offers, the number must be the number clicking it
			// returns.
			for _, it := range c.items {
				sub := base
				c.apply(&sub, it.Value)
				_, total, _, err := s.ListRepos(ctx, sub)
				if err != nil {
					t.Fatalf("ListRepos(%s %q): %v", c.name, it.Value, err)
				}
				if total != it.Count {
					t.Errorf("%s facet says %s (%d) but the filter returns %d",
						c.name, it.Value, it.Count, total)
				}
			}
		}
	})
}
