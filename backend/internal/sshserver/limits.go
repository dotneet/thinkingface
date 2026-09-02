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
//     limit is invisible to legitimate use. It is two caps, not one, and the
//     per-address half is the one that does the work: a single process-wide
//     semaphore turns the fix into a cheaper denial of service than the
//     problem it fixed, because gliderlabs arms IdleTimeout only on the first
//     read or write, so one host that opens the whole ceiling's worth of TCP
//     connections and then says nothing holds every slot for the full idle
//     timeout and locks the entire fleet out at admit. Bounding per address
//     means that host exhausts its own share and nobody else's; the global
//     ceiling is then only a backstop against a distributed flood and is set
//     high enough that one peer cannot reach it.
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
	// DefaultMaxUnauthenticatedConns is the process-wide cap applied when
	// Options leaves it at zero. It is a backstop against a distributed
	// flood, not the primary control, so it is deliberately far above what
	// one address can hold (DefaultMaxUnauthenticatedConnsPerAddr): a limit a
	// single peer can reach is a limit a single peer can use to lock everyone
	// else out.
	DefaultMaxUnauthenticatedConns = 512
	// DefaultMaxUnauthenticatedConnsPerAddr is the per-source-address cap
	// applied when Options leaves it at zero. This is the control that stops
	// one host starving the fleet. A real client holds a slot only for the
	// key exchange and its first successful authentication, so even a person
	// running several clones at once stays well inside it; a host that sits
	// on more than this is not doing anything a git client does.
	DefaultMaxUnauthenticatedConnsPerAddr = 8
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
// once, both per source address and across the process. A non-blocking acquire
// is the point: queueing would make the queue the resource being exhausted.
//
// A counter map rather than a semaphore channel, because the per-address
// dimension needs a count it can compare against a limit. Entries exist only
// while an address holds a slot and are removed on the last release, so a
// spray of distinct source addresses cannot grow the map beyond `limit`
// entries.
type connGate struct {
	mu      sync.Mutex
	held    map[string]int
	total   int
	limit   int
	perAddr int
}

func newConnGate(limit, perAddr int) *connGate {
	if limit <= 0 {
		limit = DefaultMaxUnauthenticatedConns
	}
	if perAddr <= 0 {
		perAddr = DefaultMaxUnauthenticatedConnsPerAddr
	}
	if perAddr > limit {
		// A per-address cap above the global one is a misconfiguration that
		// silently disables the per-address dimension; clamp rather than
		// refuse, since SSH is optional and a bad number here must not stop
		// the server from starting.
		perAddr = limit
	}
	return &connGate{held: map[string]int{}, limit: limit, perAddr: perAddr}
}

// acquire takes a slot for addr, or reports false immediately when either the
// address's own share or the process-wide ceiling is full.
func (g *connGate) acquire(addr string) (*connSlot, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= g.limit || g.held[addr] >= g.perAddr {
		return nil, false
	}
	g.total++
	g.held[addr]++
	return &connSlot{gate: g, addr: addr}, true
}

func (g *connGate) put(addr string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total > 0 {
		g.total--
	}
	if n := g.held[addr] - 1; n > 0 {
		g.held[addr] = n
	} else {
		delete(g.held, addr)
	}
}

// connSlot is one held slot. Releasing is idempotent because two things race
// to do it: authentication succeeding, and the connection closing.
type connSlot struct {
	gate *connGate
	addr string
	once sync.Once
}

func (s *connSlot) release() {
	if s == nil {
		return
	}
	s.once.Do(func() { s.gate.put(s.addr) })
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
