package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// The integration suite runs every Store method against a real database and
// is what proves the Postgres and SQLite paths behave the same. SQLite runs
// unconditionally on a temp file; Postgres runs when TF_TEST_DATABASE_URL
// points at a database this test may TRUNCATE (for example the compose one:
// postgres://tf:tf@localhost:5432/thinkingface?sslmode=disable).

// pgTables lists every table the Postgres tests reset between cases, in
// any order (TRUNCATE ... CASCADE takes care of references).
var pgTables = []string{
	"users", "namespaces", "org_members", "access_tokens", "repositories", "repo_files",
	"lfs_objects", "repo_lfs_objects", "parquet_files", "exp_projects", "exp_runs", "exp_points",
	"sync_jobs", "repo_lineage", "webhooks", "webhook_deliveries", "repo_download_stats",
	"repo_redirects", "repo_transfers", "user_ssh_keys", "org_audit_log",
}

type backend struct {
	name string
	open func(t *testing.T) *Store
}

func backends(t *testing.T) []backend {
	t.Helper()
	out := []backend{{
		name: "sqlite",
		open: func(t *testing.T) *Store {
			ctx := context.Background()
			s, err := Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			t.Cleanup(s.Close)
			if err := s.Migrate(ctx); err != nil {
				t.Fatalf("migrate sqlite: %v", err)
			}
			return s
		},
	}}
	if url := os.Getenv("TF_TEST_DATABASE_URL"); url != "" {
		out = append(out, backend{
			name: "postgres",
			open: func(t *testing.T) *Store {
				ctx := context.Background()
				s, err := Open(ctx, url)
				if err != nil {
					t.Fatalf("open postgres: %v", err)
				}
				t.Cleanup(s.Close)
				if err := s.WaitReady(ctx, 30*time.Second); err != nil {
					t.Fatalf("postgres not ready: %v", err)
				}
				if err := s.Migrate(ctx); err != nil {
					t.Fatalf("migrate postgres: %v", err)
				}
				var tables string
				for i, tbl := range pgTables {
					if i > 0 {
						tables += ", "
					}
					tables += tbl
				}
				if _, err := s.db.Exec(ctx, "TRUNCATE "+tables+" RESTART IDENTITY CASCADE"); err != nil {
					t.Fatalf("truncate: %v", err)
				}
				return s
			},
		})
	}
	return out
}

// forEachBackend runs fn once per available backend as a subtest.
func forEachBackend(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Helper()
	for _, b := range backends(t) {
		b := b
		t.Run(b.name, func(t *testing.T) {
			fn(t, b.open(t))
		})
	}
}

// ---------------------------------------------------------------- fixtures

type fixture struct {
	s     *Store
	ctx   context.Context
	admin *User
	alice *User
	bob   *User
}

func newFixture(t *testing.T, s *Store) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{s: s, ctx: ctx}
	var err error
	if f.admin, err = s.CreateUser(ctx, "admin", "admin@example.com", "hash", true); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if f.alice, err = s.CreateUser(ctx, "alice", "alice@example.com", "hash", false); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if f.bob, err = s.CreateUser(ctx, "bob", "bob@example.com", "hash", false); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	return f
}

func (f *fixture) ns(t *testing.T, name string) *Namespace {
	t.Helper()
	n, err := f.s.GetNamespace(f.ctx, name)
	if err != nil {
		t.Fatalf("namespace %s: %v", name, err)
	}
	return n
}

func (f *fixture) repo(t *testing.T, ns, name, kind string, card map[string]any) *Repo {
	t.Helper()
	n := f.ns(t, ns)
	r, err := f.s.CreateRepo(f.ctx, n.ID, name, kind, "desc of "+name, "main", "")
	if err != nil {
		t.Fatalf("create repo %s/%s: %v", ns, name, err)
	}
	if card != nil {
		if err := f.s.UpdateRepoIndex(f.ctx, r.ID, "abc", 10, card, "desc of "+name, false); err != nil {
			t.Fatalf("update repo index: %v", err)
		}
		if r, err = f.s.GetRepoByID(f.ctx, r.ID); err != nil {
			t.Fatalf("reload repo: %v", err)
		}
	}
	return r
}

func ptr[T any](v T) *T { return &v }

func names(repos []Repo) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.FullName())
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------ tests

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("second migrate: %v", err)
		}
		if s.Dialect() != "sqlite" && s.Dialect() != "postgres" {
			t.Fatalf("dialect = %q", s.Dialect())
		}
	})
}

func TestIntegrationUsersNamespacesTokens(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		if _, err := s.CreateUser(ctx, "alice", "x", "hash", false); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate user err = %v, want ErrConflict", err)
		}
		n, err := s.CountUsers(ctx)
		if err != nil || n != 3 {
			t.Fatalf("CountUsers = %d, %v", n, err)
		}
		u, err := s.GetUserByUsername(ctx, "alice")
		if err != nil || u.ID != f.alice.ID || u.Email != "alice@example.com" || u.IsAdmin {
			t.Fatalf("GetUserByUsername = %+v, %v", u, err)
		}
		if u.CreatedAt.IsZero() || time.Since(u.CreatedAt) > time.Minute {
			t.Fatalf("created_at = %v, want recent", u.CreatedAt)
		}
		// Logging in must not depend on how the name was typed: the namespace
		// it owns is unique case-insensitively, so the account is too.
		if u, err := s.GetUserByUsername(ctx, "ALICE"); err != nil || u.ID != f.alice.ID {
			t.Fatalf("GetUserByUsername(\"ALICE\") = %+v, %v; want alice", u, err)
		}
		if _, err := s.GetUserByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing user err = %v", err)
		}
		if u, err := s.GetUserByID(ctx, f.admin.ID); err != nil || !u.IsAdmin {
			t.Fatalf("GetUserByID = %+v, %v", u, err)
		}

		// Personal namespace + org.
		ns := f.ns(t, "alice")
		if ns.Kind != "user" || ns.OwnerUserID == nil || *ns.OwnerUserID != f.alice.ID {
			t.Fatalf("namespace = %+v", ns)
		}
		org, err := s.CreateOrg(ctx, "acme", f.alice.ID, OrgUpdate{})
		if err != nil || org.Name != "acme" {
			t.Fatalf("CreateOrg = %+v, %v", org, err)
		}
		// The organisation belongs to nobody: authority is org_members only.
		if orgNS := f.ns(t, "acme"); orgNS.Kind != "org" || orgNS.OwnerUserID != nil {
			t.Fatalf("org namespace = %+v, want kind=org and no owner", orgNS)
		}
		if _, err := s.CreateOrg(ctx, "acme", f.bob.ID, OrgUpdate{}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate org err = %v", err)
		}
		if _, err := s.CreateOrg(ctx, "bob", f.alice.ID, OrgUpdate{}); !errors.Is(err, ErrConflict) {
			t.Fatalf("org over user namespace err = %v", err)
		}

		// Namespace names collide case-insensitively (idx_namespaces_name_lower):
		// "Alice"/"alice" and "ACME"/"acme" must not both be creatable, whether
		// the clash is user-vs-user, org-vs-org, or org-vs-user.
		if _, err := s.CreateUser(ctx, "Alice", "alice2@example.com", "hash", false); !errors.Is(err, ErrConflict) {
			t.Fatalf("case-variant duplicate user err = %v, want ErrConflict", err)
		}
		if _, err := s.CreateOrg(ctx, "ACME", f.bob.ID, OrgUpdate{}); !errors.Is(err, ErrConflict) {
			t.Fatalf("case-variant duplicate org err = %v, want ErrConflict", err)
		}
		if _, err := s.CreateOrg(ctx, "BOB", f.alice.ID, OrgUpdate{}); !errors.Is(err, ErrConflict) {
			t.Fatalf("case-variant org over user namespace err = %v, want ErrConflict", err)
		}
		// A namespace lookup by any case resolves to the one row and reports
		// back the spelling it was created with, never the caller's spelling
		// (docs/thinkingface-design.md §10 -- same behaviour as GitHub).
		if got, err := s.GetNamespace(ctx, "ALICE"); err != nil || got.Name != "alice" {
			t.Fatalf("GetNamespace(ALICE) = %+v, %v, want Name=alice", got, err)
		}
		if got, err := s.GetOrg(ctx, "Acme"); err != nil || got.Name != "acme" {
			t.Fatalf("GetOrg(Acme) = %+v, %v, want Name=acme", got, err)
		}
		if role, err := s.RoleInNamespace(ctx, f.alice.ID, "ACME"); err != nil || role != "admin" {
			t.Fatalf("RoleInNamespace alice/ACME = %q, %v, want admin", role, err)
		}
		if ok, err := s.CanWriteNamespace(ctx, f.alice.ID, "AcMe"); err != nil || !ok {
			t.Fatalf("alice can write AcMe = %v, %v, want true", ok, err)
		}

		nss, err := s.NamespacesForUser(ctx, f.alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range nss {
			got = append(got, n.Name+":"+n.Role)
		}
		if !equalStrings(got, []string{"acme:admin", "alice:admin"}) {
			t.Fatalf("NamespacesForUser = %v", got)
		}
		if ok, err := s.CanWriteNamespace(ctx, f.alice.ID, "acme"); err != nil || !ok {
			t.Fatalf("alice can write acme = %v, %v", ok, err)
		}
		if ok, err := s.CanWriteNamespace(ctx, f.bob.ID, "acme"); err != nil || ok {
			t.Fatalf("bob can write acme = %v, %v", ok, err)
		}
		if ok, err := s.CanWriteNamespace(ctx, f.bob.ID, "bob"); err != nil || !ok {
			t.Fatalf("bob can write bob = %v, %v", ok, err)
		}

		// RoleInNamespace: owner (personal namespace or org admin), an
		// explicit org role, no relationship, and a missing namespace.
		if role, err := s.RoleInNamespace(ctx, f.alice.ID, "alice"); err != nil || role != "admin" {
			t.Fatalf("RoleInNamespace alice/alice = %q, %v", role, err)
		}
		if role, err := s.RoleInNamespace(ctx, f.alice.ID, "acme"); err != nil || role != "admin" {
			t.Fatalf("RoleInNamespace alice/acme = %q, %v", role, err)
		}
		if role, err := s.RoleInNamespace(ctx, f.bob.ID, "acme"); err != nil || role != "" {
			t.Fatalf("RoleInNamespace bob/acme (no relationship) = %q, %v", role, err)
		}
		if _, err := s.db.Exec(ctx,
			`INSERT INTO org_members (namespace_id, user_id, role) VALUES ($1, $2, 'write')`, org.ID, f.bob.ID); err != nil {
			t.Fatal(err)
		}
		if role, err := s.RoleInNamespace(ctx, f.bob.ID, "acme"); err != nil || role != "write" {
			t.Fatalf("RoleInNamespace bob/acme (write member) = %q, %v", role, err)
		}
		if _, err := s.RoleInNamespace(ctx, f.alice.ID, "no-such-namespace"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RoleInNamespace missing namespace err = %v", err)
		}

		// Tokens.
		tok, err := s.CreateToken(ctx, f.alice.ID, "laptop", "write", "hash-1")
		if err != nil || tok.LastUsedAt != nil || tok.Scope != "write" {
			t.Fatalf("CreateToken = %+v, %v", tok, err)
		}
		if _, err := s.CreateToken(ctx, f.bob.ID, "other", "read", "hash-2"); err != nil {
			t.Fatal(err)
		}
		u, at, err := s.LookupToken(ctx, "hash-1")
		if err != nil || u.ID != f.alice.ID || at.ID != tok.ID {
			t.Fatalf("LookupToken = %+v %+v %v", u, at, err)
		}
		if _, _, err := s.LookupToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("LookupToken missing err = %v", err)
		}
		if err := s.TouchToken(ctx, tok.ID); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListTokens(ctx, f.alice.ID)
		if err != nil || len(list) != 1 || list[0].LastUsedAt == nil {
			t.Fatalf("ListTokens = %+v, %v", list, err)
		}
		// Expired tokens are rejected by LookupToken.
		if _, err := s.db.Exec(ctx, `UPDATE access_tokens SET expires_at = $1 WHERE id = $2`,
			time.Now().Add(-time.Hour), tok.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.LookupToken(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired LookupToken err = %v", err)
		}
		if err := s.DeleteToken(ctx, f.bob.ID, tok.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteToken by other user err = %v", err)
		}
		if err := s.DeleteToken(ctx, f.alice.ID, tok.ID); err != nil {
			t.Fatalf("DeleteToken: %v", err)
		}
		if list, _ := s.ListTokens(ctx, f.alice.ID); len(list) != 0 {
			t.Fatalf("tokens after delete = %+v", list)
		}
	})
}

