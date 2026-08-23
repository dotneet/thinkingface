package store

import (
	"context"
	"fmt"
	"time"
)

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	IsAdmin      bool      `json:"is_admin"`
	CreatedAt    time.Time `json:"created_at"`
	// SessionEpoch is the revocation counter carried by tf_session. A cookie
	// signed with an older epoch is refused, which is what makes logout and a
	// password change actually invalidate outstanding sessions.
	SessionEpoch int64 `json:"-"`
}

type AccessToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"-"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CreateUser inserts the user and their personal namespace in one transaction,
// so an account can never exist without somewhere to put repositories.
func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string, isAdmin bool) (*User, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	u := &User{}
	err = tx.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, is_admin)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, username, email, password_hash, is_admin, created_at, session_epoch`,
		username, email, passwordHash, isAdmin,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.SessionEpoch)
	if s.d.isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO namespaces (name, kind, owner_user_id) VALUES ($1, 'user', $2)`,
		username, u.ID); err != nil {
		if s.d.isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("insert namespace: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(ctx,
		// Case-insensitive to match the namespace uniqueness rule: "Alice"
		// and "alice" are one identity, so logging in (and being added to an
		// organisation by name) must not depend on how the name was typed.
		`SELECT id, username, email, password_hash, is_admin, created_at, session_epoch FROM users WHERE LOWER(username) = LOWER($1)`,
		username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.SessionEpoch)
	return u, norm(err)
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(ctx,
		`SELECT id, username, email, password_hash, is_admin, created_at, session_epoch FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.SessionEpoch)
	return u, norm(err)
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ListUsers is the site administrator's account directory: every user,
// optionally narrowed by a case-insensitive substring of the username or the
// email address, plus the total ignoring the page window.
//
// The password hash is deliberately not selected. Nothing above this layer
// needs it for a listing, and a column that is never read cannot leak.
func (s *Store) ListUsers(ctx context.Context, search string, limit, offset int) ([]User, int64, error) {
	limit, offset = pageWindow(limit, offset)

	// ILIKE is rewritten to LIKE for SQLite (dialect.go), whose LIKE is
	// already case-insensitive for ASCII -- the same compromise the
	// repository and organisation listings make.
	where := ""
	var countArgs []any
	countBind := binder(&countArgs)
	if search != "" {
		p := countBind("%" + search + "%")
		where = ` WHERE (username ILIKE ` + p + ` OR email ILIKE ` + p + `)`
	}

	var total int64
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	var args []any
	bind := binder(&args)
	listWhere := ""
	if search != "" {
		p := bind("%" + search + "%")
		listWhere = ` WHERE (username ILIKE ` + p + ` OR email ILIKE ` + p + `)`
	}
	limitP, offsetP := bind(limit), bind(offset)

	rows, err := s.db.Query(ctx,
		`SELECT id, username, email, is_admin, created_at FROM users`+listWhere+
			` ORDER BY username LIMIT `+limitP+` OFFSET `+offsetP, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.IsAdmin, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// pageWindow clamps an offset-based page request. A limit outside the range
// falls back to the default rather than erroring, matching ListOrgs.
func pageWindow(limit, offset int) (int, int) {
	if limit <= 0 || limit > maxUserPageSize {
		limit = defaultUserPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

const (
	defaultUserPageSize = 50
	maxUserPageSize     = 200
)

// UpdateUserPassword replaces the stored bcrypt hash. It does not touch
// session_epoch: revoking the outstanding sessions is a separate decision the
// caller makes (the API layer bumps the epoch and, for a self-service change,
// re-issues the caller's own cookie), and access tokens are unaffected either
// way -- they are an independent credential (docs/dev/api-contract.md §1.3).
func (s *Store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	n, err := s.db.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, passwordHash)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserAdmin grants or revokes instance-wide administrator rights.
// Revoking it from the last remaining administrator is ErrLastSiteAdmin: the
// flag is the only thing that can hand it back, so an instance that loses its
// last administrator can only be repaired from the database.
//
// The read-modify-write runs under an advisory lock rather than a row lock:
// the rule is about the *count* of administrators, so two concurrent
// demotions of two different accounts must not both observe two.
func (s *Store) SetUserAdmin(ctx context.Context, userID int64, isAdmin bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	if err := s.d.advisoryXactLock(ctx, tx, "site-admins", 0); err != nil {
		return err
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT is_admin FROM users WHERE id = $1`, userID).Scan(&current); err != nil {
		return norm(err)
	}
	if current && !isAdmin {
		var admins int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_admin`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastSiteAdmin
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET is_admin = $2 WHERE id = $1`, userID, isAdmin); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CreateToken(ctx context.Context, userID int64, name, scope, tokenHash string) (*AccessToken, error) {
	t := &AccessToken{}
	err := s.db.QueryRow(ctx,
		`INSERT INTO access_tokens (user_id, name, token_hash, scope) VALUES ($1, $2, $3, $4)
		 RETURNING id, user_id, name, scope, last_used_at, created_at`,
		userID, name, tokenHash, scope,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.LastUsedAt, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert token: %w", err)
	}
	return t, nil
}

// LookupToken resolves a hashed token to its owner, rejecting expired ones.
func (s *Store) LookupToken(ctx context.Context, tokenHash string) (*User, *AccessToken, error) {
	u := &User{}
	t := &AccessToken{}
	err := s.db.QueryRow(ctx,
		`SELECT t.id, t.user_id, t.name, t.scope, t.last_used_at, t.created_at,
		        u.id, u.username, u.email, u.password_hash, u.is_admin, u.created_at, u.session_epoch
		 FROM access_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = $1 AND (t.expires_at IS NULL OR t.expires_at > now())`,
		tokenHash,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.LastUsedAt, &t.CreatedAt,
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.SessionEpoch)
	if err != nil {
		return nil, nil, norm(err)
	}
	return u, t, nil
}

// TouchToken records last use. Failures are not worth failing a request over,
// so callers may ignore the error.
func (s *Store) TouchToken(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE access_tokens SET last_used_at = now() WHERE id = $1`, id)
	return err
}

func (s *Store) ListTokens(ctx context.Context, userID int64) ([]AccessToken, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, user_id, name, scope, last_used_at, created_at
		 FROM access_tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AccessToken{}
	for rows.Next() {
		var t AccessToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteToken(ctx context.Context, userID, id int64) error {
	n, err := s.db.Exec(ctx, `DELETE FROM access_tokens WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
