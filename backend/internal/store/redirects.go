package store

import (
	"context"
	"time"
)

// RepoRedirect is one former (kind, namespace, name) a repository used to be
// reachable at, left behind by a transfer or rename
// (docs/repo-transfer-design.md §5, §9). A lookup that misses in
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
func (s *Store) ResolveRepoRedirect(ctx context.Context, kind, ns, name string) (*Repo, error) {
	var repoID int64
	err := s.db.QueryRow(ctx,
		`SELECT repo_id FROM repo_redirects WHERE kind = $1 AND from_namespace = $2 AND from_name = $3`,
		kind, ns, name).Scan(&repoID)
	if err != nil {
		return nil, norm(err)
	}
	return s.GetRepoByID(ctx, repoID)
}

// ListRepoRedirects returns every former name that now points at repoID,
// newest first -- the operational `repo-info` command uses it
// (docs/repo-transfer-design.md §11).
func (s *Store) ListRepoRedirects(ctx context.Context, repoID int64) ([]RepoRedirect, error) {
	rows, err := s.db.Query(ctx,
		`SELECT kind, from_namespace, from_name, repo_id, created_at
		 FROM repo_redirects WHERE repo_id = $1 ORDER BY created_at DESC`, repoID)
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
// when no redirect matches.
func (s *Store) DeleteRepoRedirect(ctx context.Context, kind, ns, name string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM repo_redirects WHERE kind = $1 AND from_namespace = $2 AND from_name = $3`,
		kind, ns, name)
	return err
}
