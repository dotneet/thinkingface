// Package sshserver serves git over SSH: public key authentication against
// the keys users registered at /settings/ssh-keys, and nothing else.
//
// The security posture is deliberately narrow:
//
//   - Public key authentication only. There is no password callback, so no
//     amount of guessing reaches an account.
//   - Exactly two commands may run, git-upload-pack and git-receive-pack, on
//     a repository path that has to match ns/name (command.go). No shell, no
//     PTY, no subsystem (so no sftp/scp), no port forwarding.
//   - The path the client types never reaches a command line. It is parsed
//     into (kind, namespace, name), looked up in the database, and the git
//     service is started against the *stored* storage path.
//   - Authorization is not implemented here. Resolution and the read/write
//     checks are delegated to the same code the HTTP transport uses, so a
//     private repository is exactly as private over SSH as over HTTPS.
package sshserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	gssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/gitserver"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// Keys resolves an offered public key to the account that registered it.
// Implemented by *store.Store.
type Keys interface {
	LookupSSHKey(ctx context.Context, fingerprint string) (*store.User, *store.SSHKey, error)
	TouchSSHKey(ctx context.Context, id int64) error
}

// Git runs one git service for an authenticated user. Implemented by
// *api.Server, which is what keeps repository resolution, the private-repo
// visibility rule and the write check identical across both transports.
//
// An error whose type also has ClientMessage() string is relayed to the user;
// anything else is logged and reported as a generic failure, so an internal
// error never becomes a hint about a repository the user cannot see.
type Git interface {
	ServeGit(ctx context.Context, user *store.User, service gitserver.Service,
		kind, namespace, name, gitProtocol string, streams gitserver.Streams) error
}

// clientMessager marks an error as safe to show to the SSH client.
type clientMessager interface {
	ClientMessage() string
}

type Options struct {
	// Addr is the listen address, e.g. ":2222".
	Addr string
	// HostKeyPath is where the server's identity lives; see
	// LoadOrCreateHostKey.
	HostKeyPath string
	// IdleTimeout closes a connection that goes quiet. Zero disables it.
	// Clones of large repositories stream continuously, so this only bites
	// abandoned connections.
	IdleTimeout time.Duration
	// AuthRateLimitPerMinute caps *failed* public-key attempts per client
	// address per minute, the SSH counterpart of the HTTP auth guard (it is
	// fed from the same TF_AUTH_RATE_LIMIT_PER_MIN). Zero disables it, which
	// is only sensible in tests: without it an unauthenticated peer drives a
	// LookupSSHKey database round trip per authentication attempt, six per
	// connection, at whatever rate it opens connections.
	AuthRateLimitPerMinute int
	// MaxUnauthenticatedConns caps how many connections may sit in the
	// pre-authentication phase at once. Zero means
	// DefaultMaxUnauthenticatedConns. The slot is released as soon as a
	// connection authenticates, so this never limits real work.
	MaxUnauthenticatedConns int
}

type Server struct {
	keys Keys
	git  Git
	srv  *gssh.Server

	budget *authBudget
	gate   *connGate
	// touches bounds the fire-and-forget TouchSSHKey writes; see
	// recordKeyUse.
	touches chan struct{}
}

// contextKey namespaces the values the auth callback hands to the session
// handler on the same connection.
type contextKey string

const (
	ctxKeyUser  contextKey = "thinkingface.user"
	ctxKeyKeyID contextKey = "thinkingface.ssh_key_id"
	// ctxKeyConnSlot carries the connection's gate slot so authentication can
	// hand it back the moment the peer stops being unauthenticated.
	ctxKeyConnSlot contextKey = "thinkingface.conn_slot"
)

