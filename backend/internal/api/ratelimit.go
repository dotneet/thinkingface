package api

import (
	"net"
	"net/http"
	"net/netip"
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
//   - failure buckets stop guessing (see passwordKeys for the three a password
//     attempt is metered against). Only *failed* attempts consume a token, so
//     a busy CI run or the e2e suite -- many successful logins from one
//     address -- never trips them.
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
	// userCeilingFactor multiplies the per-minute rate to get the global,
	// all-addresses failure ceiling for one username. It is deliberately far
	// above the per-(username, address) rate (perMinute/2): at 10x that rate,
	// emptying the ceiling takes failures from at least ten distinct addresses
	// -- ten IPv4 addresses or ten IPv6 /64s, see clientAddrKey -- each
	// already spending its own full budget on this one account. A single host
	// or network therefore cannot lock anybody else out. An attacker spread
	// over enough networks still can empty the ceiling and hold the account
	// shut for as long as they keep it up; that is the deliberate trade-off,
	// since the alternative is no bound at all on distributed guessing against
	// one account. See passwordKeys.
	userCeilingFactor = 5.0
	// authBucketIdle is how long an untouched bucket is kept before the
	// sweeper drops it, bounding the map under a spray of distinct keys.
	// Dropping one is indistinguishable from keeping it: a bucket goes from
	// its floor (-capacity) back to full in 2*authBucketBurst minutes, well
	// inside this, and a missing bucket reads as full (peekLocked).
	authBucketIdle = 10 * time.Minute
	// maxUsernameKeyLen is the longest username a failure bucket may name.
	// It is validateName's limit (nameRe: 1-96 characters of an ASCII set),
	// which every account created through the API passed, so a longer string
	// cannot be anybody's username. Without the cap an HTTP Basic username --
	// bounded only by the 1 MiB header limit -- went verbatim into two map
	// keys and pinned that memory until the sweep.
	maxUsernameKeyLen = 96
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// authGuard holds the failure buckets and the bcrypt semaphore.
type authGuard struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	// perMinute is the refill rate for an address bucket; the rates of the
	// two username buckets are derived from it in rateFor.
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
	switch {
	case strings.HasPrefix(key, userAddrKeyPrefix):
		// Half the address rate: a single account has no legitimate reason
		// to fail as often, from one place, as a shared NAT egress does.
		return g.perMinute / 2
	case strings.HasPrefix(key, userKeyPrefix):
		return g.perMinute * userCeilingFactor
	}
	return g.perMinute
}

// retryAfter reports how long the caller must wait before another failed
// attempt would be counted for any of these keys. Zero means "go ahead"; this
// call never consumes a token, so a correct password is never rate limited.
//
// It is read-only, and in particular never creates a bucket: it runs before
// anything else on every password attempt, throttled ones included, and
// those cost no bcrypt -- so if reading a key stored it, each cheap refused
// request with a fresh username or address would leave an entry behind until
// the sweep. Only penalize, which sits behind a bcrypt slot and a passing
// retryAfter, allocates.
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
		tokens := g.peekLocked(key, rate, now)
		if tokens >= 1 {
			continue
		}
		// Seconds until the bucket holds one whole token again.
		need := (1 - tokens) / (rate / 60)
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
	g.sweepLocked(now)
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

// peekLocked is the token count key would have after refilling to now,
// without storing anything: a key with no bucket reads as a full one, which
// is exactly what refillLocked would create for it.
func (g *authGuard) peekLocked(key string, rate float64, now time.Time) float64 {
	capacity := rate * authBucketBurst
	b, ok := g.buckets[key]
	if !ok {
		return capacity
	}
	tokens := b.tokens
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		tokens += elapsed * (rate / 60)
	}
	return min(tokens, capacity)
}

// refillLocked is peekLocked for a caller about to change the bucket: it
// creates the bucket if needed and brings it up to date in place.
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

