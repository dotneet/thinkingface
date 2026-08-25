package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/ulid"
)

type Repo struct {
	ID          int64  `json:"id"`
	NamespaceID int64  `json:"-"`
	Namespace   string `json:"namespace"`
	// NamespaceKind is the owning namespace's kind ("user" or "org"), joined
	// so callers can link to the right profile page without a second query.
	NamespaceKind string         `json:"namespace_kind"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	DefaultBranch string         `json:"default_branch"`
	Description   string         `json:"description"`
	Card          map[string]any `json:"card"`
	HeadSHA       string         `json:"head_sha"`
	TotalSize     int64          `json:"total_size"`
	Downloads     int64          `json:"downloads"`
	IsExperiment  bool           `json:"is_experiment"`
	// StoragePath is where this repository's git history physically lives
	// (wal/{StoragePath}/ and {root}/{StoragePath}.git). Assigned at creation
	// and never changed, so a transfer or rename never moves data. Legacy
	// rows carry "{models|datasets}/{ns}/{name}", new rows "repos/{ulid}".
	// Never exposed over the API.
	StoragePath string    `json:"-"`
	NumFiles    int       `json:"num_files"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// ArchivedAt is when the repository was made read-only, or nil while it
	// is active. It is both the flag and the audit timestamp; see
	// Archived().
	ArchivedAt *time.Time `json:"archived_at"`
}

func (r *Repo) FullName() string { return r.Namespace + "/" + r.Name }

// Archived reports whether the repository is read-only. Every write path
// (push, commit, edit, transfer, experiment ingest) refuses an archived
// repository; deleting one is still allowed.
func (r *Repo) Archived() bool { return r.ArchivedAt != nil }

// Tags pulls the tag list out of the README front matter.
func (r *Repo) Tags() []string {
	return stringSliceFromCard(r.Card, "tags")
}

func (r *Repo) License() string {
	if v, ok := r.Card["license"].(string); ok {
		return v
	}
	return ""
}

func stringSliceFromCard(card map[string]any, key string) []string {
	raw, ok := card[key]
	if !ok {
		return []string{}
	}
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return []string{}
}

const repoColumns = `r.id, r.namespace_id, n.name, n.kind, r.name, r.kind, r.default_branch,
	r.description, r.card, r.head_sha, r.total_size, r.downloads, r.is_experiment, r.storage_path,
	r.created_at, r.updated_at, r.archived_at,
	(SELECT count(*) FROM repo_files f WHERE f.repo_id = r.id AND f.ref = r.default_branch)`

func scanRepo(row rowScanner) (*Repo, error) {
	return scanRepoWith(row)
}

// scanRepoWith scans a row that selected repoColumns followed by `extra`
// columns of its own, so a join can read a repository and its joined columns in
// the single Scan call pgx allows per row.
func scanRepoWith(row rowScanner, extra ...any) (*Repo, error) {
	r := &Repo{}
	var cardRaw []byte
	dest := []any{&r.ID, &r.NamespaceID, &r.Namespace, &r.NamespaceKind, &r.Name, &r.Kind, &r.DefaultBranch,
		&r.Description, &cardRaw, &r.HeadSHA, &r.TotalSize, &r.Downloads, &r.IsExperiment, &r.StoragePath,
		&r.CreatedAt, &r.UpdatedAt, &r.ArchivedAt, &r.NumFiles}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, norm(err)
	}
	r.Card = map[string]any{}
	if len(cardRaw) > 0 {
		_ = json.Unmarshal(cardRaw, &r.Card)
	}
	return r, nil
}

// NewStoragePath returns the physical location for a repository created now:
// an opaque "repos/{ulid}" that carries no trace of the owner or name, so the
// repository can later be transferred or renamed without moving anything.
func NewStoragePath() string { return "repos/" + ulid.New() }

