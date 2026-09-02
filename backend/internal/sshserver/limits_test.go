package sshserver

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// ------------------------------------------------------------ auth budget

func TestAuthBudget_BlocksAnAddressAfterRepeatedFailures(t *testing.T) {
	b := newAuthBudget(4)
	now := time.Now()
	b.now = func() time.Time { return now }

	for i := 0; i < 4; i++ {
		if !b.allow("10.0.0.1") {
			t.Fatalf("attempt %d refused while the budget still had tokens", i)
		}
		b.penalize("10.0.0.1")
	}
	if b.allow("10.0.0.1") {
		t.Fatal("an address that used its whole budget must be refused before any lookup")
	}
	// Every other source is unaffected: the budget is per address, so one
	// noisy peer cannot lock out the rest of the network.
	if !b.allow("10.0.0.2") {
		t.Fatal("an unrelated address was caught by another's budget")
	}

	// And it recovers: half a minute at 4/min refills two tokens.
	now = now.Add(30 * time.Second)
	if !b.allow("10.0.0.1") {
		t.Fatal("the budget never refilled")
	}
}

// Only failures cost anything. An agent that offers three unregistered keys
// before the right one is completely normal, and must leave no mark.
func TestAuthBudget_SuccessClearsTheFailuresThatPrecededIt(t *testing.T) {
	b := newAuthBudget(4)
	for i := 0; i < 3; i++ {
		b.allow("10.0.0.1")
		b.penalize("10.0.0.1")
	}
	b.reset("10.0.0.1")
	for i := 0; i < 4; i++ {
		if !b.allow("10.0.0.1") {
			t.Fatalf("attempt %d refused after a success reset the address", i)
		}
		b.penalize("10.0.0.1")
	}
}

// Zero is "off", which is what TF_AUTH_RATE_LIMIT_PER_MIN=0 and the transport
// tests want.
func TestAuthBudget_ZeroRateNeverRefuses(t *testing.T) {
	b := newAuthBudget(0)
	for i := 0; i < 100; i++ {
		b.penalize("10.0.0.1")
	}
	if !b.allow("10.0.0.1") {
		t.Fatal("a disabled budget refused an attempt")
	}
}

func TestAuthBudget_DropsIdleBuckets(t *testing.T) {
	b := newAuthBudget(4)
	now := time.Now()
	b.now = func() time.Time { return now }
	b.allow("10.0.0.1")
	b.penalize("10.0.0.1")

	// The sweep bounds the map against a spray of distinct source addresses.
	now = now.Add(2 * authBucketIdle)
	b.allow("10.0.0.2")

	b.mu.Lock()
	_, stillThere := b.buckets["10.0.0.1"]
	b.mu.Unlock()
	if stillThere {
		t.Error("an idle bucket was never swept: the map grows without bound")
	}
}

// ------------------------------------------------------------- conn gate

func TestConnGate_RefusesBeyondItsLimitAndRecoversOnRelease(t *testing.T) {
	g := newConnGate(2, 2)
	first, ok := g.acquire("10.0.0.1")
	if !ok {
		t.Fatal("the first slot was refused")
	}
	if _, ok := g.acquire("10.0.0.2"); !ok {
		t.Fatal("the second slot was refused")
	}
	if _, ok := g.acquire("10.0.0.3"); ok {
		t.Fatal("a third slot was handed out past a limit of 2")
	}

	// Releasing twice is normal — authentication and the connection closing
	// both do it — and must free exactly one slot.
	first.release()
	first.release()
	if _, ok := g.acquire("10.0.0.3"); !ok {
		t.Fatal("releasing a slot did not free it")
	}
	if _, ok := g.acquire("10.0.0.4"); ok {
		t.Fatal("a double release freed two slots")
	}
}

