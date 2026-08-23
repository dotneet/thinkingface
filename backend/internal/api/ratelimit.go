package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Brute-force and CPU-exhaustion defence for the two places a password is
// checked: POST /api/v1/auth/login and the HTTP Basic branch of
// resolveIdentity, which every route accepts.
//
// Everything here is per-process and in-memory on purpose. The instance
// topology this server targets is a single writer (SQLite mode is explicitly
// one process, docs/dev/thinkingface-design.md §10/§14), and a shared counter
// would mean a round trip to the database on the exact path an attacker is
// trying to flood. Under multiple replicas the effective limit is
// per-replica; that is recorded in §14 rather than papered over.
//
// Two independent controls, because they answer different threats:
//
//   - failure buckets (per client address, per username) stop guessing. Only
//     *failed* attempts consume a token, so a busy CI run or the e2e suite --
//     many successful logins from one address -- never trips them.
//   - a bcrypt semaphore caps how much CPU unauthenticated callers can force
//     the process to spend. Guessing many usernames from many addresses slips
//     past the buckets, but bcrypt(cost 10) at unbounded concurrency is a
//     denial of service on its own.

const (
	// authBucketBurst multiplies the per-minute rate to get the bucket depth,
	// so a person who mistypes a password a few times in a row is not locked
	// out by a rate that is otherwise fine.
	authBucketBurst = 1.0
	// bcryptConcurrency bounds simultaneous password verifications. bcrypt is
	// deliberately CPU-heavy; this is what keeps that cost bounded by the
	// machine rather than by the request rate.
	bcryptConcurrency = 4
	// bcryptWait is how long a request will queue for a bcrypt slot before it
	// is turned away. Long enough to absorb a burst, short enough that the
	// queue cannot itself become the resource being exhausted.
	bcryptWait = 2 * time.Second
	// authBucketIdle is how long an untouched bucket is kept before the
	// sweeper drops it, bounding the map under a spray of distinct keys.
	authBucketIdle = 10 * time.Minute
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// authGuard holds the failure buckets and the bcrypt semaphore.
type authGuard struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	// perMinute is the refill rate for an address bucket; a username bucket
	// refills at half that, since a single account has no legitimate reason
	// to fail as often as a shared NAT egress does.
	perMinute float64
	lastSweep time.Time

	sem chan struct{}

	// now is swappable so tests can advance time instead of sleeping.
	now func() time.Time
}

func newAuthGuard(perMinute int) *authGuard {
	g := &authGuard{
		buckets:   map[string]*tokenBucket{},
		perMinute: float64(perMinute),
		sem:       make(chan struct{}, bcryptConcurrency),
		now:       time.Now,
	}
	g.lastSweep = g.now()
	return g
}

// enabled reports whether the failure buckets do anything. A zero rate turns
// them off; the bcrypt semaphore stays on regardless, because it protects the
// process rather than an account.
func (g *authGuard) enabled() bool { return g != nil && g.perMinute > 0 }

func (g *authGuard) rateFor(key string) float64 {
	if strings.HasPrefix(key, "user:") {
		return g.perMinute / 2
	}
	return g.perMinute
}

// retryAfter reports how long the caller must wait before another failed
// attempt would be counted for any of these keys. Zero means "go ahead"; this
// call never consumes a token, so a correct password is never rate limited.
func (g *authGuard) retryAfter(keys ...string) time.Duration {
	if !g.enabled() {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.sweepLocked(now)

	var worst time.Duration
	for _, key := range keys {
		rate := g.rateFor(key)
		if rate <= 0 {
			continue
		}
		b := g.refillLocked(key, rate, now)
		if b.tokens >= 1 {
			continue
		}
		// Seconds until the bucket holds one whole token again.
		need := (1 - b.tokens) / (rate / 60)
		if d := time.Duration(need * float64(time.Second)); d > worst {
			worst = d
		}
	}
	return worst
}

// penalize records one failed attempt against every key.
func (g *authGuard) penalize(keys ...string) {
	if !g.enabled() {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for _, key := range keys {
		rate := g.rateFor(key)
		if rate <= 0 {
			continue
		}
		b := g.refillLocked(key, rate, now)
		b.tokens--
		// Floor the debt: without it a sustained flood drives the bucket
		// arbitrarily negative and the key stays locked long after the
		// attack stops.
		if floor := -rate * authBucketBurst; b.tokens < floor {
			b.tokens = floor
		}
	}
}

// reset forgets the failures recorded against these keys, which is what a
// successful authentication does.
func (g *authGuard) reset(keys ...string) {
	if !g.enabled() {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, key := range keys {
		delete(g.buckets, key)
	}
}

func (g *authGuard) refillLocked(key string, rate float64, now time.Time) *tokenBucket {
	capacity := rate * authBucketBurst
	b, ok := g.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: capacity, last: now}
		g.buckets[key] = b
		return b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * (rate / 60)
		if b.tokens > capacity {
			b.tokens = capacity
		}
	}
	b.last = now
	return b
}

func (g *authGuard) sweepLocked(now time.Time) {
	if now.Sub(g.lastSweep) < authBucketIdle {
		return
	}
	g.lastSweep = now
	for key, b := range g.buckets {
		if now.Sub(b.last) > authBucketIdle {
			delete(g.buckets, key)
		}
	}
}

// acquireBcrypt takes a slot on the password-verification semaphore. It
// returns false when the wait ran out, in which case the caller must not run
// bcrypt -- that refusal is the whole point.
func (g *authGuard) acquireBcrypt() bool {
	if g == nil {
		return true
	}
	timer := time.NewTimer(bcryptWait)
	defer timer.Stop()
	select {
	case g.sem <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (g *authGuard) releaseBcrypt() {
	if g == nil {
		return
	}
	select {
	case <-g.sem:
	default:
	}
}

// clientAddrKey identifies the caller for rate-limiting purposes.
//
// RemoteAddr by default, exactly as the note in Handler() prescribes:
// X-Forwarded-For is client-controlled, and reading it unconditionally would
// let an attacker pick a fresh bucket per request. TF_TRUST_PROXY_IPS opts in
// for deployments where a proxy the operator controls rewrites the header.
func (s *Server) clientAddrKey(r *http.Request) string {
	if s.cfg != nil && s.cfg.TrustProxyIPs {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if first = strings.TrimSpace(first); first != "" {
				return "addr:" + first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "addr:" + host
}

func usernameKey(username string) string {
	return "user:" + strings.ToLower(strings.TrimSpace(username))
}

// tooManyAttempts answers a rate-limited authentication attempt. The message
// says nothing about whether the account exists.
func tooManyAttempts(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(retryAfter.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "rate_limited",
		"too many authentication attempts; try again later")
}

// serviceOverloaded answers a request that was refused because the server ran
// out of password-hashing capacity, not because anything was wrong with it.
// 503 rather than 429: the caller is not the one being limited, and Retry-After
// tells honest clients when to come back.
func serviceOverloaded(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(retryAfter.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusServiceUnavailable, "overloaded",
		"the server is busy verifying other sign-ins; try again shortly")
}
