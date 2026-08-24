// URL path parameters, and the one rule every handler that reads one has to
// follow: decode exactly once, and only when chi actually handed over an
// encoded value.
//
// The whole problem comes from huggingface_hub quoting revisions with
// `quote(revision, safe="")`, so a branch called "feature/x" travels as
// "feature%2Fx" -- and from what that single "%2F" does to the rest of the
// request.

package api

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// pathParam reads one chi URL path parameter, decoded exactly once.
//
// chi routes on the *escaped* path when there is one and on the plain path
// otherwise (Mux.routeHTTP: `routePath := rctx.RoutePath; if routePath == "" {
// if r.URL.RawPath != "" { routePath = r.URL.RawPath } else { routePath =
// r.URL.Path } }`), and the parameters it hands a handler are slices of
// whichever string it routed on. net/url sets RawPath only when the escaped
// and unescaped forms of the path actually differ (url.URL.setPath), so the
// two cases are:
//
//   - RawPath == "" -- the parameters are already decoded, and unescaping here
//     would decode a second time. That is not hypothetical: a file named
//     "a%25b.txt" arrives as "a%b.txt", where a second pass fails outright
//     ("%b." is not a valid escape), and a file named "a%252Fb" arrives as
//     "a%2Fb", where a second pass invents a directory separator that was
//     never in the name.
//   - RawPath != "" -- the parameters are still percent-encoded. One "%2F" in
//     the revision is enough to put the *entire* request in this case, so the
//     file path of that same request needs decoding too. That is why a
//     slash-bearing branch broke reads and writes in both halves at once.
//
// The error is only reachable in the second case, and barely: net/http rejects
// a request line whose path has a bad escape before any handler runs, so by
// the time RawPath is non-empty net/url has already unescaped the whole path
// successfully, and a chi parameter is a "/"-delimited slice of it (a "/"
// never appears inside a "%XX" escape, so no slice can split one). It is
// reported rather than swallowed anyway, because guessing at a path a caller
// meant is worse than saying no.
func pathParam(r *http.Request, key string) (string, error) {
	raw := chi.URLParam(r, key)
	if r.URL.RawPath == "" {
		return raw, nil
	}
	return url.PathUnescape(raw)
}
