package sshserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/gitserver"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ---------------------------------------------------------------- doubles

type fakeKeys struct {
	mu sync.Mutex
	// byFingerprint maps a fingerprint to the row the database would return.
	byFingerprint map[string]struct {
		user *store.User
		key  *store.SSHKey
	}
	touched []int64
}

func newFakeKeys() *fakeKeys {
	return &fakeKeys{byFingerprint: map[string]struct {
		user *store.User
		key  *store.SSHKey
	}{}}
}

func (f *fakeKeys) register(userID int64, username string, keyID int64, fingerprint, publicKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byFingerprint[fingerprint] = struct {
		user *store.User
		key  *store.SSHKey
	}{
		user: &store.User{ID: userID, Username: username},
		key:  &store.SSHKey{ID: keyID, UserID: userID, Fingerprint: fingerprint, PublicKey: publicKey},
	}
}

func (f *fakeKeys) LookupSSHKey(_ context.Context, fingerprint string) (*store.User, *store.SSHKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.byFingerprint[fingerprint]
	if !ok {
		return nil, nil, store.ErrNotFound
	}
	return row.user, row.key, nil
}

func (f *fakeKeys) TouchSSHKey(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, id)
	return nil
}

func (f *fakeKeys) touchedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.touched...)
}

type gitCall struct {
	user        string
	service     gitserver.Service
	kind        string
	namespace   string
	name        string
	gitProtocol string
}

type fakeGit struct {
	mu     sync.Mutex
	calls  []gitCall
	reply  string
	failed error
}

func (g *fakeGit) ServeGit(_ context.Context, user *store.User, service gitserver.Service,
	kind, namespace, name, gitProtocol string, streams gitserver.Streams,
) error {
	g.mu.Lock()
	g.calls = append(g.calls, gitCall{user.Username, service, kind, namespace, name, gitProtocol})
	reply, failure := g.reply, g.failed
	g.mu.Unlock()

	if failure != nil {
		return failure
	}
	fmt.Fprint(streams.Out, reply)
	return nil
}

func (g *fakeGit) recorded() []gitCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gitCall(nil), g.calls...)
}

// visibleError mimics api.GitAccessError: an error whose text may be shown.
type visibleError struct{ msg string }

func (e *visibleError) Error() string         { return e.msg }
func (e *visibleError) ClientMessage() string { return e.msg }

// ---------------------------------------------------------------- harness

type harness struct {
	addr string
	keys *fakeKeys
	git  *fakeGit
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	keys, git := newFakeKeys(), &fakeGit{reply: "PACK"}
	srv, err := New(Options{
		HostKeyPath: filepath.Join(t.TempDir(), "host_ed25519"),
		IdleTimeout: 30 * time.Second,
	}, keys, git)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return &harness{addr: l.Addr().String(), keys: keys, git: git}
}

// clientKey generates a fresh keypair and returns the signer plus the
// authorized_keys line the server would have stored for it.
func clientKey(t *testing.T) (gossh.Signer, string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	authorized := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub)))
	return signer, authorized, auth.SSHKeyFingerprint(sshPub)
}

func (h *harness) dial(t *testing.T, signer gossh.Signer) (*gossh.Client, error) {
	t.Helper()
	client, err := gossh.Dial("tcp", h.addr, &gossh.ClientConfig{
		User:            "git",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // test client
		Timeout:         5 * time.Second,
	})
	if err == nil {
		t.Cleanup(func() { _ = client.Close() })
	}
	return client, err
}

// run executes one command and reports stdout, stderr and the exit status.
func run(t *testing.T, client *gossh.Client, command string, env map[string]string) (string, string, int) {
	t.Helper()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess.Close()
	for k, v := range env {
		if err := sess.Setenv(k, v); err != nil {
			t.Fatalf("setenv %s: %v", k, err)
		}
	}
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr

	err = sess.Run(command)
	status := 0
	var exit *gossh.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		status = exit.ExitStatus()
	default:
		t.Fatalf("run %q: %v", command, err)
	}
	return stdout.String(), stderr.String(), status
}

// ------------------------------------------------------------------ tests

func TestAuthRejectsUnregisteredKey(t *testing.T) {
	h := newHarness(t)
	signer, _, _ := clientKey(t)

	if _, err := h.dial(t, signer); err == nil {
		t.Fatal("dial with an unregistered key succeeded")
	}
}

func TestAuthRejectsAKeyThatDoesNotMatchItsStoredMaterial(t *testing.T) {
	h := newHarness(t)
	signer, _, fingerprint := clientKey(t)
	_, otherAuthorized, _ := clientKey(t)
	// A row whose fingerprint matches but whose key material does not: the
	// server must compare the bytes, not trust the digest.
	h.keys.register(1, "alice", 10, fingerprint, otherAuthorized)

	if _, err := h.dial(t, signer); err == nil {
		t.Fatal("dial succeeded despite a key/fingerprint mismatch")
	}
}

