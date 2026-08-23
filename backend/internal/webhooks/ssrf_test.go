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