func New(opts Options, keys Keys, git Git) (*Server, error) {
	hostKey, err := LoadOrCreateHostKey(opts.HostKeyPath)
	if err != nil {
		return nil, err
	}
	s := &Server{
		keys:    keys,
		git:     git,
		budget:  newAuthBudget(opts.AuthRateLimitPerMinute),
		gate:    newConnGate(opts.MaxUnauthenticatedConns),
		touches: make(chan struct{}, touchConcurrency),
	}
	s.srv = &gssh.Server{
		Addr:        opts.Addr,
		HostSigners: []gssh.Signer{hostKey},
		Version:     "thinkingface",
		IdleTimeout: opts.IdleTimeout,
		Handler:     s.handleSession,

		// Every connection passes the gate before a single byte of the SSH
		// handshake is read, which is the only place a peer can be turned
		// away before it costs anything.
		ConnCallback: s.admit,

		PublicKeyHandler: s.authenticate,
		// No PasswordHandler and no KeyboardInteractiveHandler: with
		// PublicKeyHandler set, gliderlabs offers publickey alone.

		// Deny everything that is not a git exec. PtyCallback returning false
		// refuses the pty-req; the nil forwarding callbacks deny forwarding;
		// the empty subsystem map refuses sftp.
		PtyCallback:       func(gssh.Context, gssh.Pty) bool { return false },
		SubsystemHandlers: map[string]gssh.SubsystemHandler{},
		ChannelHandlers: map[string]gssh.ChannelHandler{
			"session": gssh.DefaultSessionHandler,
		},
		RequestHandlers: map[string]gssh.RequestHandler{},
	}
	return s, nil
}

// Addr reports the configured listen address.
func (s *Server) Addr() string { return s.srv.Addr }

func (s *Server) ListenAndServe() error { return s.srv.ListenAndServe() }

// Serve accepts connections on an already-open listener. Used by tests, which
// need an ephemeral port.
func (s *Server) Serve(l net.Listener) error { return s.srv.Serve(l) }

// Shutdown stops accepting connections and waits for the in-flight ones,
// then closes whatever is left.
//
// Close runs on *every* path, including the timeout. gliderlabs' Shutdown
// waits indefinitely for connections and returns ctx.Err() when the budget
// runs out — it does not close anything it was still waiting on — so
// returning there left the listeners and every stuck clone open, and the
// process hung on an SSH session that had gone quiet instead of exiting.
// The deadline is the whole point of passing one.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	if cerr := s.srv.Close(); err == nil {
		err = cerr
	}
	return err
}

// admit is the gate every connection passes before the SSH handshake begins.
// Returning nil makes gliderlabs close the connection immediately, which is
// the cheapest possible refusal.
func (s *Server) admit(ctx gssh.Context, conn net.Conn) net.Conn {
	slot, ok := s.gate.acquire()
	if !ok {
		slog.Warn("ssh: refused a connection, too many unauthenticated connections in flight",
			"remote", addrKey(conn.RemoteAddr()), "limit", cap(s.gate.sem))
		return nil
	}
	ctx.SetValue(ctxKeyConnSlot, slot)
	return &guardedConn{Conn: conn, slot: slot}
}

// ErrServerClosed is returned by ListenAndServe after Shutdown.
var ErrServerClosed = gssh.ErrServerClosed

// authenticate resolves the offered key to a user.
//
// The fingerprint is only an index: the stored key material is re-parsed and
// compared byte for byte, so authentication never rests on a digest alone.
//
// The budget check comes *before* the lookup, which is the whole reason it
// exists here rather than around it: x/crypto lets a peer make six
// authentication attempts per connection, and each one that reached the
// database was a free query for anybody who could open a socket.
func (s *Server) authenticate(ctx gssh.Context, offered gssh.PublicKey) bool {
	addr := addrKey(ctx.RemoteAddr())
	if !s.budget.allow(addr) {
		slog.Warn("ssh: too many failed authentication attempts, refusing without a lookup", "remote", addr)
		return false
	}

	fingerprint := auth.SSHKeyFingerprint(offered)
	user, key, err := s.keys.LookupSSHKey(ctx, fingerprint)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			// A database failure is the server's problem, not the client's:
			// charging the address for it would let one outage lock out every
			// user who kept trying.
			slog.Error("ssh: look up public key", "fingerprint", fingerprint, "error", err)
			return false
		}
		s.budget.penalize(addr)
		return false
	}

	stored, _, _, _, err := gossh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		slog.Error("ssh: stored public key is unreadable", "key_id", key.ID, "error", err)
		return false
	}
	if !gssh.KeysEqual(offered, stored) {
		// Only reachable through a SHA-256 collision or a corrupted row, but
		// the check costs nothing and removes the digest from the trust path.
		slog.Warn("ssh: offered key does not match the stored key for its fingerprint",
			"fingerprint", fingerprint, "key_id", key.ID)
		s.budget.penalize(addr)
		return false
	}

	// Authenticated: forget the address's failures (an agent that walked
	// through three keys first must leave no mark) and hand back the
	// unauthenticated-connection slot, so a long clone never occupies one.
	s.budget.reset(addr)
	if slot, ok := ctx.Value(ctxKeyConnSlot).(*connSlot); ok {
		slot.release()
	}

	ctx.SetValue(ctxKeyUser, user)
	ctx.SetValue(ctxKeyKeyID, key.ID)
	return true
}

