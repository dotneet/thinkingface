// Package hub is the HTTP client the tf CLI uses to talk to a thinkingface
// server. It covers exactly the HuggingFace-compatible subset an upload needs
// (docs/dev/api-contract.md §1, §2, §3, §8) plus the two thinkingface-only calls
// behind `tf login` (password login + token minting).
//
// Design rules:
//   - stdlib only (net/http, encoding/json, crypto/*); no third-party deps.
//   - Every non-2xx answer becomes *Error carrying the server's JSON error
//     body ({"error":{"type","message"}}) so callers can branch on status.
//   - Nothing here touches the terminal or the filesystem: callers hand in
//     io.Readers and get events back (see upload.go).
package hub

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind is a repository type as the HF API spells it in JSON ("dataset" / "model").
type Kind string

const (
	KindDataset Kind = "dataset"
	KindModel   Kind = "model"
)

// ParseKind accepts "dataset"/"datasets"/"model"/"models" (case-insensitive).
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dataset", "datasets":
		return KindDataset, nil
	case "model", "models":
		return KindModel, nil
	}
	return "", fmt.Errorf("kind must be dataset or model, got %q", s)
}

// Plural is the URL segment used by the HF API ("datasets" / "models").
func (k Kind) Plural() string {
	if k == KindModel {
		return "models"
	}
	return "datasets"
}

// Ref names one repository.
type Ref struct {
	Kind      Kind
	Namespace string
	Name      string
}

// ID is the HF repo_id form "ns/name".
func (r Ref) ID() string { return r.Namespace + "/" + r.Name }

// String is the human form "datasets/ns/name".
func (r Ref) String() string { return r.Kind.Plural() + "/" + r.ID() }

// Client talks to one server with one token. The zero value is not usable;
// construct it with New.
type Client struct {
	endpoint  string // scheme://host[:port], no trailing slash
	token     string // "" = anonymous
	http      *http.Client
	userAgent string
}

// Option tweaks a Client at construction time.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client (tests, proxies, timeouts).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithUserAgent overrides the User-Agent header (default "thinkingface-tf/<version>").
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// New builds a client for endpoint (trailing slashes are stripped) that
// authenticates with `Authorization: Bearer <token>` when token != "".
func New(endpoint, token string, opts ...Option) *Client {
	c := &Client{
		endpoint:  strings.TrimRight(endpoint, "/"),
		token:     token,
		http:      newHTTPClient(),
		userAgent: "thinkingface-tf",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// maxRedirects bounds how many hops one request may follow. The server itself
// redirects at most once (a resolve pointing at object storage), so anything
// past a handful is a loop or a misconfigured proxy.
const maxRedirects = 5

// responseHeaderTimeout bounds the wait for response headers *after* the
// request body has been written, so it never cuts a slow upload short -- only
// a server that accepted the bytes and then went silent.
const responseHeaderTimeout = 2 * time.Minute

// sharedTransport is created once and reused by every Client so connections
// (and TLS sessions) are pooled across the calls one `tf up` makes. It is
// http.DefaultTransport's settings -- proxy support, the 30s dial and 10s TLS
// handshake deadlines -- plus a header timeout.
var sharedTransport = sync.OnceValue(func() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = responseHeaderTimeout
	return t
})

// newHTTPClient is the client New uses when the caller supplies none.
//
// Deliberately not http.DefaultClient: that follows up to ten redirects and
// has no timeout whatsoever. Redirects matter because this client carries a
// write-scoped personal access token, and net/http only strips Authorization
// when the *hostname* changes -- a redirect to a different port on the same
// host, or an https -> http downgrade, would hand the token over. checkRedirect
// closes that gap.
//
// There is no Client.Timeout on purpose: a single LFS PUT of a multi-gigabyte
// checkpoint is one request that legitimately runs for a long time, and a
// whole-request deadline would abort exactly the transfers that are hardest to
// restart. The bounds live on the phases that should never be slow (connect,
// TLS handshake, waiting for response headers); cancellation of the whole
// operation is the caller's ctx.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport:     sharedTransport(),
		CheckRedirect: checkRedirect,
	}
}

