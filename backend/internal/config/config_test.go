package config

import (
	"strings"
	"testing"
	"time"
)

// setBase supplies the minimum environment Load requires, so each test only
// states what it is about.
func setBase(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://x/y")
	t.Setenv("STORAGE_DRIVER", "gcs")
	t.Setenv("TF_WAL_MODE", "")
	t.Setenv("TF_GIT_HOOKS_PATH", "")
}

func TestLoad_WALModeDefaultsToOff(t *testing.T) {
	setBase(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.WALMode != "off" {
		t.Fatalf("WALMode = %q, want off", c.WALMode)
	}
}

func TestLoad_WALModeRejectsUnknownValues(t *testing.T) {
	setBase(t)
	t.Setenv("TF_WAL_MODE", "authoritive") // near-miss of "authoritative" must not boot
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TF_WAL_MODE") {
		t.Fatalf("Load() err = %v, want TF_WAL_MODE validation error", err)
	}
}

// The half-wired state — WAL on but no hook path — would let every git push
// bypass the WAL. Refusing to start is the guard.
func TestLoad_WALModeRequiresHooksPath(t *testing.T) {
	for _, mode := range []string{"shadow", "authoritative"} {
		t.Run(mode, func(t *testing.T) {
			setBase(t)
			t.Setenv("TF_WAL_MODE", mode)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TF_GIT_HOOKS_PATH") {
				t.Fatalf("Load() err = %v, want TF_GIT_HOOKS_PATH requirement", err)
			}
			t.Setenv("TF_GIT_HOOKS_PATH", "/opt/thinkingface/hooks")
			c, err := Load()
			if err != nil {
				t.Fatalf("Load with hooks path: %v", err)
			}
			if c.WALMode != mode {
				t.Fatalf("WALMode = %q, want %q", c.WALMode, mode)
			}
		})
	}
}

// DATABASE_URL scheme validation. Anything other than postgres:// /
// postgresql:// / sqlite:// is rejected at startup (guards against a typo
// silently booting with the wrong driver).
func TestLoad_DatabaseURLAcceptsSQLiteScheme(t *testing.T) {
	setBase(t)
	t.Setenv("DATABASE_URL", "sqlite:///data/db/thinkingface.db")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DatabaseURL != "sqlite:///data/db/thinkingface.db" {
		t.Fatalf("DatabaseURL = %q, want sqlite:///data/db/thinkingface.db", c.DatabaseURL)
	}
}

func TestLoad_DatabaseURLRejectsUnknownScheme(t *testing.T) {
	setBase(t)
	t.Setenv("DATABASE_URL", "mysql://x/y")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL must start with") {
		t.Fatalf("Load() err = %v, want DATABASE_URL scheme validation error", err)
	}
}

func TestLoad_CacheBudgetsHaveDefaultsAndParse(t *testing.T) {
	setBase(t)
	t.Setenv("TF_GIT_CACHE_BYTES", "1048576")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitCacheBytes != 1<<20 {
		t.Fatalf("GitCacheBytes = %d, want %d", c.GitCacheBytes, 1<<20)
	}
	if c.ViewerMetadataCacheBytes != 256<<20 {
		t.Fatalf("ViewerMetadataCacheBytes default = %d, want %d", c.ViewerMetadataCacheBytes, int64(256<<20))
	}
}

// SignedURLTTL is the floor and SignedURLMaxTTL the ceiling of a signed
// transfer URL's size-derived lifetime; the ceiling must default well above
// the floor or every upload would clamp to the same duration.
func TestLoad_SignedURLTTLDefaultsToFloorAndCeiling(t *testing.T) {
	setBase(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SignedURLTTL != time.Hour {
		t.Fatalf("SignedURLTTL default = %v, want %v", c.SignedURLTTL, time.Hour)
	}
	if c.SignedURLMaxTTL != 12*time.Hour {
		t.Fatalf("SignedURLMaxTTL default = %v, want %v", c.SignedURLMaxTTL, 12*time.Hour)
	}
}

func TestLoad_SignedURLMaxTTLParsesFromEnv(t *testing.T) {
	setBase(t)
	t.Setenv("TF_SIGNED_URL_MAX_TTL", "6h")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SignedURLMaxTTL != 6*time.Hour {
		t.Fatalf("SignedURLMaxTTL = %v, want %v", c.SignedURLMaxTTL, 6*time.Hour)
	}
}

// [S6] / [S2] The development defaults are published in .env.example: the
// seeded admin password is a known value and the default session secret lets
// anyone forge a tf_session for any user id. An https public URL is the
// clearest available signal that an instance is not a laptop, so it is the
// line where a warning becomes a refusal. Plain http keeps booting on the
// defaults -- `docker compose up` and the e2e suite depend on that.
func TestLoad_RefusesDevelopmentSecretsOverHTTPS(t *testing.T) {
	const longSecret = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "https with the default admin password",
			env:     map[string]string{"TF_PUBLIC_URL": "https://hub.example.com", "TF_SESSION_SECRET": longSecret},
			wantErr: "TF_ADMIN_PASSWORD",
		},
		{
			name:    "https with the default session secret",
			env:     map[string]string{"TF_PUBLIC_URL": "https://hub.example.com", "TF_ADMIN_PASSWORD": "hunter2hunter2"},
			wantErr: "TF_SESSION_SECRET",
		},
		{
			name: "https with a too-short session secret",
			env: map[string]string{
				"TF_PUBLIC_URL": "https://hub.example.com", "TF_ADMIN_PASSWORD": "hunter2hunter2",
				"TF_SESSION_SECRET": "short",
			},
			wantErr: "at least 32 bytes",
		},
		{
			name: "https fully configured",
			env: map[string]string{
				"TF_PUBLIC_URL": "https://hub.example.com", "TF_ADMIN_PASSWORD": "hunter2hunter2",
				"TF_SESSION_SECRET": longSecret,
			},
		},
		{
			name: "http on the defaults still boots",
			env:  map[string]string{"TF_PUBLIC_URL": "http://localhost:8080"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBase(t)
			t.Setenv("TF_ADMIN_PASSWORD", "")
			t.Setenv("TF_SESSION_SECRET", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Load() = %v, want success", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Load() = nil, want an error mentioning %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Load() = %v, want an error mentioning %q", err, tt.wantErr)
			}
		})
	}
}

