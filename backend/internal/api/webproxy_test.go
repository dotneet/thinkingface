package api

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/config"
)

// Behind the web UI's same-origin API proxy every browser request arrives from
// the web container. X-TF-Client-Addr names the browser, and it is only
// believed from the web tier itself (webproxy.go).

const testWebProxySecret = "0123456789abcdef0123456789abcdef"

func webProxyServer(cfg *config.Config) *Server {
	s := &Server{cfg: cfg}
	s.webProxies = newTrustedWebProxies(cfg.TrustedWebProxies)
	return s
}

func TestWebProxyClientAddrTrustDecision(t *testing.T) {
	tests := []struct {
		name   string
		cfg    config.Config
		remote string
		client string
		secret string
		want   string
	}{
		{
			name:   "ignored when nothing is configured",
			remote: "172.18.0.5:40000", client: "203.0.113.7",
			want: "172.18.0.5",
		},
		{
			name:   "believed from a trusted peer address",
			cfg:    config.Config{TrustedWebProxies: []string{"172.18.0.5"}},
			remote: "172.18.0.5:40000", client: "203.0.113.7",
			want: "203.0.113.7",
		},
		{
			name:   "believed from a trusted prefix",
			cfg:    config.Config{TrustedWebProxies: []string{"172.18.0.0/16"}},
			remote: "172.18.3.9:40000", client: "203.0.113.7",
			want: "203.0.113.7",
		},
		{
			// A direct caller of the published API port arrives from Docker's
			// gateway, not from the web container: its header is its own
			// choice and must not pick its rate-limit bucket.
			name:   "ignored from any other peer",
			cfg:    config.Config{TrustedWebProxies: []string{"172.18.0.5"}},
			remote: "172.18.0.1:40000", client: "203.0.113.7",
			want: "172.18.0.1",
		},
		{
			name:   "believed with the shared secret from any peer",
			cfg:    config.Config{WebProxySecret: testWebProxySecret},
			remote: "10.1.2.3:40000", client: "203.0.113.7", secret: testWebProxySecret,
			want: "203.0.113.7",
		},
		{
			name:   "ignored with a wrong secret",
			cfg:    config.Config{WebProxySecret: testWebProxySecret},
			remote: "10.1.2.3:40000", client: "203.0.113.7", secret: "not-the-secret-not-the-secret-xx",
			want: "10.1.2.3",
		},
		{
			// An empty configured secret must not match an empty header.
			name:   "a secret header is meaningless when none is configured",
			remote: "10.1.2.3:40000", client: "203.0.113.7", secret: "anything",
			want: "10.1.2.3",
		},
		{
			name:   "an unparseable address falls back to the peer",
			cfg:    config.Config{TrustedWebProxies: []string{"172.18.0.5"}},
			remote: "172.18.0.5:40000", client: "203.0.113.7, 198.51.100.1",
			want: "172.18.0.5",
		},
		{
			name:   "an IPv4-mapped address is reported as IPv4",
			cfg:    config.Config{TrustedWebProxies: []string{"172.18.0.5"}},
			remote: "[::ffff:172.18.0.5]:40000", client: "::ffff:203.0.113.7",
			want: "203.0.113.7",
		},
		{
			// The web tier's answer wins over X-Forwarded-For: under
			// TF_TRUST_PROXY_IPS the rightmost entry would be the web
			// container's own view, which is the address being replaced.
			name:   "takes precedence over trusted X-Forwarded-For",
			cfg:    config.Config{TrustedWebProxies: []string{"172.18.0.5"}, TrustProxyIPs: true, TrustedProxyHops: 1},
			remote: "172.18.0.5:40000", client: "203.0.113.7",
			want: "203.0.113.7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			s := webProxyServer(&cfg)
			r := requestWith(tt.remote)
			if tt.cfg.TrustProxyIPs {
				r.Header.Set("X-Forwarded-For", "172.18.0.5")
			}
			if tt.client != "" {
				r.Header.Set(headerWebClientAddr, tt.client)
			}
			if tt.secret != "" {
				r.Header.Set(headerWebProxySecret, tt.secret)
			}
			if got := s.clientIP(r); got != tt.want {
				t.Fatalf("clientIP = %q; want %q", got, tt.want)
			}
		})
	}
}

// The regression this exists for: behind the proxy, one visitor failing to
// sign in used to exhaust the address bucket every browser shared.
func TestWebProxyGivesEachBrowserItsOwnFailureBucket(t *testing.T) {
	s := webProxyServer(&config.Config{TrustedWebProxies: []string{"172.18.0.5"}})
	s.authGuard = newAuthGuard(2)

	attacker := requestWith("172.18.0.5:40000")
	attacker.Header.Set(headerWebClientAddr, "203.0.113.66")
	victim := requestWith("172.18.0.5:40001")
	victim.Header.Set(headerWebClientAddr, "198.51.100.20")

	for range 5 {
		s.authGuard.penalize(s.clientAddrKey(attacker))
	}
	if s.authGuard.retryAfter(s.clientAddrKey(attacker)) == 0 {
		t.Fatal("the attacker's own bucket should be exhausted")
	}
	if wait := s.authGuard.retryAfter(s.clientAddrKey(victim)); wait != 0 {
		t.Fatalf("another browser behind the same web container is throttled for %s", wait)
	}
}

func TestTrustedWebProxyHostNamesResolveAndExpire(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lookups := 0
	answer := []netip.Addr{netip.MustParseAddr("172.18.0.5")}
	var failWith error
	tp := newTrustedWebProxies([]string{"Web"})
	tp.now = func() time.Time { return now }
	tp.resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		lookups++
		if host != "web" {
			t.Fatalf("resolved %q; host names are matched case-insensitively as web", host)
		}
		return answer, failWith
	}
	ctx := context.Background()
	peer := netip.MustParseAddr("172.18.0.5")

	for range 2 {
		if !tp.contains(ctx, peer) {
			t.Fatal("the resolved address should be trusted")
		}
	}
	if lookups != 1 {
		t.Fatalf("lookups = %d; the answer should be cached", lookups)
	}

	// A recreated container comes back on a new address.
	answer = []netip.Addr{netip.MustParseAddr("172.18.0.9")}
	now = now.Add(webProxyResolveTTL + time.Second)
	if tp.contains(ctx, peer) {
		t.Fatal("the old address is still trusted after the cache expired")
	}
	if !tp.contains(ctx, netip.MustParseAddr("172.18.0.9")) {
		t.Fatal("the new address is not trusted")
	}

	// A failed lookup trusts nothing, and is retried soon rather than after
	// the full TTL (the web container starts after the api one).
	failWith = errors.New("no such host")
	now = now.Add(webProxyResolveTTL + time.Second)
	if tp.contains(ctx, netip.MustParseAddr("172.18.0.9")) {
		t.Fatal("a failed lookup must not keep trusting the previous answer")
	}
	failWith = nil
	now = now.Add(webProxyFailedResolveTTL + time.Second)
	if !tp.contains(ctx, netip.MustParseAddr("172.18.0.9")) {
		t.Fatal("the lookup was not retried after the failure window")
	}
}

func TestTrustedWebProxiesNilTrustsNobody(t *testing.T) {
	if newTrustedWebProxies(nil) != nil {
		t.Fatal("an empty list should parse to nil")
	}
	var tp *trustedWebProxies
	if tp.contains(context.Background(), netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("a nil list trusted a peer")
	}
}