func TestIntegrationRepos(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx

		bert := f.repo(t, "alice", "bert-base", "model", map[string]any{
			"tags": []any{"nlp", "pytorch"}, "license": "apache-2.0", "pipeline_tag": "text-classification",
		})
		gpt := f.repo(t, "alice", "gpt-2", "model", map[string]any{
			"tags": []any{"nlp"}, "license": "mit", "pipeline_tag": "text-generation", "summary": "a tiny gpt",
		})
		imdb := f.repo(t, "bob", "imdb", "dataset", map[string]any{
			"tags": []any{"nlp", "sentiment"}, "license": "mit", "task_categories": []any{"text-classification", "summarization"},
		})
		secret := f.repo(t, "bob", "secret-set", "dataset", map[string]any{
			"tags": []any{"internal"}, "license": "mit", "task_categories": "summarization",
		})
		// Experiment repos are datasets flagged by the indexer.
		if err := s.UpdateRepoIndex(ctx, imdb.ID, "def", 20, imdb.Card, "imdb reviews", true); err != nil {
			t.Fatal(err)
		}

		if _, err := s.CreateRepo(ctx, bert.NamespaceID, "bert-base", "model", "", "main", ""); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate repo err = %v", err)
		}
		if r, err := s.GetRepo(ctx, "model", "alice", "bert-base"); err != nil || r.ID != bert.ID ||
			r.License() != "apache-2.0" || !equalStrings(r.Tags(), []string{"nlp", "pytorch"}) || r.HeadSHA != "abc" {
			t.Fatalf("GetRepo = %+v, %v", r, err)
		}
		if _, err := s.GetRepo(ctx, "dataset", "alice", "bert-base"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetRepo wrong kind err = %v", err)
		}
		if r, err := s.GetRepoAnyKind(ctx, "bob", "imdb"); err != nil || r.Kind != "dataset" || !r.IsExperiment || r.Description != "imdb reviews" {
			t.Fatalf("GetRepoAnyKind = %+v, %v", r, err)
		}

		type listCase struct {
			name   string
			filter RepoFilter
			want   []string
			total  int64
		}
		cases := []listCase{
			{"no filter lists everything", RepoFilter{Sort: "name"}, []string{"alice/bert-base", "alice/gpt-2", "bob/imdb", "bob/secret-set"}, 4},
			{"kind", RepoFilter{Kind: "model", Sort: "name"}, []string{"alice/bert-base", "alice/gpt-2"}, 2},
			{"author", RepoFilter{Author: "bob", Sort: "name"}, []string{"bob/imdb", "bob/secret-set"}, 2},
			// /Bob and /bob are one profile, so the facet behind them agrees.
			{"author folds case", RepoFilter{Author: "BOB", Sort: "name"}, []string{"bob/imdb", "bob/secret-set"}, 2},
			{"legacy substring query is case-insensitive", RepoFilter{Query: "BERT"}, []string{"alice/bert-base"}, 1},
			{"legacy query matches description", RepoFilter{Query: "reviews"}, []string{"bob/imdb"}, 1},
			{"tags AND", RepoFilter{Tags: []string{"nlp", "pytorch"}}, []string{"alice/bert-base"}, 1},
			{"tags single", RepoFilter{Tags: []string{"nlp"}, Sort: "name"}, []string{"alice/bert-base", "alice/gpt-2", "bob/imdb"}, 3},
			{"license", RepoFilter{License: "mit", Sort: "name"}, []string{"alice/gpt-2", "bob/imdb", "bob/secret-set"}, 3},
			{"task via pipeline_tag or task_categories", RepoFilter{Task: "text-classification", Sort: "name"}, []string{"alice/bert-base", "bob/imdb"}, 2},
			{"is_experiment", RepoFilter{IsExperiment: ptr(true)}, []string{"bob/imdb"}, 1},
			{"kind dataset", RepoFilter{Kind: "dataset", Sort: "name"}, []string{"bob/imdb", "bob/secret-set"}, 2},
			{"search prefix", RepoFilter{Search: "ber"}, []string{"alice/bert-base"}, 1},
			{"search hyphenated name", RepoFilter{Search: "gpt-2"}, []string{"alice/gpt-2"}, 1},
			{"search card summary", RepoFilter{Search: "tiny gp"}, []string{"alice/gpt-2"}, 1},
			{"search tag", RepoFilter{Search: "sentim"}, []string{"bob/imdb"}, 1},
			{"search license", RepoFilter{Search: "apache"}, []string{"alice/bert-base"}, 1},
			{"search no tokens means no filter", RepoFilter{Search: "!!!", Sort: "name"}, []string{"alice/bert-base", "alice/gpt-2", "bob/imdb", "bob/secret-set"}, 4},
			{"search no match", RepoFilter{Search: "zzzz"}, []string{}, 0},
			{"limit and offset", RepoFilter{Sort: "name", Limit: 2, Offset: 1}, []string{"alice/gpt-2", "bob/imdb"}, 4},
			{"negative offset is first page", RepoFilter{Sort: "name", Limit: 1, Offset: -5}, []string{"alice/bert-base"}, 4},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				repos, total, _, err := s.ListRepos(ctx, c.filter)
				if err != nil {
					t.Fatalf("ListRepos: %v", err)
				}
				if !equalStrings(names(repos), c.want) || total != c.total {
					t.Fatalf("ListRepos = %v (total %d), want %v (total %d)", names(repos), total, c.want, c.total)
				}
			})
		}

		// Search term sharing a prefix with a description on another repo,
		// after re-indexing the card: the index follows the card.
		if err := s.UpdateRepoIndex(ctx, gpt.ID, "abc", 10, map[string]any{"tags": []any{"vision"}}, "renamed", false); err != nil {
			t.Fatal(err)
		}
		if repos, _, _, err := s.ListRepos(ctx, RepoFilter{Search: "tiny"}); err != nil || len(repos) != 0 {
			t.Fatalf("stale search hit after reindex: %v %v", names(repos), err)
		}
		if repos, _, _, err := s.ListRepos(ctx, RepoFilter{Search: "vis"}); err != nil || !equalStrings(names(repos), []string{"alice/gpt-2"}) {
			t.Fatalf("search after reindex = %v, %v", names(repos), err)
		}

		// Each facet dimension excludes its own filter.
		_, _, facets, err := s.ListRepos(ctx, RepoFilter{Tags: []string{"nlp"}, WithFacets: true})
		if err != nil {
			t.Fatal(err)
		}
		facet := func(items []RepoFacetItem) map[string]int64 {
			m := map[string]int64{}
			for _, it := range items {
				m[it.Value] = it.Count
			}
			return m
		}
		if got := facet(facets.Tags); got["nlp"] != 2 || got["internal"] != 1 || got["vision"] != 1 || got["sentiment"] != 1 {
			t.Fatalf("tag facet = %v", got)
		}
		if got := facet(facets.Licenses); got["apache-2.0"] != 1 || got["mit"] != 1 {
			t.Fatalf("license facet = %v", got)
		}
		if got := facet(facets.Tasks); got["text-classification"] != 2 || got["summarization"] != 1 {
			t.Fatalf("task facet = %v", got)
		}
		// A string-typed task_categories is not an array, so the facet skips
		// it (as jsonb_typeof / json_type = 'array' dictates on both engines),
		// while the task filter still matches it as a scalar.
		_, _, facets, err = s.ListRepos(ctx, RepoFilter{Kind: "dataset", WithFacets: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := facet(facets.Tasks); got["summarization"] != 1 || got["text-classification"] != 1 {
			t.Fatalf("task facet with scalar task_categories = %v", got)
		}
		if repos, _, _, err := s.ListRepos(ctx, RepoFilter{Task: "summarization", Sort: "name"}); err != nil ||
			!equalStrings(names(repos), []string{"bob/imdb", "bob/secret-set"}) {
			t.Fatalf("task filter over scalar = %v, %v", names(repos), err)
		}
		// A string-typed tags never satisfies tag containment.
		if err := s.UpdateRepoIndex(ctx, secret.ID, "abc", 1, map[string]any{"tags": "internal"}, "", false); err != nil {
			t.Fatal(err)
		}
		if repos, _, _, err := s.ListRepos(ctx, RepoFilter{Tags: []string{"internal"}}); err != nil || len(repos) != 0 {
			t.Fatalf("scalar tags matched containment: %v %v", names(repos), err)
		}

		// Sorting by downloads and stats.
		s.IncrementDownloads(ctx, imdb.ID)
		s.IncrementDownloads(ctx, imdb.ID)
		if repos, _, _, err := s.ListRepos(ctx, RepoFilter{Sort: "downloads", Limit: 1}); err != nil || names(repos)[0] != "bob/imdb" || repos[0].Downloads != 2 {
			t.Fatalf("sort by downloads = %v, %v", names(repos), err)
		}
		st, err := s.Stats(ctx)
		if err != nil || st.Datasets != 2 || st.Models != 2 || st.Experiments != 1 || st.TotalSize != 41 {
			t.Fatalf("Stats = %+v, %v", st, err)
		}
		if err := s.SetRepoHead(ctx, bert.ID, "fff"); err != nil {
			t.Fatal(err)
		}
		if r, _ := s.GetRepoByID(ctx, bert.ID); r.HeadSHA != "fff" || !r.UpdatedAt.After(bert.UpdatedAt) {
			t.Fatalf("SetRepoHead: %+v (was %v)", r, bert.UpdatedAt)
		}
		refs, err := s.AllRepoRefs(ctx)
		if err != nil || len(refs) != 4 || refs[0].Namespace != "alice" || refs[3].Name != "secret-set" {
			t.Fatalf("AllRepoRefs = %+v, %v", refs, err)
		}
		// Delete cascades to everything hanging off the repository.
		if err := s.ReplaceRepoFiles(ctx, bert.ID, "main", []RepoFile{{Path: "a", Size: 1, BlobSHA: "x"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteRepo(ctx, bert.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteRepo(ctx, bert.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second DeleteRepo err = %v", err)
		}
		if files, _ := s.ListRepoFiles(ctx, bert.ID, "main"); len(files) != 0 {
			t.Fatalf("repo_files survived delete: %+v", files)
		}
		if repos, _, _, _ := s.ListRepos(ctx, RepoFilter{Search: "bert"}); len(repos) != 0 {
			t.Fatalf("search index survived delete: %v", names(repos))
		}
	})
}

func TestIntegrationFilesLFSParquet(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "data", "dataset", nil)
		other := f.repo(t, "alice", "data2", "dataset", nil)

		files := []RepoFile{
			{Path: "README.md", Size: 10, BlobSHA: "b1"},
			{Path: "data/train.parquet", Size: 100, BlobSHA: "b2", LFSOID: ptr("oid-train")},
			{Path: "data/test.parquet", Size: 50, BlobSHA: "b3", LFSOID: ptr("oid-test")},
		}
		if err := s.ReplaceRepoFiles(ctx, r.ID, "main", files); err != nil {
			t.Fatalf("ReplaceRepoFiles: %v", err)
		}
		got, err := s.ListRepoFiles(ctx, r.ID, "main")
		if err != nil || len(got) != 3 || got[0].Path != "README.md" || got[1].LFSOID == nil || *got[1].LFSOID != "oid-test" || got[0].LFSOID != nil {
			t.Fatalf("ListRepoFiles = %+v, %v", got, err)
		}
		if repo, _ := s.GetRepoByID(ctx, r.ID); repo.NumFiles != 3 {
			t.Fatalf("NumFiles = %d", repo.NumFiles)
		}
		if err := s.ReplaceRepoFiles(ctx, r.ID, "main", files[:1]); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.ListRepoFiles(ctx, r.ID, "main"); len(got) != 1 {
			t.Fatalf("replace did not clear: %+v", got)
		}
		if err := s.ReplaceRepoFiles(ctx, r.ID, "main", nil); err != nil {
			t.Fatal(err)
		}

		// LFS bookkeeping.
		if _, ok, err := s.HasLFSObject(ctx, "oid-1"); err != nil || ok {
			t.Fatalf("HasLFSObject before = %v, %v", ok, err)
		}
		if err := s.RecordLFSObject(ctx, r.ID, "oid-1", 123, nil); err == nil {
			t.Fatal("RecordLFSObject without confirmPresent must fail")
		}
		if err := s.RecordLFSObject(ctx, r.ID, "oid-1", 123, func(string) (bool, error) { return false, nil }); !errors.Is(err, ErrLFSObjectGone) {
			t.Fatalf("RecordLFSObject gone err = %v", err)
		}
		if _, ok, _ := s.HasLFSObject(ctx, "oid-1"); ok {
			t.Fatal("rolled back RecordLFSObject left a row")
		}
		if err := s.RecordLFSObject(ctx, r.ID, "oid-1", 123, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatalf("RecordLFSObject: %v", err)
		}
		if err := s.RecordLFSObject(ctx, r.ID, "oid-1", 123, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatalf("RecordLFSObject again: %v", err)
		}
		if size, ok, err := s.HasLFSObject(ctx, "oid-1"); err != nil || !ok || size != 123 {
			t.Fatalf("HasLFSObject = %d %v %v", size, ok, err)
		}
		if err := s.RecordLFSObject(ctx, r.ID, "oid-2", 5, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatal(err)
		}
		// LinkLFSObjects links only oids that exist and ignores duplicates.
		if err := s.LinkLFSObjects(ctx, other.ID, []string{"oid-1", "oid-2", "oid-missing", "oid-1"}); err != nil {
			t.Fatalf("LinkLFSObjects: %v", err)
		}
		if err := s.LinkLFSObjects(ctx, other.ID, nil); err != nil {
			t.Fatal(err)
		}
		usage, err := s.UsageByRepo(ctx, []string{"alice"})
		if err != nil || len(usage) != 2 || usage[0].LFSSize != 128 || usage[1].LFSSize != 128 {
			t.Fatalf("UsageByRepo = %+v, %v", usage, err)
		}
		if usage, err := s.UsageByRepo(ctx, []string{"bob"}); err != nil || len(usage) != 0 {
			t.Fatalf("UsageByRepo bob = %+v, %v", usage, err)
		}
		if usage, err := s.UsageByRepo(ctx, nil); err != nil || len(usage) != 0 {
			t.Fatalf("UsageByRepo nil = %+v, %v", usage, err)
		}
		referenced, err := s.ListReferencedLFSOIDs(ctx)
		if err != nil || len(referenced) != 2 || !referenced["oid-1"] || !referenced["oid-2"] {
			t.Fatalf("ListReferencedLFSOIDs = %v, %v", referenced, err)
		}
		all, err := s.ListLFSObjects(ctx)
		if err != nil || len(all) != 2 {
			t.Fatalf("ListLFSObjects = %v, %v", all, err)
		}

		// GC: a referenced object is never deleted; an orphan is, storage first.
		if deleted, err := s.DeleteOrphanedLFSObject(ctx, "oid-1", func() error { return errors.New("must not be called") }); err != nil || deleted {
			t.Fatalf("DeleteOrphanedLFSObject referenced = %v, %v", deleted, err)
		}
		if err := s.DeleteRepo(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteRepo(ctx, other.ID); err != nil {
			t.Fatal(err)
		}
		if referenced, _ := s.ListReferencedLFSOIDs(ctx); len(referenced) != 0 {
			t.Fatalf("repo_lfs_objects survived repo delete: %v", referenced)
		}
		storageErr := errors.New("storage down")
		if deleted, err := s.DeleteOrphanedLFSObject(ctx, "oid-1", func() error { return storageErr }); !errors.Is(err, storageErr) || deleted {
			t.Fatalf("DeleteOrphanedLFSObject storage failure = %v, %v", deleted, err)
		}
		if _, ok, _ := s.HasLFSObject(ctx, "oid-1"); !ok {
			t.Fatal("row deleted although storage delete failed")
		}
		removed := false
		if deleted, err := s.DeleteOrphanedLFSObject(ctx, "oid-1", func() error { removed = true; return nil }); err != nil || !deleted || !removed {
			t.Fatalf("DeleteOrphanedLFSObject = %v, %v (removed %v)", deleted, err, removed)
		}
		if deleted, err := s.DeleteOrphanedLFSObject(ctx, "oid-1", func() error { return nil }); err != nil || deleted {
			t.Fatalf("DeleteOrphanedLFSObject twice = %v, %v", deleted, err)
		}
		if deleted, err := s.DeleteOrphanedLFSObject(ctx, "", func() error { return nil }); err != nil || deleted {
			t.Fatalf("DeleteOrphanedLFSObject empty = %v, %v", deleted, err)
		}

		// Parquet metadata.
		r = f.repo(t, "bob", "pq", "dataset", nil)
		if err := s.ReplaceRepoFiles(ctx, r.ID, "main", []RepoFile{{Path: "a.parquet", Size: 7, BlobSHA: "x", LFSOID: ptr("oid-a")}}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordLFSObject(ctx, r.ID, "oid-a", 700, func(string) (bool, error) { return true, nil }); err != nil {
			t.Fatal(err)
		}
		schema := json.RawMessage(`[{"name":"a"},{"name":"b"}]`)
		if err := s.UpsertParquetFile(ctx, r.ID, "main", "a.parquet", 10, 1, schema); err != nil {
			t.Fatalf("UpsertParquetFile: %v", err)
		}
		if err := s.UpsertParquetFile(ctx, r.ID, "main", "a.parquet", 12, 2, schema); err != nil {
			t.Fatalf("UpsertParquetFile again: %v", err)
		}
		if err := s.UpsertParquetFile(ctx, r.ID, "main", "b.parquet", 1, 1, nil); err != nil {
			t.Fatal(err)
		}
		pfs, err := s.ListParquetFiles(ctx, r.ID, "main")
		if err != nil || len(pfs) != 2 {
			t.Fatalf("ListParquetFiles = %+v, %v", pfs, err)
		}
		if pfs[0].Path != "a.parquet" || pfs[0].NumRows != 12 || pfs[0].NumRowGroups != 2 || pfs[0].NumColumns != 2 || pfs[0].Size != 700 {
			t.Fatalf("parquet a = %+v", pfs[0])
		}
		if pfs[1].NumColumns != 0 || string(pfs[1].Schema) != "[]" || pfs[1].Size != 0 {
			t.Fatalf("parquet b = %+v", pfs[1])
		}
		if err := s.DeleteParquetFiles(ctx, r.ID, "main", []string{"a.parquet", "nope"}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteParquetFiles(ctx, r.ID, "main", nil); err != nil {
			t.Fatal(err)
		}
		if pfs, _ := s.ListParquetFiles(ctx, r.ID, "main"); len(pfs) != 1 || pfs[0].Path != "b.parquet" {
			t.Fatalf("after DeleteParquetFiles = %+v", pfs)
		}
	})
}

func TestIntegrationSyncJobs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "data", "dataset", nil)

		if j, err := s.ClaimSyncJob(ctx); err != nil || j != nil {
			t.Fatalf("claim on empty queue = %+v, %v", j, err)
		}
		if err := s.EnqueueSync(ctx, r.ID, "main", "", "s1"); err != nil {
			t.Fatal(err)
		}
		// A second push to the same ref collapses into the pending job.
		if err := s.EnqueueSync(ctx, r.ID, "main", "s1", "s2"); err != nil {
			t.Fatal(err)
		}
		if err := s.EnqueueSync(ctx, r.ID, "dev", "", "d1"); err != nil {
			t.Fatal(err)
		}
		if n, err := s.PendingSyncCount(ctx, r.ID); err != nil || n != 2 {
			t.Fatalf("PendingSyncCount = %d, %v", n, err)
		}
		j, err := s.ClaimSyncJob(ctx)
		if err != nil || j == nil || j.Ref != "main" || j.OldSHA != "" || j.NewSHA != "s2" || j.Attempts != 1 {
			t.Fatalf("ClaimSyncJob = %+v, %v", j, err)
		}
		// EnqueueSync always writes the default "push" kind with an empty payload.
		if j.Kind != "push" {
			t.Fatalf("ClaimSyncJob kind = %q, want push", j.Kind)
		}
		// Running jobs still count as pending for the UI hint.
		if n, _ := s.PendingSyncCount(ctx, r.ID); n != 2 {
			t.Fatalf("PendingSyncCount while running = %d", n)
		}
		if err := s.FinishSyncJob(ctx, j.ID, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		// Failed once: back to pending, claimable again, attempts grow.
		j2, err := s.ClaimSyncJob(ctx)
		if err != nil || j2 == nil || j2.ID != j.ID || j2.Attempts != 2 {
			t.Fatalf("re-claim = %+v, %v", j2, err)
		}
		if err := s.FinishSyncJob(ctx, j2.ID, nil); err != nil {
			t.Fatal(err)
		}
		j3, err := s.ClaimSyncJob(ctx)
		if err != nil || j3 == nil || j3.Ref != "dev" {
			t.Fatalf("claim dev = %+v, %v", j3, err)
		}
		// Interrupted process: running jobs go back to pending.
		if n, err := s.RequeueRunningJobs(ctx); err != nil || n != 1 {
			t.Fatalf("RequeueRunningJobs = %d, %v", n, err)
		}
		j4, err := s.ClaimSyncJob(ctx)
		if err != nil || j4 == nil || j4.ID != j3.ID || j4.Attempts != 2 {
			t.Fatalf("claim after requeue = %+v, %v", j4, err)
		}
		// Three failures park the job.
		_ = s.FinishSyncJob(ctx, j4.ID, errors.New("x"))
		j5, _ := s.ClaimSyncJob(ctx)
		if j5 == nil || j5.Attempts != 3 {
			t.Fatalf("third claim = %+v", j5)
		}
		_ = s.FinishSyncJob(ctx, j5.ID, errors.New("x"))
		if j6, err := s.ClaimSyncJob(ctx); err != nil || j6 != nil {
			t.Fatalf("failed job was claimable: %+v, %v", j6, err)
		}
		if n, _ := s.PendingSyncCount(ctx, r.ID); n != 0 {
			t.Fatalf("PendingSyncCount after failure = %d", n)
		}

		// Concurrent claimers never get the same job.
		for i := 0; i < 20; i++ {
			if err := s.EnqueueSync(ctx, r.ID, fmt.Sprintf("ref-%d", i), "", "x"); err != nil {
				t.Fatal(err)
			}
		}
		var mu sync.Mutex
		seen := map[int64]int{}
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					j, err := s.ClaimSyncJob(ctx)
					if err != nil {
						t.Errorf("concurrent claim: %v", err)
						return
					}
					if j == nil {
						return
					}
					mu.Lock()
					seen[j.ID]++
					mu.Unlock()
					_ = s.FinishSyncJob(ctx, j.ID, nil)
				}
			}()
		}
		wg.Wait()
		if len(seen) != 20 {
			t.Fatalf("claimed %d distinct jobs, want 20", len(seen))
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("job %d claimed %d times", id, n)
			}
		}
	})
}

