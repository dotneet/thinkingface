package config

import (
	"io/fs"
	"os"
	"path/filepath"
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

// hooksDir builds a core.hooksPath directory holding a pre-receive hook with
// the given permissions. mode 0 leaves the hook out entirely.
func hooksDir(t *testing.T, mode fs.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	if mode == 0 {
		return dir
	}
	if err := os.WriteFile(filepath.Join(dir, PreReceiveHookName),
		[]byte("#!/bin/sh\nexec /usr/local/bin/thinkingface hook pre-receive\n"), mode); err != nil {
		t.Fatal(err)
	}
	return dir
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
			t.Setenv("TF_GIT_HOOKS_PATH", hooksDir(t, 0o755))
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

// A path that exists is not the same as a hook that runs. git skips a missing
// or non-executable hook *silently* and lets the push through, so an image
// that lost hooks/pre-receive, a typo in the path, or a bind mount shadowing
// the directory would ack every push with no WAL entry and no CAS — and the
// loss would only surface at the next Materialize, where writeRefs deletes the
// refs those pushes created.
func TestCheckPreReceiveHook_RejectsAHookGitWouldSilentlySkip(t *testing.T) {
	cases := []struct {
		name string
		dir  func(t *testing.T) string
		want string
	}{
		{"missing", func(t *testing.T) string { return hooksDir(t, 0) }, "unreadable"},
		{"not executable", func(t *testing.T) string { return hooksDir(t, 0o644) }, "not executable"},
		{"directory does not exist", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "typo")
		}, "unreadable"},
		{"hook is a directory", func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, PreReceiveHookName), 0o755); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "is a directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckPreReceiveHook(tc.dir(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckPreReceiveHook() err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Owner-only 0700 is a correctly locked-down hook, not a broken one: which of
// the three execute bits applies is a property of the deployment.
func TestCheckPreReceiveHook_AcceptsOwnerOnlyExecute(t *testing.T) {
	if err := CheckPreReceiveHook(hooksDir(t, 0o700)); err != nil {
		t.Fatalf("CheckPreReceiveHook: %v", err)
	}
}

// The break-glass regression: this check used to run inside Load, which every
// subcommand calls — so a bind mount shadowing the hooks directory, exactly
// the incident an operator reaches for `thinkingface admin` to recover from,
// made the password reset refuse to start for a reason that has nothing to do
// with resetting a password. Load must be satisfied by the path being set; the
// filesystem is runServe's business.
func TestLoad_DoesNotGateEverySubcommandOnTheHookFile(t *testing.T) {
	setBase(t)
	t.Setenv("TF_WAL_MODE", "authoritative")
	broken := hooksDir(t, 0) // the directory is there, the hook is not
	t.Setenv("TF_GIT_HOOKS_PATH", broken)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v, the break-glass `admin` path is gated on a hook it never runs", err)
	}
	if c.GitHooksPath != broken {
		t.Fatalf("GitHooksPath = %q, want %q", c.GitHooksPath, broken)
	}
	// And the check itself still refuses, so runServe keeps failing closed.
	if err := CheckPreReceiveHook(c.GitHooksPath); err == nil {
		t.Fatal("CheckPreReceiveHook accepted a hooks directory with no hook in it")
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
// The development defaults are public knowledge (.env.example ships the admin
// password, and the default session secret lets anyone forge a tf_session for
// any user id), so the only deployment allowed to boot on them is one nobody
// else can reach. The test is the *host*, not the scheme: an internal
// instance served over plain http on a LAN address is exactly the case the
// old https-only guard let through, and it is the intended deployment shape.
func TestLoad_RefusesDevelopmentSecretsOffLoopback(t *testing.T) {
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
		// Loopback keeps booting on the defaults: this is what `docker compose
		// up` and the e2e suite use, and nothing else can reach it.
		{
			name: "http on localhost still boots",
			env:  map[string]string{"TF_PUBLIC_URL": "http://localhost:8080"},
		},
		{
			name: "http on a literal loopback address still boots",
			env:  map[string]string{"TF_PUBLIC_URL": "http://127.0.0.1:8080"},
		},
		{
			name: "http on an IPv6 loopback address still boots",
			env:  map[string]string{"TF_PUBLIC_URL": "http://[::1]:8080"},
		},
		{
			name: "a .localhost subdomain still boots",
			env:  map[string]string{"TF_PUBLIC_URL": "http://api.localhost:8080"},
		},
		// The case the https-only guard missed. An internal deployment on a
		// LAN name is reachable by everyone on that network, so the published
		// defaults are no more acceptable there than on the public internet.
		{
			name:    "http on an internal hostname refuses the default admin password",
			env:     map[string]string{"TF_PUBLIC_URL": "http://hub.internal", "TF_SESSION_SECRET": longSecret},
			wantErr: "TF_ADMIN_PASSWORD",
		},
		{
			name:    "http on an internal hostname refuses the default session secret",
			env:     map[string]string{"TF_PUBLIC_URL": "http://hub.internal", "TF_ADMIN_PASSWORD": "hunter2hunter2"},
			wantErr: "TF_SESSION_SECRET",
		},
		{
			name: "http on a LAN address refuses a too-short session secret",
			env: map[string]string{
				"TF_PUBLIC_URL": "http://10.0.0.5:8080", "TF_ADMIN_PASSWORD": "hunter2hunter2",
				"TF_SESSION_SECRET": "short",
			},
			wantErr: "at least 32 bytes",
		},
		{
			name: "http on an internal hostname boots once configured",
			env: map[string]string{
				"TF_PUBLIC_URL": "http://hub.internal", "TF_ADMIN_PASSWORD": "hunter2hunter2",
				"TF_SESSION_SECRET": longSecret,
			},
		},
		// A URL that cannot be parsed is not loopback, so it fails closed.
		{
			name:    "an unparseable public URL is not treated as loopback",
			env:     map[string]string{"TF_PUBLIC_URL": "://nonsense", "TF_SESSION_SECRET": longSecret},
			wantErr: "TF_ADMIN_PASSWORD",
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

// TF_SIGNUP_EMAIL_DOMAINS is normalised once, here, so that every comparison
// against it stays a plain string equality (see api.checkSignupEmailDomain).
func TestLoad_SignupEmailDomainsAreNormalised(t *testing.T) {
	setBase(t)
	t.Setenv("TF_SIGNUP_EMAIL_DOMAINS", " Example.COM , @corp.example.org ,, ")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"example.com", "corp.example.org"}
	if len(c.SignupEmailDomains) != len(want) {
		t.Fatalf("SignupEmailDomains = %q, want %q", c.SignupEmailDomains, want)
	}
	for i := range want {
		if c.SignupEmailDomains[i] != want[i] {
			t.Fatalf("SignupEmailDomains = %q, want %q", c.SignupEmailDomains, want)
		}
	}
	if c.SignupRequireApproval {
		t.Error("TF_SIGNUP_REQUIRE_APPROVAL defaults to true; it must default to off")
	}
}

// An unset (or empty) value is no restriction at all -- nil rather than a
// one-element list containing the empty string, which would refuse everybody.
func TestLoad_SignupEmailDomainsEmptyMeansUnrestricted(t *testing.T) {
	setBase(t)
	t.Setenv("TF_SIGNUP_EMAIL_DOMAINS", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.SignupEmailDomains) != 0 {
		t.Fatalf("SignupEmailDomains = %q, want empty", c.SignupEmailDomains)
	}
}

// A signed URL is minted as `now + TF_SIGNED_URL_TTL`, and lfs.TTLFor only ever
// clamps that value down -- so a zero or negative base hands out URLs that are
// already expired and every LFS transfer fails against GCS with a 403 that
// names no cause. Nothing in the logs would say why, which is what makes this
// a startup check rather than something to notice in production.
func TestLoad_SignedURLTTLMustBePositive(t *testing.T) {
	for _, v := range []string{"-1h", "0"} {
		t.Run(v, func(t *testing.T) {
			setBase(t)
			t.Setenv("TF_SIGNED_URL_TTL", v)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TF_SIGNED_URL_TTL") {
				t.Fatalf("Load() err = %v, want TF_SIGNED_URL_TTL validation error", err)
			}
		})
	}
}

// The ceiling is different: lfs.TTLFor reads "<= 0" as "no ceiling of my own",
// which is a documented way to run it (the signing limit still applies). Only a
// negative value is refused, since as a ceiling it can only mean "expired".
func TestLoad_SignedURLMaxTTLZeroMeansNoCeiling(t *testing.T) {
	setBase(t)
	t.Setenv("TF_SIGNED_URL_MAX_TTL", "0")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SignedURLMaxTTL != 0 {
		t.Fatalf("SignedURLMaxTTL = %v, want 0", c.SignedURLMaxTTL)
	}

	setBase(t)
	t.Setenv("TF_SIGNED_URL_MAX_TTL", "-30m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TF_SIGNED_URL_MAX_TTL") {
		t.Fatalf("Load() err = %v, want TF_SIGNED_URL_MAX_TTL validation error", err)
	}
}

// A ceiling below the base means one of the two values is not doing what it was
// set for: TTLFor honours the ceiling, so the base is never reached.
func TestLoad_SignedURLMaxTTLMustNotBeBelowTheBase(t *testing.T) {
	setBase(t)
	t.Setenv("TF_SIGNED_URL_TTL", "6h")
	t.Setenv("TF_SIGNED_URL_MAX_TTL", "1h")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TF_SIGNED_URL_MAX_TTL") {
		t.Fatalf("Load() err = %v, want a base/ceiling ordering error", err)
	}
}

// The deployment this check actually stops is not one with a contradictory
// pair of values: it is one that set TF_SIGNED_URL_TTL above 12h alone,
// years ago, and is being upgraded onto a server that now refuses to start.
// Refusing is deliberate -- resolving it by raising the ceiling would
// lengthen the life of every signed URL the instance issues, silently, during
// an upgrade -- so the whole burden falls on the message, which has to name
// the *other* variable and the value that would make this one legal.
func TestLoad_SignedURLTTLAboveTheDefaultCeilingSaysHowToFixIt(t *testing.T) {
	setBase(t)
	t.Setenv("TF_SIGNED_URL_TTL", "24h")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded, want a base/ceiling ordering error")
	}
	msg := err.Error()
	for _, want := range []string{
		// Both variables, so the operator knows which pair is in conflict...
		"TF_SIGNED_URL_TTL",
		"TF_SIGNED_URL_MAX_TTL",
		// ...that the ceiling they never set is the default one...
		"not set",
		DefaultSignedURLMaxTTL.String(),
		// ...and both ways out, with the values that would work.
		"raise TF_SIGNED_URL_MAX_TTL to 24h0m0s or more",
		"lower TF_SIGNED_URL_TTL to 12h0m0s or less",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}

	// An explicitly set ceiling gets the same refusal without the "not set"
	// aside, which would be a lie.
	setBase(t)
	t.Setenv("TF_SIGNED_URL_TTL", "24h")
	t.Setenv("TF_SIGNED_URL_MAX_TTL", DefaultSignedURLMaxTTL.String())
	_, err = Load()
	if err == nil {
		t.Fatal("Load() succeeded with an explicit ceiling below the base")
	}
	if strings.Contains(err.Error(), "not set") {
		t.Errorf("error %q claims TF_SIGNED_URL_MAX_TTL is unset", err)
	}
}

// The defaults have to satisfy their own rules, or every deployment that sets
// neither would be refused.
func TestLoad_SignedURLDefaultsAreValid(t *testing.T) {
	setBase(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SignedURLTTL != time.Hour || c.SignedURLMaxTTL != 12*time.Hour {
		t.Fatalf("defaults = %v / %v, want 1h / 12h", c.SignedURLTTL, c.SignedURLMaxTTL)
	}
}
