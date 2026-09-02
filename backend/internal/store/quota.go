// Storage quotas: what a namespace is allowed to keep in object storage, and
// how much of it is already spent (namespaces.storage_quota_bytes).
//
// The allowance is namespaces.storage_quota_bytes, NULL for a namespace with
// no override of its own. Resolving NULL against the instance-wide default is
// deliberately *not* done here -- the default is configuration
// (TF_DEFAULT_STORAGE_QUOTA_BYTES), the store holds data, and folding one
// into the other would leave no way to tell an explicit quota of zero from a
// namespace that simply inherits. EffectiveQuota below is the one place the
// two are combined, and every caller goes through it.
//
// The usage half reuses UsageByRepo / AggregateUsageByNamespace (usage.go)
// rather than growing a second SUM over repo_lfs_objects: the number a quota
// is checked against and the number the usage dashboard displays must be the
// same number, and the cheapest way to guarantee that is for there to be only
// one query producing it.

package store

import "context"

const (
	defaultNamespacePageSize = 50
	maxNamespacePageSize     = 200
)

// NamespaceQuota is one namespace's allowance next to what it is using.
type NamespaceQuota struct {
	NamespaceID int64
	Namespace   string
	// Kind is "user" or "org". Quotas apply to both: an organisation is a
	// namespace like any other, and it is the one that usually holds the
	// large datasets.
	Kind string
	// QuotaBytes is the namespace's own override. Nil means it has none and
	// the instance default applies; a non-nil zero is a real quota of zero
	// bytes. The two are different states and nothing may collapse them.
	QuotaBytes *int64
	// UsedBytes is the LFS byte count its repositories are responsible for,
	// the same figure GET /api/v1/usage reports.
	UsedBytes int64
	NumRepos  int64
}

// EffectiveQuota resolves a namespace's override against the instance-wide
// default: the override when it has one (zero included), otherwise the
// default, and nil for "unlimited".
//
// defaultBytes is TF_DEFAULT_STORAGE_QUOTA_BYTES, where 0 means unlimited --
// an instance that never configures quotas must not start refusing uploads.
// That asymmetry (0 is unlimited for the default, zero bytes for an override)
// is why this is a function rather than a COALESCE in SQL: an override of 0
// has to survive the resolution, and a default of 0 has to disappear.
func EffectiveQuota(override *int64, defaultBytes int64) *int64 {
	if override != nil {
		return override
	}
	if defaultBytes > 0 {
		return &defaultBytes
	}
	return nil
}

// NamespaceQuotaForRepo answers, for the namespace owning repoID, what it may
// store and what it is already storing. It is what the LFS upload path checks
// before it hands out an upload URL, so it takes the repository id the
// request already resolved rather than a name.
//
// ErrNotFound when there is no such repository.
func (s *Store) NamespaceQuotaForRepo(ctx context.Context, repoID int64, defaultBytes int64) (NamespaceQuota, error) {
	var q NamespaceQuota
	err := s.db.QueryRow(ctx,
		`SELECT n.id, n.name, n.kind, n.storage_quota_bytes
		 FROM repositories r JOIN namespaces n ON n.id = r.namespace_id
		 WHERE r.id = $1`, repoID,
	).Scan(&q.NamespaceID, &q.Namespace, &q.Kind, &q.QuotaBytes)
	if err != nil {
		return NamespaceQuota{}, norm(err)
	}
	// Usage is only worth computing once there is something to compare it
	// against. It aggregates every repository in the namespace, and this runs
	// on the upload path of every push, so an instance that has configured no
	// quota at all must not pay for an answer nothing reads. UsedBytes stays
	// 0 in that case, which is why the caller has to resolve the limit before
	// looking at it -- and does.
	if EffectiveQuota(q.QuotaBytes, defaultBytes) == nil {
		return q, nil
	}
	if err := s.fillNamespaceUsage(ctx, []*NamespaceQuota{&q}); err != nil {
		return NamespaceQuota{}, err
	}
	return q, nil
}

// GetNamespaceQuota reads one namespace's allowance and usage by name,
// case-insensitively (see GetNamespace). ErrNotFound when there is no such
// namespace.
func (s *Store) GetNamespaceQuota(ctx context.Context, name string) (NamespaceQuota, error) {
	var q NamespaceQuota
	err := s.db.QueryRow(ctx,
		`SELECT n.id, n.name, n.kind, n.storage_quota_bytes
		 FROM namespaces n WHERE LOWER(n.name) = LOWER($1)`, name,
	).Scan(&q.NamespaceID, &q.Namespace, &q.Kind, &q.QuotaBytes)
	if err != nil {
		return NamespaceQuota{}, norm(err)
	}
	if err := s.fillNamespaceUsage(ctx, []*NamespaceQuota{&q}); err != nil {
		return NamespaceQuota{}, err
	}
	return q, nil
}

