package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ErrDisallowedTarget marks a delivery URL that resolves (or is a literal)
// local/private address without the operator opting in via
// TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS. It is reported to callers as a bad
// request, never as a server error.
var ErrDisallowedTarget = errors.New("webhooks: target address is local or private")

// ValidateTargetURL is the cheap, synchronous check run when a webhook is
// created or edited: only http/https, and (unless allowPrivate) not an
// address that is obviously local. It is a best-effort surface-level check —
// a hostname that only resolves to a private address at delivery time slips
// past it by design, which is exactly why the delivery transport below
// re-checks the address it actually connects to.
func ValidateTargetURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("URL must include a host")
	}
	// No userinfo. config.NormalizeEndpoint already refuses it on the URL the
	// operator configures, and a webhook target is the same kind of value with
	// the same two problems: the credentials are stored in a column the
	// settings UI renders back to anyone who can administer the namespace, and
	// "http://legitimate-host@attacker.example/" reads as a URL for
	// legitimate-host to every human who reviews it while naming a completely
	// different server. There is nothing it buys either -- the delivery
	// carries its own HMAC signature (sign.go), not HTTP credentials.
	if u.User != nil {
		return errors.New("URL must not carry a username or password")
	}
	if allowPrivate {
		return nil
	}
	// ".localhost" as well as "localhost" itself: RFC 6761 reserves the whole
	// tree for loopback and resolvers honour it, so "hook.localhost" is just
	// another spelling of the address the line below refuses. config's
	// isLoopbackURL already treats the two the same way.
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("%w: %s", ErrDisallowedTarget, host)
	}
	if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
		return fmt.Errorf("%w: %s", ErrDisallowedTarget, host)
	}
	return nil
}

// blockedPrefixes are the reserved ranges no net.IP / netip.Addr method
// classifies for us. Each one is listed with the reason it does not belong in
// a webhook URL, because an entry nobody can justify is an entry nobody dares
// remove:
//
//   - 0.0.0.0/8 — "this host, this network" (RFC 1122 §3.2.1.3). Only 0.0.0.0
//     itself is IsUnspecified, yet Linux treats the whole /8 as the local
//     host, which makes http://0.1.2.3/ another spelling of loopback.
//   - 100.64.0.0/10 — shared address space for carrier-grade NAT (RFC 6598).
//     On a CGNAT'd network these addresses are other subscribers' equipment,
//     not the public internet.
//   - 192.0.0.0/24 — IETF protocol assignments (RFC 6890), which is where the
//     DS-Lite gateway (192.0.0.2) and the NAT64 well-known address
//     (192.0.0.170) live. All of them answer on the operator's own side.
//   - 198.18.0.0/15 — benchmarking (RFC 2544). Where it is routed at all it
//     points at local test equipment.
//   - 240.0.0.0/4 — reserved for future use (RFC 1112), which also covers the
//     limited broadcast address 255.255.255.255.
//   - ::/96 — IPv4-compatible IPv6, deprecated by RFC 4291. ::7f00:1 is
//     127.0.0.1 written in a form To4/Unmap does not translate, so without
//     this it would slip past every loopback check there is.
//
// The documentation ranges (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24,
// 2001:db8::/32) are deliberately absent: they are unreachable rather than
// dangerous, and the existing tests use them to stand in for public addresses.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
}

// nat64WellKnown is the prefix a NAT64 translator maps IPv4 into (RFC 6052).
// Blocking it outright would be wrong -- an IPv6-only network legitimately
// reaches public IPv4 hosts through it -- so the IPv4 address it carries in
// its low 32 bits is pulled out and judged on its own instead. 64:ff9b::7f00:1
// is a route to 127.0.0.1 and has to be refused; 64:ff9b::8.8.8.8 is not.
var nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")

// sixToFour is 6to4 (RFC 3056), another prefix that carries an IPv4 address
// inside an IPv6 one: the 32 bits after 2002:: are the IPv4 address of the
// relay/host the packet is destined for. It is treated exactly like NAT64
// above -- extract and judge the embedded address -- and for the same reason:
// 2002:7f00:1:: is a route to 127.0.0.1, so without this
// "http://[2002:7f00:1::]/hook" walked through both layers of this guard, the
// write-time URL check and the connect-time re-check, and reached loopback.
// Blocking the prefix outright would be wrong for the same reason it is wrong
// for NAT64: 2002:0808:0808:: is a legitimate route to 8.8.8.8.
var sixToFour = netip.MustParsePrefix("2002::/16")

// teredo is RFC 4380's prefix. A Teredo address is a tunnel endpoint written
// as an IPv6 one: bits 32..63 hold the relay server's IPv4 address and the
// last 32 bits the client's, obfuscated by a bitwise NOT so it does not show
// up in a naive scan of the address. Both are extracted and judged, for the
// same reason 6to4 is: 2001:0:4136:e378:8000:63bf:3fff:fdd2 is a route to a
// real host, and the shape says nothing about whether that host is local.
// A client on 127.0.0.1 behind a relay on 5.9.5.9 is spelled
// 2001:0:509:509:0:0:80ff:fffe -- 80ff:fffe being the complement of
// 7f00:0001.
var teredo = netip.MustParsePrefix("2001::/32")

