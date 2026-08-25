// Package config stores the tf CLI's credentials (one token per endpoint) and
// resolves which endpoint/token a command should use from flags, environment
// and the config file.
//
// File location: $TF_CONFIG if set, else $XDG_CONFIG_HOME/thinkingface/config.json,
// else ~/.config/thinkingface/config.json. Written 0600 inside a 0700 directory,
// atomically (write temp + rename). The 0700 is re-applied on every Save, but
// only to a directory this package invented -- see ensurePrivateDir.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Credential is one saved login.
type Credential struct {
	Endpoint  string    `json:"endpoint"`
	Token     string    `json:"token"`
	TokenID   int64     `json:"token_id,omitempty"` // 0 when the token was pasted rather than minted by `tf login`
	Username  string    `json:"username,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// File is the on-disk document.
type File struct {
	// DefaultEndpoint is used when neither a flag nor an env var names one.
	DefaultEndpoint string `json:"default_endpoint,omitempty"`
	// Credentials keyed by normalised endpoint.
	Credentials map[string]Credential `json:"credentials"`
}

// Path returns the config file location (see package doc). It does not create anything.
func Path() (string, error) {
	if v := os.Getenv("TF_CONFIG"); v != "" {
		return v, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "thinkingface", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "thinkingface", "config.json"), nil
}

// Load reads the file; a missing file yields an empty *File, not an error.
func Load() (*File, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &File{Credentials: map[string]Credential{}}, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", p, err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", p, err)
	}
	if f.Credentials == nil {
		f.Credentials = map[string]Credential{}
	}
	return &f, nil
}

// Save writes f to Path() (0600 / dir 0700, atomic).
func (f *File) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr != nil {
		return fmt.Errorf("config: write %s: %w", tmpPath, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("config: close %s: %w", tmpPath, closeErr)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("config: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, p); err != nil {
		return fmt.Errorf("config: rename %s to %s: %w", tmpPath, p, err)
	}
	return nil
}

// ensurePrivateDir drops the group and other bits from the directory holding
// the config file.
//
// os.MkdirAll only applies its mode to the directories it actually creates, so
// a thinkingface/ directory left over from an older version -- or from
// anything else that happened to create it first, under whatever umask -- keeps
// the permissions it was born with, and Save alone would never correct them.
// The token itself stays unreadable either way (the file is 0600), but a
// group- or world-searchable directory still tells every other local account
// that this machine has tf credentials, and re-tightening costs one stat.
//
// It deliberately does nothing when TF_CONFIG names the file: that path is the
// user's own choice, its parent is a directory we did not invent and may well
// be shared on purpose (in the extreme, TF_CONFIG=$HOME/tf.json makes it the
// home directory itself), and silently narrowing somebody else's directory is
// a surprise far bigger than the exposure it removes. Only the
// $XDG_CONFIG_HOME/thinkingface and ~/.config/thinkingface directories, which
// exist solely because Path() made them up, are ours to tighten.
//
// A failure to stat or chmod is returned rather than ignored: at that point we
// are about to write a token into a directory whose permissions we could not
// establish.
func ensurePrivateDir(dir string) error {
	if os.Getenv("TF_CONFIG") != "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("config: stat %s: %w", dir, err)
	}
	perm := info.Mode().Perm()
	if perm&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, perm&^0o077); err != nil {
		return fmt.Errorf("config: chmod %s: %w", dir, err)
	}
	return nil
}

// Get returns the credential for a normalised endpoint.
func (f *File) Get(endpoint string) (Credential, bool) {
	c, ok := f.Credentials[endpoint]
	return c, ok
}

// Set stores c under c.Endpoint and makes it the default endpoint.
func (f *File) Set(c Credential) {
	if f.Credentials == nil {
		f.Credentials = map[string]Credential{}
	}
	f.Credentials[c.Endpoint] = c
	f.DefaultEndpoint = c.Endpoint
}

// Remove forgets an endpoint; if it was the default, the default is cleared
// (or moved to the only remaining endpoint when exactly one is left).
func (f *File) Remove(endpoint string) {
	delete(f.Credentials, endpoint)
	if f.DefaultEndpoint == endpoint {
		f.DefaultEndpoint = ""
		if len(f.Credentials) == 1 {
			for k := range f.Credentials {
				f.DefaultEndpoint = k
			}
		}
	}
}

// NormalizeEndpoint turns user input into the canonical key: trims
// whitespace and trailing slashes, lower-cases the scheme and host, and
// prepends "https://" when no scheme is given ("http://" for localhost /
// 127.0.0.1 / [::1]). A value that is not an absolute http(s) URL, or that
// carries a path, query or fragment, is an error.
func NormalizeEndpoint(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("config: empty endpoint")
	}
	if !strings.Contains(s, "://") {
		if isLocalHostport(s) {
			s = "http://" + s
		} else {
			s = "https://" + s
		}
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("config: invalid endpoint %q: %w", s, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("config: endpoint %q must be http or https", s)
	}
	if u.Host == "" {
		return "", fmt.Errorf("config: endpoint %q is missing a host", s)
	}
	if strings.Trim(u.Path, "/") != "" {
		return "", fmt.Errorf("config: endpoint %q must not have a path", s)
	}
	if u.RawQuery != "" {
		return "", fmt.Errorf("config: endpoint %q must not have a query", s)
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("config: endpoint %q must not have a fragment", s)
	}
	if u.User != nil {
		return "", fmt.Errorf("config: endpoint %q must not have userinfo", s)
	}

	return scheme + "://" + strings.ToLower(u.Host), nil
}

// isLocalHostport reports whether hostport (a bare host, or "host:port")
// names localhost, 127.0.0.1 or ::1.
func isLocalHostport(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// Resolved is the outcome of Resolve, with provenance for --verbose output.
type Resolved struct {
	Endpoint       string
	Token          string // "" = anonymous
	EndpointSource string // "flag" | "env TF_ENDPOINT" | "env THINKINGFACE_ENDPOINT" | "env HF_ENDPOINT" | "config"
	TokenSource    string // "flag" | "env THINKINGFACE_API_KEY" | "env TF_TOKEN" | "env THINKINGFACE_TOKEN" | "config" | "env HF_TOKEN" | "" (none)
	Username       string // from the config credential, when that is the token source
}

// ErrNoEndpoint is returned by Resolve when nothing names a server.
var ErrNoEndpoint = errors.New("no endpoint: pass --endpoint, set THINKINGFACE_ENDPOINT, or run `tf login <url>`")

// Resolve picks the endpoint and token for a command.
//
// Endpoint precedence: flagEndpoint > TF_ENDPOINT > THINKINGFACE_ENDPOINT >
// HF_ENDPOINT > file.DefaultEndpoint > ErrNoEndpoint. The winner is passed
// through NormalizeEndpoint.
//
// Token precedence: flagToken > THINKINGFACE_API_KEY > TF_TOKEN >
// THINKINGFACE_TOKEN > file.Credentials[endpoint] > HF_TOKEN (only when
// HF_ENDPOINT, normalised, equals the resolved endpoint -- a real
// huggingface.co token must never be sent to a thinkingface server by
// accident) > "" (anonymous; the command decides whether that is acceptable).
// An API key from the environment therefore makes every command behave as if
// `tf login` had been run, without touching the config file.
//
// env is the environment lookup (os.Getenv in production, a map in tests);
// file may be nil.
func Resolve(flagEndpoint, flagToken string, env func(string) string, file *File) (Resolved, error) {
	var res Resolved

	rawEndpoint, endpointSource := "", ""
	switch {
	case flagEndpoint != "":
		rawEndpoint, endpointSource = flagEndpoint, "flag"
	case strings.TrimSpace(env("TF_ENDPOINT")) != "":
		rawEndpoint, endpointSource = strings.TrimSpace(env("TF_ENDPOINT")), "env TF_ENDPOINT"
	case strings.TrimSpace(env("THINKINGFACE_ENDPOINT")) != "":
		rawEndpoint, endpointSource = strings.TrimSpace(env("THINKINGFACE_ENDPOINT")), "env THINKINGFACE_ENDPOINT"
	case strings.TrimSpace(env("HF_ENDPOINT")) != "":
		rawEndpoint, endpointSource = strings.TrimSpace(env("HF_ENDPOINT")), "env HF_ENDPOINT"
	case file != nil && file.DefaultEndpoint != "":
		rawEndpoint, endpointSource = file.DefaultEndpoint, "config"
	default:
		return Resolved{}, ErrNoEndpoint
	}

	endpoint, err := NormalizeEndpoint(rawEndpoint)
	if err != nil {
		return Resolved{}, fmt.Errorf("config: resolve endpoint: %w", err)
	}
	res.Endpoint = endpoint
	res.EndpointSource = endpointSource

	switch {
	case flagToken != "":
		res.Token, res.TokenSource = flagToken, "flag"
	case strings.TrimSpace(env("THINKINGFACE_API_KEY")) != "":
		res.Token, res.TokenSource = strings.TrimSpace(env("THINKINGFACE_API_KEY")), "env THINKINGFACE_API_KEY"
	case strings.TrimSpace(env("TF_TOKEN")) != "":
		res.Token, res.TokenSource = strings.TrimSpace(env("TF_TOKEN")), "env TF_TOKEN"
	case strings.TrimSpace(env("THINKINGFACE_TOKEN")) != "":
		res.Token, res.TokenSource = strings.TrimSpace(env("THINKINGFACE_TOKEN")), "env THINKINGFACE_TOKEN"
	default:
		if file != nil {
			if cred, ok := file.Get(endpoint); ok && cred.Token != "" {
				res.Token, res.TokenSource, res.Username = cred.Token, "config", cred.Username
				return res, nil
			}
		}
		if hfEndpointRaw := strings.TrimSpace(env("HF_ENDPOINT")); hfEndpointRaw != "" {
			if hfEndpoint, err := NormalizeEndpoint(hfEndpointRaw); err == nil && hfEndpoint == endpoint {
				if hfToken := strings.TrimSpace(env("HF_TOKEN")); hfToken != "" {
					res.Token, res.TokenSource = hfToken, "env HF_TOKEN"
				}
			}
		}
	}

	return res, nil
}