// recordKeyUse stamps a key's last-used time without making the client wait.
//
// Detached, but neither unbounded nor deadline-free — the two things that made
// the previous `go s.keys.TouchSSHKey(context.Background(), id)` a liability.
// A stalled database turned every session into a goroutine holding a
// connection forever, and nothing capped how many. The shape matches
// detachedWrite in internal/api, which exists for exactly this.
//
// Dropping the write when the semaphore is full is deliberate: last-used is
// advisory, and shedding it is strictly better than queueing behind a database
// that is already in trouble.
func (s *Server) recordKeyUse(keyID int64) {
	select {
	case s.touches <- struct{}{}:
	default:
		slog.Debug("ssh: dropped a key-use record, too many in flight", "key_id", keyID)
		return
	}
	go func() {
		defer func() { <-s.touches }()
		ctx, cancel := context.WithTimeout(context.Background(), touchTimeout)
		defer cancel()
		if err := s.keys.TouchSSHKey(ctx, keyID); err != nil {
			slog.Debug("ssh: record key use", "key_id", keyID, "error", err)
		}
	}()
}

func (s *Server) handleSession(sess gssh.Session) {
	user, _ := sess.Context().Value(ctxKeyUser).(*store.User)
	if user == nil {
		// Unreachable while PublicKeyHandler is the only auth method, but a
		// session without an identity must never run a git service.
		fail(sess, "authentication required")
		return
	}
	if sess.Subsystem() != "" {
		fail(sess, "subsystems are not available; this server offers git over SSH only")
		return
	}

	raw := sess.RawCommand()
	if strings.TrimSpace(raw) == "" {
		// The GitHub-style greeting: authentication worked, there is just
		// nothing to log in to.
		fail(sess, "Hi "+user.Username+"! You have successfully authenticated, but thinkingface does not provide shell access.")
		return
	}

	req, err := ParseCommand(raw)
	if err != nil {
		slog.Info("ssh: rejected command", "user", user.Username, "error", err)
		fail(sess, strings.TrimPrefix(err.Error(), "sshserver: "))
		return
	}

	// Detached from the session so a slow write never delays the clone, and
	// recorded before the permission check: the key was used either way.
	if keyID, ok := sess.Context().Value(ctxKeyKeyID).(int64); ok {
		s.recordKeyUse(keyID)
	}

	streams := gitserver.Streams{In: sess, Out: sess, Err: sess.Stderr()}
	err = s.git.ServeGit(sess.Context(), user, req.Service, req.Kind, req.Namespace, req.Name,
		gitProtocol(sess.Environ()), streams)
	if err != nil {
		var visible clientMessager
		if errors.As(err, &visible) {
			slog.Info("ssh: refused", "user", user.Username, "repo", req.FullName(),
				"service", req.Service, "reason", visible.ClientMessage())
			fail(sess, visible.ClientMessage())
			return
		}
		slog.Error("ssh: git service failed", "user", user.Username, "repo", req.FullName(),
			"service", req.Service, "error", err)
		fail(sess, "the server could not complete this request")
		return
	}
	_ = sess.Exit(0)
}

// fail writes one line to the client's stderr and exits non-zero, which is
// what git surfaces as "fatal: <line>".
func fail(sess gssh.Session, message string) {
	fmt.Fprintln(sess.Stderr(), message)
	_ = sess.Exit(1)
}

// gitProtocol picks the client's protocol version out of the session
// environment. git sends it as an env request before the exec; everything
// else the client tries to set is ignored.
func gitProtocol(environ []string) string {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "GIT_PROTOCOL="); ok {
			return v
		}
	}
	return ""
}
