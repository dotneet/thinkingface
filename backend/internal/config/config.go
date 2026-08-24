// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Development defaults that are safe only on a laptop. They are named so the
// server can warn when a deployment is still running on them.
const (
	DefaultAdminPassword = "admin"
	DefaultSessionSecret = "dev-insecure-session-secret"
)

// MinSessionSecretLen is the shortest session secret accepted in a production
// (https) deployment. The secret keys an HMAC-SHA256 that both session
// cookies and LFS transfer URLs rely on, so anything shorter than the digest's
// block-relevant size is not worth signing with.
const MinSessionSecretLen = 32

type Config struct {
	Addr        string
	PublicURL   string
	DatabaseURL string

	GitRoot string

	StorageDriver string // "gcs" or "gcs-emulator"
	GCSBucket     string
	GCSPrefix     string
	EmulatorHost  string

	// WALMode selects how far the Continuity migration has progressed
	// (docs/dev/continuity-design.md §15):
	//   "off"           — WAL untouched; the on-disk repositories are the truth
	//   "shadow"        — pushes are mirrored into the WAL best-effort; disk
	//                     stays authoritative and WAL failures never fail a push
	//   "authoritative" — the WAL is the truth: reads materialise from it and
	//                     a push is acknowledged only after its CAS (§6)
	WALMode string
	// GitHooksPath is the core.hooksPath directory baked into the image
	// (/opt/thinkingface/hooks). Empty disables hook wiring entirely, which
	// is the right state for WALMode=off.
	GitHooksPath string
	// GitCacheBytes bounds the materialised-repository cache under GitRoot
	// when the WAL is authoritative (§9). Zero disables eviction.
	GitCacheBytes int64
	// ViewerMetadataCacheBytes bounds the parquet viewer's in-process footer
	// (metadata) cache. The viewer reads parquet objects directly from
	// storage via range requests rather than downloading them, so this is a
	// budget on the server process's *heap*, not on tmpfs/disk space (§12).
	ViewerMetadataCacheBytes int64

	// SignedURLTTL is the floor of a signed transfer URL's lifetime, not a
	// fixed duration: the actual lifetime is derived from the object's byte
	// count (large uploads need longer than a fast connection takes for a
	// small one) and then clamped into [SignedURLTTL, SignedURLMaxTTL]. Raise
	// it if clients on slow links are still failing small transfers before
	// the floor expires.
	SignedURLTTL time.Duration
	// SignedURLMaxTTL is the ceiling that clamps the size-derived lifetime
	// described above. It also bounds how long an LFS upload may sit in the
	// tmp/uploads/ staging area before `thinkingface gc` may treat it as
	// abandoned (see stagingGrace in cmd/thinkingface/gc.go), so raising it
	// pushes that grace period out too.
	SignedURLMaxTTL time.Duration

	// Seed values applied on first boot when the users table is empty.
	AdminUsername string
	AdminPassword string
	AdminEmail    string

	// OrgCreation is who may create an organisation: "anyone" (the default)
	// or "admin" for site admins only (docs/dev/organization-design.md §4.1).
	OrgCreation string

	SessionSecret string
	// ViewerCacheDir is a scratch directory on the memory-backed filesystem
	// (tmpfs on Cloud Run). The parquet viewer no longer uses it — it now
	// reads objects via range requests instead of caching them to disk — but
	// WAL compaction (backend/cmd/thinkingface/walops.go) still uses it as
	// its working directory for materialising repositories during compaction.
	ViewerCacheDir string
	SyncWorkers    int
	AllowSignup    bool

	// SessionTTL bounds how long an issued tf_session cookie stays valid.
	// It is also the blast radius of a stolen cookie that is never noticed,
	// so it is deliberately much shorter than a personal access token's life.
	SessionTTL time.Duration
	// CookieSecure forces the Secure attribute on the session cookie. Nil
	// means "infer from PublicURL", which is only right when TLS is not
	// terminated somewhere else; a deployment behind an HTTPS load balancer
	// that speaks plain HTTP internally must set TF_COOKIE_SECURE=true.
	CookieSecure *bool
	// AllowedOrigins lists the browser origins allowed to make credentialed
	// cross-origin calls. Anything not in here gets no CORS headers at all,
	// and a cookie-authenticated state change from it is refused.
	// huggingface_hub, git and curl send no Origin and are unaffected.
	AllowedOrigins []string
	// AuthRateLimitPerMinute caps failed password attempts per client IP per
	// minute (per process -- see docs/dev/thinkingface-design.md §14). Per-username
	// attempts are capped at half this. Zero disables the limiter entirely,
	// which is only sensible in tests.
	AuthRateLimitPerMinute int
	// TrustProxyIPs makes the rate limiter read the client address from the
	// leftmost X-Forwarded-For entry. Only enable it when a proxy you control
	// rewrites that header, otherwise a client picks its own bucket key.
	TrustProxyIPs bool

	// ExpFlushInterval is how long the native ingest API's points may stay
	// database-only before the sync worker writes them into the dataset
	// repository's parquet (docs/dev/thinkingface-design.md §8, route B). Shorter
	// means fresher git history at the cost of more machine-generated
	// commits; a run that reaches finished/failed is always flushed at once,
	// regardless of this value. Zero or negative disables the flush entirely,
	// which leaves route B's data in the database only.
	ExpFlushInterval time.Duration

	// SSHEnabled turns on the git-over-SSH listener (internal/sshserver).
	// Off by default: it opens a second port, and it needs a host key that
	// survives restarts, so enabling it is an explicit deployment decision.
	SSHEnabled bool
	// SSHAddr is the listen address for that listener.
	SSHAddr string
	// SSHHostKeyPath is where the server's SSH host key lives. It is
	// generated on first start if absent, and MUST point at persistent
	// storage: on an ephemeral filesystem every cold start mints a new
	// identity and every client sees a host key mismatch.
	SSHHostKeyPath string
	// SSHIdleTimeout closes an SSH connection that goes quiet. Clones stream
	// continuously, so this only reaps abandoned connections. Zero disables.
	SSHIdleTimeout time.Duration

	WebhookWorkers int
	// AllowPrivateWebhookTargets opts out of the webhook SSRF guard so a
	// webhook may point at localhost/a private network, which is only ever
	// appropriate for local development. Defaults to false.
	AllowPrivateWebhookTargets bool
}

