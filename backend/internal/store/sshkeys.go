package store

import (
	"context"
	"fmt"
	"time"
)

// SSHKey is one registered public key. The key material itself is public, so
// unlike an access token it is stored and returned verbatim; the fingerprint
// is the lookup key the SSH server authenticates against.
type SSHKey struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"-"`
	Title  string `json:"title"`
	// PublicKey is the canonical "<type> <base64>" authorized_keys form,
	// without a comment (see auth.ParseSSHPublicKey).
	PublicKey   string     `json:"public_key"`
	Fingerprint string     `json:"fingerprint"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

const sshKeyColumns = `id, user_id, title, public_key, fingerprint, last_used_at, created_at`

func scanSSHKey(row rowScanner, k *SSHKey) error {
	return row.Scan(&k.ID, &k.UserID, &k.Title, &k.PublicKey, &k.Fingerprint, &k.LastUsedAt, &k.CreatedAt)
}

// CreateSSHKey registers a key. ErrConflict when the fingerprint is already
// registered -- by this user or any other, since the SSH server resolves an
// identity from the offered key alone and cannot disambiguate a shared one.
func (s *Store) CreateSSHKey(ctx context.Context, userID int64, title, publicKey, fingerprint string) (*SSHKey, error) {
	k := &SSHKey{}
	err := scanSSHKey(s.db.QueryRow(ctx,
		`INSERT INTO user_ssh_keys (user_id, title, public_key, fingerprint)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+sshKeyColumns,
		userID, title, publicKey, fingerprint), k)
	if s.d.isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("insert ssh key: %w", err)
	}
	return k, nil
}

func (s *Store) ListSSHKeys(ctx context.Context, userID int64) ([]SSHKey, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+sshKeyColumns+`
		 FROM user_ssh_keys WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []SSHKey{}
	for rows.Next() {
		var k SSHKey
		if err := scanSSHKey(rows, &k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteSSHKey removes one of the user's own keys. The user_id predicate is
// the authorization check: it must stay in the statement so a guessed id
// cannot delete somebody else's key.
func (s *Store) DeleteSSHKey(ctx context.Context, userID, id int64) error {
	n, err := s.db.Exec(ctx, `DELETE FROM user_ssh_keys WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// LookupSSHKey resolves a fingerprint offered during public key
// authentication to its owner. ErrNotFound when the key is not registered --
// or when its owner is suspended, which is the same answer on purpose.
//
// SSH is the one identity path that does not run through the HTTP middleware,
// so the disabled_at predicate is what makes a suspended account unable to
// `git push` over SSH. internal/sshserver treats ErrNotFound as "this key
// authenticates nobody" and refuses the connection, which is exactly right:
// there is nothing useful to tell a client at the public-key stage, and
// answering differently would confirm that the key is registered.
func (s *Store) LookupSSHKey(ctx context.Context, fingerprint string) (*User, *SSHKey, error) {
	u := &User{}
	k := &SSHKey{}
	err := s.db.QueryRow(ctx,
		`SELECT k.id, k.user_id, k.title, k.public_key, k.fingerprint, k.last_used_at, k.created_at,
		        `+userColumnsOn("u")+`
		 FROM user_ssh_keys k JOIN users u ON u.id = k.user_id
		 WHERE k.fingerprint = $1 AND u.disabled_at IS NULL`, fingerprint,
	).Scan(&k.ID, &k.UserID, &k.Title, &k.PublicKey, &k.Fingerprint, &k.LastUsedAt, &k.CreatedAt,
		&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.CreatedAt, &u.SessionEpoch,
		&u.DisabledAt, &u.DisabledBy)
	if err != nil {
		return nil, nil, norm(err)
	}
	return u, k, nil
}

// TouchSSHKey records last use. Like TouchToken, failures are not worth
// failing an operation over, so callers may ignore the error.
func (s *Store) TouchSSHKey(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE user_ssh_keys SET last_used_at = now() WHERE id = $1`, id)
	return err
}
