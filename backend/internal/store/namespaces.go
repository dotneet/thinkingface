// Namespaces: the one concept behind both a user account and an
// organisation (docs/namespace-design.md §3). A namespaces row owns the name,
// the profile columns, and -- for kind='org' -- the membership policy; the
// user- and organisation-shaped views over it live in users.go and orgs.go.

package store

import (
	"context"
	"time"
)

// Namespace is somewhere the user may create repositories.
type Namespace struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	OwnerUserID *int64 `json:"-"`
	Role        string `json:"role,omitempty"`
}

// NamespaceProfile is a namespaces row read without caring which kind it is
// (docs/namespace-design.md §6). The profile columns are shared by both
// kinds; MembersVisibility only means anything for an organisation.
type NamespaceProfile struct {
	ID   int64
	Name string
	// Kind is "user" or "org".
	Kind        string
	DisplayName string
	Description string
	Website     string
	AvatarURL   string
	// MembersVisibility is "members" or "public". User namespaces carry the
	// column's default and never use it.
	MembersVisibility string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NamespaceCounts is what a namespace holds, as the namespace page shows it.
// Datasets excludes experiment repositories: they are datasets on disk but a
// separate tab in the UI, so counting them twice would make the tabs add up
// to more than the namespace has.
type NamespaceCounts struct {
	Models      int64
	Datasets    int64
	Experiments int64
	// Members is the org_members count, 0 for a user namespace.
	Members int64
}

// NamespaceUpdate is a partial profile update: a nil field is left alone, a
// non-nil one replaces the stored value (an empty string clears it). The
// name itself is absent on purpose -- namespaces are never renamed
// (docs/namespace-design.md §5.4).
type NamespaceUpdate struct {
	DisplayName *string
	Description *string
	Website     *string
	AvatarURL   *string
}

// namespaceProfileColumns reads a namespaces row as a profile. updated_at is
// scanned through a pointer for the reason given at orgColumns: SQLite could
// not add it NOT NULL, so the column is nullable there.
const namespaceProfileColumns = `n.id, n.name, n.kind, n.display_name, n.description,
	n.website, n.avatar_url, n.members_visibility, n.created_at, n.updated_at`

func scanNamespaceProfile(row rowScanner) (*NamespaceProfile, error) {
	p := &NamespaceProfile{}
	var updatedAt *time.Time
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.DisplayName, &p.Description,
		&p.Website, &p.AvatarURL, &p.MembersVisibility, &p.CreatedAt, &updatedAt)
	if err != nil {
		return nil, norm(err)
	}
	p.UpdatedAt = p.CreatedAt
	if updatedAt != nil {
		p.UpdatedAt = *updatedAt
	}
	return p, nil
}

// GetNamespace looks a namespace up case-insensitively: "Alice" and "alice"
// name the same namespace (idx_namespaces_name_lower enforces this at the
// schema level; migrations/postgres/0026_namespace_name_ci_unique.sql). The
// returned Name is the spelling the namespace was created with, never the
// spelling the caller looked it up by.
func (s *Store) GetNamespace(ctx context.Context, name string) (*Namespace, error) {
	n := &Namespace{}
	err := s.db.QueryRow(ctx,
		`SELECT id, name, kind, owner_user_id FROM namespaces WHERE LOWER(name) = LOWER($1)`, name,
	).Scan(&n.ID, &n.Name, &n.Kind, &n.OwnerUserID)
	return n, norm(err)
}

// GetNamespaceProfile reads the profile of a namespace of either kind, by
// name, case-insensitively (see GetNamespace). ErrNotFound when there is no
// such namespace.
func (s *Store) GetNamespaceProfile(ctx context.Context, name string) (*NamespaceProfile, error) {
	return scanNamespaceProfile(s.db.QueryRow(ctx,
		`SELECT `+namespaceProfileColumns+` FROM namespaces n WHERE LOWER(n.name) = LOWER($1)`, name))
}

// GetNamespaceProfileByID is GetNamespaceProfile keyed by the id the caller
// already holds.
func (s *Store) GetNamespaceProfileByID(ctx context.Context, id int64) (*NamespaceProfile, error) {
	return scanNamespaceProfile(s.db.QueryRow(ctx,
		`SELECT `+namespaceProfileColumns+` FROM namespaces n WHERE n.id = $1`, id))
}

