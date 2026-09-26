package config

import (
	"fmt"
	"strings"
	"testing"
)

// attrsToMap turns the flat (key, value, key, value, ...) slice LogAttrs
// returns into a map, failing the test if it is malformed (odd length or a
// non-string key), so every test below can assert against something the
// eventual slog call would actually receive.
func attrsToMap(t *testing.T, attrs []any) map[string]any {
	t.Helper()
	if len(attrs)%2 != 0 {
		t.Fatalf("LogAttrs() returned an odd number of elements: %d", len(attrs))
	}
	m := make(map[string]any, len(attrs)/2)
	for i := 0; i < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok {
			t.Fatalf("LogAttrs()[%d] = %v (%T), want a string key", i, attrs[i], attrs[i])
		}
		m[key] = attrs[i+1]
	}
	return m
}

// TestLogAttrs_NeverLeaksSecrets is the load-bearing test: whatever fields
// LogAttrs gains over time, none of its *values* may ever contain the
// database password, the session secret, or the admin password -- only
// booleans saying whether the secret-shaped settings are still at their
// well-known development defaults.
func TestLogAttrs_NeverLeaksSecrets(t *testing.T) {
	const (
		dsnPassword   = "correct-horse-battery-staple"
		sessionSecret = "s3cr3t-session-value-that-must-never-be-logged-0123456789"
		adminPassword = "hunter2-admin-password"
		proxySecret   = "web-proxy-secret-that-must-never-be-logged-012345"
		databaseURL   = "postgres://tf:" + dsnPassword + "@db.internal:5432/thinkingface?sslmode=disable"
	)
	c := &Config{
		PublicURL:     "https://hub.example.com",
		Addr:          ":8080",
		DatabaseURL:   databaseURL,
		StorageDriver: "gcs",
		GCSBucket:     "thinkingface",
		SessionSecret: sessionSecret,
		AdminPassword: adminPassword,

		WebProxySecret: proxySecret,
	}

	attrs := c.LogAttrs()
	m := attrsToMap(t, attrs)

	// Render the way slog would (fmt.Sprint over every value) and check the
	// secrets are absent from the rendered line, not just from the map's
	// direct values -- a slice or nested struct could still smuggle one in.
	var rendered strings.Builder
	for _, a := range attrs {
		fmt.Fprintf(&rendered, "%v ", a)
	}
	line := rendered.String()

	for _, secret := range []string{dsnPassword, sessionSecret, adminPassword, databaseURL, proxySecret} {
		if strings.Contains(line, secret) {
			t.Fatalf("LogAttrs() output contains a secret value %q:\n%s", secret, line)
		}
	}

	if got, want := m["database_driver"], "postgres"; got != want {
		t.Errorf("database_driver = %v, want %v", got, want)
	}
	if got, want := m["session_secret_default"], false; got != want {
		t.Errorf("session_secret_default = %v, want %v (a non-default secret was set)", got, want)
	}
	if got, want := m["admin_password_default"], false; got != want {
		t.Errorf("admin_password_default = %v, want %v (a non-default password was set)", got, want)
	}
	if got, want := m["web_proxy_secret_set"], true; got != want {
		t.Errorf("web_proxy_secret_set = %v, want %v", got, want)
	}
}

func TestLogAttrs_FlagsDevelopmentDefaults(t *testing.T) {
	c := &Config{
		DatabaseURL:   "sqlite:///tmp/thinkingface.db",
		SessionSecret: DefaultSessionSecret,
		AdminPassword: DefaultAdminPassword,
	}
	m := attrsToMap(t, c.LogAttrs())

	if got, want := m["database_driver"], "sqlite"; got != want {
		t.Errorf("database_driver = %v, want %v", got, want)
	}
	if got, want := m["session_secret_default"], true; got != want {
		t.Errorf("session_secret_default = %v, want %v", got, want)
	}
	if got, want := m["admin_password_default"], true; got != want {
		t.Errorf("admin_password_default = %v, want %v", got, want)
	}
}

func TestLogAttrs_CookieSecureReflectsInferenceState(t *testing.T) {
	c := &Config{DatabaseURL: "sqlite:///tmp/x.db"}
	m := attrsToMap(t, c.LogAttrs())
	if got, want := m["cookie_secure"], "inferred"; got != want {
		t.Errorf("cookie_secure (nil CookieSecure) = %v, want %v", got, want)
	}

	yes := true
	c.CookieSecure = &yes
	m = attrsToMap(t, c.LogAttrs())
	if got, want := m["cookie_secure"], "true"; got != want {
		t.Errorf("cookie_secure (true) = %v, want %v", got, want)
	}

	no := false
	c.CookieSecure = &no
	m = attrsToMap(t, c.LogAttrs())
	if got, want := m["cookie_secure"], "false"; got != want {
		t.Errorf("cookie_secure (false) = %v, want %v", got, want)
	}
}

func TestDatabaseDriver(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@h/db":   "postgres",
		"postgresql://u:p@h/db": "postgres",
		"sqlite:///tmp/x.db":    "sqlite",
		"mysql://u:p@h/db":      "unknown",
		"":                      "unknown",
	}
	for url, want := range cases {
		if got := DatabaseDriver(url); got != want {
			t.Errorf("DatabaseDriver(%q) = %q, want %q", url, got, want)
		}
	}
}
