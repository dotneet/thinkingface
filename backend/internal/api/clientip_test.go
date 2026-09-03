package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/config"
)

// The address the rate limiter charges and the address the authentication log
// names are the same one, and neither may be something the caller chose.
//
// X-Forwarded-For is *appended* to by every proxy this server is deployed
// behind, so the leftmost entry is client-supplied. Reading it -- which is
// what TF_TRUST_PROXY_IPS used to do -- meant a caller could send a different
// random value on every request and take a fresh addr: bucket each time.

func requestWith(remoteAddr string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	r.RemoteAddr = remoteAddr
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func serverWithProxy(trust bool, hops int) *Server {
	return &Server{cfg: &config.Config{TrustProxyIPs: trust, TrustedProxyHops: hops}}
}

func TestClientIPIgnoresForwardedForByDefault(t *testing.T) {
	s := serverWithProxy(false, 0)
	got := s.clientIP(requestWith("10.0.0.9:5555", "1.2.3.4"))
	if got != "10.0.0.9" {
		t.Fatalf("clientIP = %q; want the connection peer 10.0.0.9", got)
	}
}

func TestClientIPCountsTrustedHopsFromTheRight(t *testing.T) {
	tests := []struct {
		name string
		hops int
		xff  []string
		want string
	}{
		{
			// The attack: one appending proxy in front, and the caller
			// prefixes whatever it likes. The proxy's own entry is the last
			// one, so it is the only one that identifies the peer.
			name: "a forged prefix cannot displace the proxy's entry",
			hops: 1,
			xff:  []string{"203.0.113.7, 198.51.100.4"},
			want: "198.51.100.4",
		},
		{
			name: "a client that sends nothing is identified by the proxy",
			hops: 1,
			xff:  []string{"198.51.100.4"},
			want: "198.51.100.4",
		},
		{
			// GCLB appends the client and then the GFE, so the client is two
			// from the right whether or not the caller prefixed anything.
			name: "two hops, no forgery",
			hops: 2,
			xff:  []string{"198.51.100.4, 35.191.0.1"},
			want: "198.51.100.4",
		},
		{
			name: "two hops, forged prefix",
			hops: 2,
			xff:  []string{"203.0.113.7, 198.51.100.4, 35.191.0.1"},
			want: "198.51.100.4",
		},
		{
			// net/http keeps repeated header lines in arrival order, and a
			// chain split across them is the same chain.
			name: "the chain may be split across header lines",
			hops: 1,
			xff:  []string{"203.0.113.7", "198.51.100.4"},
			want: "198.51.100.4",
		},
		{
			name: "an appended host:port is reduced to the address",
			hops: 1,
			xff:  []string{"198.51.100.4:41234"},
			want: "198.51.100.4",
		},
		{
			name: "ipv6 survives, brackets and all",
			hops: 1,
			xff:  []string{"[2001:db8::1]:443"},
			want: "2001:db8::1",
		},
		{
			// Fewer entries than configured hops means the header is not the
			// chain the deployment describes; the unforgeable peer wins.
			name: "a short chain falls back to the connection peer",
			hops: 2,
			xff:  []string{"203.0.113.7"},
			want: "10.0.0.9",
		},
		{
			name: "an empty header falls back to the connection peer",
			hops: 1,
			xff:  []string{"   "},
			want: "10.0.0.9",
		},
		{
			// "unknown" and the obfuscated identifiers are legal in the
			// header and are not addresses; naming one in a log would be a
			// lie, and bucketing on one is a free bucket.
			name: "a non-address entry falls back to the connection peer",
			hops: 1,
			xff:  []string{"203.0.113.7, _hidden"},
			want: "10.0.0.9",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serverWithProxy(true, tt.hops)
			if got := s.clientIP(requestWith("10.0.0.9:5555", tt.xff...)); got != tt.want {
				t.Fatalf("clientIP = %q; want %q", got, tt.want)
			}
		})
	}
}

// A hop count that was never set means the single-proxy case, which is what
// the boolean meant on its own before the count existed.
func TestClientIPDefaultsToOneHop(t *testing.T) {
	s := serverWithProxy(true, 0)
	got := s.clientIP(requestWith("10.0.0.9:5555", "203.0.113.7, 198.51.100.4"))
	if got != "198.51.100.4" {
		t.Fatalf("clientIP = %q; want 198.51.100.4", got)
	}
}

// The whole point of the fix: a caller cannot mint a new failure bucket per
// request by varying the header it controls.
func TestForgedForwardedForCannotSprayBuckets(t *testing.T) {
	s := serverWithProxy(true, 1)
	forged := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"}
	for _, f := range forged {
		key := s.clientAddrKey(requestWith("10.0.0.9:5555", f+", 198.51.100.4"))
		if key != "addr:198.51.100.4" {
			t.Fatalf("clientAddrKey for a forged %q = %q; want addr:198.51.100.4", f, key)
		}
	}
}
