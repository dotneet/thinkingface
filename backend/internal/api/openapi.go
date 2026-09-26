// GET /api/openapi.json (docs/dev/agent-features.md §5, proposal P11): a
// hand-maintained OpenAPI 3.1 document describing the programmatic surface an
// agent or script drives -- auth, tokens, repository listing/creation and the
// whole of /api/v1/experiments. It is embedded in the binary and served
// as-is, except that the "servers" entry is patched at serve time to the
// instance's own configured public URL, so the document a client fetches is
// directly usable without hand-editing.
//
// openapi_test.go is what keeps this honest: it walks the chi router and
// fails when a path this file documents is not actually routed, or when a
// routed /api/v1/experiments or /api/v1/tokens path is not documented here.
package api

import (
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
)

//go:embed openapi.json
var openAPISpec []byte

// openAPITemplate is openapi.json decoded once at package init, as raw
// top-level members so re-serializing it does not have to re-decode (and
// re-validate) every nested schema on every request -- only the "servers"
// member is ever replaced.
//
// Decoding at init() rather than lazily means a malformed embedded file
// fails the build immediately (any test that imports this package, and the
// running binary itself) rather than surfacing as a 500 on the first
// request.
var openAPITemplate map[string]json.RawMessage

func init() {
	if err := json.Unmarshal(openAPISpec, &openAPITemplate); err != nil {
		panic("internal/api: openapi.json does not parse: " + err.Error())
	}
}

// openAPIServersFor renders the "servers" member for one base URL: the
// configured TF_PUBLIC_URL, or "/" when it is unset (the zero-value Server
// defaultRoutes() builds for routing tests, and a misconfigured instance).
func openAPIServersFor(publicURL string) json.RawMessage {
	if publicURL == "" {
		publicURL = "/"
	}
	b, err := json.Marshal([]map[string]string{{"url": publicURL}})
	if err != nil {
		// Marshaling a one-field literal cannot fail; kept as a guard rather
		// than a silent empty body.
		panic("internal/api: marshal openapi servers: " + err.Error())
	}
	return b
}

// openAPIDocument renders the served document for this Server: the embedded
// template with "servers" patched to the instance's own configured public
// URL. Every top-level member other than "servers" is copied as the raw
// bytes decoded at init(), so this is a handful of map entries plus one
// small literal re-serialized -- not a re-encode of the whole spec -- cheap
// enough to do on every request for an endpoint nothing calls in a hot path,
// and it avoids caching keyed by *Server (which would grow without bound
// across the many short-lived Server values the test suite constructs).
func (s *Server) openAPIDocument() []byte {
	publicURL := ""
	if s.cfg != nil {
		publicURL = s.cfg.PublicURL
	}
	merged := make(map[string]json.RawMessage, len(openAPITemplate))
	for k, v := range openAPITemplate {
		merged[k] = v
	}
	merged["servers"] = openAPIServersFor(publicURL)
	b, err := json.Marshal(merged)
	if err != nil {
		slog.Error("marshal openapi document", "error", err)
		return openAPISpec // the static, un-patched document is still a valid answer
	}
	return b
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	// Public and small enough that a short cache is a plain win: an agent
	// building a client from it fetches once per session rather than once
	// per call, and the document changes only on a deploy.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(s.openAPIDocument())
}
