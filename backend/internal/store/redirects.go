package store

import (
	"context"
	"time"
)

// RepoRedirect is one former (kind, namespace, name) a repository used to be
// reachable at, left behind by a transfer or rename
// (docs/dev/repo-transfer-design.md §5, §9). A lookup that misses in
// repositories falls back here before answering 404.
type RepoRedirect struct {
	Kind          string
	FromNamespace string
	FromName      string
	RepoID        int64
	CreatedAt     time.Time
}

// ResolveRepoRedirect returns the repository that (kind, ns, name) used to
// name, so a handler can answer with a redirect to the current name instead
// of 404. ErrNotFound when no redirect exists for that name.
//
// The namespace half folds case and the name half does not, which is exactly
// how GetRepo reaches a live repository. Matching exactly here would make a
// transfer change what /Alice/foo means: the URL resolved before the move and
// would 404 after it. Backed by idx_repo_redirects_from_lower
// (migrations 0029 / 0023), so the fold costs no scan.
func (s *Store) ResolveRepoRedirect(ctx context.Context, kind, ns, name string) (*Repo, error) {
	var repoID int64
	err := s.db.QueryRow(ctx,
		// Namespace names are unique case-insensitively, so at most one live
		// namespace folds to ns; two rows can still collide only if a
		// namespace was deleted and recreated with different casing, and the
		// newest redirect is the one that describes the latest move.
		`SELECT repo_id FROM repo_redirects
		 WHERE kind = $1 AND LOWER(from_namespace) = LOWER($2) AND from_name = $3
		 ORDER BY created_at DESC, from_namespace LIMIT 1`,
		kind, ns, name).Scan(&repoID)
	if err != nil {
		return nil, norm(err)
	}
	return s.GetRepoByID(ctx, repoID)
}

// ListRepoRedirects returns every former name that now points at repoID,
// newest first -- the operational `repo-info` command uses it
// (docs/dev/repo-transfer-design.md §11).
func (s *Store) ListRepoRedirects(ctx context.Context, repoID int64) ([]RepoRedirect, error) {
	rows, err := s.db.Query(ctx,
		`SELECT kind, from_namespace, from_name, repo_id, created_at
		 FROM repo_redirects WHERE repo_id = $1
		 ORDER BY created_at DESC, kind, from_namespace, from_name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RepoRedirect{}
	for rows.Next() {
		var r RepoRedirect
		if err := rows.Scan(&r.Kind, &r.FromNamespace, &r.FromName, &r.RepoID, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRepoRedirect removes a redirect. It is not an error to call this
// when no redirect matches. The name is matched the way
// ResolveRepoRedirect matches it, so anything reachable can also be removed.
func (s *Store) DeleteRepoRedirect(ctx context.Context, kind, ns, name string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM repo_redirects WHERE kind = $1 AND LOWER(from_namespace) = LOWER($2) AND from_name = $3`,
		kind, ns, name)
	return err
}
