package api

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// stripClientRequestID clears any client-supplied X-Request-Id before
// middleware.RequestID runs, so the request ID that ends up in this
// process's logs and response headers is always server-generated.
//
// chi's middleware.RequestID (go-chi/chi/v5/middleware/request_id.go) only
// generates an ID when the incoming request has none; if the client sends
// one, it is echoed back verbatim with no length or character validation.
// The ID this server assigns is the join key between a user's bug report and
// its log lines, so trusting a client-chosen value would let a caller inject
// arbitrary content into structured logs (unbounded length, control
// characters, a value crafted to collide with a real request) or hand out a
// misleading correlation ID. There is no legitimate reason for a caller of
// this API to set its own request id, so the server always wins.
func stripClientRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(middleware.RequestIDHeader) != "" {
			r.Header.Del(middleware.RequestIDHeader)
		}
		next.ServeHTTP(w, r)
	})
}

// setRequestIDHeader echoes the request ID middleware.RequestID put in the
// context back onto the response as X-Request-Id, so a client (or a human
// comparing a bug report against the server's logs) can quote it.
//
// Must run after middleware.RequestID, so the ID already exists in the
// context, and before any handler writes the response body: headers set
// after the first Write (or WriteHeader) are silently dropped.
func setRequestIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := middleware.GetReqID(r.Context()); id != "" {
			w.Header().Set(middleware.RequestIDHeader, id)
		}
		next.ServeHTTP(w, r)
	})
}
