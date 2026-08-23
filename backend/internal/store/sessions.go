package store

import "context"

// BumpSessionEpoch invalidates every session cookie already issued for a
// user. The cookie is stateless (auth.Sessions signs userID.epoch.exp), so
// this counter is the only revocation handle there is: logout and any
// credential change increment it, and auth.Sessions.Verify's caller compares
// the value carried by the cookie against the stored one.
func (s *Store) BumpSessionEpoch(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx,
		`UPDATE users SET session_epoch = session_epoch + 1 WHERE id = $1`, userID)
	return err
}