// clientIP resolves the caller's address.
//
// RemoteAddr by default, exactly as the note in Handler() prescribes:
// X-Forwarded-For is client-controlled, and reading it unconditionally would
// let an attacker pick a fresh bucket per request. TF_TRUST_PROXY_IPS opts in
// for deployments where a proxy the operator controls sits in front.
//
// Counted from the *right*, never from the left. Every proxy this server is
// deployed behind appends rather than overwrites -- GCLB and Cloud Run do,
// and so does nginx under its own default (`proxy_add_x_forwarded_for`) -- so
// the leftmost entry is whatever the client chose to send and the rightmost
// entries are the ones a trusted hop wrote. Reading the leftmost entry was
// therefore not "trusting the proxy" at all: with the flag on, `X-Forwarded-For:
// <random>` bought a fresh addr: bucket on every request and put a forged
// address in the authentication log next to it.
//
// TF_TRUSTED_PROXY_HOPS says how many appending hops stand in front, because
// that number is a property of the deployment and not something this server
// can infer: one for Cloud Run reached directly (its front end appends the
// real peer), two behind a Google load balancer (GCLB appends the client and
// then the GFE). A header carrying fewer entries than that does not match the
// configured topology, so it is not read at all -- RemoteAddr, which no
// client can forge, is the answer instead.
//
// This is the *only* implementation of that rule. The rate limiter and the
// authentication logs both need an address, and two readings of these
// settings would eventually disagree -- at which point the address an
// operator sees in a log would not be the one the failure budget was charged
// against, and a report of a guessing run would name the wrong host.
func (s *Server) clientIP(r *http.Request) string {
	if hops := s.trustedProxyHops(); hops > 0 {
		if ip, ok := forwardedClientIP(r.Header.Values("X-Forwarded-For"), hops); ok {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// trustedProxyHops is how many trailing X-Forwarded-For entries were written
// by a proxy the operator controls. Zero means the header is ignored.
func (s *Server) trustedProxyHops() int {
	if s == nil || s.cfg == nil || !s.cfg.TrustProxyIPs {
		return 0
	}
	// The flag on with no hop count is the single-proxy case, which is what
	// TF_TRUST_PROXY_IPS meant on its own before the count existed.
	if s.cfg.TrustedProxyHops < 1 {
		return 1
	}
	return s.cfg.TrustedProxyHops
}

// forwardedClientIP picks the client out of an appended X-Forwarded-For
// chain: the entry hops places from the right, since each trusted proxy
// appended exactly one. It reports false when the header cannot be trusted to
// hold that entry, and the caller falls back to RemoteAddr.
func forwardedClientIP(values []string, hops int) (string, bool) {
	if hops < 1 {
		return "", false
	}
	// One header line per proxy or one comma-separated line are the same
	// chain; net/http keeps them in the order they arrived.
	var chain []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				chain = append(chain, p)
			}
		}
	}
	// Fewer entries than trusted hops means the chain is not the one the
	// configuration describes. Taking the leftmost entry anyway is exactly
	// the bug this replaced: a client that strips the header would hand
	// itself the first slot.
	if len(chain) < hops {
		return "", false
	}
	return normalizeClientIP(chain[len(chain)-hops])
}

// normalizeClientIP reduces one X-Forwarded-For entry to a bare IP address.
// Anything that is not one is refused rather than used as a bucket key: an
// obfuscated identifier ("_hidden"), "unknown" and a hostname are all legal
// in the header, and none of them is an address the failure budget or the
// authentication log should name.
func normalizeClientIP(entry string) (string, bool) {
	// Some proxies append host:port. SplitHostPort fails on a bare IPv6
	// address (too many colons), which is the case that must survive.
	if host, _, err := net.SplitHostPort(entry); err == nil {
		entry = host
	}
	entry = strings.TrimPrefix(strings.TrimSuffix(entry, "]"), "[")
	// A zone ("fe80::1%eth0") is the sender's interface, not part of the
	// identity of the peer.
	if i := strings.IndexByte(entry, '%'); i > 0 {
		entry = entry[:i]
	}
	ip, err := netip.ParseAddr(entry)
	if err != nil {
		return "", false
	}
	return ip.String(), true
}