// [S4] Unset TF_ALLOWED_ORIGINS must still let the local web UI talk to a
// development instance, while a production (https) one gets only its own
// origin -- naming the web host there is a deliberate act.
func TestLoad_AllowedOriginsDefaults(t *testing.T) {
	t.Run("http development", func(t *testing.T) {
		setBase(t)
		t.Setenv("TF_PUBLIC_URL", "http://localhost:8080")
		t.Setenv("TF_ALLOWED_ORIGINS", "")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		want := []string{"http://localhost:8080", "http://localhost:3000", "http://127.0.0.1:3000"}
		if len(c.AllowedOrigins) != len(want) {
			t.Fatalf("AllowedOrigins = %v, want %v", c.AllowedOrigins, want)
		}
		for i := range want {
			if c.AllowedOrigins[i] != want[i] {
				t.Fatalf("AllowedOrigins = %v, want %v", c.AllowedOrigins, want)
			}
		}
	})

	t.Run("https production", func(t *testing.T) {
		setBase(t)
		t.Setenv("TF_PUBLIC_URL", "https://api.hub.example.com/")
		t.Setenv("TF_ADMIN_PASSWORD", "hunter2hunter2")
		t.Setenv("TF_SESSION_SECRET", "0123456789abcdef0123456789abcdef")
		t.Setenv("TF_ALLOWED_ORIGINS", "")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(c.AllowedOrigins) != 1 || c.AllowedOrigins[0] != "https://api.hub.example.com" {
			t.Fatalf("AllowedOrigins = %v, want just the public origin", c.AllowedOrigins)
		}
	})

	t.Run("explicit list wins", func(t *testing.T) {
		setBase(t)
		t.Setenv("TF_PUBLIC_URL", "http://localhost:8080")
		t.Setenv("TF_ALLOWED_ORIGINS", "https://a.example.com, https://b.example.com/ ,")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(c.AllowedOrigins) != 2 ||
			c.AllowedOrigins[0] != "https://a.example.com" ||
			c.AllowedOrigins[1] != "https://b.example.com" {
			t.Fatalf("AllowedOrigins = %v, want the two named origins trimmed", c.AllowedOrigins)
		}
	})
}

// [S6] TF_COOKIE_SECURE has to be tri-state: unset means "infer", and an
// explicit false must not be mistaken for unset.
func TestLoad_CookieSecureIsTriState(t *testing.T) {
	setBase(t)
	t.Setenv("TF_PUBLIC_URL", "http://localhost:8080")
	t.Setenv("TF_COOKIE_SECURE", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.CookieSecure != nil {
		t.Fatalf("CookieSecure = %v, want nil when unset", *c.CookieSecure)
	}

	for _, raw := range []string{"true", "false"} {
		t.Setenv("TF_COOKIE_SECURE", raw)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.CookieSecure == nil {
			t.Fatalf("CookieSecure = nil for TF_COOKIE_SECURE=%s", raw)
		}
		if got := *c.CookieSecure; got != (raw == "true") {
			t.Fatalf("CookieSecure = %v for TF_COOKIE_SECURE=%s", got, raw)
		}
	}
}

// A number/boolean/duration that does not parse is a startup error, not a
// silent fall back to the default: an operator who typed TF_SYNC_WORKERS=two
// would otherwise run on 2 workers believing they had set something else.
func TestLoad_RejectsUnparseableTypedValues(t *testing.T) {
	cases := []struct{ key, value string }{
		{"TF_SYNC_WORKERS", "two"},
		{"TF_WEBHOOK_WORKERS", "1.5"},
		{"TF_AUTH_RATE_LIMIT_PER_MIN", "-"},
		{"TF_GIT_CACHE_BYTES", "2GB"},
		{"TF_VIEWER_METADATA_CACHE_BYTES", "256MiB"},
		{"TF_SESSION_TTL", "7dd"},
		{"TF_SIGNED_URL_TTL", "1 hour"},
		{"TF_EXP_FLUSH_INTERVAL", "60"},
		{"TF_ALLOW_SIGNUP", "yes"},
		{"TF_SSH_ENABLED", "on"},
		{"TF_COOKIE_SECURE", "maybe"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			setBase(t)
			t.Setenv(c.key, c.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("Load() with %s=%q err = %v, want an error naming %s", c.key, c.value, err, c.key)
			}
		})
	}
}

// The other half of the rule: an unset variable is not an invalid one, and
// still yields the documented default.
func TestLoad_UnsetTypedValuesKeepTheirDefaults(t *testing.T) {
	setBase(t)
	for _, key := range []string{
		"TF_SYNC_WORKERS", "TF_GIT_CACHE_BYTES", "TF_SESSION_TTL",
		"TF_ALLOW_SIGNUP", "TF_SSH_ENABLED", "TF_COOKIE_SECURE",
	} {
		t.Setenv(key, "")
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SyncWorkers != 2 || c.GitCacheBytes != 2<<30 || c.SessionTTL != 7*24*time.Hour ||
		!c.AllowSignup || c.SSHEnabled || c.CookieSecure != nil {
		t.Fatalf("defaults not applied: %+v", c)
	}
}
