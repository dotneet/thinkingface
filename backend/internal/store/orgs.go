package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Org is the organisation view of a namespaces row (kind = 'org'), holding
// the profile fields and the per-organisation policies of
// docs/dev/organization-design.md §6.1. User namespaces carry the same columns
// but never use them.
//
// An organisation has no owner: namespaces.owner_user_id is NULL for
// kind='org' and org_members is the only source of authority (§3).
// CreatedBy records who founded it and confers no permission.
type Org struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	Website     string
	AvatarURL   string
	// MembersVisibility is "members" or "public". It governs the member list
	// only: repositories have no visibility of their own
	// (docs/dev/content-addressed-storage-design.md §1).
	MembersVisibility string
	CreatedBy         *int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// OrgMember is one org_members row joined with the account it names.
type OrgMember struct {
	UserID   int64
	Username string
	Email    string
	// Role is "admin", "write" or "read".
	Role      string
	AddedBy   *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OrgSummary is an organisation as a listing shows it: the row plus the
// counts, and the viewer's own role when the listing is scoped to a user.
type OrgSummary struct {
	Org
	// Role is the viewer's org_members role, "" when they are not a member.
	Role       string
	NumMembers int64
	NumRepos   int64
}

// OrgUpdate is the partial update behind CreateOrg and UpdateOrg: a nil
// field is left at its default (create) or unchanged (update).
type OrgUpdate struct {
	DisplayName       *string
	Description       *string
	Website           *string
	AvatarURL         *string
	MembersVisibility *string
}

// AuditEntry is one line of an organisation's audit log
// (docs/dev/organization-design.md §5). ActorName / TargetName are denormalised
// so a line still reads after the account it names is deleted.
type AuditEntry struct {
	ID           int64
	ActorUserID  *int64
	ActorName    string
	Action       string
	TargetUserID *int64
	TargetName   string
	Details      json.RawMessage
	CreatedAt    time.Time
}

// NamespaceRole is a user's effective relationship to a namespace, resolved
// in one round trip: the API's authorization layer needs the namespace kind
// alongside the role to tell "owner of a personal namespace" from "org
// member" (docs/dev/organization-design.md §3.1).
type NamespaceRole struct {
	NamespaceID int64
	// Kind is "user" or "org".
	Kind string
	// Role is "admin", "write", "read", or "" for no relationship.
	Role string
}

// orgColumns reads a namespaces row as an organisation. updated_at is
// scanned through a pointer because SQLite cannot add a NOT NULL column with
// a non-constant default, so the column is nullable there
// (migrations/sqlite/0004); a COALESCE would work but erases the declared
// column type the SQLite driver needs to hand back a time.Time.
const orgColumns = `n.id, n.name, n.display_name, n.description, n.website, n.avatar_url,
	n.members_visibility, n.created_by, n.created_at, n.updated_at`

func scanOrg(row rowScanner, extra ...any) (*Org, error) {
	o := &Org{}
	var updatedAt *time.Time
	dest := []any{&o.ID, &o.Name, &o.DisplayName, &o.Description, &o.Website, &o.AvatarURL,
		&o.MembersVisibility, &o.CreatedBy, &o.CreatedAt, &updatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, norm(err)
	}
	o.UpdatedAt = o.CreatedAt
	if updatedAt != nil {
		o.UpdatedAt = *updatedAt
	}
	return o, nil
}

// orgRepoCount counts the repositories under the namespace aliased n. There is
// no repository visibility, so every caller sees the same number.
const orgRepoCount = `(SELECT count(*) FROM repositories r WHERE r.namespace_id = n.id)`

const orgMemberCount = `(SELECT count(*) FROM org_members m WHERE m.namespace_id = n.id)`

const (
	defaultOrgPageSize = 50
	maxOrgPageSize     = 200
)

// binder hands out $N placeholders while collecting the values they bind.
func binder(args *[]any) func(any) string {
	return func(v any) string {
		*args = append(*args, v)
		return "$" + strconv.Itoa(len(*args))
	}
}

// CreateOrg creates an organisation and makes its creator the first admin,
// in one transaction so an organisation can never exist with nobody able to
// administer it. owner_user_id stays NULL: authority comes from org_members
// alone (docs/dev/organization-design.md §3). ErrConflict when the name is
// already taken by a user or another organisation.
func (s *Store) CreateOrg(ctx context.Context, name string, creator int64, in OrgUpdate) (*Org, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	// RETURNING is written as a follow-up SELECT rather than on the INSERT
	// itself: SQLite rejects table-qualified names in an INSERT ... RETURNING
	// list, and orgColumns is qualified so the SELECT paths can join.
	var id int64
	err = tx.QueryRow(ctx,
		`INSERT INTO namespaces (name, kind, owner_user_id, created_by,
		     display_name, description, website, avatar_url,
		     members_visibility, updated_at)
		 VALUES ($1, 'org', NULL, $2, $3, $4, $5, $6, $7, now())
		 RETURNING id`,
		name, creator,
		strOr(in.DisplayName, ""), strOr(in.Description, ""), strOr(in.Website, ""), strOr(in.AvatarURL, ""),
		strOr(in.MembersVisibility, "members")).Scan(&id)
	if s.d.isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("insert organisation: %w", err)
	}
	org, err := scanOrg(tx.QueryRow(ctx,
		`SELECT `+orgColumns+` FROM namespaces n WHERE n.id = $1`, id))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO org_members (namespace_id, user_id, role, added_by, created_at, updated_at)
		 VALUES ($1, $2, 'admin', $2, now(), now())`, org.ID, creator); err != nil {
		return nil, fmt.Errorf("insert founding admin: %w", err)
	}
	return org, tx.Commit(ctx)
}

func strOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// GetOrg looks an organisation up by name, case-insensitively (see
// GetNamespace). A user namespace of that name is ErrNotFound: the two share
// a name space but only organisations answer here.
func (s *Store) GetOrg(ctx context.Context, name string) (*Org, error) {
	return scanOrg(s.db.QueryRow(ctx,
		`SELECT `+orgColumns+` FROM namespaces n WHERE LOWER(n.name) = LOWER($1) AND n.kind = 'org'`, name))
}

// UpdateOrg applies the non-nil fields of u and returns the updated row. The
// profile columns are shared with user namespaces, so the UPDATE itself is
// updateNamespaceRow (namespaces.go); this adds the organisation-only
// members_visibility and the `kind = 'org'` guard that makes a user
// namespace's id ErrNotFound here.
func (s *Store) UpdateOrg(ctx context.Context, id int64, u OrgUpdate) (*Org, error) {
	profile := NamespaceUpdate{
		DisplayName: u.DisplayName,
		Description: u.Description,
		Website:     u.Website,
		AvatarURL:   u.AvatarURL,
	}
	if err := s.updateNamespaceRow(ctx, id, profile, u.MembersVisibility, "org"); err != nil {
		return nil, err
	}
	return s.GetOrgByID(ctx, id)
}

// GetOrgByID is GetOrg keyed by the namespace id the caller already holds.
func (s *Store) GetOrgByID(ctx context.Context, id int64) (*Org, error) {
	return scanOrg(s.db.QueryRow(ctx,
		`SELECT `+orgColumns+` FROM namespaces n WHERE n.id = $1 AND n.kind = 'org'`, id))
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// DeleteOrg removes the organisation, cascading its memberships, webhooks
// and audit log. It refuses (ErrConflict) while any repository still lives
// there: dropping dozens of repositories behind one click is exactly the
// accident this friction exists to prevent
// (docs/dev/organization-design.md §5 "deleting an organisation"). The count and the delete
// share a transaction so a repository created concurrently cannot slip past.
func (s *Store) DeleteOrg(ctx context.Context, id int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var kind string
	if err := tx.QueryRow(ctx,
		`SELECT kind FROM namespaces WHERE id = $1`+s.d.forUpdate(""), id).Scan(&kind); err != nil {
		return norm(err)
	}
	if kind != "org" {
		return ErrNotFound
	}
	var n int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM repositories WHERE namespace_id = $1`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM namespaces WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CountOrgRepos counts the repositories under the organisation.
func (s *Store) CountOrgRepos(ctx context.Context, id int64) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx,
		`SELECT `+orgRepoCount+` FROM namespaces n WHERE n.id = $1`, id).Scan(&n)
	return n, norm(err)
}