// SetNamespaceQuota writes a namespace's override, or clears it when quota is
// nil so the instance default applies again. A non-nil zero stores a quota of
// zero bytes, which is a different state from cleared and is preserved as
// one. The name is matched case-insensitively; ErrNotFound when nothing
// matched.
//
// It is the site administrator's lever alone. Nothing reachable by an
// organisation admin calls it: a cap its holder may raise is not a cap.
func (s *Store) SetNamespaceQuota(ctx context.Context, name string, quota *int64) error {
	n, err := s.db.Exec(ctx,
		`UPDATE namespaces SET storage_quota_bytes = $1 WHERE LOWER(name) = LOWER($2)`,
		quota, name)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListNamespaceQuotas is one page of the namespace directory, with each
// namespace's allowance and usage. search is a case-insensitive substring of
// the name; limit defaults to 50 and is capped at 200. Total counts every
// matching namespace, ignoring the page window.
//
// Ordering is by name so paging is stable while an administrator works
// through the list, rather than by usage, which moves under them on every
// push.
func (s *Store) ListNamespaceQuotas(ctx context.Context, search string, limit, offset int) ([]NamespaceQuota, int64, error) {
	limit, offset = pageWindow(limit, offset, defaultNamespacePageSize, maxNamespacePageSize)

	// ILIKE is rewritten to LIKE for SQLite (dialect.go), whose LIKE is
	// already case-insensitive for ASCII -- namespace names are ASCII by
	// construction (api/repos.go's nameRe). The search text is a substring,
	// not a pattern, so it goes through like.go's pair.
	var args []any
	bind := binder(&args)
	where := ""
	if c := searchClause(bind, search, "name"); c != "" {
		where = ` WHERE ` + c
	}
	// The count runs on the clause's own parameters; the page window is
	// bound after it so this prefix is exactly them (see searchClause).
	countArgs := append([]any{}, args...)

	var total int64
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM namespaces`+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitP, offsetP := bind(limit), bind(offset)

	rows, err := s.db.Query(ctx,
		`SELECT id, name, kind, storage_quota_bytes FROM namespaces`+where+
			` ORDER BY name LIMIT `+limitP+` OFFSET `+offsetP, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []NamespaceQuota{}
	for rows.Next() {
		var q NamespaceQuota
		if err := rows.Scan(&q.NamespaceID, &q.Namespace, &q.Kind, &q.QuotaBytes); err != nil {
			return nil, 0, err
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	refs := make([]*NamespaceQuota, 0, len(out))
	for i := range out {
		refs = append(refs, &out[i])
	}
	if err := s.fillNamespaceUsage(ctx, refs); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// NamespaceQuotaOverrides reads the raw storage_quota_bytes of the named
// namespaces, keyed by their stored spelling. A namespace with no override is
// absent from the map rather than present with a nil value, so a caller
// cannot mistake "not listed" for "explicitly unlimited". An empty name list
// returns an empty map without querying.
//
// It exists for the usage dashboard, which already has the usage rows and
// needs only the allowance to go with them.
func (s *Store) NamespaceQuotaOverrides(ctx context.Context, names []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT name, storage_quota_bytes FROM namespaces
		 WHERE name `+s.d.inArray("$1")+` AND storage_quota_bytes IS NOT NULL`,
		s.d.stringArrayArg(names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var quota int64
		if err := rows.Scan(&name, &quota); err != nil {
			return nil, err
		}
		out[name] = quota
	}
	return out, rows.Err()
}

// fillNamespaceUsage sets UsedBytes / NumRepos on each row from the shared
// usage aggregation. A namespace holding no repositories is simply absent
// from the aggregate and keeps its zeroes, which is the honest answer rather
// than a missing row.
func (s *Store) fillNamespaceUsage(ctx context.Context, rows []*NamespaceQuota) error {
	if len(rows) == 0 {
		return nil
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Namespace)
	}
	repoUsage, err := s.UsageByRepo(ctx, names)
	if err != nil {
		return err
	}
	byNS := map[string]NamespaceUsage{}
	for _, u := range AggregateUsageByNamespace(repoUsage) {
		byNS[u.Namespace] = u
	}
	for _, r := range rows {
		u := byNS[r.Namespace]
		r.UsedBytes = u.LFSSize
		r.NumRepos = u.NumRepos
	}
	return nil
}
