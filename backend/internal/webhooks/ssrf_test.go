package webhooks

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestValidateTargetURLScheme(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://example.com/hook", false},
		{"http://example.com/hook", false},
		{"ftp://example.com/hook", true},
		{"file:///etc/passwd", true},
		{"not a url", true},
	}
	for _, c := range cases {
		err := ValidateTargetURL(c.url, false)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidateTargetURL(%q, false) error = %v, wantErr %v", c.url, err, c.wantErr)
		}
	}
}

func TestValidateTargetURLBlocksPrivateAddresses(t *testing.T) {
	blocked := []string{
		"http://localhost:8080/hook",
		"http://127.0.0.1/hook",
		"http://127.5.5.5/hook",
		"http://10.0.0.5/hook",
		"http://172.16.0.5/hook",
		"http://192.168.1.5/hook",
		"http://169.254.1.1/hook",
		"http://[::1]/hook",
	}
	for _, u := range blocked {
		if err := ValidateTargetURL(u, false); !errors.Is(err, ErrDisallowedTarget) {
			t.Errorf("ValidateTargetURL(%q, false) = %v, want ErrDisallowedTarget", u, err)
		}
	}
}

func TestValidateTargetURLAllowsPublicAddresses(t *testing.T) {
	allowed := []string{
		"https://example.com/hook",
		"http://8.8.8.8/hook",
		"http://203.0.113.10/hook",
	}
	for _, u := range allowed {
		if err := ValidateTargetURL(u, false); err != nil {
			t.Errorf("ValidateTargetURL(%q, false) = %v, want nil", u, err)
		}
	}
}

func TestValidateTargetURLAllowPrivateOptOut(t *testing.T) {
	if err := ValidateTargetURL("http://127.0.0.1:9000/hook", true); err != nil {
		t.Errorf("ValidateTargetURL with allowPrivate=true should accept loopback, got %v", err)
	}
	// The scheme restriction still applies even with the opt-out.
	if err := ValidateTargetURL("ftp://127.0.0.1/hook", true); err == nil {
		t.Error("allowPrivate must not bypass the scheme check")
	}
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.31.0.1", "192.168.0.1", "169.254.0.1", "::1", "0.0.0.0"}
	for _, s := range blocked {
		if ip := net.ParseIP(s); !isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "203.0.113.10"}
	for _, s := range allowed {
		if ip := net.ParseIP(s); isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = true, want false", s)
		}
	}
}

// TestDeliveryTransportBlocksLoopbackAtDialTime is the defense-in-depth
// check: even a hostname that only resolves to a private address at connect
// time (rather than being a literal like "127.0.0.1") must not get a
// request through, since that is exactly the DNS-rebinding gap
// ValidateTargetURL alone cannot close.
func TestDeliveryTransportBlocksLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()

	transport := newDeliveryTransport(false, time.Second)
	// httptest.NewServer listens on 127.0.0.1, so dialing its literal address
	// stands in for a hostname that resolved to loopback.
	_, err := transport.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if !errors.Is(err, ErrDisallowedTarget) {
		t.Fatalf("DialContext to a loopback address = %v, want ErrDisallowedTarget", err)
	}
}

func TestDeliveryTransportAllowsPrivateWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()

	transport := newDeliveryTransport(true, time.Second)
	conn, err := transport.DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("DialContext with allowPrivate=true failed: %v", err)
	}
	_ = conn.Close()
}

