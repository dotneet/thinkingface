package config

import "strings"

// LogAttrs returns the effective configuration as slog attribute pairs
// (key, value, key, value, ...), suitable for a single startup
// `slog.Info("effective configuration", cfg.LogAttrs()...)` call
// (docs/dev/agent-features.md §1.3).
//
// It is deliberately an allowlist rather than a reflection over Config: a
// field added to Config in the future does not appear here until someone
// decides it belongs in the log, which is the right default for a struct
// that holds DatabaseURL (carries a password in its DSN) and SessionSecret
// (signs every session cookie and LFS transfer URL). Neither of those, nor
// AdminPassword, is ever included -- only a boolean saying whether each
// secret-shaped setting was left at its well-known development default,
// which is itself information an operator needs at startup but never the
// value that would make the log line a secret.
func (c *Config) LogAttrs() []any {
	return []any{
		"public_url", c.PublicURL,
		"listen_addr", c.Addr,
		"ssh_enabled", c.SSHEnabled,
		"ssh_addr", c.SSHAddr,
		"ssh_public_port", c.SSHPublicPort,
		"allowed_origins", c.AllowedOrigins,
		"database_driver", DatabaseDriver(c.DatabaseURL),
		"storage_driver", c.StorageDriver,
		"storage_bucket", c.GCSBucket,
		"storage_prefix", c.GCSPrefix,
		"storage_emulator_host", c.EmulatorHost,
		"wal_mode", c.WALMode,
		"allow_signup", c.AllowSignup,
		"signup_require_approval", c.SignupRequireApproval,
		"signup_email_domains_count", len(c.SignupEmailDomains),
		"org_creation", c.OrgCreation,
		"require_auth_for_read", c.RequireAuthForRead,
		"cookie_secure", cookieSecureAttr(c.CookieSecure),
		"trust_proxy_ips", c.TrustProxyIPs,
		"trusted_proxy_hops", c.TrustedProxyHops,
		"trusted_web_proxies", c.TrustedWebProxies,
		"web_proxy_secret_set", c.WebProxySecret != "",
		"session_secret_default", c.SessionSecret == DefaultSessionSecret,
		"admin_password_default", c.AdminPassword == DefaultAdminPassword,
	}
}

// cookieSecureAttr renders CookieSecure for the log: the pointer's nil state
// (meaning "infer from PublicURL", see the field's doc comment) is not the
// same as an explicit false, and the log line should not collapse the two.
func cookieSecureAttr(v *bool) string {
	if v == nil {
		return "inferred"
	}
	if *v {
		return "true"
	}
	return "false"
}

// DatabaseDriver reports the storage backend selected by a DATABASE_URL,
// deriving it from the scheme alone. It never returns anything else
// contained in the URL: a Postgres DSN carries the connection password in
// the userinfo component, and that is precisely why callers that only need
// to log "which database" must go through this instead of the raw
// DatabaseURL field.
func DatabaseDriver(databaseURL string) string {
	switch {
	case strings.HasPrefix(databaseURL, "postgres://"), strings.HasPrefix(databaseURL, "postgresql://"):
		return "postgres"
	case strings.HasPrefix(databaseURL, "sqlite://"):
		return "sqlite"
	default:
		return "unknown"
	}
}
