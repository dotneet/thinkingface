package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
)

// handleServerInfo answers GET /api/v1/server-info: the two deployment
// switches a signed-out client has to know about before it can do anything
// useful (docs/dev/agent-features.md §1.5). It is on requireAuthForRead's
// allowlist -- it is how the web UI finds out that it must send a visitor to
// /login rather than render every page as an error -- so it must never carry
// anything that is not already implied by the login page itself.
func (s *Server) handleServerInfo(w http.ResponseWriter, _ *http.Request) {
	// Not cached: it is one struct literal to compute, and a cache holding it
	// across a restart with a different environment would keep redirecting
	// (or not) on stale news. The web UI's proxy.ts keeps its own short
	// in-memory copy.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, apitypes.ServerInfo{
		RequireAuthForRead: s.cfg.RequireAuthForRead,
		AllowSignup:        s.cfg.AllowSignup,
	})
}

// requireAuthForRead is TF_REQUIRE_AUTH_FOR_READ: with it on, a request that
// identify could not attach a user to is answered 401 before it reaches any
// handler, apart from the short allowlist in readAuthExempt and the signed
// LFS transfer URLs in signedLFSTransfer. With it off (the default) it is not
// in the chain at all.
//
// It must run after identify, which is the only thing that puts a user in the
// context. An account identify refuses -- suspended, or still waiting for
// approval -- is anonymous here too, which is the point: such an account must
// not be able to read what the switch closes.
//
// The 401 carries the same WWW-Authenticate challenge unauthorized() sends, so
// git and git-lfs prompt for (or fetch from a credential helper) a token and
// retry, and huggingface_hub surfaces the X-Error-Message. The error type is
// distinct from the handlers' own "unauthorized" so a client can tell "this
// instance is closed to anonymous callers" apart from "this call needs a
// user".
//
// SSH is unaffected: internal/sshserver authenticates every session with a
// registered key before any of this package runs.
func (s *Server) requireAuthForRead(next http.Handler) http.Handler {
	if s.cfg == nil || !s.cfg.RequireAuthForRead {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r.Context()) != nil || readAuthExempt(r) || s.signedLFSTransfer(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="thinkingface"`)
		writeError(w, http.StatusUnauthorized, "authentication_required",
			"this instance requires authentication for every request: sign in, or send an access token")
	})
}

// readAuthExempt is the allowlist of routes an anonymous caller may still
// reach when reads require authentication: what it takes to find out that
// they do, and to sign in. Matched on the exact decoded path, so no other
// route can be reached through a prefix of one of these.
//
// GET /api/v1/me is listed although it already refuses an anonymous caller,
// so that its answer stays the ordinary one the web UI treats as "signed
// out". GET /api/openapi.json is deliberately not listed: it describes this
// instance's surface and gives an anonymous caller nothing it can use.
func readAuthExempt(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		// CORS preflights carry no credentials by definition; s.cors answers
		// them before this runs, and this keeps it that way if the order
		// ever changes.
		return true
	}
	switch r.URL.Path {
	case "/healthz":
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	case "/api/v1/auth/login", "/api/v1/auth/signup", "/api/v1/auth/logout":
		return r.Method == http.MethodPost
	case "/api/v1/me", "/api/v1/server-info":
		return r.Method == http.MethodGet
	}
	return false
}

// signedLFSTransfer reports whether r is one of the emulator transfer proxy's
// self-authenticating URLs (lfs.Handler.proxyHref and the verify href the batch
// response mints) carrying a valid, unexpired signature.
//
// Those URLs stay valid under TF_REQUIRE_AUTH_FOR_READ. They are only ever
// issued by the batch endpoint, which the caller had to be authenticated to
// reach with the switch on, and git-lfs and huggingface_hub send no
// Authorization header with the transfer itself -- refusing them would break
// every LFS upload and download against the emulator. The signature is exactly
// the credential a GCS signed URL is in production, where these requests never
// touch this server at all. The handler re-checks the signature; this only
// decides whether the request may reach it.
func (s *Server) signedLFSTransfer(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/lfs/")
	if !ok || s.lfs == nil {
		return false
	}
	idRaw, tail, ok := strings.Cut(rest, "/")
	if !ok || strings.Contains(tail, "/") {
		return false
	}
	repoID, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil {
		return false
	}
	var op, oid string
	switch {
	case tail == "verify" && r.Method == http.MethodPost:
		op = "verify"
	case gitrepo.ValidOID(tail) && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		op, oid = "download", tail
	case gitrepo.ValidOID(tail) && r.Method == http.MethodPut:
		op, oid = "upload", tail
	default:
		return false
	}
	return s.lfsProxyAuthorized(r, op, repoID, oid)
}