// checkRedirect bounds the redirect chain and keeps credentials from crossing
// an origin. net/http's own rule compares host names only, so it happily
// forwards Authorization from https://hub:443 to http://hub:9999; this one
// compares scheme, host and port, and drops the Authorization and Cookie
// headers the moment any of them changes. That covers the LFS action's own
// credentials too, not just the client's bearer token: the server only ever
// hands those back under the Authorization key (internal/lfs/lfs.go's
// authHeader), never a differently-named header, so there is nothing else to
// strip today -- but a header this function does not know to name would cross
// origins unstripped, so a server that started returning LFS credentials
// under some other header would need this list extended to match. The
// redirect is still followed -- an LFS batch answer legitimately points at
// object storage on another origin, and a signed URL needs no header of ours.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	prev := via[len(via)-1]
	if !sameOrigin(prev.URL, req.URL) {
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
	}
	return nil
}

// sameOrigin compares scheme, hostname and effective port.
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		portOrDefault(a) == portOrDefault(b)
}

// portOrDefault is u's explicit port, or the scheme's default.
func portOrDefault(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// Endpoint returns the normalised base URL.
func (c *Client) Endpoint() string { return c.endpoint }

// Error is a non-2xx answer from the server.
type Error struct {
	Status  int    // HTTP status
	Type    string // error.type from the JSON body ("not_found", "conflict", ...), "" if none
	Message string // error.message, or the raw body / status text
	Method  string
	URL     string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s %s: %d %s", e.Method, e.URL, e.Status, e.Message)
	}
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.URL, e.Status, http.StatusText(e.Status))
}

// statusIs reports whether err is an *Error with the given status.
func statusIs(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsConflict reports a 409.
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

// IsUnauthorized reports a 401.
func IsUnauthorized(err error) bool { return statusIs(err, http.StatusUnauthorized) }

// IsForbidden reports a 403.
func IsForbidden(err error) bool { return statusIs(err, http.StatusForbidden) }

// ---------------------------------------------------------------- transport

// maxErrorBody bounds how much of a failed response is read before the body is
// discarded. Error bodies are a couple of hundred bytes; anything larger is a
// misbehaving proxy, not something worth buffering.
const maxErrorBody = 64 << 10

// errorMessageLimit is how much of an unparseable body ends up in Error.Message.
const errorMessageLimit = 200

const (
	contentTypeJSON   = "application/json"
	contentTypeNDJSON = "application/x-ndjson"
	contentTypeLFS    = "application/vnd.git-lfs+json"
	contentTypeBinary = "application/octet-stream"
)

// newRequest builds a request carrying the client's identity: User-Agent, the
// bearer token when there is one, and a JSON Accept.
func (c *Client) newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, rawURL, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", contentTypeJSON)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// send performs req and turns every non-2xx answer into *Error. On success the
// caller owns resp.Body and must close it.
func (c *Client) send(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Redacted(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, parseError(resp, req.Method, req.URL.Redacted())
	}
	return resp, nil
}

// doJSON sends in as a JSON body (nil for none) and decodes the answer into out
// (nil to discard it).
func (c *Client) doJSON(ctx context.Context, method, rawURL string, in, out any) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := c.newRequest(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", contentTypeJSON)
	}
	resp, err := c.send(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeBody(resp, out, method, rawURL)
}

// decodeBody reads resp into out, or drains it when out is nil so the
// connection can be reused.
func decodeBody(resp *http.Response, out any, method, rawURL string) error {
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, rawURL, err)
	}
	return nil
}