func Load() (*Config, error) {
	e := &envReader{}
	c := &Config{
		Addr:          env("TF_ADDR", ":8080"),
		PublicURL:     strings.TrimSuffix(env("TF_PUBLIC_URL", "http://localhost:8080"), "/"),
		DatabaseURL:   env("DATABASE_URL", ""),
		GitRoot:       env("GIT_ROOT", "/data/git"),
		StorageDriver: env("STORAGE_DRIVER", "gcs-emulator"),
		GCSBucket:     env("GCS_BUCKET", "thinkingface"),
		GCSPrefix:     strings.Trim(env("GCS_PREFIX", ""), "/"),
		EmulatorHost:  env("STORAGE_EMULATOR_HOST", ""),
		WALMode:       env("TF_WAL_MODE", "off"),
		GitHooksPath:  env("TF_GIT_HOOKS_PATH", ""),
		GitCacheBytes: e.int64("TF_GIT_CACHE_BYTES", 2<<30),

		ViewerMetadataCacheBytes: e.int64("TF_VIEWER_METADATA_CACHE_BYTES", 256<<20),
		SignedURLTTL:             e.duration("TF_SIGNED_URL_TTL", time.Hour),
		SignedURLMaxTTL:          e.duration("TF_SIGNED_URL_MAX_TTL", 12*time.Hour),
		AdminUsername:            env("TF_ADMIN_USERNAME", "admin"),
		AdminPassword:            env("TF_ADMIN_PASSWORD", DefaultAdminPassword),
		AdminEmail:               env("TF_ADMIN_EMAIL", "admin@example.com"),
		OrgCreation:              env("TF_ORG_CREATION", "anyone"),
		SessionSecret:            env("TF_SESSION_SECRET", DefaultSessionSecret),
		SessionTTL:               e.duration("TF_SESSION_TTL", 7*24*time.Hour),
		ViewerCacheDir:           env("TF_VIEWER_CACHE_DIR", "/data/cache"),
		SyncWorkers:              e.int("TF_SYNC_WORKERS", 2),
		AllowSignup:              e.bool("TF_ALLOW_SIGNUP", true),
		ExpFlushInterval:         e.duration("TF_EXP_FLUSH_INTERVAL", time.Minute),

		SSHEnabled:     e.bool("TF_SSH_ENABLED", false),
		SSHAddr:        env("TF_SSH_ADDR", ":2222"),
		SSHHostKeyPath: env("TF_SSH_HOST_KEY_PATH", "/data/ssh/host_ed25519"),
		SSHIdleTimeout: e.duration("TF_SSH_IDLE_TIMEOUT", 10*time.Minute),

		WebhookWorkers:             e.int("TF_WEBHOOK_WORKERS", 1),
		AllowPrivateWebhookTargets: e.bool("TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS", false),

		CookieSecure:           e.boolPtr("TF_COOKIE_SECURE"),
		AuthRateLimitPerMinute: e.int("TF_AUTH_RATE_LIMIT_PER_MIN", 10),
		TrustProxyIPs:          e.bool("TF_TRUST_PROXY_IPS", false),
	}
	c.AllowedOrigins = parseOrigins(env("TF_ALLOWED_ORIGINS", ""), c.PublicURL)

	// A malformed number/boolean/duration is a configuration error, not a
	// reason to fall back: an operator who typed TF_SYNC_WORKERS=two must not
	// be left believing the value took effect. Checked before the rest so the
	// first thing reported is the value that could not even be read.
	if e.err != nil {
		return nil, e.err
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if !strings.HasPrefix(c.DatabaseURL, "postgres://") &&
		!strings.HasPrefix(c.DatabaseURL, "postgresql://") &&
		!strings.HasPrefix(c.DatabaseURL, "sqlite://") {
		return nil, fmt.Errorf("DATABASE_URL must start with postgres://, postgresql:// or sqlite://")
	}
	switch c.StorageDriver {
	case "gcs", "gcs-emulator":
	default:
		return nil, fmt.Errorf("STORAGE_DRIVER must be gcs or gcs-emulator, got %q", c.StorageDriver)
	}
	if c.StorageDriver == "gcs-emulator" && c.EmulatorHost == "" {
		return nil, fmt.Errorf("STORAGE_EMULATOR_HOST is required when STORAGE_DRIVER=gcs-emulator")
	}
	switch c.WALMode {
	case "off", "shadow", "authoritative":
	default:
		return nil, fmt.Errorf("TF_WAL_MODE must be off, shadow or authoritative, got %q", c.WALMode)
	}
	switch c.OrgCreation {
	case "anyone", "admin":
	default:
		return nil, fmt.Errorf("TF_ORG_CREATION must be anyone or admin, got %q", c.OrgCreation)
	}
	if c.WALMode != "off" && c.GitHooksPath == "" {
		// Without the hook, pushes over git smart HTTP would bypass the WAL
		// entirely — silently in shadow mode, catastrophically once
		// authoritative. Refuse to start rather than run half-wired.
		return nil, fmt.Errorf("TF_GIT_HOOKS_PATH is required when TF_WAL_MODE=%s", c.WALMode)
	}
	// Production is anything served over https. The development defaults are
	// public knowledge -- the seeded admin password is in .env.example and the
	// default session secret lets anyone forge a tf_session for any user id --
	// so a deployment reachable over TLS must not boot on them. http keeps
	// working unchanged, which is what `docker compose up` and the e2e suite use.
	if strings.HasPrefix(c.PublicURL, "https://") {
		if c.AdminPassword == DefaultAdminPassword {
			return nil, fmt.Errorf("TF_ADMIN_PASSWORD must be set to something other than the default %q when TF_PUBLIC_URL is https", DefaultAdminPassword)
		}
		if c.SessionSecret == DefaultSessionSecret {
			return nil, fmt.Errorf("TF_SESSION_SECRET must be set to a private value when TF_PUBLIC_URL is https")
		}
		if len(c.SessionSecret) < MinSessionSecretLen {
			return nil, fmt.Errorf("TF_SESSION_SECRET must be at least %d bytes, got %d", MinSessionSecretLen, len(c.SessionSecret))
		}
	}
	if c.SessionTTL <= 0 {
		return nil, fmt.Errorf("TF_SESSION_TTL must be positive")
	}
	if c.AuthRateLimitPerMinute < 0 {
		return nil, fmt.Errorf("TF_AUTH_RATE_LIMIT_PER_MIN must not be negative")
	}
	if c.SSHEnabled {
		if c.SSHAddr == "" {
			return nil, fmt.Errorf("TF_SSH_ADDR is required when TF_SSH_ENABLED=true")
		}
		if c.SSHHostKeyPath == "" {
			return nil, fmt.Errorf("TF_SSH_HOST_KEY_PATH is required when TF_SSH_ENABLED=true")
		}
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envReader reads the typed settings, remembering the first variable whose
// value did not parse. Accumulating instead of returning per call keeps
// Load()'s struct literal readable, and Load() reports the failure before it
// validates anything else. An unset (empty) variable is not a failure: it
// keeps the documented default.
type envReader struct{ err error }

func (r *envReader) fail(key string, err error) {
	if r.err == nil {
		r.err = fmt.Errorf("%s: %w", key, err)
	}
}

func (r *envReader) int(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.fail(key, err)
		return def
	}
	return n
}

func (r *envReader) int64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		r.fail(key, err)
		return def
	}
	return n
}

