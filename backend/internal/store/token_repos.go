package store

import (
	"context"
	"fmt"
	"time"
)

// Repository-restricted access tokens (docs/dev/agent-features.md §3). The
// table and the reasoning behind its shape are in migration
// 0008_access_token_repos.sql; the rules a restricted token is held to live in
// the api package (authz.go), which is the only consumer of what is here.

// TokenRepo is one repository an access token is restricted to.
//
// RepoID is what enforcement matches on and is 0 once the repository has been
// deleted. Kind is the singular stored spelling ("dataset" / "model");
// Namespace and Name are the repository's current name when it still exists,
// and the name it had when the token was minted otherwise.
type TokenRepo struct {
	RepoID    int64
	Kind      string
	Namespace string
	Name      string
}

// TokenRestriction is what an authenticating request needs to know about a
// token's repository list.
type TokenRestriction struct {
	// Restricted is true when the token names any repository at all --
	// including ones that have since been deleted. It is deliberately not
	// derived from len(RepoIDs): a restricted token whose repositories are
	// all gone has no ids left and must still be refused everywhere.
	Restricted bool
	// RepoIDs are the repositories the token may change, deleted ones
	// omitted.
	RepoIDs []int64
}

// CreateTokenWithRepos is CreateToken for a token restricted to repos. The
// token row and its repository rows are written in one transaction, so a
// failure can never leave behind a token that exists without the restriction
// it was minted with -- which would be a token with more power than the
// caller asked for. An empty repos is an ordinary unrestricted token.
//
// Every entry must carry a RepoID: the caller has already resolved and
// authorised each repository, and this method does not second-guess that.
func (s *Store) CreateTokenWithRepos(ctx context.Context, userID int64, name, scope, tokenHash string, expiresAt *time.Time, repos []TokenRepo) (*AccessToken, error) {
	for _, r := range repos {
		if r.RepoID <= 0 {
			return nil, fmt.Errorf("token repository %s/%s/%s has no id", r.Kind, r.Namespace, r.Name)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	t := &AccessToken{}
	err = tx.QueryRow(ctx,
		`INSERT INTO access_tokens (user_id, name, token_hash, scope, expires_at) VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, user_id, name, scope, last_used_at, expires_at, created_at`,
		userID, name, tokenHash, scope, expiresAt,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.LastUsedAt, &t.ExpiresAt, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert token: %w", err)
	}
	for _, r := range repos {
		if _, err := tx.Exec(ctx,
			`INSERT INTO access_token_repos (token_id, repo_id, repo_kind, namespace, name)
			 VALUES ($1, $2, $3, $4, $5)`,
			t.ID, r.RepoID, r.Kind, r.Namespace, r.Name); err != nil {
			if s.d.isUniqueViolation(err) {
				return nil, fmt.Errorf("token repository %s/%s/%s listed twice: %w", r.Kind, r.Namespace, r.Name, ErrConflict)
			}
			return nil, fmt.Errorf("insert token repository: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	t.Repos = append([]TokenRepo{}, repos...)
	return t, nil
}

// LookupTokenRestriction loads the repository restriction of one token. It
// is called on every token-authenticated request, right after LookupToken; an
// error must be treated as a failed authentication, never as "unrestricted".
func (s *Store) LookupTokenRestriction(ctx context.Context, tokenID int64) (TokenRestriction, error) {
	rows, err := s.db.Query(ctx,
		`SELECT repo_id FROM access_token_repos WHERE token_id = $1`, tokenID)
	if err != nil {
		return TokenRestriction{}, err
	}
	defer rows.Close()
	var out TokenRestriction
	for rows.Next() {
		var id *int64
		if err := rows.Scan(&id); err != nil {
			return TokenRestriction{}, err
		}
		out.Restricted = true
		if id != nil {
			out.RepoIDs = append(out.RepoIDs, *id)
		}
	}
	if err := rows.Err(); err != nil {
		return TokenRestriction{}, err
	}
	return out, nil
}

// tokenReposFor loads the repository lists of every token userID holds, keyed
// by token id, for ListTokens. The current name is preferred over the one
// recorded at mint time, so a renamed or transferred repository is listed
// where it is now.
func (s *Store) tokenReposFor(ctx context.Context, userID int64) (map[int64][]TokenRepo, error) {
	rows, err := s.db.Query(ctx,
		`SELECT atr.token_id, COALESCE(r.id, 0),
		        COALESCE(r.kind, atr.repo_kind), COALESCE(n.name, atr.namespace), COALESCE(r.name, atr.name)
		 FROM access_token_repos atr
		 JOIN access_tokens t ON t.id = atr.token_id
		 LEFT JOIN repositories r ON r.id = atr.repo_id
		 LEFT JOIN namespaces n ON n.id = r.namespace_id
		 WHERE t.user_id = $1
		 ORDER BY atr.token_id, 3, 4, 5`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]TokenRepo{}
	for rows.Next() {
		var tokenID int64
		var r TokenRepo
		if err := rows.Scan(&tokenID, &r.RepoID, &r.Kind, &r.Namespace, &r.Name); err != nil {
			return nil, err
		}
		out[tokenID] = append(out[tokenID], r)
	}
	return out, rows.Err()
}