// ListOrgsForUser lists the organisations userID belongs to, with their role.
func (s *Store) ListOrgsForUser(ctx context.Context, userID int64) ([]OrgSummary, error) {
	var args []any
	bind := binder(&args)
	member := bind(userID)

	rows, err := s.db.Query(ctx,
		`SELECT `+orgColumns+`, m.role, `+orgMemberCount+`, `+orgRepoCount+`
		 FROM namespaces n JOIN org_members m ON m.namespace_id = n.id AND m.user_id = `+member+`
		 WHERE n.kind = 'org'
		 ORDER BY n.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []OrgSummary{}
	for rows.Next() {
		var sum OrgSummary
		org, err := scanOrg(rows, &sum.Role, &sum.NumMembers, &sum.NumRepos)
		if err != nil {
			return nil, err
		}
		sum.Org = *org
		out = append(out, sum)
	}
	return out, rows.Err()
}

// ListOrgs is the public directory: every organisation, optionally filtered
// by a name/display-name substring, with the counts a card shows. Role is
// the viewer's own membership where they have one, so a directory page can
// badge the organisations they belong to without a query per row.
func (s *Store) ListOrgs(ctx context.Context, search string, viewerID *int64, limit, offset int) ([]OrgSummary, int64, error) {
	limit, offset = pageWindow(limit, offset, defaultOrgPageSize, maxOrgPageSize)

	where := `WHERE n.kind = 'org'`
	var countArgs []any
	countBind := binder(&countArgs)
	if search != "" {
		p := countBind("%" + search + "%")
		where += ` AND (n.name ILIKE ` + p + ` OR n.display_name ILIKE ` + p + `)`
	}

	var total int64
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM namespaces n `+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	var args []any
	bind := binder(&args)
	// The viewer's own membership, joined rather than re-queried per row.
	viewerRole, viewerJoin := `''`, ``
	if viewerID != nil {
		v := bind(*viewerID)
		viewerRole = `COALESCE(vm.role, '')`
		viewerJoin = ` LEFT JOIN org_members vm ON vm.namespace_id = n.id AND vm.user_id = ` + v
	}
	listWhere := `WHERE n.kind = 'org'`
	if search != "" {
		p := bind("%" + search + "%")
		listWhere += ` AND (n.name ILIKE ` + p + ` OR n.display_name ILIKE ` + p + `)`
	}
	limitP, offsetP := bind(limit), bind(offset)

	rows, err := s.db.Query(ctx,
		`SELECT `+orgColumns+`, `+viewerRole+`, `+orgMemberCount+`, `+orgRepoCount+`
		 FROM namespaces n`+viewerJoin+` `+listWhere+`
		 ORDER BY n.name LIMIT `+limitP+` OFFSET `+offsetP, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []OrgSummary{}
	for rows.Next() {
		var sum OrgSummary
		org, err := scanOrg(rows, &sum.Role, &sum.NumMembers, &sum.NumRepos)
		if err != nil {
			return nil, 0, err
		}
		sum.Org = *org
		out = append(out, sum)
	}
	return out, total, rows.Err()
}

// See orgColumns for why the two timestamps are scanned through pointers.
const orgMemberColumns = `m.user_id, u.username, u.email, m.role, m.added_by,
	m.created_at, m.updated_at`

func scanOrgMember(row rowScanner) (*OrgMember, error) {
	m := &OrgMember{}
	var createdAt, updatedAt *time.Time
	err := row.Scan(&m.UserID, &m.Username, &m.Email, &m.Role, &m.AddedBy, &createdAt, &updatedAt)
	if err != nil {
		return nil, norm(err)
	}
	if createdAt != nil {
		m.CreatedAt = *createdAt
	}
	m.UpdatedAt = m.CreatedAt
	if updatedAt != nil {
		m.UpdatedAt = *updatedAt
	}
	return m, nil
}

// ListOrgMembers returns the members, admins first and alphabetical inside
// each role.
func (s *Store) ListOrgMembers(ctx context.Context, id int64) ([]OrgMember, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+orgMemberColumns+`
		 FROM org_members m JOIN users u ON u.id = m.user_id
		 WHERE m.namespace_id = $1
		 ORDER BY CASE m.role WHEN 'admin' THEN 0 WHEN 'write' THEN 1 ELSE 2 END, u.username`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []OrgMember{}
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// GetOrgMember reads one membership row. ErrNotFound when the user is not a
// member.
func (s *Store) GetOrgMember(ctx context.Context, id, userID int64) (*OrgMember, error) {
	return scanOrgMember(s.db.QueryRow(ctx,
		`SELECT `+orgMemberColumns+`
		 FROM org_members m JOIN users u ON u.id = m.user_id
		 WHERE m.namespace_id = $1 AND m.user_id = $2`, id, userID))
}

// AddOrgMember grants userID the given role. ErrConflict when they already
// belong to the organisation -- changing a role is UpdateOrgMemberRole.
func (s *Store) AddOrgMember(ctx context.Context, id, userID int64, role string, addedBy int64) (*OrgMember, error) {
	_, err := s.db.Exec(ctx,
		`INSERT INTO org_members (namespace_id, user_id, role, added_by, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, now(), now())`, id, userID, role, addedBy)
	if s.d.isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return s.GetOrgMember(ctx, id, userID)
}

// lockOrgForMembershipChange serialises the read-modify-write behind the
// last-admin rule. On Postgres the organisation's namespaces row is locked
// FOR UPDATE so two concurrent demotions cannot both see two admins; on
// SQLite forUpdate renders nothing because the single writer connection
// already serialises write transactions.
func (s *Store) lockOrgForMembershipChange(ctx context.Context, ex executor, id int64) error {
	var got int64
	err := ex.QueryRow(ctx,
		`SELECT id FROM namespaces WHERE id = $1 AND kind = 'org'`+s.d.forUpdate(""), id).Scan(&got)
	return norm(err)
}

// countOrgAdmins is only ever called inside the transaction that holds the
// lock above.
func countOrgAdmins(ctx context.Context, ex executor, id int64) (int64, error) {
	var n int64
	err := ex.QueryRow(ctx,
		`SELECT count(*) FROM org_members WHERE namespace_id = $1 AND role = 'admin'`, id).Scan(&n)
	return n, err
}

// UpdateOrgMemberRole changes an existing member's role. Demoting the only
// admin is ErrLastAdmin: an organisation with no admin could never be
// administered again (docs/dev/organization-design.md §5).
func (s *Store) UpdateOrgMemberRole(ctx context.Context, id, userID int64, role string) (*OrgMember, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := s.lockOrgForMembershipChange(ctx, tx, id); err != nil {
		return nil, err
	}
	var current string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM org_members WHERE namespace_id = $1 AND user_id = $2`, id, userID,
	).Scan(&current); err != nil {
		return nil, norm(err)
	}
	if current == "admin" && role != "admin" {
		admins, err := countOrgAdmins(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if admins <= 1 {
			return nil, ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE org_members SET role = $3, updated_at = now()
		 WHERE namespace_id = $1 AND user_id = $2`, id, userID, role); err != nil {
		return nil, err
	}
	m, err := scanOrgMember(tx.QueryRow(ctx,
		`SELECT `+orgMemberColumns+`
		 FROM org_members m JOIN users u ON u.id = m.user_id
		 WHERE m.namespace_id = $1 AND m.user_id = $2`, id, userID))
	if err != nil {
		return nil, err
	}
	return m, tx.Commit(ctx)
}

// RemoveOrgMember drops a membership, whether an admin removed someone or a
// member left of their own accord. Removing the only admin is ErrLastAdmin.
func (s *Store) RemoveOrgMember(ctx context.Context, id, userID int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := s.lockOrgForMembershipChange(ctx, tx, id); err != nil {
		return err
	}
	var current string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM org_members WHERE namespace_id = $1 AND user_id = $2`, id, userID,
	).Scan(&current); err != nil {
		return norm(err)
	}
	if current == "admin" {
		admins, err := countOrgAdmins(ctx, tx, id)
		if err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM org_members WHERE namespace_id = $1 AND user_id = $2`, id, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AppendOrgAudit records one administrative action. Callers treat a failure
// as non-fatal: the audit log must never be the reason an operation that
// already succeeded reports an error.
func (s *Store) AppendOrgAudit(ctx context.Context, id int64, e AuditEntry) error {
	details := e.Details
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO org_audit_log
		     (namespace_id, actor_user_id, actor_name, action, target_user_id, target_name, details)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, e.ActorUserID, e.ActorName, e.Action, e.TargetUserID, e.TargetName, []byte(details))
	return err
}

// ListOrgAudit returns one page of the log, newest first. beforeID > 0
// continues after a previous page's last id.
func (s *Store) ListOrgAudit(ctx context.Context, id int64, beforeID int64, limit int) ([]AuditEntry, error) {
	limit = pageLimit(limit, defaultOrgPageSize, maxOrgPageSize)
	args := []any{id}
	bind := binder(&args)
	where := `WHERE namespace_id = $1`
	if beforeID > 0 {
		where += ` AND id < ` + bind(beforeID)
	}
	limitP := bind(limit)

	rows, err := s.db.Query(ctx,
		`SELECT id, actor_user_id, actor_name, action, target_user_id, target_name, details, created_at
		 FROM org_audit_log `+where+` ORDER BY id DESC LIMIT `+limitP, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var details []byte
		if err := rows.Scan(&e.ID, &e.ActorUserID, &e.ActorName, &e.Action,
			&e.TargetUserID, &e.TargetName, &details, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Details = json.RawMessage(details)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ----------------------------------------------------------- namespace roles

// NamespaceRoleFor resolves what userID may do in the namespace named ns.
// A user namespace answers "admin" to its owner and "" to everyone else; an
// organisation answers the org_members role. Site admins are not considered
// here -- that is the API layer's job (docs/dev/organization-design.md §3.1).
// ErrNotFound when the namespace does not exist. ns is matched
// case-insensitively (see GetNamespace).
func (s *Store) NamespaceRoleFor(ctx context.Context, userID int64, ns string) (NamespaceRole, error) {
	var out NamespaceRole
	var ownerID *int64
	err := s.db.QueryRow(ctx,
		`SELECT id, kind, owner_user_id FROM namespaces WHERE LOWER(name) = LOWER($1)`, ns,
	).Scan(&out.NamespaceID, &out.Kind, &ownerID)
	if err != nil {
		return NamespaceRole{}, norm(err)
	}
	if ownerID != nil && *ownerID == userID {
		out.Role = "admin"
		return out, nil
	}
	var role string
	err = s.db.QueryRow(ctx,
		`SELECT role FROM org_members WHERE namespace_id = $1 AND user_id = $2`, out.NamespaceID, userID,
	).Scan(&role)
	if isNoRows(err) {
		return out, nil
	}
	if err != nil {
		return NamespaceRole{}, err
	}
	out.Role = role
	return out, nil
}

// RoleInNamespace reports a user's relationship to a namespace: "admin" for
// the owner of a user namespace or an org admin, "write"/"read" for the
// corresponding org_members roles, and "" when the user has no relationship.
// ErrNotFound when the namespace does not exist.
func (s *Store) RoleInNamespace(ctx context.Context, userID int64, ns string) (string, error) {
	r, err := s.NamespaceRoleFor(ctx, userID, ns)
	if err != nil {
		return "", err
	}
	return r.Role, nil
}