// clientAddrKey is clientIP as a failure-bucket key. The prefix keeps the
// address space and the username space apart in one map (see rateFor).
//
// An IPv6 client is keyed by its /64, not its full address. A /64 is the
// smallest block routinely handed to one host or one site (SLAAC needs it),
// and every address in it is the same caller for this purpose: keyed by the
// full address, a single machine could rotate through 2^64 of its own
// addresses and get a fresh address bucket -- and a fresh per-(username,
// address) bucket -- on every request, which made ten "distinct addresses"
// against userCeilingFactor's ceiling no harder to find than one. IPv4, and
// IPv4 reached over an IPv4-mapped IPv6 socket, is keyed by the address.
//
// Only the key is coarsened. The authentication log keeps clientIP's full
// address, which is what an operator needs to find the host.
func (s *Server) clientAddrKey(r *http.Request) string {
	return "addr:" + addrBucket(s.clientIP(r))
}

// addrBucket is the part of clientAddrKey after the prefix. A value that does
// not parse as an address (RemoteAddr with no port, from a test or an odd
// listener) is used as it is, as it always was.
func addrBucket(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String()
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return p.String()
}

const (
	userKeyPrefix     = "user:"
	userAddrKeyPrefix = "useraddr:"
)

// usernameKey is the global failure ceiling for one account, shared by every
// address. It is never the only username bucket consulted -- see passwordKeys.
func usernameKey(username string) string {
	return userKeyPrefix + normalizeUsernameKey(username)
}

// userAddrKey is the failure bucket for one account *from one address*.
func userAddrKey(addrKey, username string) string {
	return userAddrKeyPrefix + normalizeUsernameKey(username) + "|" + addrKey
}

func normalizeUsernameKey(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// passwordKeys is every failure bucket one password attempt for username from
// addrKey (s.clientAddrKey) is read and charged against:
//
//   - the address bucket, which stops one address guessing across many
//     usernames;
//   - the (username, address) bucket, which is the tight per-account limit
//     (perMinute/2);
//   - the username ceiling, shared by every address but userCeilingFactor
//     times the address rate.
//
// The per-account limit used to be the shared username bucket alone, at
// perMinute/2. That made it a lockout switch: HTTP Basic is accepted on every
// route, so five `Authorization: Basic alice:wrong` requests a minute to
// /healthz from one address kept alice -- site administrators included --
// from signing in anywhere, while that address's own bucket was never close to
// empty. Scoping the tight limit to the address means an attacker only ever
// spends their own budget; the ceiling keeps what a distributed run can try
// against one account bounded, and emptying it takes many addresses, each
// already throttled on its own (see userCeilingFactor).
//
// A username longer than maxUsernameKeyLen cannot be an account, so it gets
// the address bucket alone: no key ever embeds an over-long string, and there
// is no account whose buckets it could be charged to. checkPassword answers
// it as a wrong password against that bucket (impossibleUsername).
func passwordKeys(addrKey, username string) []string {
	if impossibleUsername(username) {
		return []string{addrKey}
	}
	return []string{addrKey, userAddrKey(addrKey, username), usernameKey(username)}
}

// impossibleUsername reports whether username is too long to belong to any
// account (maxUsernameKeyLen).
func impossibleUsername(username string) bool {
	return len(username) > maxUsernameKeyLen
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
	serviceOverloadedWith(w, retryAfter,
		"the server is busy verifying other sign-ins; try again shortly")
}

// serviceOverloadedWith is serviceOverloaded for a caller that has its own
// reason to give. The shape -- 503 `overloaded` plus Retry-After -- is the
// whole point of sharing it: "nothing is wrong with your request, come back in
// a moment" has one representation on this server, whether the contended
// resource is the bcrypt budget or a repository's WAL index.
func serviceOverloadedWith(w http.ResponseWriter, retryAfter time.Duration, message string) {
	secs := int(retryAfter.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusServiceUnavailable, "overloaded", message)
}