// CountNamespaceResources counts what lives under the namespace in one round
// trip. The three repository buckets are disjoint, so the namespace page's
// tab counts add up to the number of repositories it actually holds. There
// is no repository visibility, so every caller sees the same numbers.
func (s *Store) CountNamespaceResources(ctx context.Context, id int64) (NamespaceCounts, error) {
	var c NamespaceCounts
	// SUM(CASE ...) rather than the Postgres-only FILTER clause, and the
	// member count as a scalar subquery so the whole thing stays one
	// statement on both engines.
	err := s.db.QueryRow(ctx,
		`SELECT
		   COALESCE(SUM(CASE WHEN r.kind = 'model' THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN r.kind = 'dataset' AND r.is_experiment = $2 THEN 1 ELSE 0 END), 0),
		   COALESCE(SUM(CASE WHEN r.kind = 'dataset' AND r.is_experiment = $3 THEN 1 ELSE 0 END), 0),
		   (SELECT count(*) FROM org_members m WHERE m.namespace_id = $1)
		 FROM repositories r WHERE r.namespace_id = $1`,
		id, false, true,
	).Scan(&c.Models, &c.Datasets, &c.Experiments, &c.Members)
	return c, err
}

// updateNamespaceRow is the single UPDATE behind both UpdateNamespaceProfile
// and UpdateOrg. kind narrows it to one namespace kind ("" for either), so
// the organisation path keeps its `AND kind = 'org'` guard without a second
// copy of the statement. ErrNotFound when nothing matched.
func (s *Store) updateNamespaceRow(ctx context.Context, id int64, u NamespaceUpdate, membersVisibility *string, kind string) error {
	var args []any
	bind := binder(&args)
	set := []string{`updated_at = now()`}
	if u.DisplayName != nil {
		set = append(set, `display_name = `+bind(*u.DisplayName))
	}
	if u.Description != nil {
		set = append(set, `description = `+bind(*u.Description))
	}
	if u.Website != nil {
		set = append(set, `website = `+bind(*u.Website))
	}
	if u.AvatarURL != nil {
		set = append(set, `avatar_url = `+bind(*u.AvatarURL))
	}
	if membersVisibility != nil {
		set = append(set, `members_visibility = `+bind(*membersVisibility))
	}
	where := `WHERE id = ` + bind(id)
	if kind != "" {
		where += ` AND kind = ` + bind(kind)
	}

	n, err := s.db.Exec(ctx, `UPDATE namespaces SET `+joinComma(set)+` `+where, args...)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateNamespaceProfile applies the non-nil fields of u to the namespace of
// either kind and returns the updated row. It is what PATCH /api/v1/me/profile
// edits; organisations go through UpdateOrg, which adds members_visibility.
func (s *Store) UpdateNamespaceProfile(ctx context.Context, id int64, u NamespaceUpdate) (*NamespaceProfile, error) {
	if err := s.updateNamespaceRow(ctx, id, u, nil, ""); err != nil {
		return nil, err
	}
	return s.GetNamespaceProfileByID(ctx, id)
}

// NamespacesForUser lists everywhere the user may create repositories: their
// own namespace plus organisations they belong to.
func (s *Store) NamespacesForUser(ctx context.Context, userID int64) ([]Namespace, error) {
	rows, err := s.db.Query(ctx,
		`SELECT n.id, n.name, n.kind, n.owner_user_id,
		        COALESCE(m.role, CASE WHEN n.owner_user_id = $1 THEN 'admin' ELSE '' END) AS role
		 FROM namespaces n
		 LEFT JOIN org_members m ON m.namespace_id = n.id AND m.user_id = $1
		 WHERE n.owner_user_id = $1 OR m.user_id = $1
		 ORDER BY n.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Namespace
	for rows.Next() {
		var n Namespace
		if err := rows.Scan(&n.ID, &n.Name, &n.Kind, &n.OwnerUserID, &n.Role); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CanWriteNamespace reports whether the user may push to repositories under ns.
func (s *Store) CanWriteNamespace(ctx context.Context, userID int64, ns string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx,
		`SELECT EXISTS(
		   SELECT 1 FROM namespaces n
		   LEFT JOIN org_members m ON m.namespace_id = n.id AND m.user_id = $1
		   WHERE LOWER(n.name) = LOWER($2)
		     AND (n.owner_user_id = $1 OR m.role IN ('admin', 'write'))
		 )`, userID, ns).Scan(&ok)
	return ok, err
}