// isatapIID matches the interface identifier RFC 5214 gives an ISATAP
// address: 00-00-5e-fe (or 02-00-5e-fe when the address is claimed to be
// globally unique) followed by the IPv4 address, in the low 64 bits of *any*
// prefix. That last part is why it is matched on the suffix rather than
// listed as a prefix like the other translations.
func isatapIID(b [16]byte) bool {
	return (b[8] == 0x00 || b[8] == 0x02) && b[9] == 0x00 && b[10] == 0x5e && b[11] == 0xfe
}

// isBlockedIP is the single verdict both layers of this guard ask for: the
// write-time URL check in ValidateTargetURL and the connect-time re-check in
// newDeliveryTransport. Keeping one function is the point -- an address the
// first layer refuses and the second accepts (or the other way round) is a
// hole, not a difference of opinion.
func isBlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		// Not something that can be reasoned about (nil, or a slice of the
		// wrong length). Refusing is the only safe answer.
		return true
	}
	return isBlockedAddr(addr)
}

func isBlockedAddr(addr netip.Addr) bool {
	// An IPv4-mapped address (::ffff:127.0.0.1) names exactly the same
	// destination as the IPv4 address inside it, so everything below has to
	// see the unwrapped form. net.IP's own methods unmap internally; the
	// prefix list cannot, and a mapped spelling would otherwise be a free
	// bypass of every range in it.
	addr = addr.Unmap()

	if addr.IsLoopback() ||
		addr.IsPrivate() || // RFC 1918 and IPv6 unique-local fc00::/7
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() || // wider than IsLinkLocalMulticast: all of 224/4 and ff00::/8
		addr.IsUnspecified() {
		return true
	}
	// The IPv6 forms that carry an IPv4 address inside them. None can be
	// blocked wholesale -- each is a legitimate way to reach a public IPv4
	// host -- so the embedded address is pulled out and judged on its own.
	// Where it sits, and how it is encoded, differs in every one of them.
	//
	// These four (plus the IPv4-mapped form unwrapped above) are the
	// translations that can be recognised from the address alone. A NAT64
	// deployed on a network-specific prefix (RFC 6052 §3.1 allows /32, /40,
	// /48, /56 and /64 prefixes chosen by the operator) is *not* among them:
	// nothing in such an address distinguishes it from any other global
	// unicast address, so on a network that runs one, this guard sees only
	// the outer address. That is a known limit, not an oversight -- an
	// operator in that position wants the prefix in a deny list of their own.
	if nat64WellKnown.Contains(addr) {
		b := addr.As16()
		return isBlockedAddr(netip.AddrFrom4([4]byte(b[12:16])))
	}
	if sixToFour.Contains(addr) {
		b := addr.As16()
		return isBlockedAddr(netip.AddrFrom4([4]byte(b[2:6])))
	}
	if teredo.Contains(addr) {
		b := addr.As16()
		// Either endpoint being local is enough to refuse: a packet to a
		// Teredo client is delivered through its server, so a local server
		// address is as much a route inwards as a local client address is.
		server := netip.AddrFrom4([4]byte(b[4:8]))
		client := netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]})
		return isBlockedAddr(server) || isBlockedAddr(client)
	}
	if b := addr.As16(); addr.Is6() && isatapIID(b) {
		return isBlockedAddr(netip.AddrFrom4([4]byte(b[12:16])))
	}
	for _, p := range blockedPrefixes {
		// Contains is family-aware, so an IPv4 prefix never matches an IPv6
		// address and vice versa.
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// newDeliveryTransport builds the HTTP transport deliveries are sent through.
// Scheme/host validation at write time (ValidateTargetURL) cannot stop a
// hostname that resolves to a private address only at request time (DNS
// rebinding, or a webhook created before an operator's DNS changed), so this
// transport re-validates the actual IP address of every connection it opens
// and refuses to use it if that address is local/private — closing the gap
// ValidateTargetURL leaves open, unless allowPrivate opts out for local dev.
func newDeliveryTransport(allowPrivate bool, timeout time.Duration) *http.Transport {
	dialer := &net.Dialer{Timeout: timeout}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if allowPrivate {
				return conn, nil
			}
			host, _, splitErr := net.SplitHostPort(conn.RemoteAddr().String())
			if splitErr != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("webhooks: could not read remote address: %w", splitErr)
			}
			ip := net.ParseIP(host)
			if ip == nil || isBlockedIP(ip) {
				_ = conn.Close()
				return nil, fmt.Errorf("%w: %s", ErrDisallowedTarget, host)
			}
			return conn, nil
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		// Deliveries are one-shot, low-volume requests; nothing here benefits
		// from keeping idle connections around, and disabling reuse keeps
		// every request's DialContext check honest.
		DisableKeepAlives: true,
	}
}