// CreateRepo inserts the row. storagePath is normally NewStoragePath(); tests
// and the migration path may pass a legacy "{models|datasets}/{ns}/{name}".
//
// If (kind, ns, name) used to name a repository that has since moved
// elsewhere, that redirect is deleted in the same transaction: a repository
// newly created at an old name takes the name over rather than being shadowed
// by the redirect (docs/dev/repo-transfer-design.md §5 "conflicts").
func (s *Store) CreateRepo(ctx context.Context, nsID int64, name, kind, description, defaultBranch, storagePath string) (*Repo, error) {
	if storagePath == "" {
		storagePath = NewStoragePath()
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var id int64
	err = tx.QueryRow(ctx,
		`INSERT INTO repositories (namespace_id, name, kind, description, default_branch, storage_path)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		nsID, name, kind, description, defaultBranch, storagePath).Scan(&id)
	if s.d.isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("insert repository: %w", err)
	}

	var nsName string
	if err := tx.QueryRow(ctx, `SELECT name FROM namespaces WHERE id = $1`, nsID).Scan(&nsName); err != nil {
		return nil, norm(err)
	}
	// Folded on the namespace, exactly as ResolveRepoRedirect matches: a
	// redirect this create cannot see is a redirect it cannot clear.
	if _, err := tx.Exec(ctx,
		`DELETE FROM repo_redirects WHERE kind = $1 AND LOWER(from_namespace) = LOWER($2) AND from_name = $3`,
		kind, nsName, name); err != nil {
		return nil, err
	}

	repo, err := scanRepo(tx.QueryRow(ctx,
		`SELECT `+repoColumns+` FROM repositories r JOIN namespaces n ON n.id = r.namespace_id WHERE r.id = $1`, id))
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

func (s *Store) GetRepoByID(ctx context.Context, id int64) (*Repo, error) {
	return scanRepo(s.db.QueryRow(ctx,
		`SELECT `+repoColumns+` FROM repositories r JOIN namespaces n ON n.id = r.namespace_id WHERE r.id = $1`, id))
}

// GetRepo resolves kind/ns/name. ns is matched case-insensitively (see
// GetNamespace); name (the repository name within the namespace) is not --
// that scope is unaffected by this change.
func (s *Store) GetRepo(ctx context.Context, kind, ns, name string) (*Repo, error) {
	return scanRepo(s.db.QueryRow(ctx,
		`SELECT `+repoColumns+` FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
		 WHERE r.kind = $1 AND LOWER(n.name) = LOWER($2) AND r.name = $3`, kind, ns, name))
}

// GetRepoAnyKind resolves ns/name when the caller does not know whether it is a
// dataset or a model. The git smart-HTTP routes hit this. ns is matched
// case-insensitively (see GetNamespace).
func (s *Store) GetRepoAnyKind(ctx context.Context, ns, name string) (*Repo, error) {
	return scanRepo(s.db.QueryRow(ctx,
		`SELECT `+repoColumns+` FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
		 WHERE LOWER(n.name) = LOWER($1) AND r.name = $2 ORDER BY r.kind LIMIT 1`, ns, name))
}

// SetRepoArchived flips a repository between read-only and active and returns
// the row as it now stands. Archiving stamps archived_at/archived_by;
// unarchiving clears both. updated_at is deliberately left alone: archiving is
// bookkeeping, not a content change, and bumping it would push the repository
// to the top of the "recently updated" listing.
func (s *Store) SetRepoArchived(ctx context.Context, id int64, archived bool, actorID int64) (*Repo, error) {
	var query string
	args := []any{id}
	if archived {
		query = `UPDATE repositories SET archived_at = now(), archived_by = $2
		         WHERE id = $1 AND archived_at IS NULL`
		args = append(args, actorID)
	} else {
		query = `UPDATE repositories SET archived_at = NULL, archived_by = NULL
		         WHERE id = $1 AND archived_at IS NOT NULL`
	}
	// A no-op update (already in the requested state) is not an error: the
	// caller reads the row back either way, so the endpoint is idempotent.
	if _, err := s.db.Exec(ctx, query, args...); err != nil {
		return nil, err
	}
	return s.GetRepoByID(ctx, id)
}

// DeleteRepo drops the repository row; everything keyed by repo_id (files,
// parquet index, lineage, webhooks, LFS links) cascades with it.
//
// Nothing in object storage is deleted here and nothing is queued to be:
// lfs/ and blobs/ are content-addressed layers shared across repositories, so
// a delete only drops references. `thinkingface gc` is what reclaims the
// objects nothing references any more.
func (s *Store) DeleteRepo(ctx context.Context, id int64) error {
	n, err := s.db.Exec(ctx, `DELETE FROM repositories WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type RepoFilter struct {
	Kind string
	// Query is a legacy substring match against name / namespace /
	// description (ILIKE). It backs the HF-compatible list_models /
	// list_datasets `search=` parameter and the also-HF-compatible
	// `q=` — its behaviour must not change, since huggingface_hub relies on
	// plain substring matching (e.g. "bert" matching "distilbert").
	Query string
	// Search is free text matched against the full text index (tsvector on
	// Postgres, FTS5 on SQLite) as an AND of word prefixes; see
	// dialect.searchPredicate. It backs the Web UI's `search=` parameter and
	// is new: nothing HF-compatible sets it. Text with no usable tokens is
	// the same as no search filter.
	Search string
	Author string
	// Tags requires every listed tag to be present (AND), via containment
	// against the `tags` array of the card.
	Tags []string
	// License and Task filter on the corresponding repository card fields.
	// Task matches either a model's `pipeline_tag` or a dataset's
	// `task_categories` list.
	License string
	Task    string
	// BaseModel narrows to the repositories whose card names this "ns/name"
	// as a base model -- the derivatives of one model. A revision pinned on
	// either side is ignored: "ns/name@v1" filters by the repository, since
	// requiring the revision to match too would leave the listing all but
	// always empty.
	BaseModel string
	// Relation narrows to repositories carrying a base_model edge that
	// declares this relation (finetune / adapter / quantized / merge, or
	// whatever else a card wrote). Combined with BaseModel it constrains the
	// same edge, so base_model=a/b + relation=quantized asks for the
	// quantisations *of a/b*.
	Relation string
	// Dataset narrows to the repositories trained on this "ns/name" dataset,
	// i.e. those carrying a `dataset` lineage edge pointing at it. Run edges
	// are deliberately not included: a model produced by a run that logged
	// into a dataset repository was not trained on that repository.
	Dataset string
	// BaseOnly keeps only the repositories that declare no base model at all
	// -- HuggingFace's "Base only" toggle. It is the complement of BaseModel
	// / Relation, so combining them yields nothing.
	BaseOnly bool
	Sort     string
	Limit    int
	Offset   int
	// WithFacets additionally computes tag/license/task/relation counts for
	// the current filter set. It defaults to off so the frequently-polled
	// HF-compatible list endpoints don't pay for facet aggregation they
	// throw away.
	WithFacets   bool
	IsExperiment *bool
	// Archived filters on the read-only flag: nil (the default) lists
	// archived and active repositories together, since hiding an archived
	// repository would make it look deleted. false narrows to active ones,
	// true to the archive.
	Archived *bool
}

// repoFilterScope controls which dimensions of f are applied to a WHERE
// clause. Facet counts exclude their own dimension, so a repository still
// contributes to (say) the license facet even though it doesn't match the
// license the user currently has selected — that is what lets the sidebar
// show "how many more results if I also picked this one".
//
// BaseModel / Dataset / BaseOnly have no scope flag: they are not facet
// dimensions, so they stay applied everywhere the way Kind and Author do.
// Only Relation is a facet dimension of its own.
type repoFilterScope struct {
	tags     bool
	license  bool
	task     bool
	relation bool
}

var repoFilterScopeAll = repoFilterScope{tags: true, license: true, task: true, relation: true}

// lineageRelationDefault is what an empty relation on a base_model edge means:
// a fine-tune. It is the Hub's own default (repocard.RelationFinetune) and the
// only thing a row indexed before the relation column existed can be read as,
// so filtering and counting both normalise through it -- exactly as the model
// tree does in the UI (docs/dev/api-contract.md §12).
const lineageRelationDefault = "finetune"

// relationExpr is the normalised relation of the joined repo_lineage row `l`.
const relationExpr = `COALESCE(NULLIF(l.relation, ''), '` + lineageRelationDefault + `')`

// neverMatches is the predicate for a filter that cannot be satisfied — a
// "ns/name" reference that does not parse. Dropping the filter instead would
// silently widen the listing to everything, which is the wrong way to fail:
// `?base_model=garbage` must return nothing, not the whole hub.
const neverMatches = "1 = 0"

// splitRepoRef parses a "ns/name" lineage filter, dropping any "@rev" suffix.
// An edge pinned to a revision still belongs to its repository, and lineage
// filters match at repository granularity.
func splitRepoRef(ref string) (ns, name string, ok bool) {
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		ref = ref[:at]
	}
	ns, name, ok = strings.Cut(ref, "/")
	if !ok || ns == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return ns, name, true
}

// lineageEdgeExists renders "repository r declares an edge of edgeKind", with
// the extra conditions appended. The edge kind is a package constant, never
// user input; every value reaches the database as a bind parameter.
func lineageEdgeExists(edgeKind string, conds ...string) string {
	all := append([]string{`l.repo_id = r.id`, `l.edge_kind = '` + edgeKind + `'`}, conds...)
	return `EXISTS (SELECT 1 FROM repo_lineage l WHERE ` + strings.Join(all, " AND ") + `)`
}

// baseModelClause renders the BaseModel / Relation pair. Both constrain the
// same edge: asking for the quantisations of a/b must not also match a model
// that is a quantisation of something else and separately derives from a/b.
func baseModelClause(bind func(any) string, target, relation string) string {
	conds := []string{}
	if target != "" {
		ns, name, ok := splitRepoRef(target)
		if !ok {
			return neverMatches
		}
		// The namespace half folds case, like the lineage queries that read
		// the same card-authored text (see ListLineageDependents). The name
		// half does not: repository names stay case-sensitive.
		conds = append(conds, `LOWER(l.target_namespace) = LOWER(`+bind(ns)+`)`, `l.target_name = `+bind(name))
	}
	if relation != "" {
		conds = append(conds, relationExpr+` = `+bind(relation))
	}
	return lineageEdgeExists(LineageKindBaseModel, conds...)
}

// buildRepoWhere renders f into a WHERE clause (or "" when nothing filters)
// plus its positional args, in the order the placeholders were allocated.
// It touches no I/O, so it is covered directly by repos_test.go rather than
// through a live database.
func buildRepoWhere(d dialect, f RepoFilter, scope repoFilterScope) (string, []any) {
	where := []string{}
	args := []any{}
	// bind appends a value and returns its placeholder, so a clause can
	// reference the same value more than once without any renumbering.
	bind := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if f.Kind != "" {
		where = append(where, `r.kind = `+bind(f.Kind))
	}
	if f.Author != "" {
		// Case-insensitive, like every other namespace lookup: /Alice and
		// /alice are one profile, so the facet behind them has to agree.
		where = append(where, `LOWER(n.name) = LOWER(`+bind(f.Author)+`)`)
	}
	if f.Query != "" {
		// A plain substring, escaped to a literal so a "%" or "_" a user
		// typed narrows the listing instead of widening it (see like.go).
		q := bind(likeContains(f.Query))
		where = append(where, likeAnyOf(q, "r.name", "n.name", "r.description"))
	}
	if f.Search != "" {
		if pred := d.searchPredicate(bind, f.Search); pred != "" {
			where = append(where, pred)
		}
	}
	if scope.tags && len(f.Tags) > 0 {
		where = append(where, d.jsonArrayContainsAll("r.card", "tags", bind, f.Tags))
	}
	if scope.license && f.License != "" {
		where = append(where, `r.card->>'license' = `+bind(f.License))
	}
	if scope.task && f.Task != "" {
		task := bind(f.Task)
		where = append(where, `(r.card->>'pipeline_tag' = `+task+` OR `+d.jsonArrayHas("r.card", "task_categories", task)+`)`)
	}
	// Relation is a facet dimension and drops out of its own facet; BaseModel
	// is not and stays, so the relation facet still answers "of this base
	// model, how many are quantisations?".
	relation := ""
	if scope.relation {
		relation = f.Relation
	}
	if f.BaseModel != "" || relation != "" {
		where = append(where, baseModelClause(bind, f.BaseModel, relation))
	}
	if f.Dataset != "" {
		if ns, name, ok := splitRepoRef(f.Dataset); ok {
			where = append(where, lineageEdgeExists(LineageKindDataset,
				`LOWER(l.target_namespace) = LOWER(`+bind(ns)+`)`, `l.target_name = `+bind(name)))
		} else {
			where = append(where, neverMatches)
		}
	}
	if f.BaseOnly {
		where = append(where, `NOT `+lineageEdgeExists(LineageKindBaseModel))
	}
	if f.IsExperiment != nil {
		where = append(where, `r.is_experiment = `+bind(*f.IsExperiment))
	}
	if f.Archived != nil {
		// Expressed against NULL rather than a boolean bind: archived_at is
		// the flag, and both engines index the partial "IS NOT NULL" form.
		if *f.Archived {
			where = append(where, `r.archived_at IS NOT NULL`)
		} else {
			where = append(where, `r.archived_at IS NULL`)
		}
	}
	if len(where) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// RepoFacetItem is one value of a facet (a tag, a license, a task) together
// with how many repositories in the current result set carry it.
type RepoFacetItem struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// RepoFacets aggregates the filterable card dimensions the listing sidebar
// offers, computed under the filters currently in effect (see
// repoFilterScope).
type RepoFacets struct {
	Tags     []RepoFacetItem `json:"tags"`
	Licenses []RepoFacetItem `json:"licenses"`
	Tasks    []RepoFacetItem `json:"tasks"`
	// Relations counts the base_model relations present in the result set,
	// so the sidebar can offer "quantized (12)" next to a selected base
	// model. Repositories with no base model at all contribute to no bucket
	// -- "base only" is the BaseOnly filter, not a relation.
	Relations []RepoFacetItem `json:"relations"`
}

const facetLimit = 40

// The listing page size, as docs/dev/api-contract.md documents it
// ("limit (default 30, max 100)").
const (
	defaultRepoPageSize = 30
	maxRepoPageSize     = 100
)

// repoOrderBy renders the ORDER BY for a listing sort. Every sort ends in
// r.id so that the ordering is *total*. Without a unique last key, LIMIT /
// OFFSET may cut anywhere inside a run of tied rows, and neither engine
// promises to order that run the same way twice: a repository would then show
// up on two consecutive pages, or on none at all. Ties are the norm here, not
// an edge case -- total_size is 0 until a push is indexed, a bulk import
// shares one updated_at (SQLite keeps milliseconds), and (n.name, r.name) is
// unique only per kind, so a model and a dataset of one name tie in a listing
// that does not filter by kind.
func repoOrderBy(sort string) string {
	switch sort {
	case "created":
		return "r.created_at DESC, r.id DESC"
	case "downloads":
		return "r.downloads DESC, r.updated_at DESC, r.id DESC"
	case "name":
		return "n.name ASC, r.name ASC, r.id ASC"
	case "size":
		return "r.total_size DESC, r.id DESC"
	default:
		return "r.updated_at DESC, r.id DESC"
	}
}

// RepoRef is the minimal identity of one repository, for operational commands
// that walk every repository (wal-seed / wal-verify / compact) without paying
// for the full row or the list-endpoint's visibility filtering.
type RepoRef struct {
	Kind        string
	Namespace   string
	Name        string
	StoragePath string
}

// AllRepoRefs returns every repository's identity: the callers are
// server-side operations, not a listing surface.
func (s *Store) AllRepoRefs(ctx context.Context) ([]RepoRef, error) {
	rows, err := s.db.Query(ctx,
		`SELECT r.kind, n.name, r.name, r.storage_path
		   FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
		  ORDER BY n.name, r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoRef
	for rows.Next() {
		var ref RepoRef
		if err := rows.Scan(&ref.Kind, &ref.Namespace, &ref.Name, &ref.StoragePath); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func (s *Store) ListRepos(ctx context.Context, f RepoFilter) ([]Repo, int64, RepoFacets, error) {
	clause, args := buildRepoWhere(s.d, f, repoFilterScopeAll)

	var total int64
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM repositories r JOIN namespaces n ON n.id = r.namespace_id`+clause,
		args...).Scan(&total); err != nil {
		return nil, 0, RepoFacets{}, fmt.Errorf("count repositories: %w", err)
	}

	order := repoOrderBy(f.Sort)
	limit, offset := pageWindow(f.Limit, f.Offset, defaultRepoPageSize, maxRepoPageSize)

	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(ctx,
		`SELECT `+repoColumns+` FROM repositories r JOIN namespaces n ON n.id = r.namespace_id`+clause+
			` ORDER BY `+order+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(listArgs)-1, len(listArgs)), listArgs...)
	if err != nil {
		return nil, 0, RepoFacets{}, fmt.Errorf("list repositories: %w", err)
	}
	defer rows.Close()

	out := []Repo{}
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, 0, RepoFacets{}, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, RepoFacets{}, err
	}

	if !f.WithFacets {
		return out, total, RepoFacets{}, nil
	}
	facets, err := s.repoFacets(ctx, f)
	if err != nil {
		return nil, 0, RepoFacets{}, err
	}
	return out, total, facets, nil
}

func (s *Store) repoFacets(ctx context.Context, f RepoFilter) (RepoFacets, error) {
	var facets RepoFacets
	var err error
	if facets.Tags, err = s.tagFacet(ctx, f); err != nil {
		return facets, fmt.Errorf("tag facet: %w", err)
	}
	if facets.Licenses, err = s.licenseFacet(ctx, f); err != nil {
		return facets, fmt.Errorf("license facet: %w", err)
	}
	if facets.Tasks, err = s.taskFacet(ctx, f); err != nil {
		return facets, fmt.Errorf("task facet: %w", err)
	}
	if facets.Relations, err = s.relationFacet(ctx, f); err != nil {
		return facets, fmt.Errorf("relation facet: %w", err)
	}
	return facets, nil
}

// tagFacet counts repositories, not tag occurrences: the join over the card's
// `tags` array fans one repository out into one row per element, so a card
// that repeats a tag would otherwise be counted twice for it.
func (s *Store) tagFacet(ctx context.Context, f RepoFilter) ([]RepoFacetItem, error) {
	clause, args := buildRepoWhere(s.d, f, repoFilterScope{license: true, task: true})
	from, elem := s.d.jsonArrayElements("r.card", "tags")
	query := `SELECT ` + elem + ` AS value, count(DISTINCT r.id) AS repos FROM repositories r
		JOIN namespaces n ON n.id = r.namespace_id
		` + from + clause + `
		GROUP BY value ORDER BY repos DESC, value ASC LIMIT ` + strconv.Itoa(facetLimit)
	return s.queryFacet(ctx, query, args)
}

func (s *Store) licenseFacet(ctx context.Context, f RepoFilter) ([]RepoFacetItem, error) {
	clause, args := buildRepoWhere(s.d, f, repoFilterScope{tags: true, task: true})
	licenseClause := andClause(clause, `r.card->>'license' IS NOT NULL AND r.card->>'license' <> ''`)
	query := `SELECT r.card->>'license' AS value, count(*) FROM repositories r
		JOIN namespaces n ON n.id = r.namespace_id` + licenseClause + `
		GROUP BY value ORDER BY count(*) DESC, value ASC LIMIT ` + strconv.Itoa(facetLimit)
	return s.queryFacet(ctx, query, args)
}

// taskFacet counts both places a task name can live: a model's single
// `pipeline_tag` and a dataset's `task_categories` list. The same clause
// (and therefore the same bound args) is used in both halves of the UNION,
// which both engines allow since a placeholder like $1 may appear more than
// once.
//
// The halves overlap -- a card may declare the same task in both keys, and a
// task_categories list may repeat one -- so the repository id is carried
// through and counted distinctly. UNION ALL only removes the cost of
// deduplicating whole rows; it is count(DISTINCT id) that makes the number
// "repositories with this task" rather than "mentions of this task".
func (s *Store) taskFacet(ctx context.Context, f RepoFilter) ([]RepoFacetItem, error) {
	clause, args := buildRepoWhere(s.d, f, repoFilterScope{tags: true, license: true})
	pipelineClause := andClause(clause, `r.card->>'pipeline_tag' IS NOT NULL AND r.card->>'pipeline_tag' <> ''`)
	from, elem := s.d.jsonArrayElements("r.card", "task_categories")
	query := `SELECT value, count(DISTINCT repo_id) AS repos FROM (
			SELECT r.id AS repo_id, r.card->>'pipeline_tag' AS value FROM repositories r
			JOIN namespaces n ON n.id = r.namespace_id` + pipelineClause + `
			UNION ALL
			SELECT r.id AS repo_id, ` + elem + ` AS value FROM repositories r
			JOIN namespaces n ON n.id = r.namespace_id
			` + from + clause + `
		) t GROUP BY value ORDER BY repos DESC, value ASC LIMIT ` + strconv.Itoa(facetLimit)
	return s.queryFacet(ctx, query, args)
}

// relationFacet counts how the repositories in the result set relate to their
// base model. Unlike the card facets it aggregates over a joined table, so it
// counts distinct repositories: a merge naming three base models is one merge,
// not three.
func (s *Store) relationFacet(ctx context.Context, f RepoFilter) ([]RepoFacetItem, error) {
	clause, args := buildRepoWhere(s.d, f, repoFilterScope{tags: true, license: true, task: true})
	bind := binder(&args)
	// With a base model selected, only the edges pointing at *it* may be
	// counted. baseModelClause constrains BaseModel and Relation to the same
	// edge, and the EXISTS it renders binds its own `l`, so an unrestricted
	// join here would count a repository's other base models too: clicking
	// "finetune (1)" would then return nothing, because the one repository in
	// that bucket is a finetune of something else entirely.
	edge := `JOIN repo_lineage l ON l.repo_id = r.id AND l.edge_kind = '` + LineageKindBaseModel + `'`
	if ns, name, ok := splitRepoRef(f.BaseModel); ok {
		edge += ` AND LOWER(l.target_namespace) = LOWER(` + bind(ns) + `) AND l.target_name = ` + bind(name)
	}
	query := `SELECT ` + relationExpr + ` AS value, count(DISTINCT r.id) AS repos FROM repositories r
		JOIN namespaces n ON n.id = r.namespace_id
		` + edge + clause + `
		GROUP BY value ORDER BY repos DESC, value ASC LIMIT ` + strconv.Itoa(facetLimit)
	return s.queryFacet(ctx, query, args)
}

// andClause appends an extra, always-true-shaped condition to a WHERE clause
// built by buildRepoWhere. The clause is empty whenever no filter is active,
// which is why the fallback opens the WHERE itself.
func andClause(clause, extra string) string {
	if clause == "" {
		return " WHERE " + extra
	}
	return clause + " AND " + extra
}

func (s *Store) queryFacet(ctx context.Context, query string, args []any) ([]RepoFacetItem, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []RepoFacetItem{}
	for rows.Next() {
		var item RepoFacetItem
		if err := rows.Scan(&item.Value, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// UpdateRepoIndex records what the sync worker learned from the latest commit.
//
// description is the README card's, and an empty one means the card said
// nothing rather than "the description is empty": the column is left alone in
// that case, so a description typed into the repository settings
// (SetRepoDescription) survives every later push of a README without a
// `description:` key. A card that does carry one still wins -- it is the
// source of truth whenever it speaks, which is what makes a pushed README
// enough to describe a repository.
//
// The CASE is written against the same placeholder twice, which both drivers
// bind identically: pgx sends $5 once for two references, and SQLite treats
// the repeated $5 as one named parameter sharing an index, so the six
// arguments still line up (see TestUpdateRepoIndexKeepsManualDescription).
func (s *Store) UpdateRepoIndex(ctx context.Context, repoID int64, headSHA string, totalSize int64, card map[string]any, description string, isExperiment bool) error {
	cardRaw, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal card: %w", err)
	}
	_, err = s.db.Exec(ctx,
		`UPDATE repositories
		 SET head_sha = $2, total_size = $3, card = $4,
		     description = CASE WHEN $5 = '' THEN description ELSE $5 END,
		     is_experiment = $6, updated_at = now()
		 WHERE id = $1`,
		repoID, headSHA, totalSize, cardRaw, description, isExperiment)
	return err
}

// SetRepoDescription writes the one-line description shown in listings and on
// the repository page, and returns the row as it now stands. It is the
// settings form's field, not the indexer's: UpdateRepoIndex overwrites it on
// the next push only when the README card carries a `description` of its own.
//
// updated_at is bumped for the same reason SetRepoDefaultBranch bumps it --
// this changes what a plain visit to the repository shows.
func (s *Store) SetRepoDescription(ctx context.Context, repoID int64, description string) (*Repo, error) {
	if _, err := s.db.Exec(ctx,
		`UPDATE repositories SET description = $2, updated_at = now() WHERE id = $1`,
		repoID, description); err != nil {
		return nil, err
	}
	return s.GetRepoByID(ctx, repoID)
}

func (s *Store) SetRepoHead(ctx context.Context, repoID int64, headSHA string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE repositories SET head_sha = $2, updated_at = now() WHERE id = $1`, repoID, headSHA)
	return err
}

// SetRepoDefaultBranch switches which branch clone, tree listings, the
// repository card, lineage and the parquet index treat as the repository's
// default, and returns the row as it now stands. updated_at is bumped --
// unlike SetRepoArchived's bookkeeping columns, this changes what a plain
// visit to the repository shows (README, tags, license, file list all follow
// the new branch), so it belongs in "recently updated" like an ordinary push
// does.
//
// This only flips the column; it does not touch the bare repository's HEAD
// symref (gitrepo.Repo.SetHead, called by the caller in the same request) or
// re-run the post-push indexers for the newly-default ref (the caller
// enqueues a sync job for that -- see docs/dev/api-contract.md "Changing the
// default branch").
func (s *Store) SetRepoDefaultBranch(ctx context.Context, repoID int64, branch string) (*Repo, error) {
	if _, err := s.db.Exec(ctx,
		`UPDATE repositories SET default_branch = $2, updated_at = now() WHERE id = $1`,
		repoID, branch); err != nil {
		return nil, err
	}
	return s.GetRepoByID(ctx, repoID)
}

// IncrementDownloads advances a repository's all-time download counter. It is
// called from the same detached goroutine as RecordDownload (see
// api.Server.recordDownload) and for the same request, so the two counters
// move together.
//
// Best effort: a lost download count never justifies failing a download, so
// the error is swallowed rather than returned -- but it is logged, the way
// RecordDownload logs, so a counter that has silently stopped moving is
// visible somewhere.
func (s *Store) IncrementDownloads(ctx context.Context, repoID int64) {
	_, err := s.db.Exec(ctx, `UPDATE repositories SET downloads = downloads + 1 WHERE id = $1`, repoID)
	if err != nil {
		slog.Error("increment downloads", "repo_id", repoID, "error", err)
	}
}

type Stats struct {
	Datasets    int64 `json:"datasets"`
	Models      int64 `json:"models"`
	Experiments int64 `json:"experiments"`
	TotalSize   int64 `json:"total_size"`
}

func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	st := &Stats{}
	err := s.db.QueryRow(ctx, `SELECT
		   count(*) FILTER (WHERE r.kind = 'dataset'),
		   count(*) FILTER (WHERE r.kind = 'model'),
		   count(*) FILTER (WHERE r.is_experiment),
		   COALESCE(sum(r.total_size), 0)
		 FROM repositories r`).Scan(&st.Datasets, &st.Models, &st.Experiments, &st.TotalSize)
	return st, err
}