func TestIntegrationExperiments(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "exp", "dataset", nil)

		pid, err := s.UpsertExpProject(ctx, r.ID, "proj")
		if err != nil {
			t.Fatalf("UpsertExpProject: %v", err)
		}
		if pid2, err := s.UpsertExpProject(ctx, r.ID, "proj"); err != nil || pid2 != pid {
			t.Fatalf("UpsertExpProject again = %d, %v (want %d)", pid2, err, pid)
		}
		if _, err := s.UpsertExpProject(ctx, r.ID, "other"); err != nil {
			t.Fatal(err)
		}
		// SQLite timestamps have millisecond precision; make sure the run
		// upserts below land on a later updated_at than "other".
		time.Sleep(5 * time.Millisecond)
		p, err := s.GetExpProject(ctx, r.ID, "proj")
		if err != nil || p.ID != pid || p.Name != "proj" {
			t.Fatalf("GetExpProject = %+v, %v", p, err)
		}
		if _, err := s.GetExpProject(ctx, r.ID, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetExpProject missing err = %v", err)
		}

		started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		runID, err := s.UpsertExpRun(ctx, pid, "run-a", "running",
			map[string]any{"lr": 0.1}, map[string]any{"loss": 1.5}, []string{"loss"}, 10, 100, &started)
		if err != nil {
			t.Fatalf("UpsertExpRun: %v", err)
		}
		// Incremental ingest: nil leaves stored values alone, counters only grow,
		// started_at keeps the first value, "" status keeps the old one.
		later := started.Add(time.Hour)
		runID2, err := s.UpsertExpRun(ctx, pid, "run-a", "", nil, map[string]any{"loss": 0.5, "acc": 0.9}, nil, 5, 50, &later)
		if err != nil || runID2 != runID {
			t.Fatalf("UpsertExpRun again = %d, %v", runID2, err)
		}
		run, err := s.GetExpRun(ctx, pid, "run-a")
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "running" || run.Config["lr"] != 0.1 || run.Summary["acc"] != 0.9 || run.Summary["loss"] != 0.5 ||
			!equalStrings(run.MetricKeys, []string{"loss"}) || run.LastStep != 10 || run.NumPoints != 100 ||
			run.StartedAt == nil || !run.StartedAt.Equal(started) || len(run.Tags) != 0 || run.Archived || run.IsBaseline {
			t.Fatalf("run after incremental upsert = %+v (started %v)", run, run.StartedAt)
		}
		if _, err := s.UpsertExpRun(ctx, pid, "run-b", "", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpsertExpRun(ctx, pid, "run-c", "finished", nil, nil, nil, 0, 0, nil); err != nil {
			t.Fatal(err)
		}
		runs, err := s.ListExpRuns(ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		var runNames []string
		for _, r := range runs {
			runNames = append(runNames, r.Name)
		}
		// started_at NULLS LAST, then name.
		if !equalStrings(runNames, []string{"run-a", "run-b", "run-c"}) || runs[1].Status != "finished" || runs[1].StartedAt != nil {
			t.Fatalf("ListExpRuns = %v (%+v)", runNames, runs[1])
		}
		projects, err := s.ListExpProjects(ctx, r.ID)
		if err != nil || len(projects) != 2 || projects[0].Name != "proj" || projects[0].NumRuns != 3 || projects[1].NumRuns != 0 {
			t.Fatalf("ListExpProjects = %+v, %v", projects, err)
		}

		// Annotations.
		if _, err := s.UpdateExpRunAnnotation(ctx, pid, "nope", RunAnnotation{Archived: ptr(true)}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("annotate missing run err = %v", err)
		}
		run, err = s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Tags: ptr([]string{"good", "v2"}), Archived: ptr(true)})
		if err != nil || !equalStrings(run.Tags, []string{"good", "v2"}) || !run.Archived || run.IsBaseline {
			t.Fatalf("annotate = %+v, %v", run, err)
		}
		// Partial update keeps the other fields.
		run, err = s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{IsBaseline: ptr(true)})
		if err != nil || !equalStrings(run.Tags, []string{"good", "v2"}) || !run.Archived || !run.IsBaseline {
			t.Fatalf("partial annotate = %+v, %v", run, err)
		}
		// Clearing tags with an explicit empty list.
		run, err = s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Tags: ptr([]string{})})
		if err != nil || len(run.Tags) != 0 || !run.IsBaseline {
			t.Fatalf("clear tags = %+v, %v", run, err)
		}
		// Baseline moves between runs: one per project.
		if run, err = s.UpdateExpRunAnnotation(ctx, pid, "run-b", RunAnnotation{IsBaseline: ptr(true)}); err != nil || !run.IsBaseline {
			t.Fatalf("baseline run-b = %+v, %v", run, err)
		}
		if a, _ := s.GetExpRun(ctx, pid, "run-a"); a.IsBaseline {
			t.Fatal("run-a kept the baseline flag")
		}
		// Concurrent baseline switches still leave exactly one.
		var wg sync.WaitGroup
		for _, name := range []string{"run-a", "run-b", "run-c", "run-a", "run-c"} {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				if _, err := s.UpdateExpRunAnnotation(ctx, pid, name, RunAnnotation{IsBaseline: ptr(true)}); err != nil {
					t.Errorf("concurrent baseline %s: %v", name, err)
				}
			}(name)
		}
		wg.Wait()
		runs, _ = s.ListExpRuns(ctx, pid)
		baselines := 0
		for _, r := range runs {
			if r.IsBaseline {
				baselines++
			}
		}
		if baselines != 1 {
			t.Fatalf("baselines = %d, want 1", baselines)
		}
		// Upsert from ingest never touches annotations.
		if _, err := s.UpsertExpRun(ctx, pid, "run-a", "finished", nil, nil, nil, 11, 101, nil); err != nil {
			t.Fatal(err)
		}
		if a, _ := s.GetExpRun(ctx, pid, "run-a"); !a.Archived || a.LastStep != 11 {
			t.Fatalf("ingest clobbered annotations: %+v", a)
		}

		// The note is an annotation like the rest: an ingest or a re-index must
		// not erase what a person wrote about the run.
		const note = "# lr sweep\n\nDiverged at step 4k."
		if run, err = s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Note: ptr(note)}); err != nil || run.Note != note {
			t.Fatalf("set note = %+v, %v", run, err)
		}
		if _, err := s.UpsertExpRun(ctx, pid, "run-a", "finished",
			map[string]any{"lr": 0.2}, map[string]any{"loss": 0.1}, []string{"loss"}, 12, 102, nil); err != nil {
			t.Fatal(err)
		}
		if a, _ := s.GetExpRun(ctx, pid, "run-a"); a.Note != note {
			t.Fatalf("re-index cleared the note: %q", a.Note)
		}
		// Absent means unchanged; an explicit empty string clears it.
		if run, err = s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Archived: ptr(false)}); err != nil || run.Note != note {
			t.Fatalf("partial update dropped the note: %+v, %v", run, err)
		}
		if run, err = s.UpdateExpRunAnnotation(ctx, pid, "run-a", RunAnnotation{Note: ptr("")}); err != nil || run.Note != "" {
			t.Fatalf("clear note = %+v, %v", run, err)
		}
		if (RunAnnotation{Note: ptr("x")}).IsEmpty() {
			t.Fatal("RunAnnotation.IsEmpty does not account for Note")
		}

		// Live points.
		ts := time.Date(2026, 5, 6, 7, 8, 9, 123000000, time.UTC)
		if err := s.InsertPoints(ctx, runID, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertPoints(ctx, runID, []MetricPoint{
			{Step: 2, TS: ts, Metrics: map[string]float64{"loss": 0.5}},
			{Step: 1, TS: ts.Add(-time.Second), Metrics: map[string]float64{"loss": 1}},
			{Step: 3, Metrics: map[string]float64{"loss": 0.25}},
		}); err != nil {
			t.Fatalf("InsertPoints: %v", err)
		}
		points, err := s.ListPoints(ctx, runID)
		if err != nil || len(points) != 3 || points[0].Step != 1 || points[1].Metrics["loss"] != 0.5 || !points[1].TS.Equal(ts) {
			t.Fatalf("ListPoints = %+v, %v", points, err)
		}
		if points[2].TS.IsZero() || time.Since(points[2].TS) > time.Minute {
			t.Fatalf("zero TS should default to now: %v", points[2].TS)
		}
		if n, err := s.CountPoints(ctx, runID); err != nil || n != 3 {
			t.Fatalf("CountPoints = %d, %v", n, err)
		}
		// Prune runs missing from the export, except ones that still hold
		// live points.
		if err := s.DeleteProjectRunsNotIn(ctx, pid, []string{"run-c"}); err != nil {
			t.Fatalf("DeleteProjectRunsNotIn: %v", err)
		}
		runs, _ = s.ListExpRuns(ctx, pid)
		runNames = runNames[:0]
		for _, r := range runs {
			runNames = append(runNames, r.Name)
		}
		if !equalStrings(runNames, []string{"run-a", "run-c"}) {
			t.Fatalf("runs after prune = %v", runNames)
		}
		if err := s.DeleteProjectRunsNotIn(ctx, pid, []string{}); err != nil {
			t.Fatal(err)
		}
		if runs, _ = s.ListExpRuns(ctx, pid); len(runs) != 1 || runs[0].Name != "run-a" {
			t.Fatalf("runs after prune all = %+v", runs)
		}
	})
}