// TestIsBlockedIPCoversReservedRanges is the regression test for the ranges
// net.IP's own classifiers do not know about. Every address here reaches
// something on the operator's side of the network (or is not a destination at
// all) while looking like an ordinary public address to IsLoopback / IsPrivate
// / IsLinkLocalUnicast / IsUnspecified, which is all the check used to be.
func TestIsBlockedIPCoversReservedRanges(t *testing.T) {
	blocked := map[string]string{
		"0.1.2.3":           `"this network" (RFC 1122): Linux routes the whole 0/8 to the local host`,
		"100.64.0.1":        "carrier-grade NAT shared address space (RFC 6598)",
		"100.127.255.254":   "the far end of the CGNAT range",
		"192.0.0.170":       "IETF protocol assignments (RFC 6890), here the NAT64 address",
		"198.18.0.1":        "benchmarking (RFC 2544), routed to local test equipment",
		"198.19.255.255":    "the far end of the benchmarking range",
		"240.0.0.1":         "reserved for future use (RFC 1112)",
		"255.255.255.255":   "the limited broadcast address",
		"224.0.0.1":         "link-local multicast",
		"239.255.255.250":   "administratively scoped multicast (SSDP), which IsLinkLocalMulticast misses",
		"ff05::1":           "site-local IPv6 multicast, likewise",
		"::ffff:127.0.0.1":  "loopback written as an IPv4-mapped IPv6 address",
		"::ffff:10.0.0.1":   "an RFC 1918 address written the same way",
		"::ffff:100.64.0.1": "a CGNAT address written the same way",
		"::7f00:1":          "loopback as a deprecated IPv4-compatible IPv6 address (RFC 4291)",
		"64:ff9b::7f00:1":   "loopback reached through the NAT64 well-known prefix (RFC 6052)",
		"64:ff9b::c0a8:1":   "192.168.0.1 reached through the same prefix",
		"fc00::1":           "IPv6 unique-local",
		"fe80::1":           "IPv6 link-local",
	}
	for s, why := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test address %q does not parse", s)
		}
		if !isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = false, want true: %s", s, why)
		}
	}

	// The other half of the check: hardening the list must not start refusing
	// ordinary public destinations. The NAT64 prefix in particular is only
	// blocked for what it carries, since an IPv6-only network reaches public
	// IPv4 hosts through it.
	allowed := []string{
		"8.8.8.8",
		"99.255.255.255", // immediately below the CGNAT range
		"100.128.0.1",    // immediately above it
		"192.0.1.1",      // immediately above the protocol-assignment /24
		"198.17.255.255", // immediately below the benchmarking range
		"198.20.0.1",     // immediately above it
		"2001:4860:4860::8888",
		"64:ff9b::8.8.8.8",
		"::ffff:8.8.8.8",
	}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test address %q does not parse", s)
		}
		if isBlockedIP(ip) {
			t.Errorf("isBlockedIP(%s) = true, want false", s)
		}
	}

	// An address that cannot be interpreted at all is refused rather than
	// waved through: the transport hands whatever the connection reports to
	// this function, and "I don't know" must not mean "allow".
	if !isBlockedIP(nil) {
		t.Error("isBlockedIP(nil) = false, want true")
	}
}

// TestValidateTargetURLBlocksLocalhostSubdomains covers RFC 6761's rule that
// the whole .localhost tree resolves to loopback, which internal/config's
// isLoopbackURL already assumes.
func TestValidateTargetURLBlocksLocalhostSubdomains(t *testing.T) {
	for _, u := range []string{"http://hook.localhost/x", "http://Deep.Nested.LOCALHOST:9000/x"} {
		if err := ValidateTargetURL(u, false); !errors.Is(err, ErrDisallowedTarget) {
			t.Errorf("ValidateTargetURL(%q, false) = %v, want ErrDisallowedTarget", u, err)
		}
	}
}

// TestDeliveryTransportUsesTheSameVerdictAsValidateTargetURL pins the property
// the two-layer guard depends on: the connect-time re-check and the write-time
// URL check must agree, because a range only one of them knows about is a hole
// rather than a difference of opinion.
func TestDeliveryTransportUsesTheSameVerdictAsValidateTargetURL(t *testing.T) {
	for _, s := range []string{"100.64.0.1", "192.0.0.170", "198.18.0.1", "240.0.0.1", "0.1.2.3"} {
		urlErr := ValidateTargetURL("http://"+s+"/hook", false)
		if !errors.Is(urlErr, ErrDisallowedTarget) {
			t.Errorf("ValidateTargetURL rejects %s? got %v", s, urlErr)
		}
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("the transport's dial-time check would accept %s that ValidateTargetURL refuses", s)
		}
	}
}

// 6to4 (RFC 3056) carries an IPv4 address in bytes 2..5 of an IPv6 one, so
// 2002:7f00:1:: is a route to 127.0.0.1 -- and it used to walk through both
// layers of this guard, since only NAT64 was unwrapped. Blocking the prefix
// outright would be wrong: 2002:0808:0808:: is a legitimate route to 8.8.8.8.
func TestValidateTargetURL6to4UnwrapsTheEmbeddedIPv4(t *testing.T) {
	blocked := []string{
		"http://[2002:7f00:1::]/hook",   // 127.0.0.1
		"http://[2002:a00:5::]/hook",    // 10.0.0.5
		"http://[2002:c0a8:105::]/hook", // 192.168.1.5
		"http://[2002:a9fe:101::]/hook", // 169.254.1.1
		"http://[64:ff9b::7f00:1]/hook", // NAT64 loopback, unchanged
	}
	for _, u := range blocked {
		if err := ValidateTargetURL(u, false); !errors.Is(err, ErrDisallowedTarget) {
			t.Errorf("ValidateTargetURL(%q, false) = %v, want ErrDisallowedTarget", u, err)
		}
	}
	allowed := []string{
		"http://[2002:808:808::]/hook",   // 8.8.8.8 through a 6to4 relay
		"http://[64:ff9b::808:808]/hook", // 8.8.8.8 through NAT64
	}
	for _, u := range allowed {
		if err := ValidateTargetURL(u, false); err != nil {
			t.Errorf("ValidateTargetURL(%q, false) = %v, want nil", u, err)
		}
	}
	// The connect-time layer asks the same function, so it agrees by
	// construction -- assert it rather than trusting that it stays that way.
	if !isBlockedIP(net.ParseIP("2002:7f00:1::")) {
		t.Error("isBlockedIP(2002:7f00:1::) = false, want true")
	}
}