// parseError builds an *Error from a failed response. The server speaks two
// error shapes: {"error":{"type","message"}} for the JSON API and
// {"message":...} for the LFS endpoints. Older HF-compatible handlers also
// answer with a bare string in "error" (the create-repo 409).
func parseError(resp *http.Response, method, rawURL string) *Error {
	e := &Error{Status: resp.StatusCode, Method: method, URL: rawURL}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

	var probe struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &probe) == nil {
		var obj struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		var text string
		switch {
		case len(probe.Error) > 0 && json.Unmarshal(probe.Error, &obj) == nil && (obj.Type != "" || obj.Message != ""):
			e.Type, e.Message = obj.Type, obj.Message
			return e
		case len(probe.Error) > 0 && json.Unmarshal(probe.Error, &text) == nil && text != "":
			e.Message = text
			return e
		case probe.Message != "":
			e.Message = probe.Message
			return e
		}
	}
	e.Message = truncate(strings.TrimSpace(string(body)), errorMessageLimit)
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// repoAPIBase is {endpoint}/api/{models|datasets}/{ns}/{name}.
func (c *Client) repoAPIBase(ref Ref) string {
	return c.endpoint + "/api/" + ref.Kind.Plural() + "/" +
		url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name)
}

// gitBase is the transport root a git/LFS client would use: models sit at the
// root, datasets behind /datasets (server.go's mountRepoTransport).
func (c *Client) gitBase(ref Ref) string {
	prefix := c.endpoint + "/"
	if ref.Kind == KindDataset {
		prefix = c.endpoint + "/datasets/"
	}
	return prefix + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name) + ".git"
}

// ------------------------------------------------------------------ identity

// User is the caller's identity as GET /api/whoami-v2 reports it.
type User struct {
	Name     string // username = personal namespace
	Fullname string
	Email    string
	Role     string // token scope: "read" | "write" (auth.accessToken.role)
	Orgs     []Org
}

// Org is one organisation the user belongs to.
type Org struct {
	Name      string
	Fullname  string
	RoleInOrg string // "admin" | "write" | "read"
}

// whoamiResponse mirrors the wire shape of GET /api/whoami-v2.
type whoamiResponse struct {
	Name     string `json:"name"`
	Fullname string `json:"fullname"`
	Email    string `json:"email"`
	Orgs     []struct {
		Name      string `json:"name"`
		Fullname  string `json:"fullname"`
		RoleInOrg string `json:"roleInOrg"`
	} `json:"orgs"`
	Auth struct {
		AccessToken struct {
			Role string `json:"role"`
		} `json:"accessToken"`
	} `json:"auth"`
}

// Whoami resolves the token's owner. Anonymous clients get a 401 *Error.
func (c *Client) Whoami(ctx context.Context) (*User, error) {
	var body whoamiResponse
	if err := c.doJSON(ctx, http.MethodGet, c.endpoint+"/api/whoami-v2", nil, &body); err != nil {
		return nil, err
	}
	u := &User{
		Name:     body.Name,
		Fullname: body.Fullname,
		Email:    body.Email,
		Role:     body.Auth.AccessToken.Role,
	}
	for _, o := range body.Orgs {
		u.Orgs = append(u.Orgs, Org{Name: o.Name, Fullname: o.Fullname, RoleInOrg: o.RoleInOrg})
	}
	return u, nil
}

// Token is a freshly minted personal access token (plaintext only here).
type Token struct {
	ID    int64
	Token string
	Name  string
	Scope string
}