func TestIntegrationWebhooks(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "m", "model", nil)
		ns := f.ns(t, "alice")

		wide, err := s.CreateWebhook(ctx, ns.ID, nil, "https://example.com/a", "s1", []string{"repo.created", "repo.pushed"}, true)
		if err != nil {
			t.Fatalf("CreateWebhook: %v", err)
		}
		if wide.RepoFullName() != "" || !equalStrings(wide.Events, []string{"repo.created", "repo.pushed"}) || !wide.Active || wide.Namespace != "alice" {
			t.Fatalf("wide = %+v", wide)
		}
		scoped, err := s.CreateWebhook(ctx, ns.ID, &r.ID, "https://example.com/b", "s2", []string{"repo.pushed"}, true)
		if err != nil || scoped.RepoFullName() != "alice/m" || scoped.RepoKind != "model" {
			t.Fatalf("scoped = %+v, %v", scoped, err)
		}
		inactive, err := s.CreateWebhook(ctx, ns.ID, nil, "https://example.com/c", "s3", []string{"repo.pushed"}, false)
		if err != nil {
			t.Fatal(err)
		}
		if list, err := s.ListWebhooksForNamespace(ctx, ns.ID); err != nil || len(list) != 3 {
			t.Fatalf("ListWebhooksForNamespace = %+v, %v", list, err)
		}
		if _, err := s.GetWebhook(ctx, 9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetWebhook missing err = %v", err)
		}
		ids := func(ws []Webhook) []int64 {
			var out []int64
			for _, w := range ws {
				out = append(out, w.ID)
			}
			sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
			return out
		}
		m, err := s.ListMatchingWebhooks(ctx, ns.ID, &r.ID, "repo.pushed")
		if err != nil || !equalInt64s(ids(m), []int64{wide.ID, scoped.ID}) {
			t.Fatalf("matching pushed = %v, %v", ids(m), err)
		}
		m, err = s.ListMatchingWebhooks(ctx, ns.ID, nil, "repo.created")
		if err != nil || !equalInt64s(ids(m), []int64{wide.ID}) {
			t.Fatalf("matching created = %v, %v", ids(m), err)
		}
		if m, _ := s.ListMatchingWebhooks(ctx, ns.ID, &r.ID, "experiment.run"); len(m) != 0 {
			t.Fatalf("matching unknown event = %v", ids(m))
		}

		// Partial update; nil events keeps the list.
		upd, err := s.UpdateWebhook(ctx, scoped.ID, WebhookUpdate{URL: ptr("https://example.com/b2"), Active: ptr(false)})
		if err != nil || upd.URL != "https://example.com/b2" || upd.Active || !equalStrings(upd.Events, []string{"repo.pushed"}) || upd.Secret != "s2" {
			t.Fatalf("UpdateWebhook = %+v, %v", upd, err)
		}
		upd, err = s.UpdateWebhook(ctx, scoped.ID, WebhookUpdate{Events: []string{"repo.created"}, Active: ptr(true)})
		if err != nil || !equalStrings(upd.Events, []string{"repo.created"}) || !upd.Active {
			t.Fatalf("UpdateWebhook events = %+v, %v", upd, err)
		}

		// Deliveries: claim, lease, finish, retry, park.
		if j, err := s.ClaimWebhookDelivery(ctx, time.Minute); err != nil || j != nil {
			t.Fatalf("claim empty = %+v, %v", j, err)
		}
		d1, err := s.CreateWebhookDelivery(ctx, wide.ID, "repo.pushed", []byte(`{"a":1}`))
		if err != nil {
			t.Fatalf("CreateWebhookDelivery: %v", err)
		}
		dInactive, err := s.CreateWebhookDelivery(ctx, inactive.ID, "repo.pushed", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		j, err := s.ClaimWebhookDelivery(ctx, time.Minute)
		if err != nil || j == nil || j.DeliveryID != d1 || j.WebhookID != wide.ID || j.URL != "https://example.com/a" ||
			j.Secret != "s1" || !j.WebhookActive || !jsonEqual(j.Payload, `{"a":1}`) || j.Attempts != 1 || j.Event != "repo.pushed" {
			t.Fatalf("ClaimWebhookDelivery = %+v, %v", j, err)
		}
		// Leased: not claimable again; the inactive webhook's delivery never is.
		if j2, err := s.ClaimWebhookDelivery(ctx, time.Minute); err != nil || j2 != nil {
			t.Fatalf("claim during lease = %+v, %v", j2, err)
		}
		// Failure under maxAttempts: retried after backoff (0 here).
		if err := s.FinishWebhookDelivery(ctx, d1, false, 1, 3, ptr(500), "oops", 0); err != nil {
			t.Fatal(err)
		}
		d, err := s.GetWebhookDelivery(ctx, d1)
		if err != nil || d.Status != "pending" || d.ResponseStatus == nil || *d.ResponseStatus != 500 || d.ResponseBody != "oops" || d.LastAttemptAt == nil || d.Attempts != 1 {
			t.Fatalf("delivery after failure = %+v, %v", d, err)
		}
		j, err = s.ClaimWebhookDelivery(ctx, time.Minute)
		if err != nil || j == nil || j.DeliveryID != d1 || j.Attempts != 2 {
			t.Fatalf("re-claim = %+v, %v", j, err)
		}
		if err := s.FinishWebhookDelivery(ctx, d1, true, 2, 3, ptr(204), "", 0); err != nil {
			t.Fatal(err)
		}
		if d, _ := s.GetWebhookDelivery(ctx, d1); d.Status != "success" || *d.ResponseStatus != 204 {
			t.Fatalf("delivery after success = %+v", d)
		}
		// Parked at max attempts.
		d2, _ := s.CreateWebhookDelivery(ctx, wide.ID, "repo.created", []byte(`{}`))
		if j, _ := s.ClaimWebhookDelivery(ctx, time.Minute); j == nil || j.DeliveryID != d2 {
			t.Fatalf("claim d2 = %+v", j)
		}
		if err := s.FinishWebhookDelivery(ctx, d2, false, 3, 3, nil, "dead", time.Hour); err != nil {
			t.Fatal(err)
		}
		if d, _ := s.GetWebhookDelivery(ctx, d2); d.Status != "failed" || d.ResponseStatus != nil {
			t.Fatalf("parked delivery = %+v", d)
		}
		// Backoff in the future keeps it out of the queue.
		d3, _ := s.CreateWebhookDelivery(ctx, wide.ID, "repo.created", []byte(`{}`))
		if j, _ := s.ClaimWebhookDelivery(ctx, time.Minute); j == nil || j.DeliveryID != d3 {
			t.Fatalf("claim d3 = %+v", j)
		}
		if err := s.FinishWebhookDelivery(ctx, d3, false, 1, 3, nil, "", time.Hour); err != nil {
			t.Fatal(err)
		}
		if j, err := s.ClaimWebhookDelivery(ctx, time.Minute); err != nil || j != nil {
			t.Fatalf("claimed backed-off delivery = %+v, %v", j, err)
		}
		// Redelivery clones the payload into a fresh pending row.
		d4, err := s.RedeliverWebhookDelivery(ctx, d1)
		if err != nil || d4 == d1 {
			t.Fatalf("Redeliver = %d, %v", d4, err)
		}
		if j, _ := s.ClaimWebhookDelivery(ctx, time.Minute); j == nil || j.DeliveryID != d4 || !jsonEqual(j.Payload, `{"a":1}`) {
			t.Fatalf("claim redelivery = %+v", j)
		}
		page, total, err := s.ListWebhookDeliveries(ctx, wide.ID, 2, 0)
		if err != nil || total != 4 || len(page) != 2 || page[0].ID != d4 || page[1].ID != d3 {
			t.Fatalf("ListWebhookDeliveries = %+v total %d, %v", page, total, err)
		}
		_ = dInactive
		// Reactivating the webhook makes its delivery due.
		if _, err := s.UpdateWebhook(ctx, inactive.ID, WebhookUpdate{Active: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		if j, _ := s.ClaimWebhookDelivery(ctx, time.Minute); j == nil || j.DeliveryID != dInactive {
			t.Fatalf("claim reactivated = %+v", j)
		}

		if err := s.DeleteWebhook(ctx, wide.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteWebhook(ctx, wide.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DeleteWebhook twice err = %v", err)
		}
		if _, total, _ := s.ListWebhookDeliveries(ctx, wide.ID, 10, 0); total != 0 {
			t.Fatalf("deliveries survived webhook delete: %d", total)
		}
	})
}

// jsonEqual compares JSON documents structurally (jsonb re-serialises with
// its own spacing).
func jsonEqual(got []byte, want string) bool {
	var a, b any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(want), &b) != nil {
		return false
	}
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestIntegrationDownloads(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		r := f.repo(t, "alice", "m", "model", nil)
		s.RecordDownload(ctx, r.ID)
		s.RecordDownload(ctx, r.ID)
		now := time.Now().UTC()
		if n, err := s.DownloadsSince(ctx, r.ID, now.AddDate(0, 0, -30)); err != nil || n != 2 {
			t.Fatalf("DownloadsSince 30d = %d, %v", n, err)
		}
		// Today with a time of day still counts today (inclusive by date).
		if n, err := s.DownloadsSince(ctx, r.ID, now); err != nil || n != 2 {
			t.Fatalf("DownloadsSince today = %d, %v", n, err)
		}
		if n, err := s.DownloadsSince(ctx, r.ID, now.AddDate(0, 0, 1)); err != nil || n != 0 {
			t.Fatalf("DownloadsSince tomorrow = %d, %v", n, err)
		}
		// Yesterday's counter is included, and the repo delete cascades.
		if _, err := s.db.Exec(ctx, `INSERT INTO repo_download_stats (repo_id, date, count) VALUES ($1, $2, 5)`,
			r.ID, s.d.dateArg(now.AddDate(0, 0, -1))); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.DownloadsSince(ctx, r.ID, now.AddDate(0, 0, -1)); n != 7 {
			t.Fatalf("DownloadsSince incl. yesterday = %d", n)
		}
		if n, _ := s.DownloadsSince(ctx, r.ID, now); n != 2 {
			t.Fatalf("DownloadsSince today only = %d", n)
		}
		if err := s.DeleteRepo(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.DownloadsSince(ctx, r.ID, now.AddDate(0, 0, -30)); n != 0 {
			t.Fatalf("download stats survived delete: %d", n)
		}
	})
}

