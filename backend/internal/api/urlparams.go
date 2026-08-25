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
	"strconv"

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

// int64Param reads a numeric {key} out of the URL path, answering the request
// itself when it is not a number. `what` names the thing in the message, as
// in "sync job id must be a number".
//
// The eight call sites this replaced spelled the same refusal three ways --
// "must be an integer", "must be a number" and, on the two LFS routes,
// "invalid repository id" -- for identical input, so a client could not learn
// the rule from one message and rely on it in another. "number" is the one
// kept: it is what the majority already said.
func int64Param(w http.ResponseWriter, r *http.Request, key, what string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	if err != nil {
		badRequest(w, what+" id must be a number")
		return 0, false
	}
	return id, true
}

// pageParams reads the `limit` / `offset` window of a Web UI listing, clamped
// to [1, maxLimit] with defLimit standing in for anything missing or
// unusable.
//
// There are deliberately two ways to read a page window in this package, and
// this is only one of them:
//
//   - The Web UI's own listings are lenient, which is what this function is.
//     `?limit=abc` is a page of the default size, not a 400. These query
//     strings are built by our own frontend, the response has to render
//     something for a user who did not type them, and a listing that quietly
//     falls back is a better answer than a screen-sized error for a parameter
//     nobody chose.
//   - The HuggingFace-compatible listing (hfListFilter) and the two
//     commit pagers (commitPageParams) are strict: an unusable `limit` is a
//     400. That side is not a style preference. huggingface_hub's `paginate`
//     follows Link headers this server builds out of the very parameters it
//     was sent, so silently answering a different window than the one asked
//     for produces a client that pages wrongly rather than one that reports
//     an error -- and the external protocol is the contract there (CLAUDE.md
//     invariant 5).
//
// The clamp happens here rather than being left to the store because the
// handler often has to reason about the window it actually got: a listing
// that decides "is there another page?" by comparing the rows returned
// against `limit` reads an unclamped one as "there is no more"
// (docs/dev/api-contract.md §1.1). The store clamps again regardless; the two
// agree, so the second pass is a no-op.
func pageParams(q url.Values, defLimit, maxLimit int) (limit, offset int) {
	limit, _ = strconv.Atoi(q.Get("limit"))
	offset, _ = strconv.Atoi(q.Get("offset"))
	switch {
	case limit <= 0:
		limit = defLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
