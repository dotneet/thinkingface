package sshserver

import (
	"net"
	"sync"
	"time"
)

// Resource limits for the SSH listener.
//
// HTTP has had these since the auth guard in internal/api/ratelimit.go; SSH
// had none at all, and it is the cheaper target of the two. A peer that never
// authenticates still costs a TCP connection, a key exchange, and — because
// x/crypto allows six authentication attempts per connection by default — up
// to six LookupSSHKey round trips to the database, at whatever rate it cares
// to open connections. Nothing bounded any of that.
//
// Two controls, mirroring the HTTP side because they answer the same two
// threats:
//
//   - a per-address failure budget stops key guessing, and — more importantly
//     here — stops the database lookups: once an address is out of tokens,
//     authenticate refuses before it queries anything.
//   - a cap on concurrent *unauthenticated* connections bounds what a peer can
//     tie up before proving who it is. The slot is released the moment
//     authentication succeeds, so a long clone never occupies it, and this
//     limit is invisible to legitimate use.
//
// Both are per process and in memory, for the same reason ratelimit.go gives:
// a shared counter would put a database round trip on the exact path an
// attacker is flooding. Under multiple replicas the effective limit is
// per-replica.

const (
	// authBucketBurst multiplies the per-minute rate to get bucket depth. One
	// minute of budget is enough that a person whose agent offers several keys
	// before the right one never notices.
	authBucketBurst = 1.0
	// authBucketIdle bounds the map under a spray of distinct source
	// addresses: an untouched bucket is dropped after this.
	authBucketIdle = 10 * time.Minute
	// DefaultMaxUnauthenticatedConns is the cap applied when Options leaves
	// it at zero. Well above any plausible burst of real clients — a client
	// holds a slot only for the key exchange and its first successful auth —
	// and far below what an unbounded accept loop would let a single peer
	// pin.
	DefaultMaxUnauthenticatedConns = 64
	// touchConcurrency bounds the fire-and-forget "record this key was used"
	// writes. Without a bound, a burst of sessions is a burst of unbounded
	// goroutines each holding a database connection.
	touchConcurrency = 8
	// touchTimeout is the deadline on one of those writes. It matches
	// detachedWriteTimeout in internal/api: a bookkeeping write that has been
	// cut loose from its request still has to end.
	touchTimeout = 10 * time.Second
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// authBudget is the per-address failure budget. A zero perMinute disables it
// entirely, which is what tests and TF_AUTH_RATE_LIMIT_PER_MIN=0 want.
type authBudget struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	perMinute float64
	lastSweep time.Time

	// now is swappable so tests can advance time instead of sleeping.
	now func() time.Time
}

func newAuthBudget(perMinute int) *authBudget {
	b := &authBudget{
		buckets:   map[string]*tokenBucket{},
		perMinute: float64(perMinute),
		now:       time.Now,
	}
	b.lastSweep = b.now()
	return b
}

func (b *authBudget) enabled() bool { return b != nil && b.perMinute > 0 }

// allow reports whether another authentication attempt from key may be
// *evaluated*. It consumes nothing: a client offering the right key on its
// second try is never turned away for having offered a wrong one first.
func (b *authBudget) allow(key string) bool {
	if !b.enabled() {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.sweepLocked(now)
	return b.refillLocked(key, now).tokens >= 1
}

// penalize records one failed attempt.
func (b *authBudget) penalize(key string) {
	if !b.enabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	bucket := b.refillLocked(key, b.now())
	bucket.tokens--
	// Floor the debt, or a sustained flood keeps the address locked out long
	// after it stops.
	if floor := -b.perMinute * authBucketBurst; bucket.tokens < floor {
		bucket.tokens = floor
	}
}

// reset forgets an address's failures, which is what a successful
// authentication does — an agent that walked through three keys before the
// registered one must not leave a mark.
func (b *authBudget) reset(key string) {
	if !b.enabled() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.buckets, key)
}

func (b *authBudget) refillLocked(key string, now time.Time) *tokenBucket {
	capacity := b.perMinute * authBucketBurst
	bucket, ok := b.buckets[key]
	if !ok {
		bucket = &tokenBucket{tokens: capacity, last: now}
		b.buckets[key] = bucket
		return bucket
	}
	if elapsed := now.Sub(bucket.last).Seconds(); elapsed > 0 {
		bucket.tokens += elapsed * (b.perMinute / 60)
		if bucket.tokens > capacity {
			bucket.tokens = capacity
		}
	}
	bucket.last = now
	return bucket
}

func (b *authBudget) sweepLocked(now time.Time) {
	if now.Sub(b.lastSweep) < authBucketIdle {
		return
	}
	b.lastSweep = now
	for key, bucket := range b.buckets {
		if now.Sub(bucket.last) > authBucketIdle {
			delete(b.buckets, key)
		}
	}
}

// connGate caps how many connections may be in the pre-authentication phase at
// once. A non-blocking acquire is the point: queueing would make the queue the
// resource being exhausted.
type connGate struct {
	sem chan struct{}
}

func newConnGate(limit int) *connGate {
	if limit <= 0 {
		limit = DefaultMaxUnauthenticatedConns
	}
	return &connGate{sem: make(chan struct{}, limit)}
}

// acquire takes a slot, or reports false immediately when the gate is full.
func (g *connGate) acquire() (*connSlot, bool) {
	select {
	case g.sem <- struct{}{}:
		return &connSlot{gate: g}, true
	default:
		return nil, false
	}
}

// connSlot is one held slot. Releasing is idempotent because two things race
// to do it: authentication succeeding, and the connection closing.
type connSlot struct {
	gate *connGate
	once sync.Once
}

func (s *connSlot) release() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		select {
		case <-s.gate.sem:
		default:
		}
	})
}

// guardedConn releases the connection's gate slot when the connection closes,
// which is what makes the cap self-healing for peers that hang up mid
// handshake — the case an unbounded accept loop is most exposed to.
type guardedConn struct {
	net.Conn
	slot *connSlot
}

func (c *guardedConn) Close() error {
	c.slot.release()
	return c.Conn.Close()
}

// addrKey reduces a peer address to the host part, so every connection from
// one source shares a budget regardless of ephemeral port. There is no
// X-Forwarded-For equivalent here: the TCP peer is the only address SSH has,
// which is why this needs none of the TrustProxyIPs machinery the HTTP side
// carries.
func addrKey(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
