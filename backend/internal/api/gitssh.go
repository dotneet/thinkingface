// The git-over-SSH entry point. internal/sshserver owns the SSH protocol —
// host key, public key authentication, which commands may run — and calls in
// here for everything that decides *what a user may do*, so the SSH transport
// and the smart HTTP transport share one answer to that question rather than
// two implementations of it.

package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/dotneet/thinkingface/backend/internal/gitserver"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// GitAccessError is a refusal whose text is safe to show the client: it says
// no more than the equivalent HTTP response would. internal/sshserver relays
// ClientMessage() and swallows every other error as a generic failure, so
// anything that would leak internal state must not be reported this way.
type GitAccessError struct{ Message string }

func (e *GitAccessError) Error() string         { return e.Message }
func (e *GitAccessError) ClientMessage() string { return e.Message }

func refuse(message string) error { return &GitAccessError{Message: message} }

// ServeGit runs one git service for an SSH-authenticated user.
//
// kind/namespace/name have already been validated by the SSH layer as
// well-formed identifiers; this function is what turns them into a repository
// and decides whether the user may read or write it. The repository's
// storage path -- never the client's text -- is what reaches git.
func (s *Server) ServeGit(ctx context.Context, user *store.User, service gitserver.Service,
	kind, namespace, name, gitProtocol string, streams gitserver.Streams,
) error {
	if user == nil {
		return refuse("authentication required")
	}
	// Belt and braces. store.LookupSSHKey already refuses to resolve a key
	// whose owner is suspended, so an offboarded account never authenticates
	// far enough to reach this -- but ServeGit takes the user as an argument
	// from another package, and the whole point of the suspension switch is
	// that no path may be the one that forgot.
	if user.Disabled() {
		return refuse("this account has been disabled")
	}
	// Re-use the HTTP identity keys so canRead / canWrite behave exactly as
	// they do for a request. An SSH key is a full-strength credential like a
	// password login, so it carries the write scope; whether the user may
	// actually write to *this* repository is still canWrite's decision.
	ctx = context.WithValue(ctx, ctxKeyUser, user)
	ctx = context.WithValue(ctx, ctxKeyScope, "write")

	repo, err := s.resolveRepo(ctx, kind, namespace, name)
	if err != nil {
		var moved *repoMovedError
		switch {
		case errors.As(err, &moved):
			// resolveRepo only reports a move to someone allowed to see the
			// destination, so naming it here leaks nothing.
			return refuse("repository " + namespace + "/" + name + " has moved to " + moved.Namespace + "/" + moved.Name)
		case errors.Is(err, store.ErrNotFound):
			return refuse(gitNotFoundMessage(namespace, name))
		default:
			return err
		}
	}
	if service == gitserver.ReceivePack {
		if !s.canWriteIgnoringArchive(ctx, repo) {
			return refuse("you do not have write access to " + repo.FullName())
		}
		// After the permission check, so an archived repository does not
		// answer differently to someone who could not write to it anyway.
		if repo.Archived() {
			return refuse(repo.FullName() + " is archived and read-only")
		}
		return s.sshReceivePack(ctx, repo, gitProtocol, streams)
	}

	if err := s.ensureRepoLocal(ctx, repo); err != nil {
		return err
	}
	return s.gitHTTP.ServeSSH(ctx, repo.StoragePath, gitserver.UploadPack, gitProtocol, streams)
}

// sshReceivePack mirrors handleReceivePack: snapshot the branch tips, run the
// service, adopt the pushed state before anything re-opens the repository,
// then schedule the same post-push indexing an HTTP push schedules. A push
// that lands over SSH has to reach the sync worker, or its files, card and
// experiment index would silently never update.
func (s *Server) sshReceivePack(ctx context.Context, repo *store.Repo, gitProtocol string, streams gitserver.Streams) error {
	if err := s.ensureRepoLocal(ctx, repo); err != nil {
		return err
	}
	before, err := s.gitHTTP.HeadsAfterPush(repo.StoragePath)
	if err != nil {
		return err
	}

	if err := s.gitHTTP.ServeSSH(ctx, repo.StoragePath, gitserver.ReceivePack, gitProtocol, streams); err != nil {
		return err
	}

	// Before HeadsAfterPush, for the same reason as the HTTP path: that call
	// re-opens the repository, and without the adopted state the
	// materialisation would re-download the pack this push just uploaded.
	detached := context.WithoutCancel(ctx)
	s.adoptAfterPush(detached, repo)

	after, err := s.gitHTTP.HeadsAfterPush(repo.StoragePath)
	if err != nil {
		// The push itself succeeded and the client has already been told so;
		// only the follow-up indexing is lost.
		slog.Error("read refs after ssh push", "repo", repo.FullName(), "error", err)
		return nil
	}
	for branch, newSHA := range after {
		if before[branch] == newSHA {
			continue
		}
		if err := s.sync.Enqueue(detached, repo.ID, branch, before[branch], newSHA); err != nil {
			slog.Error("schedule sync after ssh push", "repo", repo.FullName(), "branch", branch, "error", err)
		}
	}
	return nil
}

func gitNotFoundMessage(ns, name string) string {
	return "repository " + ns + "/" + name + " not found"
}