// MintToken signs in with a password (POST /api/v1/auth/login, cookie
// session) and creates a personal access token (POST /api/v1/tokens) in the
// same session. The cookie lives only for the duration of this call; the
// receiver's own token (if any) is not used. scope is "read" or "write".
func (c *Client) MintToken(ctx context.Context, username, password, name, scope string) (*Token, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	// A short-lived twin of the receiver that carries the session cookie and
	// no bearer token: the point of this call is to obtain the first one.
	// No Origin header is sent, which is what the server's same-origin check
	// expects from a non-browser caller (server.go's requireSameOrigin).
	session := &Client{
		endpoint:  c.endpoint,
		userAgent: c.userAgent,
		http: &http.Client{
			Transport:     c.http.Transport,
			CheckRedirect: c.http.CheckRedirect,
			Timeout:       c.http.Timeout,
			Jar:           jar,
		},
	}

	login := map[string]string{"username": username, "password": password}
	if err := session.doJSON(ctx, http.MethodPost, c.endpoint+"/api/v1/auth/login", login, nil); err != nil {
		return nil, err
	}

	if name == "" {
		name = "tf-cli"
	}
	if scope == "" {
		scope = "write"
	}
	var created struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Scope string `json:"scope"`
		Token string `json:"token"`
	}
	req := map[string]string{"name": name, "scope": scope}
	if err := session.doJSON(ctx, http.MethodPost, c.endpoint+"/api/v1/tokens", req, &created); err != nil {
		return nil, err
	}
	if created.Token == "" {
		return nil, errors.New("server returned an empty token")
	}
	return &Token{ID: created.ID, Token: created.Token, Name: created.Name, Scope: created.Scope}, nil
}

// RevokeToken deletes a token by id (DELETE /api/v1/tokens/{id}) using the
// receiver's credentials; a token may revoke itself. 404 is returned as-is.
func (c *Client) RevokeToken(ctx context.Context, id int64) error {
	rawURL := c.endpoint + "/api/v1/tokens/" + strconv.FormatInt(id, 10)
	return c.doJSON(ctx, http.MethodDelete, rawURL, nil, nil)
}

// -------------------------------------------------------------- repositories