func TestIntegrationLineage(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		base := f.repo(t, "alice", "base-model", "model", nil)
		ds := f.repo(t, "bob", "imdb", "dataset", nil)
		other := f.repo(t, "bob", "other-ds", "dataset", nil)
		fine := f.repo(t, "alice", "fine-tuned", "model", nil)
		exp := f.repo(t, "bob", "exp-logs", "dataset", nil)

		edges := []LineageEdge{
			{Kind: LineageKindBaseModel, Raw: "alice/base-model", Namespace: "alice", Name: "base-model", Relation: "quantized", Ordinal: 0},
			{Kind: LineageKindDataset, Raw: "bob/imdb@v1", Namespace: "bob", Name: "imdb", Rev: "v1", Ordinal: 0},
			{Kind: LineageKindDataset, Raw: "bob/other-ds", Namespace: "bob", Name: "other-ds", Ordinal: 1},
			{Kind: LineageKindDataset, Raw: "???", Ordinal: 2},
			{Kind: LineageKindDataset, Raw: "bob/imdb@v1", Namespace: "bob", Name: "imdb", Rev: "v1", Ordinal: 3}, // duplicate
			{Kind: LineageKindRun, Raw: "bob/exp-logs:proj/run-1", Namespace: "bob", Name: "exp-logs", Project: "proj", Run: "run-1"},
		}
		if err := s.ReplaceRepoLineage(ctx, fine.ID, edges); err != nil {
			t.Fatalf("ReplaceRepoLineage: %v", err)
		}
		up, err := s.ListRepoLineage(ctx, fine.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range up {
			got = append(got, fmt.Sprintf("%s:%s:%v", e.Kind, e.Raw, e.Exists))
		}
		// Ordered by kind, then ordinal (the duplicate imdb edge's last
		// ordinal wins), then raw.
		want := []string{
			"base_model:alice/base-model:true",
			"dataset:bob/other-ds:true",
			"dataset:???:false",
			"dataset:bob/imdb@v1:true",
			"run:bob/exp-logs:proj/run-1:true",
		}
		if !equalStrings(got, want) {
			t.Fatalf("ListRepoLineage = %v, want %v", got, want)
		}
		// Ordinal from the last duplicate wins (upsert).
		for _, e := range up {
			if e.Raw == "bob/imdb@v1" && e.Ordinal != 3 {
				t.Fatalf("duplicate edge ordinal = %d", e.Ordinal)
			}
		}
		// The base model relation round-trips; other kinds keep "".
		for _, e := range up {
			want := ""
			if e.Kind == LineageKindBaseModel {
				want = "quantized"
			}
			if e.Relation != want {
				t.Errorf("%s:%s relation = %q, want %q", e.Kind, e.Raw, e.Relation, want)
			}
		}

		deps, err := s.ListLineageDependents(ctx, []string{LineageKindBaseModel}, "alice", "base-model")
		if err != nil || len(deps) != 1 || deps[0].Repo.ID != fine.ID || deps[0].Edge.Kind != LineageKindBaseModel {
			t.Fatalf("ListLineageDependents = %+v, %v", deps, err)
		}
		// The reverse lookup carries the relation too: it is what the model
		// tree groups the derived repositories by.
		if deps[0].Edge.Relation != "quantized" {
			t.Errorf("dependent relation = %q, want %q", deps[0].Edge.Relation, "quantized")
		}
		deps, err = s.ListLineageDependents(ctx, []string{LineageKindDataset, LineageKindRun}, "bob", "imdb")
		if err != nil || len(deps) != 1 || deps[0].Edge.Rev != "v1" {
			t.Fatalf("ListLineageDependents imdb = %+v, %v", deps, err)
		}
		if deps, _ := s.ListLineageDependents(ctx, nil, "bob", "imdb"); len(deps) != 0 {
			t.Fatalf("no kinds should return nothing: %+v", deps)
		}
		runDeps, err := s.ListRunDependents(ctx, "bob", "exp-logs", "proj", "run-1")
		if err != nil || len(runDeps) != 1 || runDeps[0].Edge.Run != "run-1" {
			t.Fatalf("ListRunDependents = %+v, %v", runDeps, err)
		}
		if runDeps, _ := s.ListRunDependents(ctx, "bob", "exp-logs", "proj", ""); len(runDeps) != 1 {
			t.Fatalf("ListRunDependents any run = %+v", runDeps)
		}
		if runDeps, _ := s.ListRunDependents(ctx, "bob", "exp-logs", "proj", "run-2"); len(runDeps) != 0 {
			t.Fatalf("ListRunDependents other run = %+v", runDeps)
		}
		// Replace with an empty set clears, and the repo delete cascades.
		if err := s.ReplaceRepoLineage(ctx, fine.ID, nil); err != nil {
			t.Fatal(err)
		}
		if up, _ := s.ListRepoLineage(ctx, fine.ID); len(up) != 0 {
			t.Fatalf("lineage not cleared: %+v", up)
		}
		_ = s.ReplaceRepoLineage(ctx, fine.ID, edges[:1])
		_ = s.DeleteRepo(ctx, fine.ID)
		if deps, _ := s.ListLineageDependents(ctx, []string{LineageKindBaseModel}, "alice", "base-model"); len(deps) != 0 {
			t.Fatalf("lineage survived repo delete: %+v", deps)
		}
		_, _, _, _ = base, ds, other, exp
	})
}

