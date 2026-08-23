package store

import "context"

// NamespaceUsage aggregates one namespace's storage footprint: the actual
// bytes kept in GCS (LFS objects the namespace's repositories reference),
// how many files are indexed across those repositories, and how many
// repositories it holds.
type NamespaceUsage struct {
	Namespace string
	LFSSize   int64
	NumFiles  int64
	NumRepos  int64
}

// RepoUsage is one repository's contribution to storage usage.
type RepoUsage struct {
	RepoID    int64
	Namespace string
	Name      string
	Kind      string
	LFSSize   int64
	NumFiles  int64
}

// UsageByRepo returns per-repository storage usage for every repository in
// the given namespaces, sorted by LFS size descending. An empty namespace
// list returns no rows -- callers must pass the caller's visible namespaces
// explicitly rather than relying on this to mean "everything".
//
// LFSSize is the sum of repo_lfs_objects joined against lfs_objects.size:
// the actual GCS-billed bytes a repository is responsible for. Plain git
// blobs (README.md, small text files, ...) never leave the git repository,
// so they are not counted here.
func (s *Store) UsageByRepo(ctx context.Context, namespaces []string) ([]RepoUsage, error) {
	if len(namespaces) == 0 {
		return []RepoUsage{}, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT r.id, n.name, r.name, r.kind,
		       COALESCE((SELECT SUM(lo.size) FROM repo_lfs_objects rlo
		                 JOIN lfs_objects lo ON lo.oid = rlo.oid
		                 WHERE rlo.repo_id = r.id), 0) AS lfs_size,
		       (SELECT count(*) FROM repo_files f
		        WHERE f.repo_id = r.id AND f.ref = r.default_branch) AS num_files
		FROM repositories r
		JOIN namespaces n ON n.id = r.namespace_id
		WHERE n.name `+s.d.inArray("$1")+`
		ORDER BY lfs_size DESC, n.name, r.name`, s.d.stringArrayArg(namespaces))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RepoUsage{}
	for rows.Next() {
		var u RepoUsage
		if err := rows.Scan(&u.RepoID, &u.Namespace, &u.Name, &u.Kind, &u.LFSSize, &u.NumFiles); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AggregateUsageByNamespace sums per-repository usage rows into one row per
// namespace, preserving the order namespaces first appear in repos. It is a
// pure function so the aggregation can be unit tested without a database.
func AggregateUsageByNamespace(repos []RepoUsage) []NamespaceUsage {
	order := make([]string, 0)
	byNS := map[string]*NamespaceUsage{}
	for _, r := range repos {
		agg, ok := byNS[r.Namespace]
		if !ok {
			agg = &NamespaceUsage{Namespace: r.Namespace}
			byNS[r.Namespace] = agg
			order = append(order, r.Namespace)
		}
		agg.LFSSize += r.LFSSize
		agg.NumFiles += r.NumFiles
		agg.NumRepos++
	}
	out := make([]NamespaceUsage, 0, len(order))
	for _, ns := range order {
		out = append(out, *byNS[ns])
	}
	return out
}