// Teredo (RFC 4380) and ISATAP (RFC 5214) are the other two ways an IPv4
// address travels inside an IPv6 one, and the comment above isBlockedAddr
// used to claim there were only two such forms in total. There are four:
// 2001:0:509:509::80ff:fffe is 127.0.0.1 as a Teredo client (the low 32 bits
// are the address complemented) and fe80::5efe:7f00:1 is 127.0.0.1 as an
// ISATAP interface identifier, and both used to walk straight through this
// guard.
//
// As with 6to4, the prefix itself cannot be refused: Teredo and ISATAP
// addresses carrying a public IPv4 address are ordinary routes to ordinary
// hosts.
func TestIsBlockedAddrUnwrapsTeredoAndISATAP(t *testing.T) {
	blocked := map[string]string{
		"2001:0:509:509:0:0:80ff:fffe": "127.0.0.1 as a Teredo client behind a public server (^127.0.0.1)",
		"2001:0:509:509:0:0:f5ff:fffe": "10.0.0.1 as a Teredo client behind a public server (^10.0.0.1)",
		"2001:0:7f00:1:0:0:f7f7:f7f7":  "a Teredo relay server on 127.0.0.1",
		"2001:0:c0a8:1:0:0:f7f7:f7f7":  "a Teredo relay server on 192.168.0.1",
		"2001:db8::200:5efe:7f00:1":    "127.0.0.1 as a globally-unique ISATAP identifier",
		"2001:db8::5efe:a00:1":         "10.0.0.1 as an ISATAP identifier",
		"2001:db8::5efe:a9fe:1":        "169.254.0.1 (link-local) as an ISATAP identifier",
	}
	for addr, why := range blocked {
		if !isBlockedIP(net.ParseIP(addr)) {
			t.Errorf("isBlockedIP(%s) = false, want true (%s)", addr, why)
		}
		if err := ValidateTargetURL("http://["+addr+"]/hook", false); !errors.Is(err, ErrDisallowedTarget) {
			t.Errorf("ValidateTargetURL(%s) = %v, want ErrDisallowedTarget (%s)", addr, err, why)
		}
	}

	allowed := map[string]string{
		"2001:0:509:509:0:0:f7f7:f7f7": "a Teredo client on 8.8.8.8 behind a server on 5.9.5.9",
		"2001:db8::200:5efe:808:808":   "8.8.8.8 as an ISATAP identifier",
		// The identifier is what marks an address as ISATAP; without it the
		// low 32 bits are just address bits and must not be judged as IPv4.
		"2001:db8::7f00:1": "an ordinary address whose low bits look like 127.0.0.1",
	}
	for addr, why := range allowed {
		if isBlockedIP(net.ParseIP(addr)) {
			t.Errorf("isBlockedIP(%s) = true, want false (%s)", addr, why)
		}
	}
}

// A target URL may not carry credentials. They would be stored in a column the
// settings UI renders back, and "https://legitimate-host@attacker.example/"
// reads as a URL for the wrong host to every human who reviews it. Deliveries
// authenticate with their HMAC signature, so nothing is lost.
// config.NormalizeEndpoint already refuses userinfo on the URL an operator
// configures; this is the same rule on the URL a user configures.
func TestValidateTargetURLRejectsUserinfo(t *testing.T) {
	for _, u := range []string{
		"https://user:pw@example.com/hook",
		"https://user@example.com/hook",
		"http://legit-host@203.0.113.10/hook",
	} {
		err := ValidateTargetURL(u, false)
		if err == nil {
			t.Errorf("ValidateTargetURL(%q, false) = nil, want an error", u)
		}
	}
	// Still refused when private targets are allowed: this is not a
	// reachability rule, so the local-development escape hatch does not cover
	// it.
	if err := ValidateTargetURL("https://user:pw@example.com/hook", true); err == nil {
		t.Error("ValidateTargetURL(userinfo, allowPrivate=true) = nil, want an error")
	}
}
