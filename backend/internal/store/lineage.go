package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Lineage edge kinds, matching the CHECK constraint on repo_lineage.edge_kind.
const (
	LineageKindDataset   = "dataset"
	LineageKindBaseModel = "base_model"
	LineageKindRun       = "run"
	// LineageKindEvalDataset is a dataset the repository was *evaluated* on
	// rather than trained from (a model card's `model-index:`).
	LineageKindEvalDataset = "eval_dataset"
	// LineageKindNewVersion points forward in time: the repository that
	// supersedes this one. Unlike every other kind it targets a repository of
	// the same kind as its source, so a dataset may have a successor too.
	LineageKindNewVersion = "new_version"
)

// maxLineageDependents bounds a reverse lookup. Nothing stops a hundred models
// from naming the same base model, but a repository page only ever shows a
// handful, and an unbounded listing would be a denial-of-service surface.
const maxLineageDependents = 100

// LineageEdge is one provenance reference declared by a repository card.
// Raw is what the card said; the target fields are the normalised form and are
// empty when the reference does not parse (which leaves the edge dangling).
type LineageEdge struct {
	Kind      string `json:"kind"`
	Raw       string `json:"raw"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Rev       string `json:"rev"`
	// Project and Run are set for run edges only.
	Project string `json:"project"`
	Run     string `json:"run"`
	// Relation is how this repository relates to the base model it points at
	// (HuggingFace's `base_model_relation`: finetune / adapter / quantized /
	// merge, or whatever else the card declared). Set on base_model edges
	// only; "" everywhere else.
	Relation string `json:"relation"`
	Ordinal  int    `json:"ordinal"`
}

// TargetKind is the repository kind this edge points at, given the kind of
// the repository that declared it. Dataset, eval_dataset and run edges all
// target dataset repositories -- experiment logs live in one -- and a
// new_version edge targets the same kind it came from, because a successor of
// a model is a model and a successor of a dataset is a dataset.
func (e LineageEdge) TargetKind(sourceKind string) string {
	switch e.Kind {
	case LineageKindBaseModel:
		return "model"
	case LineageKindNewVersion:
		return sourceKind
	default:
		return "dataset"
	}
}

// LineageUpstream is an outgoing edge plus whether its target resolves to a
// repository the viewer may see.
type LineageUpstream struct {
	LineageEdge
	Exists bool `json:"exists"`
}

// LineageDependent is an incoming edge: a repository that named this one as
// part of its own origin.
type LineageDependent struct {
	Repo Repo        `json:"repo"`
	Edge LineageEdge `json:"edge"`
}

// ReplaceRepoLineage swaps the whole edge set for one repository. The sync
// worker rebuilds it from the card on every default-branch push, so a full
// replace is both the simplest and the only always-correct update.
func (s *Store) ReplaceRepoLineage(ctx context.Context, repoID int64, edges []LineageEdge) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Parent row first, same as ReplaceRepoFiles (see lockRepoRow).
	if err := s.lockRepoRow(ctx, tx, repoID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_lineage WHERE repo_id = $1`, repoID); err != nil {
		return fmt.Errorf("clear repo_lineage: %w", err)
	}
	for _, e := range edges {
		// Every text field here is card text, so it carries whatever bytes
		// the README had; PostgreSQL refuses the ones that are not UTF-8 and
		// the refusal parks the sync job (see text.go). Sanitised before the
		// insert, and consistently across the row: raw is part of the primary
		// key, so normalising it and not the rest would split one edge in two
		// on a re-index.
		e.Raw = sanitizeText(e.Raw)
		e.Namespace = sanitizeText(e.Namespace)
		e.Name = sanitizeText(e.Name)
		e.Rev = sanitizeText(e.Rev)
		e.Project = sanitizeText(e.Project)
		e.Run = sanitizeText(e.Run)
		e.Relation = sanitizeText(e.Relation)
		// ON CONFLICT rather than a plain insert: (repo_id, edge_kind, raw) is
		// the primary key and a card may well repeat a reference across the
		// singular and plural spellings of the same key.
		if _, err := tx.Exec(ctx,
			`INSERT INTO repo_lineage (repo_id, edge_kind, raw, target_namespace, target_name,
			                           target_rev, target_project, target_run, relation, ordinal, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
			 ON CONFLICT (repo_id, edge_kind, raw) DO UPDATE SET
			   target_namespace = EXCLUDED.target_namespace,
			   target_name      = EXCLUDED.target_name,
			   target_rev       = EXCLUDED.target_rev,
			   target_project   = EXCLUDED.target_project,
			   target_run       = EXCLUDED.target_run,
			   relation         = EXCLUDED.relation,
			   ordinal          = EXCLUDED.ordinal,
			   updated_at       = now()`,
			repoID, e.Kind, e.Raw, e.Namespace, e.Name, e.Rev, e.Project, e.Run, e.Relation, e.Ordinal); err != nil {
			return fmt.Errorf("insert repo_lineage: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ListRepoLineage returns the repository's outgoing edges. An edge whose
// target does not exist is reported as dangling. target_namespace is
// user-typed text from a repo card, so it is matched against the canonical
// namespace name case-insensitively, same as any other namespace lookup
// (see GetNamespace).
func (s *Store) ListRepoLineage(ctx context.Context, repoID int64) ([]LineageUpstream, error) {
	rows, err := s.db.Query(ctx,
		`SELECT l.edge_kind, l.raw, l.target_namespace, l.target_name, l.target_rev,
		        l.target_project, l.target_run, l.relation, l.ordinal,
		        EXISTS (
		          SELECT 1 FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
		          WHERE LOWER(n.name) = LOWER(l.target_namespace) AND r.name = l.target_name
		            AND r.kind = CASE l.edge_kind
		                           WHEN 'base_model' THEN 'model'
		                           WHEN 'new_version' THEN (SELECT src.kind FROM repositories src WHERE src.id = l.repo_id)
		                           ELSE 'dataset' END
		        )
		 FROM repo_lineage l
		 WHERE l.repo_id = $1
		 ORDER BY l.edge_kind, l.ordinal, l.raw`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list repo lineage: %w", err)
	}
	defer rows.Close()

	out := []LineageUpstream{}
	for rows.Next() {
		var u LineageUpstream
		if err := rows.Scan(&u.Kind, &u.Raw, &u.Namespace, &u.Name, &u.Rev,
			&u.Project, &u.Run, &u.Relation, &u.Ordinal, &u.Exists); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListLineageDependents returns the repositories whose card points at ns/name
// through one of edgeKinds -- the reverse of ListRepoLineage.
func (s *Store) ListLineageDependents(ctx context.Context, edgeKinds []string, ns, name string) ([]LineageDependent, error) {
	if len(edgeKinds) == 0 || ns == "" || name == "" {
		return []LineageDependent{}, nil
	}
	args := []any{s.d.stringArrayArg(edgeKinds), ns, name}
	return s.queryDependents(ctx,
		`l.edge_kind `+s.d.inArray("$1")+` AND LOWER(l.target_namespace) = LOWER($2) AND l.target_name = $3`, args)
}

// ListRunDependents returns the repositories produced by runs of one experiment
// project. An empty run matches every run in the project.
func (s *Store) ListRunDependents(ctx context.Context, ns, repoName, project, run string) ([]LineageDependent, error) {
	if ns == "" || repoName == "" || project == "" {
		return []LineageDependent{}, nil
	}
	args := []any{ns, repoName, project, run}
	return s.queryDependents(ctx,
		`l.edge_kind = 'run' AND LOWER(l.target_namespace) = LOWER($1) AND l.target_name = $2
		   AND l.target_project = $3 AND ($4 = '' OR l.target_run = $4)`, args)
}

// ListNewVersionPredecessors returns the repositories superseded by ns/name:
// the ones whose card names it in `new_version:`.
//
// It is the reverse of the successor edge and cannot go through
// ListLineageDependents, because a new_version edge targets a repository of
// its own kind. Without the `r.kind` filter, a model declaring a successor
// would surface as a predecessor of an unrelated dataset that happens to share
// the same namespace and name.
func (s *Store) ListNewVersionPredecessors(ctx context.Context, kind, ns, name string) ([]LineageDependent, error) {
	if kind == "" || ns == "" || name == "" {
		return []LineageDependent{}, nil
	}
	args := []any{kind, ns, name}
	return s.queryDependents(ctx,
		`l.edge_kind = 'new_version' AND r.kind = $1
		   AND LOWER(l.target_namespace) = LOWER($2) AND l.target_name = $3`, args)
}

// NewVersionRef names one repository on a successor chain. The kind is fixed
// for the whole chain (a successor has the kind of what it supersedes), so it
// is carried by the caller rather than repeated on every hop.
type NewVersionRef struct {
	Namespace string
	Name      string
}

// NewVersionLookup answers "what supersedes this repository?" for one hop. It
// reports false when the repository declares no successor, or when the
// successor it declares does not resolve to a repository the viewer may read
// -- a dangling successor ends the chain rather than breaking it.
type NewVersionLookup func(NewVersionRef) (NewVersionRef, bool, error)

// MaxNewVersionChainDepth bounds how far a `new_version:` chain is followed.
// Nothing stops a set of cards from forming a cycle, and even an acyclic chain
// is one database round trip per hop, so the walk is capped rather than run to
// completion.
const MaxNewVersionChainDepth = 8

// NewVersionChain is where a repository's successor chain leads.
type NewVersionChain struct {
	// Direct is the successor the card names outright.
	Direct NewVersionRef
	// Latest is the end of the chain: the newest version, which is what a
	// repository page points its reader at. It equals Direct when the chain
	// is one hop long, and also when Truncated is set.
	Latest NewVersionRef
	// Hops is how many edges were followed to reach Latest, 0 when the
	// repository has no resolvable successor at all.
	Hops int
	// Truncated reports that the chain did not end: it either formed a cycle
	// or ran past MaxNewVersionChainDepth. Latest is then the direct successor
	// only, and the UI says so rather than claiming to show the newest
	// version.
	Truncated bool
}

// ResolveNewVersionChain follows `new_version:` edges from origin and reports
// the repository the chain ends at.
//
// It is a pure function of origin and the lookup it is handed: every database
// access is behind next, so the walk's interesting behaviour -- a cycle, a
// self-reference, a chain longer than the cap -- is testable without one.
//
// A repository naming itself declares nothing, so an immediate self-reference
// yields the zero chain rather than a truncated one: there is no successor to
// warn about. A cycle further along is a real declaration that happens not to
// terminate, so it falls back to the direct successor with Truncated set.
func ResolveNewVersionChain(origin NewVersionRef, next NewVersionLookup) (NewVersionChain, error) {
	visited := map[NewVersionRef]bool{visitKey(origin): true}
	var chain NewVersionChain
	cur := origin
	for {
		hop, ok, err := next(cur)
		if err != nil {
			return NewVersionChain{}, err
		}
		if !ok {
			return chain, nil
		}
		if visited[visitKey(hop)] {
			if chain.Hops == 0 {
				return NewVersionChain{}, nil
			}
			return truncatedChain(chain), nil
		}
		if chain.Hops == MaxNewVersionChainDepth {
			return truncatedChain(chain), nil
		}
		if chain.Hops == 0 {
			chain.Direct = hop
		}
		visited[visitKey(hop)] = true
		chain.Latest = hop
		chain.Hops++
		cur = hop
	}
}

// visitKey normalises a ref for cycle detection. The hops come from card text
// resolved case-insensitively (NewVersionSuccessor), so `Alice/foo` and
// `alice/foo` are the same repository and have to count as the same visit --
// otherwise a cycle spelled with different casing is only caught by the depth
// cap. Repository names stay case-sensitive, matching the queries.
func visitKey(ref NewVersionRef) NewVersionRef {
	return NewVersionRef{Namespace: strings.ToLower(ref.Namespace), Name: ref.Name}
}

// truncatedChain collapses a chain that does not terminate down to its direct
// successor, which is the one hop that is certainly true.
func truncatedChain(chain NewVersionChain) NewVersionChain {
	return NewVersionChain{Direct: chain.Direct, Latest: chain.Direct, Hops: 1, Truncated: true}
}

// NewVersionSuccessor is the NewVersionLookup backed by the lineage index: one
// hop from (kind, ns, name) to the repository its card names in
// `new_version:`, reported only when that repository exists.
//
// The card may name several successors; the first one it wrote wins, which is
// the same ordering every other lineage list is shown in.
func (s *Store) NewVersionSuccessor(ctx context.Context, kind string) NewVersionLookup {
	return func(from NewVersionRef) (NewVersionRef, bool, error) {
		args := []any{kind, from.Namespace, from.Name}
		var to NewVersionRef
		err := s.db.QueryRow(ctx,
			`SELECT l.target_namespace, l.target_name
			 FROM repo_lineage l
			 JOIN repositories src ON src.id = l.repo_id
			 JOIN namespaces srcns ON srcns.id = src.namespace_id
			 WHERE l.edge_kind = 'new_version'
			   AND src.kind = $1 AND LOWER(srcns.name) = LOWER($2) AND src.name = $3
			   AND EXISTS (
			     SELECT 1 FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
			     WHERE LOWER(n.name) = LOWER(l.target_namespace) AND r.name = l.target_name
			       AND r.kind = $1
			   )
			 ORDER BY l.ordinal, l.raw
			 LIMIT 1`, args...).Scan(&to.Namespace, &to.Name)
		if err != nil {
			if isNoRows(err) {
				return NewVersionRef{}, false, nil
			}
			return NewVersionRef{}, false, fmt.Errorf("new version successor: %w", err)
		}
		return to, true, nil
	}
}

// queryDependents runs the shared dependents query under the given predicate.
// The predicate is built from literals in this file only; every value reaches
// the database as a bind parameter.
func (s *Store) queryDependents(ctx context.Context, where string, args []any) ([]LineageDependent, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+repoColumns+`, l.edge_kind, l.raw, l.target_rev, l.target_project, l.target_run,
		        l.relation, l.ordinal
		 FROM repo_lineage l
		 JOIN repositories r ON r.id = l.repo_id
		 JOIN namespaces n ON n.id = r.namespace_id
		 WHERE `+where+`
		 ORDER BY r.updated_at DESC, r.name, r.id, l.edge_kind, l.raw
		 LIMIT `+strconv.Itoa(maxLineageDependents), args...)
	if err != nil {
		return nil, fmt.Errorf("list lineage dependents: %w", err)
	}
	defer rows.Close()

	out := []LineageDependent{}
	for rows.Next() {
		// scanRepo cannot be reused here: the row carries the edge columns too,
		// and a row is handed to exactly one Scan call.
		var d LineageDependent
		repo, err := scanRepoWith(rows, &d.Edge.Kind, &d.Edge.Raw, &d.Edge.Rev,
			&d.Edge.Project, &d.Edge.Run, &d.Edge.Relation, &d.Edge.Ordinal)
		if err != nil {
			return nil, err
		}
		// Edge.Namespace / Edge.Name stay empty: the edge's target is the
		// repository the caller asked about, which it already knows.
		d.Repo = *repo
		out = append(out, d)
	}
	return out, rows.Err()
}