func (r *envReader) bool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(key, err)
		return def
	}
	return b
}

func (r *envReader) duration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		r.fail(key, err)
		return def
	}
	return d
}

// boolPtr distinguishes "unset" from "explicitly false", which matters for
// settings whose unset behaviour is an inference rather than a fixed default.
// An unparseable value is an error rather than another spelling of unset.
func (r *envReader) boolPtr(key string) *bool {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(key, err)
		return nil
	}
	return &b
}

// parseOrigins reads TF_ALLOWED_ORIGINS. Unset falls back to the public URL's
// own origin, plus the local Next.js dev server when the instance is plainly a
// development one (http). A production deployment whose web UI lives on a
// different host has to name it explicitly: reflecting whatever Origin arrives
// is what made the credentialed CORS policy meaningless in the first place.
func parseOrigins(raw, publicURL string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(o string) {
		o = strings.TrimSuffix(strings.TrimSpace(o), "/")
		if o == "" || seen[o] {
			return
		}
		seen[o] = true
		out = append(out, o)
	}
	if raw != "" {
		for _, o := range strings.Split(raw, ",") {
			add(o)
		}
		return out
	}
	add(originOf(publicURL))
	if !strings.HasPrefix(publicURL, "https://") {
		add("http://localhost:3000")
		add("http://127.0.0.1:3000")
	}
	return out
}

// originOf reduces a URL to scheme://host[:port]; anything unparseable yields
// the empty string, which parseOrigins drops.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
