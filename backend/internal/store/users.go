package store

import (
	"context"
	"fmt"
	"strings"
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
	// DisabledAt is when the account was suspended, nil while it is active.
	// It is the offboarding switch: every identity path in the server --
	// session cookie, password (including HTTP Basic), access token and SSH
	// public key -- refuses a user for which this is set. Nothing is deleted,
	// so restoring the account is a matter of clearing it again.
	DisabledAt *time.Time `json:"-"`
	// DisabledBy is the site administrator who suspended the account, nil
	// while it is active.
	DisabledBy *int64 `json:"-"`
}

// Disabled reports whether the account is suspended. Read it rather than
// comparing DisabledAt yourself: this is the predicate every identity path is
// expected to consult, and having one spelling of it is what keeps the paths
// from drifting apart.
func (u *User) Disabled() bool { return u != nil && u.DisabledAt != nil }

// userColumns is the SELECT list every query that materialises a User uses,
// in the order scanUser reads them. It exists so a new column cannot be added
// to one query and forgotten in the next -- which is exactly how an identity
// path ends up not knowing an account is disabled.
const userColumns = `id, username, email, password_hash, is_admin, created_at, session_epoch, disabled_at, disabled_by`

// userColumnsOn qualifies userColumns with a table alias, for the queries
// that join users to a credential table (LookupToken, LookupSSHKey).
func userColumnsOn(alias string) string {
	return alias + "." + strings.ReplaceAll(userColumns, ", ", ", "+alias+".")
}

func scanUser(row rowScanner, u *User) error {
	return row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin,
		&u.CreatedAt, &u.SessionEpoch, &u.DisabledAt, &u.DisabledBy)
}

type AccessToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"-"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// ExpiresAt is nil for a token that never expires.
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
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
	err = scanUser(tx.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, is_admin)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+userColumns,
		username, email, passwordHash, isAdmin), u)
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
	err := scanUser(s.db.QueryRow(ctx,
		// Case-insensitive to match the namespace uniqueness rule: "Alice"
		// and "alice" are one identity, so logging in (and being added to an
		// organisation by name) must not depend on how the name was typed.
		`SELECT `+userColumns+` FROM users WHERE LOWER(username) = LOWER($1)`,
		username), u)
	return u, norm(err)
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	u := &User{}
	err := scanUser(s.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id), u)
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
	limit, offset = pageWindow(limit, offset, defaultUserPageSize, maxUserPageSize)

	// ILIKE is rewritten to LIKE for SQLite (dialect.go), whose LIKE is
	// already case-insensitive for ASCII -- the same compromise the
	// repository and organisation listings make. The search text is a
	// substring, not a pattern, so it goes through like.go's pair.
	where := ""
	var countArgs []any
	countBind := binder(&countArgs)
	if search != "" {
		p := countBind(likeContains(search))
		where = ` WHERE ` + likeAnyOf(p, "username", "email")
	}

	var total int64
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`+where, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	var args []any
	bind := binder(&args)
	listWhere := ""
	if search != "" {
		p := bind(likeContains(search))
		listWhere = ` WHERE ` + likeAnyOf(p, "username", "email")
	}
	limitP, offsetP := bind(limit), bind(offset)

	rows, err := s.db.Query(ctx,
		`SELECT id, username, email, is_admin, created_at, disabled_at FROM users`+listWhere+
			` ORDER BY username LIMIT `+limitP+` OFFSET `+offsetP, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.IsAdmin, &u.CreatedAt, &u.DisabledAt); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// pageLimit resolves a requested page size. Nothing (or nonsense) asked for
// means the endpoint's default; more than the maximum is served *at* the
// maximum. It is deliberately a clamp and not a fallback: "max 100" reads as
// a ceiling, so answering ?limit=200 with 30 rows -- fewer than a caller who
// asked for nothing would get -- is the opposite of what was requested.
func pageLimit(limit, defaultSize, maxSize int) int {
	switch {
	case limit <= 0:
		return defaultSize
	case limit > maxSize:
		return maxSize
	default:
		return limit
	}
}

// pageWindow is pageLimit plus the offset half. A negative offset is the
// first page: Postgres rejects a negative OFFSET outright, so a hand-edited
// query string would otherwise be a 500 rather than a first page.
func pageWindow(limit, offset, defaultSize, maxSize int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	return pageLimit(limit, defaultSize, maxSize), offset
}

const (
	defaultUserPageSize = 50
	maxUserPageSize     = 200
)

// UpdateUserPassword replaces the stored bcrypt hash **and** revokes every
// outstanding session, in one statement, returning the new session_epoch.
//
// The two used to be separate calls, which left a window where the password
// had changed but the old cookies still worked -- a failure between them
// meant the caller saw an error while a stale session stayed valid. There is
// no caller that wants one without the other, so the pair is not a choice to
// offer: a single UPDATE makes "changing a password revokes its sessions" an
// invariant of the write rather than a rule every call site has to remember.
// (RETURNING works on both engines; see dialect_sqlite.go's
// updateExpRunAnnotation for the other use.)
//
// Access tokens are untouched. They are an independent credential and a
// password change is not evidence about any of them
// (docs/dev/api-contract.md §1.3).
func (s *Store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) (int64, error) {
	var epoch int64
	err := s.db.QueryRow(ctx,
		`UPDATE users SET password_hash = $2, session_epoch = session_epoch + 1
		 WHERE id = $1 RETURNING session_epoch`, userID, passwordHash).Scan(&epoch)
	return epoch, norm(err)
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
		// Suspended administrators do not count. They cannot authenticate on
		// any path, so leaving one as the only "remaining" administrator
		// locks the instance out just as thoroughly as leaving none -- and
		// SetUserDisabled applies the same predicate, so the two guards have
		// to agree or one of them can be walked around by going through the
		// other.
		var admins int64
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM users WHERE is_admin AND disabled_at IS NULL`).Scan(&admins); err != nil {
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

// SetUserDisabled suspends or restores an account, named by username the way
// the administration endpoint addresses it.
//
// Suspending is the offboarding switch this server previously did not have.
// A password reset leaves the account's access tokens and registered SSH keys
// working -- deliberately, see UpdateUserPassword -- so the only thing an
// administrator could do to a departed colleague was change a password that
// colleague no longer needed. Setting disabled_at stops **every** identity
// path at once, and session_epoch is incremented in the same statement so the
// cookies already issued stop working immediately rather than at their TTL.
//
// Restoring clears both columns. It brings back nothing that was revoked
// separately: tokens and keys deleted by RevokeAllTokens / DeleteAllSSHKeys
// are gone for good, which is the point of having the two actions apart.
//
// Suspending the last remaining site administrator is ErrLastSiteAdmin, for
// exactly SetUserAdmin's reason -- an instance with no usable administrator
// can only be repaired from the database -- and under the same advisory lock,
// since the rule is about the *count* of administrators and two concurrent
// suspensions must not both observe two. Disabled administrators do not count
// towards it: an account that cannot authenticate cannot administer anything.
//
// Setting the state an account is already in is a no-op, so a retried request
// does not bump the epoch a second time.
func (s *Store) SetUserDisabled(ctx context.Context, username string, disabled bool, actorID int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	if err := s.d.advisoryXactLock(ctx, tx, "site-admins", 0); err != nil {
		return err
	}
	var (
		id         int64
		isAdmin    bool
		disabledAt *time.Time
	)
	if err := tx.QueryRow(ctx,
		`SELECT id, is_admin, disabled_at FROM users WHERE LOWER(username) = LOWER($1)`,
		username).Scan(&id, &isAdmin, &disabledAt); err != nil {
		return norm(err)
	}
	if disabled == (disabledAt != nil) {
		return nil
	}
	if disabled && isAdmin {
		var admins int64
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM users WHERE is_admin AND disabled_at IS NULL`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastSiteAdmin
		}
	}
	if disabled {
		_, err = tx.Exec(ctx,
			`UPDATE users SET disabled_at = now(), disabled_by = $2,
			        session_epoch = session_epoch + 1
			 WHERE id = $1`, id, actorID)
	} else {
		_, err = tx.Exec(ctx,
			`UPDATE users SET disabled_at = NULL, disabled_by = NULL WHERE id = $1`, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RevokeAllTokens deletes every access token an account holds and reports how
// many rows went. Unlike DeleteToken it carries no ownership predicate,
// because the caller is not the owner: this is the site administrator's
// offboarding action, and the authorization for it lives in the handler.
//
// Deliberately not folded into SetUserDisabled. Suspension is reversible and
// destroys nothing; revoking credentials is neither, and an irreversible
// deletion must not ride along as a side effect of a partial update.
func (s *Store) RevokeAllTokens(ctx context.Context, userID int64) (int64, error) {
	return s.db.Exec(ctx, `DELETE FROM access_tokens WHERE user_id = $1`, userID)
}

// DeleteAllSSHKeys is RevokeAllTokens for registered public keys. The two are
// separate statements rather than one transaction on purpose: each is
// idempotent on its own and a partial failure leaves strictly fewer
// credentials than before, so an identical retry converges.
func (s *Store) DeleteAllSSHKeys(ctx context.Context, userID int64) (int64, error) {
	return s.db.Exec(ctx, `DELETE FROM user_ssh_keys WHERE user_id = $1`, userID)
}

// CreateToken inserts a new access token. expiresAt is nil for a token that
// never expires; otherwise it is the caller-computed instant the token stops
// working, already resolved to an absolute time so this method (and the
// PostgreSQL/SQLite dialects it runs against) never has to do date
// arithmetic itself.
func (s *Store) CreateToken(ctx context.Context, userID int64, name, scope, tokenHash string, expiresAt *time.Time) (*AccessToken, error) {
	t := &AccessToken{}
	err := s.db.QueryRow(ctx,
		`INSERT INTO access_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, user_id, name, scope, last_used_at, expires_at, created_at`,
		userID, name, tokenHash, scope, expiresAt,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.LastUsedAt, &t.ExpiresAt, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert token: %w", err)
	}
	return t, nil
}

// LookupToken resolves a hashed token to its owner, rejecting expired ones
// and tokens belonging to a suspended account.
//
// The disabled_at predicate is defence in depth, not the only check: the API
// layer refuses a suspended user on every identity path (see
// resolveIdentity). It lives in the statement as well because a token that
// resolves to nothing cannot be trusted by a future caller that forgets to
// ask.
func (s *Store) LookupToken(ctx context.Context, tokenHash string) (*User, *AccessToken, error) {
	u := &User{}
	t := &AccessToken{}
	err := s.db.QueryRow(ctx,
		`SELECT t.id, t.user_id, t.name, t.scope, t.last_used_at, t.created_at,
		        `+userColumnsOn("u")+`
		 FROM access_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = $1 AND (t.expires_at IS NULL OR t.expires_at > now())
		   AND u.disabled_at IS NULL`,
		tokenHash,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.LastUsedAt, &t.CreatedAt,
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.SessionEpoch,
		&u.DisabledAt, &u.DisabledBy)
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

// ListTokens returns every token the user holds, including expired ones --
// the owner needs to see why a token stopped working and still be able to
// delete it, so expiry is not a filter here (only LookupToken enforces it).
func (s *Store) ListTokens(ctx context.Context, userID int64) ([]AccessToken, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, user_id, name, scope, last_used_at, expires_at, created_at
		 FROM access_tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AccessToken{}
	for rows.Next() {
		var t AccessToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.LastUsedAt, &t.ExpiresAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes a token regardless of whether it has already expired --
// an expired row is still the owner's to clean up.
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