// The finding this pins: a gate with only a process-wide dimension is a
// cheaper denial of service than the unbounded accept loop it replaced. One
// host opens the whole ceiling's worth of connections and holds them — and
// because gliderlabs arms IdleTimeout only on the first read or write, a peer
// that never speaks holds them for the entire idle timeout — after which every
// other client on the fleet is refused at admit. The per-address dimension is
// what makes that impossible: a flooding address exhausts its own share.
func TestConnGate_OneAddressCannotStarveAnother(t *testing.T) {
	const global, perAddr = 8, 2
	g := newConnGate(global, perAddr)

	held := 0
	for i := 0; i < global; i++ {
		if _, ok := g.acquire("10.0.0.1"); ok {
			held++
		}
	}
	if held != perAddr {
		t.Fatalf("one address held %d of %d slots, want at most its own share of %d",
			held, global, perAddr)
	}
	// The point of all of it: somebody else can still get in.
	if _, ok := g.acquire("10.0.0.2"); !ok {
		t.Fatal("an unrelated address was refused after one host flooded the gate: " +
			"the gate locks the fleet out, which is worse than the unbounded accept loop it replaced")
	}
}

// A per-address cap above the global one would silently disable the dimension
// that matters, so it is clamped rather than honoured.
func TestConnGate_ClampsAPerAddressCapAboveTheGlobalOne(t *testing.T) {
	g := newConnGate(2, 100)
	for i := 0; i < 2; i++ {
		if _, ok := g.acquire("10.0.0.1"); !ok {
			t.Fatalf("slot %d was refused below the global limit", i)
		}
	}
	if _, ok := g.acquire("10.0.0.1"); ok {
		t.Fatal("one address exceeded the global limit")
	}
}

// Slots come back, so the map that carries the per-address counts is bounded
// by the number of connections in flight rather than by the number of distinct
// addresses that have ever connected — which is what a spray of source
// addresses would otherwise grow without bound.
func TestConnGate_ForgetsAnAddressOnceItHoldsNothing(t *testing.T) {
	g := newConnGate(8, 2)
	slot, ok := g.acquire("10.0.0.1")
	if !ok {
		t.Fatal("the first slot was refused")
	}
	slot.release()

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.held) != 0 {
		t.Errorf("held = %v, want an address that holds nothing to be forgotten", g.held)
	}
	if g.total != 0 {
		t.Errorf("total = %d, want 0", g.total)
	}
}

func TestAddrKey_StripsThePort(t *testing.T) {
	addr, err := net.ResolveTCPAddr("tcp", "192.0.2.10:54321")
	if err != nil {
		t.Fatal(err)
	}
	if got := addrKey(addr); got != "192.0.2.10" {
		t.Errorf("addrKey = %q, want the host alone: every connection from one source shares a budget", got)
	}
}

// ------------------------------------------------------------- end to end

// countingKeys counts how many times the database was asked about a key,
// which is the cost the budget exists to bound.
type countingKeys struct {
	mu      sync.Mutex
	lookups int
}

func (k *countingKeys) LookupSSHKey(_ context.Context, _ string) (*store.User, *store.SSHKey, error) {
	k.mu.Lock()
	k.lookups++
	k.mu.Unlock()
	return nil, nil, store.ErrNotFound
}

func (k *countingKeys) TouchSSHKey(context.Context, int64) error { return nil }

func (k *countingKeys) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.lookups
}

