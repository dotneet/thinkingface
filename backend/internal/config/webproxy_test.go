package config

import (
	"strings"
	"testing"
)

func TestLoad_TrustedWebProxiesParse(t *testing.T) {
	setBase(t)
	t.Setenv("TF_TRUSTED_WEB_PROXIES", " web , 172.18.0.0/16,, 10.0.0.7 ")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"web", "172.18.0.0/16", "10.0.0.7"}
	if strings.Join(c.TrustedWebProxies, "|") != strings.Join(want, "|") {
		t.Fatalf("TrustedWebProxies = %q, want %q", c.TrustedWebProxies, want)
	}
}

func TestLoad_TrustedWebProxiesRefusesURLsAndPorts(t *testing.T) {
	for _, bad := range []string{"http://web", "web:3000", "we b", "a..b", "10.0.0.0/33"} {
		t.Run(bad, func(t *testing.T) {
			setBase(t)
			t.Setenv("TF_TRUSTED_WEB_PROXIES", bad)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TF_TRUSTED_WEB_PROXIES") {
				t.Fatalf("Load() error = %v; want a TF_TRUSTED_WEB_PROXIES error", err)
			}
		})
	}
}

// Whoever knows the secret chooses their own rate-limit bucket, so a short one
// is refused rather than accepted as a weaker guarantee.
func TestLoad_WebProxySecretMustBeLong(t *testing.T) {
	setBase(t)
	t.Setenv("TF_WEB_PROXY_SECRET", "short")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TF_WEB_PROXY_SECRET") {
		t.Fatalf("Load() error = %v; want a TF_WEB_PROXY_SECRET length error", err)
	}
	t.Setenv("TF_WEB_PROXY_SECRET", strings.Repeat("x", MinSessionSecretLen))
	if _, err := Load(); err != nil {
		t.Fatalf("Load() with a long secret: %v", err)
	}
}