// RepoExists answers GET /api/{kinds}/{ns}/{name}: true on 200, false on 404,
// error otherwise (a 401/403 is an error, not "false").
func (c *Client) RepoExists(ctx context.Context, ref Ref) (bool, error) {
	err := c.doJSON(ctx, http.MethodGet, c.repoAPIBase(ref), nil, nil)
	switch {
	case err == nil:
		return true, nil
	case IsNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

// CreateRepo is POST /api/repos/create with {"type": kind, "name": "ns/name"}.
// created=false, err=nil when the repository already exists (409).
func (c *Client) CreateRepo(ctx context.Context, ref Ref) (created bool, err error) {
	body := map[string]string{"type": string(ref.Kind), "name": ref.ID()}
	err = c.doJSON(ctx, http.MethodPost, c.endpoint+"/api/repos/create", body, nil)
	switch {
	case err == nil:
		return true, nil
	case IsConflict(err):
		return false, nil
	default:
		return false, err
	}
}

// WebURL is the page a human opens for ref on the Web UI:
// {endpoint}/datasets/{ns}/{name} for datasets and {endpoint}/models/{ns}/{name}
// for models. This follows the UI's routes (frontend/app/{datasets,models}),
// not the HF-style bare /{ns}/{name} the server's create response uses --
// the UI does not serve that path. When the UI lives on a different origin
// than the API, only the path part is meaningful.
func (c *Client) WebURL(ref Ref) string {
	return c.endpoint + "/" + ref.Kind.Plural() + "/" + ref.ID()
}

// CommitURL is the page for one commit.
func (c *Client) CommitURL(ref Ref, oid string) string { return c.WebURL(ref) + "/commit/" + oid }

// TreeEntry is one file in a repository tree (directories are not returned).
type TreeEntry struct {
	Path string
	OID  string // git blob sha1 (of the pointer, when LFS != nil)
	Size int64  // real size (LFS target size when LFS != nil)
	LFS  *LFSInfo
}

// LFSInfo describes an LFS-tracked file.
type LFSInfo struct {
	OID  string // sha256
	Size int64
}

// Tree lists every file under rev recursively (GET .../tree/{rev}?recursive=true).
// A 404 -- an unborn branch or an empty repository -- yields (nil, nil); callers
// are expected to have checked RepoExists first.
func (c *Client) Tree(ctx context.Context, ref Ref, rev string) ([]TreeEntry, error) {
	rawURL := c.repoAPIBase(ref) + "/tree/" + url.PathEscape(rev) + "?recursive=true"
	var wire []struct {
		Type string `json:"type"`
		OID  string `json:"oid"`
		Size int64  `json:"size"`
		Path string `json:"path"`
		LFS  *struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"lfs"`
	}
	if err := c.doJSON(ctx, http.MethodGet, rawURL, nil, &wire); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	// A recursive listing carries the directories too; only files can be
	// compared with anything local.
	entries := make([]TreeEntry, 0, len(wire))
	for _, e := range wire {
		if e.Type != "file" {
			continue
		}
		entry := TreeEntry{Path: e.Path, OID: e.OID, Size: e.Size}
		if e.LFS != nil {
			entry.LFS = &LFSInfo{OID: e.LFS.OID, Size: e.LFS.Size}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// ----------------------------------------------------------------- preupload

// UploadMode is the server's routing decision for one path.
type UploadMode string

const (
	ModeRegular UploadMode = "regular"
	ModeLFS     UploadMode = "lfs"
)

// PreuploadFile is one entry of the preupload request.
type PreuploadFile struct {
	Path   string
	Size   int64
	Sample []byte // first bytes of the file (<= 512); may be nil
}

// preuploadBatch is how many files travel in one preupload request. The server
// bounds the body at 8MB; 256 short records stay far below that.
const preuploadBatch = 256

// Preupload asks the server how each path must travel (POST .../preupload/{rev}).
// Large inputs are split into batches of at most 256 files. The result maps
// Path -> mode for every requested path.
func (c *Client) Preupload(ctx context.Context, ref Ref, rev string, files []PreuploadFile) (map[string]UploadMode, error) {
	rawURL := c.repoAPIBase(ref) + "/preupload/" + url.PathEscape(rev)
	modes := make(map[string]UploadMode, len(files))

	type wireFile struct {
		Path   string `json:"path"`
		Sample string `json:"sample"`
		Size   int64  `json:"size"`
	}
	for start := 0; start < len(files); start += preuploadBatch {
		end := min(start+preuploadBatch, len(files))
		batch := files[start:end]

		payload := struct {
			Files []wireFile `json:"files"`
		}{Files: make([]wireFile, 0, len(batch))}
		for _, f := range batch {
			payload.Files = append(payload.Files, wireFile{
				Path:   f.Path,
				Sample: base64.StdEncoding.EncodeToString(f.Sample),
				Size:   f.Size,
			})
		}

		var resp struct {
			Files []struct {
				Path       string `json:"path"`
				UploadMode string `json:"uploadMode"`
			} `json:"files"`
		}
		if err := c.doJSON(ctx, http.MethodPost, rawURL, payload, &resp); err != nil {
			return nil, err
		}
		for _, f := range resp.Files {
			modes[f.Path] = UploadMode(f.UploadMode)
		}
		for _, f := range batch {
			if _, ok := modes[f.Path]; !ok {
				return nil, fmt.Errorf("preupload: server did not answer for %q", f.Path)
			}
		}
	}
	return modes, nil
}

// ----------------------------------------------------------------------- LFS

// LFSObject identifies content for the LFS batch API.
type LFSObject struct {
	OID  string // sha256 hex
	Size int64
}

// LFSAction is one action of a batch response (href + headers to send).
type LFSAction struct {
	Href      string
	Header    map[string]string
	ExpiresIn int
}

// LFSBatchResult is the server's answer for one object. Upload == nil means the
// bytes are already stored (deduplicated) and only the commit is needed. Err is
// a per-object error the server returned inside the batch.
type LFSBatchResult struct {
	OID    string
	Size   int64
	Upload *LFSAction
	Verify *LFSAction
	Err    error
}

// lfsBatchSize is how many objects travel in one batch request.
const lfsBatchSize = 100

// LFSBatchUpload is POST /{kinds?}/{ns}/{name}.git/info/lfs/objects/batch with
// operation=upload, transfers=["basic"], hash_algo=sha256. Datasets use the
// /datasets/{ns}/{name}.git prefix, models /{ns}/{name}.git. Large inputs are
// split into batches of at most 100 objects. Results come back in input order.
func (c *Client) LFSBatchUpload(ctx context.Context, ref Ref, objs []LFSObject) ([]LFSBatchResult, error) {
	rawURL := c.gitBase(ref) + "/info/lfs/objects/batch"
	out := make([]LFSBatchResult, 0, len(objs))

	type wireObject struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	}
	type wireAction struct {
		Href      string            `json:"href"`
		Header    map[string]string `json:"header"`
		ExpiresIn int               `json:"expires_in"`
	}

	for start := 0; start < len(objs); start += lfsBatchSize {
		end := min(start+lfsBatchSize, len(objs))
		batch := objs[start:end]

		payload := struct {
			Operation string       `json:"operation"`
			Transfers []string     `json:"transfers"`
			Objects   []wireObject `json:"objects"`
			HashAlgo  string       `json:"hash_algo"`
		}{Operation: "upload", Transfers: []string{"basic"}, HashAlgo: "sha256"}
		for _, o := range batch {
			payload.Objects = append(payload.Objects, wireObject(o))
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode lfs batch request: %w", err)
		}
		req, err := c.newRequest(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentTypeLFS)
		req.Header.Set("Accept", contentTypeLFS)

		resp, err := c.send(req)
		if err != nil {
			return nil, err
		}
		var answer struct {
			Objects []struct {
				OID     string                `json:"oid"`
				Size    int64                 `json:"size"`
				Actions map[string]wireAction `json:"actions"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"objects"`
		}
		err = decodeBody(resp, &answer, http.MethodPost, rawURL)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		// The spec lets a server answer in any order, so index by oid and
		// rebuild the caller's order from the request.
		byOID := make(map[string]int, len(answer.Objects))
		for i, o := range answer.Objects {
			byOID[o.OID] = i
		}
		for _, want := range batch {
			i, ok := byOID[want.OID]
			if !ok {
				return nil, fmt.Errorf("lfs batch: server did not answer for object %s", want.OID)
			}
			got := answer.Objects[i]
			res := LFSBatchResult{OID: want.OID, Size: want.Size}
			if got.Size > 0 {
				res.Size = got.Size
			}
			if got.Error != nil {
				res.Err = &Error{
					Status:  got.Error.Code,
					Message: got.Error.Message,
					Method:  http.MethodPost,
					URL:     rawURL,
				}
			}
			if a, ok := got.Actions["upload"]; ok {
				res.Upload = &LFSAction{Href: a.Href, Header: a.Header, ExpiresIn: a.ExpiresIn}
			}
			if a, ok := got.Actions["verify"]; ok {
				res.Verify = &LFSAction{Href: a.Href, Header: a.Header, ExpiresIn: a.ExpiresIn}
			}
			out = append(out, res)
		}
	}
	return out, nil
}

// PutLFSObject PUTs the object's bytes to action.Href with action.Header,
// Content-Length = size and Content-Type application/octet-stream. open is
// called (possibly more than once, on retry) to obtain a fresh reader; the
// client does NOT add its own Authorization header here -- a signed GCS URL
// must be sent bare, and the emulator proxy's token is in action.Header.
func (c *Client) PutLFSObject(ctx context.Context, action LFSAction, open func() (io.ReadCloser, error), size int64) error {
	err := c.putLFSOnce(ctx, action, open, size)
	if err != nil && retryablePut(ctx, err) {
		err = c.putLFSOnce(ctx, action, open, size)
	}
	return err
}

func (c *Client) putLFSOnce(ctx context.Context, action LFSAction, open func() (io.ReadCloser, error), size int64) error {
	body, err := open()
	if err != nil {
		return fmt.Errorf("open content for upload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, action.Href, body)
	if err != nil {
		body.Close()
		return fmt.Errorf("build PUT %s: %w", action.Href, err)
	}
	// Signed URLs and the emulator proxy both need an exact length: neither
	// can accept a chunked body.
	req.ContentLength = size
	req.GetBody = func() (io.ReadCloser, error) { return open() }
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", contentTypeBinary)
	for k, v := range action.Header {
		req.Header.Set(k, v)
	}

	// Deliberately not c.send: the bearer token must not travel to a signed
	// URL, and http.Client closes the request body for us.
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("PUT %s: %w", redactedURL(action.Href), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return parseError(resp, http.MethodPut, redactedURL(action.Href))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// retryablePut reports whether a failed transfer is worth one more attempt: a
// server-side fault or a broken connection, but never a rejected request or a
// cancelled context.
func retryablePut(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Status >= 500
	}
	// Anything that is not an HTTP answer is a transport or open failure.
	return true
}

// redactedURL strips the query string, which on a signed URL *is* the
// credential: GCS puts X-Goog-Credential / X-Goog-Signature there, and the
// emulator proxy an op/exp/sig triple. These URLs are interpolated into
// transfer errors, which `tf up` prints to stderr and CI keeps forever, so
// nothing past the path may survive. url.URL.Redacted() is not enough on its
// own -- it only masks userinfo -- so the query goes first and Redacted()
// handles the rest.
func redactedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		// Unparseable, but it may still carry a signature: keep only the
		// part before the first "?".
		if i := strings.IndexByte(raw, '?'); i >= 0 {
			return raw[:i] + "?" + redactedMarker
		}
		return raw
	}
	hadQuery := u.RawQuery != "" || u.ForceQuery
	u.RawQuery = ""
	u.ForceQuery = false
	out := u.Redacted()
	if hadQuery {
		out += "?" + redactedMarker
	}
	return out
}

// redactedMarker stands in for a removed query string, so an error still says
// that there was one.
const redactedMarker = "[redacted]"

// VerifyLFSObject POSTs {"oid","size"} to action.Href with action.Header
// (Content-Type application/vnd.git-lfs+json).
func (c *Client) VerifyLFSObject(ctx context.Context, action LFSAction, obj LFSObject) error {
	payload, err := json.Marshal(map[string]any{"oid": obj.OID, "size": obj.Size})
	if err != nil {
		return fmt.Errorf("encode verify request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, action.Href, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build POST %s: %w", action.Href, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Content-Type", contentTypeLFS)
	req.Header.Set("Accept", contentTypeLFS)
	for k, v := range action.Header {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", redactedURL(action.Href), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return parseError(resp, http.MethodPost, redactedURL(action.Href))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// -------------------------------------------------------------------- commit

// CommitOpKind selects the NDJSON line type of a commit operation.
type CommitOpKind int

const (
	OpFile         CommitOpKind = iota // {"key":"file"}: inline, base64 content
	OpLFSFile                          // {"key":"lfsFile"}: pointer to an uploaded object
	OpDeleteFile                       // {"key":"deletedFile"}
	OpDeleteFolder                     // {"key":"deletedFolder"}
)

// CommitOp is one operation of a commit.
type CommitOp struct {
	Kind CommitOpKind
	Path string
	// Open supplies the content for OpFile; it is streamed and base64-encoded
	// into the request body, never fully buffered.
	Open func() (io.ReadCloser, error)
	// OID / Size describe the object for OpLFSFile.
	OID  string
	Size int64
}

// CommitResult is the server's answer to a successful commit.
type CommitResult struct {
	OID string // new commit sha
	URL string // commitUrl as the server reported it
}

// Commit is POST .../commit/{rev} with an application/x-ndjson body:
// one header line ({"key":"header","value":{"summary","description"}}) followed
// by one line per op (docs/dev/api-contract.md §3). The body is streamed through
// an io.Pipe so a large regular file never has to sit in memory twice.
func (c *Client) Commit(ctx context.Context, ref Ref, rev, summary, description string, ops []CommitOp) (*CommitResult, error) {
	if summary == "" {
		summary = "Upload files"
	}
	rawURL := c.repoAPIBase(ref) + "/commit/" + url.PathEscape(rev)

	pr, pw := io.Pipe()
	go func() {
		// CloseWithError(nil) is a plain Close, so this covers both outcomes.
		_ = pw.CloseWithError(writeCommitBody(pw, summary, description, ops))
	}()

	req, err := c.newRequest(ctx, http.MethodPost, rawURL, pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, err
	}
	req.Header.Set("Content-Type", contentTypeNDJSON)

	resp, err := c.send(req)
	if err != nil {
		// The transport has already closed the body; this only unblocks the
		// writer if it somehow survived.
		_ = pr.CloseWithError(err)
		return nil, err
	}
	defer resp.Body.Close()

	var answer struct {
		Success   bool   `json:"success"`
		CommitURL string `json:"commitUrl"`
		CommitOID string `json:"commitOid"`
	}
	if err := decodeBody(resp, &answer, http.MethodPost, rawURL); err != nil {
		return nil, err
	}
	return &CommitResult{OID: answer.CommitOID, URL: answer.CommitURL}, nil
}

// writeCommitBody streams the NDJSON payload. Regular file content is written
// straight through a base64 encoder into the JSON string, so a large file is
// never held in memory.
func writeCommitBody(w io.Writer, summary, description string, ops []CommitOp) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw) // Encode appends the newline NDJSON wants.

	type header struct {
		Summary     string `json:"summary"`
		Description string `json:"description"`
	}
	if err := enc.Encode(map[string]any{
		"key":   "header",
		"value": header{Summary: summary, Description: description},
	}); err != nil {
		return fmt.Errorf("write commit header: %w", err)
	}

	for _, op := range ops {
		var err error
		switch op.Kind {
		case OpFile:
			err = writeFileLine(bw, op)
		case OpLFSFile:
			err = enc.Encode(map[string]any{
				"key": "lfsFile",
				"value": map[string]any{
					"path": op.Path, "algo": "sha256", "oid": op.OID, "size": op.Size,
				},
			})
		case OpDeleteFile:
			err = enc.Encode(map[string]any{
				"key": "deletedFile", "value": map[string]any{"path": op.Path},
			})
		case OpDeleteFolder:
			err = enc.Encode(map[string]any{
				"key": "deletedFolder", "value": map[string]any{"path": op.Path},
			})
		default:
			err = fmt.Errorf("unknown commit operation kind %d", op.Kind)
		}
		if err != nil {
			return fmt.Errorf("write commit operation for %q: %w", op.Path, err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush commit body: %w", err)
	}
	return nil
}

// writeFileLine hand-builds one {"key":"file"} line so the content can be
// base64-encoded straight into the JSON string literal. json.Encoder would
// need the whole encoded blob in memory first.
func writeFileLine(bw *bufio.Writer, op CommitOp) error {
	if op.Open == nil {
		return errors.New("file operation has no content")
	}
	pathJSON, err := json.Marshal(op.Path)
	if err != nil {
		return fmt.Errorf("encode path: %w", err)
	}
	if _, err := bw.WriteString(`{"key":"file","value":{"path":`); err != nil {
		return err
	}
	if _, err := bw.Write(pathJSON); err != nil {
		return err
	}
	if _, err := bw.WriteString(`,"encoding":"base64","content":"`); err != nil {
		return err
	}

	rc, err := op.Open()
	if err != nil {
		return fmt.Errorf("open content: %w", err)
	}
	// The base64 alphabet is JSON-safe, so nothing here needs escaping.
	b64 := base64.NewEncoder(base64.StdEncoding, bw)
	_, copyErr := io.Copy(b64, rc)
	closeErr := b64.Close()
	rcErr := rc.Close()
	switch {
	case copyErr != nil:
		return fmt.Errorf("read content: %w", copyErr)
	case closeErr != nil:
		return closeErr
	case rcErr != nil:
		return fmt.Errorf("close content: %w", rcErr)
	}

	_, err = bw.WriteString("\"}}\n")
	return err
}