func TestIntegrationRepoTransfer(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		aliceNS := f.ns(t, "alice")
		bobNS := f.ns(t, "bob")

		r := f.repo(t, "alice", "foo", "model", nil)
		storagePath := r.StoragePath

		dep := f.repo(t, "bob", "dep", "model", nil)
		if err := s.ReplaceRepoLineage(ctx, dep.ID, []LineageEdge{
			{Kind: LineageKindBaseModel, Raw: "alice/foo", Namespace: "alice", Name: "foo"},
		}); err != nil {
			t.Fatal(err)
		}
		// A dataset may share the model's name; edges pointing at *it* must
		// not follow the model's move.
		f.repo(t, "alice", "foo", "dataset", nil)
		dsDep := f.repo(t, "bob", "ds-dep", "model", nil)
		if err := s.ReplaceRepoLineage(ctx, dsDep.ID, []LineageEdge{
			{Kind: LineageKindDataset, Raw: "alice/foo", Namespace: "alice", Name: "foo"},
		}); err != nil {
			t.Fatal(err)
		}

		repoWH, err := s.CreateWebhook(ctx, aliceNS.ID, &r.ID, "https://example.com/repo", "s1", []string{"repo.pushed"}, true)
		if err != nil {
			t.Fatal(err)
		}
		nsWH, err := s.CreateWebhook(ctx, aliceNS.ID, nil, "https://example.com/ns", "s2", []string{"repo.pushed"}, true)
		if err != nil {
			t.Fatal(err)
		}

		got, err := s.TransferRepo(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.bob.ID})
		if err != nil {
			t.Fatalf("TransferRepo: %v", err)
		}
		if got.Namespace != "bob" || got.Name != "foo" || got.NamespaceID != bobNS.ID {
			t.Fatalf("TransferRepo result = %+v", got)
		}
		// The physical location never moves.
		if got.StoragePath != storagePath {
			t.Fatalf("StoragePath changed: %q -> %q", storagePath, got.StoragePath)
		}

		// Old name resolves via redirect; the new name is a normal GetRepo.
		if _, err := s.GetRepo(ctx, "model", "alice", "foo"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetRepo old name err = %v", err)
		}
		red, err := s.ResolveRepoRedirect(ctx, "model", "alice", "foo")
		if err != nil || red.ID != r.ID || red.Namespace != "bob" || red.Name != "foo" {
			t.Fatalf("ResolveRepoRedirect = %+v, %v", red, err)
		}
		if got2, err := s.GetRepo(ctx, "model", "bob", "foo"); err != nil || got2.ID != r.ID {
			t.Fatalf("GetRepo new name = %+v, %v", got2, err)
		}
		if list, err := s.ListRepoRedirects(ctx, r.ID); err != nil || len(list) != 1 ||
			list[0].FromNamespace != "alice" || list[0].FromName != "foo" {
			t.Fatalf("ListRepoRedirects = %+v, %v", list, err)
		}

		// repo_lineage's target follows the move.
		if deps, err := s.ListLineageDependents(ctx, []string{LineageKindBaseModel}, "bob", "foo"); err != nil ||
			len(deps) != 1 || deps[0].Repo.ID != dep.ID {
			t.Fatalf("ListLineageDependents new target = %+v, %v", deps, err)
		}
		if deps, _ := s.ListLineageDependents(ctx, []string{LineageKindBaseModel}, "alice", "foo"); len(deps) != 0 {
			t.Fatalf("stale lineage target still matches: %+v", deps)
		}
		if deps, err := s.ListLineageDependents(ctx, []string{LineageKindDataset}, "alice", "foo"); err != nil ||
			len(deps) != 1 || deps[0].Repo.ID != dsDep.ID {
			t.Fatalf("dataset lineage edge followed a model move: %+v, %v", deps, err)
		}
		if deps, _ := s.ListLineageDependents(ctx, []string{LineageKindDataset}, "bob", "foo"); len(deps) != 0 {
			t.Fatalf("dataset lineage edge retargeted by a model move: %+v", deps)
		}

		// The repo-scoped webhook is gone; the namespace-wide one survives.
		if _, err := s.GetWebhook(ctx, repoWH.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("repo webhook survived transfer: %v", err)
		}
		if _, err := s.GetWebhook(ctx, nsWH.ID); err != nil {
			t.Fatalf("namespace webhook deleted by transfer: %v", err)
		}

		// A transfer moves nothing in object storage -- every key is
		// content-addressed -- so it must not queue any sync work either.
		if job, err := s.ClaimSyncJob(ctx); err != nil || job != nil {
			t.Fatalf("transfer enqueued a sync job = %+v, %v", job, err)
		}

		// One accepted repo_transfers row records the move.
		rt, err := s.GetRepoTransfer(ctx, lastRepoTransferID(t, s, ctx, r.ID))
		if err != nil || rt.Status != "accepted" || rt.Kind != "model" ||
			rt.FromNamespace != "alice" || rt.FromName != "foo" ||
			rt.ToNamespace != "bob" || rt.ToName != "foo" ||
			rt.DecidedBy == nil || *rt.DecidedBy != f.bob.ID {
			t.Fatalf("GetRepoTransfer accepted row = %+v, %v", rt, err)
		}

		// Conflict: another repository cannot move onto an occupied name.
		other := f.repo(t, "alice", "bar", "model", nil)
		if _, err := s.TransferRepo(ctx, TransferSpec{RepoID: other.ID, ToNamespaceID: bobNS.ID, ToName: "foo", ActorID: f.admin.ID}); !errors.Is(err, ErrConflict) {
			t.Fatalf("TransferRepo onto occupied name err = %v", err)
		}

		// No-op: moving to the repository's own (namespace, name) is rejected too.
		if _, err := s.TransferRepo(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.bob.ID}); !errors.Is(err, ErrConflict) {
			t.Fatalf("no-op TransferRepo err = %v", err)
		}

		// Missing repository / target namespace.
		if _, err := s.TransferRepo(ctx, TransferSpec{RepoID: 999999, ToNamespaceID: bobNS.ID, ActorID: f.admin.ID}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("TransferRepo missing repo err = %v", err)
		}
		if _, err := s.TransferRepo(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: 999999, ActorID: f.admin.ID}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("TransferRepo missing namespace err = %v", err)
		}

		// Multi-hop: every former name a repository has ever had resolves to
		// its current location (docs/repo-transfer-design.md §5).
		got3, err := s.TransferRepo(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: aliceNS.ID, ToName: "foo3", ActorID: f.admin.ID})
		if err != nil {
			t.Fatalf("second TransferRepo: %v", err)
		}
		if got3.Namespace != "alice" || got3.Name != "foo3" {
			t.Fatalf("second TransferRepo result = %+v", got3)
		}
		for _, old := range []struct{ ns, name string }{{"alice", "foo"}, {"bob", "foo"}} {
			red, err := s.ResolveRepoRedirect(ctx, "model", old.ns, old.name)
			if err != nil || red.ID != r.ID || red.Namespace != "alice" || red.Name != "foo3" {
				t.Fatalf("ResolveRepoRedirect(%s/%s) = %+v, %v, want current alice/foo3", old.ns, old.name, red, err)
			}
		}

		// Creating a new repository at an old name reclaims it: the stale
		// redirect is dropped and the new repository answers there instead
		// (docs/repo-transfer-design.md §5 "conflicts").
		reclaimed := f.repo(t, "bob", "foo", "model", nil)
		if _, err := s.ResolveRepoRedirect(ctx, "model", "bob", "foo"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("stale redirect survived reclaiming create: %v", err)
		}
		if got, err := s.GetRepo(ctx, "model", "bob", "foo"); err != nil || got.ID != reclaimed.ID {
			t.Fatalf("GetRepo reclaimed name = %+v, %v", got, err)
		}
	})
}

