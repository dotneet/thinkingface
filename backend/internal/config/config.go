// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

// The signed-URL lifetimes a deployment gets when it sets neither variable.
// They are named because the validation below has to be able to say "the
// ceiling you never set is 12h" -- an upgrade that had only ever set
// TF_SIGNED_URL_TTL is exactly the case that trips it.
const (
	DefaultSignedURLTTL    = time.Hour
	DefaultSignedURLMaxTTL = 12 * time.Hour
)

// MinSessionSecretLen is the shortest session secret accepted once
// TF_PUBLIC_URL is anything but loopback. The secret keys an HMAC-SHA256 that
// both session cookies and LFS transfer URLs rely on, so anything shorter
// than the digest's block-relevant size is not worth signing with.
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

	// DefaultStorageQuotaBytes is the storage ceiling applied to every
	// namespace that carries no override of its own
	// (namespaces.storage_quota_bytes). Zero -- the default -- means
	// unlimited, which is what an instance that never configures this gets,
	// so enabling quotas is opt-in and no existing deployment starts
	// refusing uploads on an upgrade. It is enforced on the LFS upload path,
	// where the bytes actually reach the bucket.
	DefaultStorageQuotaBytes int64

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
	// SignupEmailDomains restricts self-service sign-up to these email
	// domains (TF_SIGNUP_EMAIL_DOMAINS, comma separated). Empty means no
	// restriction, which is the default.
	//
	// Entries are stored lower-cased and matched **exactly**: "example.com"
	// admits alice@example.com and refuses alice@sub.example.com. Letting a
	// domain imply its subdomains is the sort of surprise that only shows up
	// as an unwanted account -- anybody who controls a subdomain of yours
	// would be admitted -- so a subdomain that should be allowed is listed
	// on its own.
	//
	// It applies to POST /api/v1/auth/signup only. An administrator adding
	// an account at POST /api/v1/admin/users is a deliberate act by somebody
	// already trusted, and gating that on the list would make it another
	// one-way door -- the same reason TF_ALLOW_SIGNUP is not consulted there.
	SignupEmailDomains []string
	// SignupRequireApproval puts every self-registration in a waiting room:
	// the account is created with users.approval_pending_at set, no session
	// is issued, and it authenticates on no path at all until a site
	// administrator approves it at PATCH /api/v1/admin/users/{username}.
	//
	// It is orthogonal to SignupEmailDomains: the domain list decides who
	// may register, this decides whether registering is enough.
	SignupRequireApproval bool

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
	// SSHPublicPort is the port clients should dial, when it differs from the
	// one SSHAddr listens on. Empty means "the same".
	//
	// The two are routinely different and only the deployment knows: compose
	// and Kubernetes remap ports, and a load balancer in front terminates on
	// its own. Publishing the listen port in ssh_clone_url would then hand
	// every user a URL that does not connect, which is worse than showing
	// none -- so this exists to be set wherever the mapping happens.
	SSHPublicPort string
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
		SignedURLTTL:             e.duration("TF_SIGNED_URL_TTL", DefaultSignedURLTTL),
		SignedURLMaxTTL:          e.duration("TF_SIGNED_URL_MAX_TTL", DefaultSignedURLMaxTTL),
		DefaultStorageQuotaBytes: e.int64("TF_DEFAULT_STORAGE_QUOTA_BYTES", 0),
		AdminUsername:            env("TF_ADMIN_USERNAME", "admin"),
		AdminPassword:            env("TF_ADMIN_PASSWORD", DefaultAdminPassword),
		AdminEmail:               env("TF_ADMIN_EMAIL", "admin@example.com"),
		OrgCreation:              env("TF_ORG_CREATION", "anyone"),
		SessionSecret:            env("TF_SESSION_SECRET", DefaultSessionSecret),
		SessionTTL:               e.duration("TF_SESSION_TTL", 7*24*time.Hour),
		ViewerCacheDir:           env("TF_VIEWER_CACHE_DIR", "/data/cache"),
		SyncWorkers:              e.int("TF_SYNC_WORKERS", 2),
		AllowSignup:              e.bool("TF_ALLOW_SIGNUP", true),
		SignupEmailDomains:       parseSignupEmailDomains(env("TF_SIGNUP_EMAIL_DOMAINS", "")),
		SignupRequireApproval:    e.bool("TF_SIGNUP_REQUIRE_APPROVAL", false),
		ExpFlushInterval:         e.duration("TF_EXP_FLUSH_INTERVAL", time.Minute),

		SSHEnabled:     e.bool("TF_SSH_ENABLED", false),
		SSHAddr:        env("TF_SSH_ADDR", ":2222"),
		SSHPublicPort:  env("TF_SSH_PUBLIC_PORT", ""),
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
	// A negative default would be a quota nothing could satisfy, which is not
	// what anybody means by it -- "no limit" is spelled 0, and zero bytes is
	// spelled 0 on a namespace's own override, never here.
	if c.DefaultStorageQuotaBytes < 0 {
		return nil, fmt.Errorf("TF_DEFAULT_STORAGE_QUOTA_BYTES must not be negative, got %d", c.DefaultStorageQuotaBytes)
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
	if c.WALMode != "off" {
		if c.GitHooksPath == "" {
			// Without the hook, pushes over git smart HTTP would bypass the
			// WAL entirely — silently in shadow mode, catastrophically once
			// authoritative. Refuse to start rather than run half-wired.
			return nil, fmt.Errorf("TF_GIT_HOOKS_PATH is required when TF_WAL_MODE=%s", c.WALMode)
		}
		if err := checkPreReceiveHook(c.GitHooksPath); err != nil {
			return nil, fmt.Errorf("TF_WAL_MODE=%s: %w", c.WALMode, err)
		}
	}
	// The development defaults are public knowledge -- the seeded admin
	// password is in .env.example, and the default session secret lets anyone
	// forge a `tf_session` for any user id, which on a fresh instance means
	// `1.0.<future>.<hmac>` and the initial administrator's whole authority.
	//
	// The line used to be drawn at https, and that was the wrong line. This
	// server is built for internal and team use (design §1), and the most
	// common way such an instance is actually run is plain http on a VPN or
	// an office LAN -- precisely the case the https test let through. What
	// separates a laptop from a deployment is not TLS, it is whether anyone
	// else can reach it, so the test is the *host*: loopback boots on the
	// defaults, anything else must be configured.
	//
	// Generating a secret at startup instead was considered and rejected. The
	// secret keys the session cookie *and* the LFS transfer URLs, and Cloud
	// Run runs several instances (infra/ defaults api_max_instances to 4), so
	// a per-process value would sign cookies and upload URLs each replica
	// rejects from the others. Persisting one would need somewhere to put it
	// -- a schema change, or a file on storage that is not guaranteed to be
	// shared -- which is a larger decision than this guard; a deployment that
	// wants one runs `openssl rand -hex 32` once and sets it.
	//
	// `docker compose up`, `make dev-api` and the e2e suite all use
	// http://localhost, so they keep booting on the defaults unchanged.
	if !isLoopbackURL(c.PublicURL) {
		if c.AdminPassword == DefaultAdminPassword {
			return nil, fmt.Errorf("TF_ADMIN_PASSWORD must be set to something other than the default %q unless TF_PUBLIC_URL points at localhost (got %q)", DefaultAdminPassword, c.PublicURL)
		}
		if c.SessionSecret == DefaultSessionSecret {
			return nil, fmt.Errorf("TF_SESSION_SECRET must be set to a private value unless TF_PUBLIC_URL points at localhost (got %q); generate one with `openssl rand -hex 32`", c.PublicURL)
		}
		if len(c.SessionSecret) < MinSessionSecretLen {
			return nil, fmt.Errorf("TF_SESSION_SECRET must be at least %d bytes, got %d", MinSessionSecretLen, len(c.SessionSecret))
		}
	}
	if c.SessionTTL <= 0 {
		return nil, fmt.Errorf("TF_SESSION_TTL must be positive")
	}
	// A signed URL is minted with `now + TTL` as its expiry, and lfs.TTLFor
	// only ever clamps the value *down* (to SignedURLMaxTTL, then to the
	// signing limit) -- so a zero or negative base makes every URL this server
	// issues expire at or before the moment it is handed out. Nothing would
	// say so: the server starts, the LFS batch response looks entirely normal,
	// and every upload and download fails against GCS with a 403 that names no
	// cause. The proxy path (`exp` in the query) fails the same way.
	if c.SignedURLTTL <= 0 {
		return nil, fmt.Errorf("TF_SIGNED_URL_TTL must be positive, got %s", c.SignedURLTTL)
	}
	// The ceiling, on the other hand, is legitimately unset: lfs.TTLFor reads
	// "<= 0" as "no ceiling of my own", leaving the signing limit as the only
	// bound. Only a *negative* value is refused, since it can only be a typo
	// -- as a ceiling it would mean "already expired", which is the same
	// silent, unexplained 403.
	if c.SignedURLMaxTTL < 0 {
		return nil, fmt.Errorf("TF_SIGNED_URL_MAX_TTL must not be negative, got %s", c.SignedURLMaxTTL)
	}
	// A ceiling below the base is not a contradiction the server can resolve
	// on its own -- TTLFor honours the ceiling, so the base is simply never
	// reached -- but it does mean one of the two values is not doing what the
	// operator set it for, and saying so at startup costs nothing.
	//
	// The message spells out both ways out, because the deployment most
	// likely to hit this never wrote a contradictory pair: it set
	// TF_SIGNED_URL_TTL alone, above the 12h ceiling it did not know it had,
	// and the server it upgrades from was silently clamping to that ceiling
	// all along. Refusing to start is still the right answer -- resolving it
	// by raising the ceiling would quietly lengthen the life of every signed
	// URL the instance hands out, which is not a decision to make on an
	// operator's behalf during an upgrade -- but only if the error says
	// exactly which variable to change and to what.
	if c.SignedURLMaxTTL > 0 && c.SignedURLMaxTTL < c.SignedURLTTL {
		hint := ""
		if _, set := os.LookupEnv("TF_SIGNED_URL_MAX_TTL"); !set {
			hint = fmt.Sprintf(" (TF_SIGNED_URL_MAX_TTL is not set, so it is its default %s)", DefaultSignedURLMaxTTL)
		}
		return nil, fmt.Errorf(
			"TF_SIGNED_URL_MAX_TTL (%s) must not be shorter than TF_SIGNED_URL_TTL (%s)%s: "+
				"the ceiling clamps every signed URL, so the base lifetime is never reached. "+
				"Either raise TF_SIGNED_URL_MAX_TTL to %s or more (0 disables the ceiling, leaving GCS's 7-day signing limit), "+
				"or lower TF_SIGNED_URL_TTL to %s or less",
			c.SignedURLMaxTTL, c.SignedURLTTL, hint, c.SignedURLTTL, c.SignedURLMaxTTL)
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

// PreReceiveHookName is the file git looks for inside core.hooksPath when a
// push arrives. Named because both the check below and the deployment that
// bakes it into the image have to agree on it
// (docs/dev/continuity-design.md §6.2).
const PreReceiveHookName = "pre-receive"

// checkPreReceiveHook is the fail-closed half of the TF_GIT_HOOKS_PATH
// requirement, and it is a filesystem check inside Load for a reason.
//
// A non-empty path only proves an operator typed something. git treats a hook
// that is missing, is a directory, or is not executable as *no hook at all*:
// it runs no hook, reports nothing, and lets the push proceed. So an image
// that lost hooks/pre-receive, a path with a typo in it, or a bind mount that
// shadowed the directory produces a server that applies every push over HTTP
// and SSH to disk and acks it to the client with no WAL entry and no CAS —
// invariant 4 of §5 broken silently. Nothing surfaces until the next
// Materialize, where writeRefs projects the index over the copy and deletes
// the refs those pushes created.
//
// Startup is the only honest place to catch that: by the time a push arrives
// the damage is already acknowledged.
func checkPreReceiveHook(hooksPath string) error {
	hook := filepath.Join(hooksPath, PreReceiveHookName)
	info, err := os.Stat(hook)
	if err != nil {
		return fmt.Errorf("the pre-receive hook %s is unreadable (%w): "+
			"without it every git push is applied and acknowledged without a WAL entry", hook, err)
	}
	if info.IsDir() {
		return fmt.Errorf("the pre-receive hook %s is a directory, not an executable", hook)
	}
	// Any of the three execute bits: the server may run as owner, as a member
	// of the file's group, or as neither, and which one applies is a property
	// of the deployment rather than of the image. Requiring all three would
	// reject a correctly locked-down 0700 hook owned by the server's user.
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("the pre-receive hook %s is not executable (mode %s): "+
			"git silently skips a non-executable hook, so every git push would be "+
			"applied and acknowledged without a WAL entry", hook, info.Mode().Perm())
	}
	return nil
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

// isLoopbackURL reports whether raw addresses this machine and nothing else.
// It is what decides whether the development defaults may be used (see
// Load), so it fails closed: a URL that will not parse, or has no host, is
// treated as reachable from elsewhere. Better to make an operator spell out a
// secret they did not need than to let a typo re-open the hole.
//
// "localhost" and any name under it (`foo.localhost`, which browsers and
// RFC 6761 resolve to loopback) count, as does any literal loopback address
// -- 127.0.0.0/8 and ::1.
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parseSignupEmailDomains splits TF_SIGNUP_EMAIL_DOMAINS into the allow list
// SignupEmailDomains holds. Blank entries are dropped (so a trailing comma is
// harmless), a leading "@" is tolerated because "@example.com" is what people
// type, and every entry is lower-cased here rather than at every comparison
// -- domains are case-insensitive, and normalising once is what keeps the
// comparison a plain string equality.
//
// Nil for an empty value, which is what "no restriction" is spelled as.
func parseSignupEmailDomains(raw string) []string {
	var out []string
	for _, d := range strings.Split(raw, ",") {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}
