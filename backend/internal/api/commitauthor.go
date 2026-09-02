// Who a server-side write is attributed to.

package api

import (
	"context"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

const (
	// serverCommitName and serverCommitEmail are the identity a commit this
	// server builds falls back to when the request carries no authenticated
	// user. That is not the ordinary case for the API -- every write endpoint
	// is behind a token -- but a signature is not optional in git, so there
	// has to be one, and it says plainly that the server made the commit
	// rather than inventing a person who did.
	serverCommitName  = "thinkingface"
	serverCommitEmail = "noreply@thinkingface.local"
)

// commitAuthor builds the signature every server-side commit, tag and squash
// is written with: the authenticated caller when there is one, the server's
// own identity otherwise.
//
// An account with no email address keeps the fallback address rather than
// getting an empty one: git will store an empty email, and a history full of
// "<>" is worse than one that says where the commit was made.
//
// Same shape as the fallback gitrepo uses for its own writes, deliberately
// not shared with it: this package decides who an *HTTP request* is from, and
// gitrepo has no notion of a request.
func commitAuthor(ctx context.Context) gitrepo.Signature {
	sig := gitrepo.Signature{Name: serverCommitName, Email: serverCommitEmail, When: time.Now()}
	if user := currentUser(ctx); user != nil {
		sig.Name = user.Username
		if user.Email != "" {
			sig.Email = user.Email
		}
	}
	return sig
}