// lastRepoTransferID returns the most recent repo_transfers row for a
// repository -- a direct query rather than a public listing method, since
// this package's tests may reach into the schema directly.
func lastRepoTransferID(t *testing.T, s *Store, ctx context.Context, repoID int64) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow(ctx,
		`SELECT id FROM repo_transfers WHERE repo_id = $1 ORDER BY id DESC LIMIT 1`, repoID).Scan(&id); err != nil {
		t.Fatalf("lastRepoTransferID: %v", err)
	}
	return id
}

func TestIntegrationRepoTransferRequests(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		bobNS := f.ns(t, "bob")

		r1 := f.repo(t, "alice", "one", "dataset", nil)
		r2 := f.repo(t, "alice", "two", "dataset", nil)
		r3 := f.repo(t, "alice", "three", "dataset", nil)

		// A pending request does not move anything yet.
		pt, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r1.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, 7*24*time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer: %v", err)
		}
		if pt.Status != "pending" || pt.FromNamespace != "alice" || pt.FromName != "one" ||
			pt.ToNamespace != "bob" || pt.ToName != "one" || pt.RequestedByName != "alice" {
			t.Fatalf("CreateRepoTransfer result = %+v", pt)
		}
		if got, err := s.GetRepo(ctx, "dataset", "alice", "one"); err != nil || got.Namespace != "alice" {
			t.Fatalf("repo moved before acceptance: %+v, %v", got, err)
		}
		if got, err := s.PendingRepoTransfer(ctx, r1.ID); err != nil || got.ID != pt.ID {
			t.Fatalf("PendingRepoTransfer = %+v, %v", got, err)
		}
		// A second pending request for the same repository conflicts (the
		// partial unique index enforces "one pending transfer at a time").
		if _, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r1.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, time.Hour); !errors.Is(err, ErrConflict) {
			t.Fatalf("second pending transfer err = %v", err)
		}

		// Accept: the same physical move as TransferRepo, and the pending
		// row itself is flipped rather than a new one being inserted.
		got, err := s.AcceptRepoTransfer(ctx, pt.ID, f.bob.ID)
		if err != nil || got.Namespace != "bob" || got.Name != "one" {
			t.Fatalf("AcceptRepoTransfer = %+v, %v", got, err)
		}
		accepted, err := s.GetRepoTransfer(ctx, pt.ID)
		if err != nil || accepted.Status != "accepted" || accepted.DecidedBy == nil || *accepted.DecidedBy != f.bob.ID {
			t.Fatalf("GetRepoTransfer after accept = %+v, %v", accepted, err)
		}
		if _, err := s.PendingRepoTransfer(ctx, r1.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("PendingRepoTransfer after accept err = %v", err)
		}
		if _, err := s.AcceptRepoTransfer(ctx, pt.ID, f.bob.ID); !errors.Is(err, ErrTransferNotPending) {
			t.Fatalf("re-accept err = %v", err)
		}

		// Reject.
		rt2, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r2.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, 7*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RejectRepoTransfer(ctx, rt2.ID, f.bob.ID); err != nil {
			t.Fatalf("RejectRepoTransfer: %v", err)
		}
		if got, err := s.GetRepoTransfer(ctx, rt2.ID); err != nil || got.Status != "rejected" ||
			got.DecidedBy == nil || *got.DecidedBy != f.bob.ID {
			t.Fatalf("GetRepoTransfer after reject = %+v, %v", got, err)
		}
		if _, err := s.GetRepo(ctx, "dataset", "alice", "two"); err != nil {
			t.Fatal("rejected transfer moved the repository")
		}
		if err := s.RejectRepoTransfer(ctx, rt2.ID, f.bob.ID); !errors.Is(err, ErrTransferNotPending) {
			t.Fatalf("re-reject err = %v", err)
		}

		// Cancel.
		rt3, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r3.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, 7*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CancelRepoTransfer(ctx, rt3.ID, f.alice.ID); err != nil {
			t.Fatalf("CancelRepoTransfer: %v", err)
		}
		if got, err := s.GetRepoTransfer(ctx, rt3.ID); err != nil || got.Status != "cancelled" {
			t.Fatalf("GetRepoTransfer after cancel = %+v, %v", got, err)
		}

		// Expired: an already-past-due pending request cannot be accepted,
		// and the row is flipped to 'expired' as a side effect of noticing.
		r4 := f.repo(t, "alice", "four", "dataset", nil)
		exp, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r4.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, -time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AcceptRepoTransfer(ctx, exp.ID, f.bob.ID); !errors.Is(err, ErrTransferNotPending) {
			t.Fatalf("accept expired err = %v", err)
		}
		if got, err := s.GetRepoTransfer(ctx, exp.ID); err != nil || got.Status != "expired" {
			t.Fatalf("expired transfer status = %+v, %v", got, err)
		}
		if _, err := s.PendingRepoTransfer(ctx, r4.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("PendingRepoTransfer for expired err = %v", err)
		}
		if err := s.RejectRepoTransfer(ctx, exp.ID, f.bob.ID); !errors.Is(err, ErrTransferNotPending) {
			t.Fatalf("reject expired err = %v", err)
		}

		// Missing repository / namespace / transfer id.
		if _, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: 999999, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, time.Hour); !errors.Is(err, ErrNotFound) {
			t.Fatalf("CreateRepoTransfer missing repo err = %v", err)
		}
		if _, err := s.GetRepoTransfer(ctx, 999999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetRepoTransfer missing err = %v", err)
		}
		if err := s.CancelRepoTransfer(ctx, 999999, f.alice.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("CancelRepoTransfer missing err = %v", err)
		}

		// ListRepoTransfersForUser: incoming (write access to the target
		// namespace -- its owner, or an org member with an admin/write
		// role) vs outgoing (write access to the source namespace).
		org, err := s.CreateOrg(ctx, "acme", f.bob.ID, OrgUpdate{})
		if err != nil {
			t.Fatal(err)
		}
		r5 := f.repo(t, "alice", "five", "dataset", nil)
		pt5, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r5.ID, ToNamespaceID: org.ID, ActorID: f.alice.ID}, 7*24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx,
			`INSERT INTO org_members (namespace_id, user_id, role) VALUES ($1, $2, 'write')`, org.ID, f.admin.ID); err != nil {
			t.Fatal(err)
		}

		inAlice, outAlice, err := s.ListRepoTransfersForUser(ctx, f.alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(inAlice) != 0 {
			t.Fatalf("alice has no incoming transfers to accept, got %+v", inAlice)
		}
		if ids := transferIDs(outAlice); !equalInt64s(ids, []int64{pt5.ID}) {
			t.Fatalf("alice outgoing = %v, want [%d]", ids, pt5.ID)
		}

		inAdmin, outAdmin, err := s.ListRepoTransfersForUser(ctx, f.admin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ids := transferIDs(inAdmin); !equalInt64s(ids, []int64{pt5.ID}) {
			t.Fatalf("admin (acme write member) incoming = %v, want [%d]", ids, pt5.ID)
		}
		if len(outAdmin) != 0 {
			t.Fatalf("admin has no outgoing transfers, got %+v", outAdmin)
		}

		inBob, outBob, err := s.ListRepoTransfersForUser(ctx, f.bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ids := transferIDs(inBob); !equalInt64s(ids, []int64{pt5.ID}) {
			t.Fatalf("bob (acme owner) incoming = %v, want [%d]", ids, pt5.ID)
		}
		if len(outBob) != 0 {
			t.Fatalf("bob has no outgoing transfers, got %+v", outBob)
		}
	})
}

