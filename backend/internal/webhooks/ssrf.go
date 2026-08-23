package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	if allowPrivate {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("%w: %s", ErrDisallowedTarget, host)
	}
	if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
		return fmt.Errorf("%w: %s", ErrDisallowedTarget, host)
	}
	return nil
}

// isBlockedIP covers every range the task calls out by name (127.0.0.0/8,
// 10/8, 172.16/12, 192.168/16, 169.254/16, ::1) plus the handful of other
// non-routable ranges the stdlib already classifies for us, so the allowlist
// doesn't have to be maintained by hand.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
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
