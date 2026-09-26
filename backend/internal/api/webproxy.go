// The browser's address behind the web UI's same-origin API proxy.
//
// When the web UI proxies the browser's API calls (docs/dev/agent-features.md
// §1.2), every one of them reaches this server from the web container, so the
// connection peer names the web tier and never the browser. The password rate
// limiter keys on that address, and with one address for everybody a single
// visitor failing to sign in over and over made every browser sign-in answer
// 429. The web tier's custom server (frontend/server.ts) knows the socket peer
// and sends it in X-TF-Client-Addr; this file decides whether to believe it.
//
// It is believed only from a caller that proves it *is* the web tier -- by
// connecting from one of TF_TRUSTED_WEB_PROXIES, or by presenting
// TF_WEB_PROXY_SECRET. Anyone else sending the header gets it ignored, which
// is what keeps a direct caller of the API port from picking its own bucket:
// X-Forwarded-For under TF_TRUST_PROXY_IPS cannot make that distinction, since
// it trusts the header from every connection alike.

package api

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	// headerWebClientAddr carries the browser's address, as the web tier's
	// custom server saw it on the socket.
	headerWebClientAddr = "X-TF-Client-Addr"
	// headerWebProxySecret carries TF_WEB_PROXY_SECRET.
	headerWebProxySecret = "X-TF-Proxy-Secret"
)

// webProxyResolveTTL is how long a host name in TF_TRUSTED_WEB_PROXIES keeps
// its resolved addresses. A recreated web container comes back on a new
// address, so the answer must not be kept for long; resolving on every request
// would put DNS on the sign-in path.
const webProxyResolveTTL = 30 * time.Second

// webProxyFailedResolveTTL is how long a failed resolution is remembered. The
// web container starts after this one in the compose file, so the name does
// not exist yet for the first few seconds -- which must not be cached for the
// full TTL, and must not be retried on every request either.
const webProxyFailedResolveTTL = 5 * time.Second

// trustedWebProxies is the parsed TF_TRUSTED_WEB_PROXIES.
type trustedWebProxies struct {
	prefixes []netip.Prefix
	hosts    []string
	resolve  func(ctx context.Context, host string) ([]netip.Addr, error)
	now      func() time.Time

	mu    sync.Mutex
	cache map[string]resolvedHost
}

type resolvedHost struct {
	addrs   []netip.Addr
	expires time.Time
}

// newTrustedWebProxies parses the configured entries (already validated by
// config.Load). It answers nil for an empty list, which trusts no peer.
func newTrustedWebProxies(entries []string) *trustedWebProxies {
	if len(entries) == 0 {
		return nil
	}
	t := &trustedWebProxies{
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		now:   time.Now,
		cache: map[string]resolvedHost{},
	}
	for _, entry := range entries {
		if p, err := netip.ParsePrefix(entry); err == nil {
			t.prefixes = append(t.prefixes, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(entry); err == nil {
			a = a.Unmap()
			t.prefixes = append(t.prefixes, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		t.hosts = append(t.hosts, strings.ToLower(entry))
	}
	return t
}

// contains reports whether peer is one of the trusted proxies. A host name
// that cannot be resolved matches nothing: failing closed only costs the
// shared rate-limit bucket this feature exists to avoid.
func (t *trustedWebProxies) contains(ctx context.Context, peer netip.Addr) bool {
	if t == nil || !peer.IsValid() {
		return false
	}
	peer = peer.Unmap()
	for _, p := range t.prefixes {
		if p.Contains(peer) {
			return true
		}
	}
	for _, host := range t.hosts {
		for _, a := range t.addrsOf(ctx, host) {
			if a.Unmap() == peer {
				return true
			}
		}
	}
	return false
}

func (t *trustedWebProxies) addrsOf(ctx context.Context, host string) []netip.Addr {
	now := t.now()
	t.mu.Lock()
	cached, ok := t.cache[host]
	t.mu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached.addrs
	}
	// Bounded so a stalled resolver delays one request by a second rather
	// than holding the sign-in path open.
	lookupCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	addrs, err := t.resolve(lookupCtx, host)
	entry := resolvedHost{addrs: addrs, expires: now.Add(webProxyResolveTTL)}
	if err != nil {
		entry = resolvedHost{expires: now.Add(webProxyFailedResolveTTL)}
	}
	t.mu.Lock()
	t.cache[host] = entry
	t.mu.Unlock()
	return entry.addrs
}

// webProxyClientIP is the browser's address as the web tier reported it, when
// the request provably came from the web tier. ok is false whenever the header
// is absent, unparseable or not vouched for, and the caller falls back to its
// usual answer.
func (s *Server) webProxyClientIP(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get(headerWebClientAddr))
	if raw == "" || !s.fromTrustedWebProxy(r) {
		return "", false
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return "", false
	}
	return addr.Unmap().WithZone("").String(), true
}

// fromTrustedWebProxy reports whether this request was sent by the web UI's
// API proxy: it presents TF_WEB_PROXY_SECRET, or its connection comes from an
// address in TF_TRUSTED_WEB_PROXIES.
func (s *Server) fromTrustedWebProxy(r *http.Request) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	if secret := s.cfg.WebProxySecret; secret != "" {
		if got := r.Header.Get(headerWebProxySecret); got != "" &&
			subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1 {
			return true
		}
	}
	if s.webProxies == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return s.webProxies.contains(r.Context(), peer)
}