func TestUploadPackReachesTheGitLayer(t *testing.T) {
	h := newHarness(t)
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(7, "alice", 42, fingerprint, authorized)

	client, err := h.dial(t, signer)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	stdout, stderr, status := run(t, client, "git-upload-pack 'alice/mymodel.git'",
		map[string]string{"GIT_PROTOCOL": "version=2"})

	if status != 0 {
		t.Fatalf("exit status = %d, stderr = %q", status, stderr)
	}
	if stdout != "PACK" {
		t.Errorf("stdout = %q, want the git service's output", stdout)
	}
	calls := h.git.recorded()
	if len(calls) != 1 {
		t.Fatalf("git calls = %+v, want exactly one", calls)
	}
	want := gitCall{"alice", gitserver.UploadPack, "model", "alice", "mymodel", "version=2"}
	if calls[0] != want {
		t.Errorf("call = %+v, want %+v", calls[0], want)
	}

	// last_used_at is recorded asynchronously.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ids := h.keys.touchedIDs(); len(ids) == 1 && ids[0] == 42 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("TouchSSHKey was not called with the key id; got %v", h.keys.touchedIDs())
}

func TestReceivePackReachesTheGitLayerWithoutAProtocolRequest(t *testing.T) {
	h := newHarness(t)
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(7, "alice", 42, fingerprint, authorized)

	client, err := h.dial(t, signer)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, stderr, status := run(t, client, "git-receive-pack 'datasets/alice/corpus'", nil); status != 0 {
		t.Fatalf("exit status = %d, stderr = %q", status, stderr)
	}
	calls := h.git.recorded()
	want := gitCall{"alice", gitserver.ReceivePack, "dataset", "alice", "corpus", ""}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("calls = %+v, want one %+v", calls, want)
	}
}

func TestDisallowedCommandsNeverReachTheGitLayer(t *testing.T) {
	commands := []string{
		"bash",
		"rm -rf /",
		"git-upload-archive 'alice/mymodel'",
		"git-upload-pack 'alice/mymodel' --upload-pack=/bin/sh",
		"git-upload-pack '../../etc/passwd'",
		"scp -f /etc/passwd",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			h := newHarness(t)
			signer, authorized, fingerprint := clientKey(t)
			h.keys.register(7, "alice", 42, fingerprint, authorized)

			client, err := h.dial(t, signer)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			stdout, stderr, status := run(t, client, command, nil)
			if status == 0 {
				t.Errorf("exit status = 0 for %q; it should fail", command)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
			if strings.TrimSpace(stderr) == "" {
				t.Error("stderr is empty; the client gets no explanation")
			}
			if calls := h.git.recorded(); len(calls) != 0 {
				t.Errorf("git layer was reached with %+v", calls)
			}
		})
	}
}

func TestShellSessionIsRefused(t *testing.T) {
	h := newHarness(t)
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(7, "alice", 42, fingerprint, authorized)

	client, err := h.dial(t, signer)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess.Close()

	// A PTY is refused outright, before any shell can be asked for.
	if err := sess.RequestPty("xterm", 40, 80, gossh.TerminalModes{}); err == nil {
		t.Error("the server granted a PTY")
	}

	sess2, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer sess2.Close()
	var stderr bytes.Buffer
	sess2.Stderr = &stderr
	if err := sess2.Shell(); err != nil {
		t.Fatalf("shell request: %v", err)
	}
	_ = sess2.Wait()
	if !strings.Contains(stderr.String(), "does not provide shell access") {
		t.Errorf("stderr = %q, want the no-shell greeting", stderr.String())
	}
	if calls := h.git.recorded(); len(calls) != 0 {
		t.Errorf("git layer was reached with %+v", calls)
	}
}

func TestClientVisibleRefusalIsRelayed(t *testing.T) {
	h := newHarness(t)
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(7, "alice", 42, fingerprint, authorized)
	h.git.failed = &visibleError{msg: "repository alice/secret not found"}

	client, err := h.dial(t, signer)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, stderr, status := run(t, client, "git-upload-pack 'alice/secret'", nil)
	if status == 0 {
		t.Error("exit status = 0 for a refused request")
	}
	if !strings.Contains(stderr, "repository alice/secret not found") {
		t.Errorf("stderr = %q, want the refusal message", stderr)
	}
}

func TestInternalErrorIsNotLeakedToTheClient(t *testing.T) {
	h := newHarness(t)
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(7, "alice", 42, fingerprint, authorized)
	h.git.failed = errors.New("dial tcp 10.0.0.5:5432: connection refused")

	client, err := h.dial(t, signer)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, stderr, status := run(t, client, "git-upload-pack 'alice/mymodel'", nil)
	if status == 0 {
		t.Error("exit status = 0 for a failed request")
	}
	if strings.Contains(stderr, "10.0.0.5") || strings.Contains(stderr, "connection refused") {
		t.Errorf("stderr = %q, which leaks internal detail", stderr)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("stderr is empty; the client gets no explanation at all")
	}
}

func TestLoadOrCreateHostKeyIsStableAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "host_ed25519")
	first, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateHostKey: %v", err)
	}
	second, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateHostKey again: %v", err)
	}
	if gossh.FingerprintSHA256(first.PublicKey()) != gossh.FingerprintSHA256(second.PublicKey()) {
		t.Error("the host key changed on the second load; clients would see a host key mismatch")
	}
}