// The finding, end to end: an unauthenticated peer used to drive a database
// lookup per authentication attempt — six per connection by x/crypto's default
// — at whatever rate it could open sockets. The budget cuts it off before the
// lookup, so the cost of a guessing run is bounded by the rate, not by the
// attacker's connection count.
func TestAuthenticate_StopsQueryingTheDatabaseOnceTheBudgetIsSpent(t *testing.T) {
	keys := &countingKeys{}
	srv, err := New(Options{
		HostKeyPath:            t.TempDir() + "/host_ed25519",
		AuthRateLimitPerMinute: 3,
	}, keys, &fakeGit{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// Ten connections, each offering an unregistered key. Without the budget
	// every one of them reaches the database.
	for i := 0; i < 10; i++ {
		signer, _, _ := clientKey(t)
		_, dialErr := gossh.Dial("tcp", l.Addr().String(), &gossh.ClientConfig{
			User:            "git",
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // test client
			Timeout:         5 * time.Second,
		})
		if dialErr == nil {
			t.Fatal("an unregistered key authenticated")
		}
	}

	// 3 tokens, and each failed attempt spends one. The exact number depends
	// on how many times the client offers the key per connection, so the
	// assertion is the shape: bounded by the budget, not by the attempts.
	if got := keys.count(); got > 4 {
		t.Errorf("database lookups = %d after 10 connections on a budget of 3: "+
			"the budget is not cutting the lookups off", got)
	}
}

// The slot a connection holds before it authenticates has to come back, or a
// server that has served MaxUnauthenticatedConns clients over its life would
// refuse everything afterwards.
func TestAdmit_ReleasesTheSlotOnceAuthenticationSucceeds(t *testing.T) {
	h := newHarnessWith(t, Options{
		IdleTimeout:                    30 * time.Second,
		MaxUnauthenticatedConns:        1,
		MaxUnauthenticatedConnsPerAddr: 1,
	})
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(1, "alice", 7, fingerprint, authorized)

	// Three clients held open at once against a one-slot gate. Only the
	// release at authentication time makes that possible; the connections
	// stay open, so nothing else can be handing the slot back.
	for i := 0; i < 3; i++ {
		client, err := h.dial(t, signer)
		if err != nil {
			t.Fatalf("dial %d: %v, the gate slot was never released at authentication", i, err)
		}
		if _, _, status := run(t, client, "git-upload-pack 'acme/widgets'", nil); status != 0 {
			t.Fatalf("exit status = %d, want 0", status)
		}
	}
}

// TouchSSHKey used to run as `go s.keys.TouchSSHKey(context.Background(), id)`:
// no deadline and no bound, so a stalled database turned every session into a
// goroutine holding a connection forever. countingKeys fails the write when it
// arrives without a deadline.
func TestRecordKeyUse_RunsWithADeadlineAndStaysOffTheSessionPath(t *testing.T) {
	h := newHarness(t)
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(1, "alice", 7, fingerprint, authorized)
	client, err := h.dial(t, signer)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, _, status := run(t, client, "git-upload-pack 'acme/widgets'", nil); status != 0 {
		t.Fatalf("exit status = %d, want 0", status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.keys.touchedIDs()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ids := h.keys.touchedIDs(); len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("touched = %v, want [7]", ids)
	}

	if !h.keys.touchHadDeadline() {
		t.Error("the detached key-use write ran on a context with no deadline: " +
			"a stalled database leaves one goroutine per session holding a connection forever")
	}
}

// ------------------------------------------------------------- shutdown

// Shutdown used to return on timeout without ever calling Close, leaving the
// listeners and every stuck connection open — so the deadline it was given
// bounded nothing, and the process hung on an SSH session that had gone quiet.
func TestShutdown_ClosesEverythingWhenTheDeadlinePasses(t *testing.T) {
	h := newHarnessWith(t, Options{IdleTimeout: time.Hour})
	signer, authorized, fingerprint := clientKey(t)
	h.keys.register(1, "alice", 7, fingerprint, authorized)

	// An idle authenticated connection: gliderlabs' Shutdown waits on it
	// indefinitely, so this deadline is the only thing that ends the wait.
	client, err := h.dial(t, signer)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := h.srv.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown reported success while a connection was still open")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Shutdown took %v: it waited past its own deadline", elapsed)
	}

	// The connection is closed, not merely abandoned: something the client
	// does on it now has to fail.
	if _, err := client.NewSession(); err == nil {
		t.Error("the connection survived a timed-out Shutdown: Close was never called")
	}
}