func transferIDs(ts []RepoTransfer) []int64 {
	out := make([]int64, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

// TestIntegrationRepoTransferStalePending covers the race Bugbot flagged: a
// pending request must not outlive a move of the repository it describes,
// and accepting a request whose "from" no longer matches must void it.
func TestIntegrationRepoTransferStalePending(t *testing.T) {
	forEachBackend(t, func(t *testing.T, s *Store) {
		f := newFixture(t, s)
		ctx := f.ctx
		aliceNS := f.ns(t, "alice")
		bobNS := f.ns(t, "bob")
		r := f.repo(t, "alice", "foo", "model", nil)

		// 1. An immediate move cancels the pending request.
		pend, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer: %v", err)
		}
		if _, err := s.TransferRepo(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: aliceNS.ID, ToName: "bar", ActorID: f.alice.ID}); err != nil {
			t.Fatalf("TransferRepo rename: %v", err)
		}
		got, err := s.GetRepoTransfer(ctx, pend.ID)
		if err != nil || got.Status != "cancelled" {
			t.Fatalf("pending after rename = %+v, %v (want cancelled)", got, err)
		}
		if _, err := s.AcceptRepoTransfer(ctx, pend.ID, f.bob.ID); !errors.Is(err, ErrTransferNotPending) {
			t.Fatalf("Accept cancelled transfer err = %v", err)
		}
		if cur, _ := s.GetRepoByID(ctx, r.ID); cur.Namespace != "alice" || cur.Name != "bar" {
			t.Fatalf("repo moved by a voided request: %+v", cur)
		}

		// 2. A pending row whose from-location no longer matches (forced here
		//    by hand, as the store itself cancels it in step 1) is voided on
		//    accept rather than executed.
		pend2, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer 2: %v", err)
		}
		if _, err := s.db.Exec(ctx, `UPDATE repo_transfers SET from_name = 'baz' WHERE id = $1`, pend2.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AcceptRepoTransfer(ctx, pend2.ID, f.bob.ID); !errors.Is(err, ErrTransferNotPending) {
			t.Fatalf("Accept stale transfer err = %v", err)
		}
		if got, _ := s.GetRepoTransfer(ctx, pend2.ID); got.Status != "cancelled" {
			t.Fatalf("stale transfer status = %q, want cancelled", got.Status)
		}
		if cur, _ := s.GetRepoByID(ctx, r.ID); cur.Namespace != "alice" || cur.Name != "bar" {
			t.Fatalf("repo moved by a stale request: %+v", cur)
		}

		// 3. A matching pending request still accepts normally.
		pend3, err := s.CreateRepoTransfer(ctx, TransferSpec{RepoID: r.ID, ToNamespaceID: bobNS.ID, ActorID: f.alice.ID}, time.Hour)
		if err != nil {
			t.Fatalf("CreateRepoTransfer 3: %v", err)
		}
		moved, err := s.AcceptRepoTransfer(ctx, pend3.ID, f.bob.ID)
		if err != nil || moved.Namespace != "bob" || moved.Name != "bar" {
			t.Fatalf("Accept = %+v, %v", moved, err)
		}
	})
}
