// Tests for the X-Request-Id response header: every response must carry one,
// two requests must not share one, and a client-supplied value must never be
// echoed back (see stripClientRequestID's doc comment in requestid.go for
// why: chi's middleware.RequestID trusts that header verbatim, and this ID
// is the join key between a user's bug report and this process's logs).
//
// Driven over real HTTP against a real Server, the same way revision_test.go
// and errors_signal_test.go are.

package api

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/dotneet/thinkingface/backend/internal/auth"
	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/gitrepo"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

func newRequestIDFixtureServer(t *testing.T) *Server {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	gitMgr := gitrepo.NewManager(t.TempDir())
	cfg := &config.Config{
		PublicURL: "http://test.local", WALMode: "off", SessionSecret: "test-secret-test-secret",
	}
	return NewServer(Deps{
		Config:   cfg,
		Store:    st,
		Git:      gitMgr,
		Storage:  newMemStore(),
		Sessions: auth.NewSessions(cfg.SessionSecret, time.Hour),
		Syncer:   noopEnqueuer{},
	})
}

// TestRequestID_SetOnSuccess checks a plain 200 response carries a
// non-empty X-Request-Id.
func TestRequestID_SetOnSuccess(t *testing.T) {
	s := newRequestIDFixtureServer(t)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(middleware.RequestIDHeader); got == "" {
		t.Fatalf("X-Request-Id missing on success response")
	}
}

// TestRequestID_SetOnError checks an error response (unmatched route -> 404)
// also carries the header. This is the case that actually matters: it is
// error responses a user quotes back in a bug report.
func TestRequestID_SetOnError(t *testing.T) {
	s := newRequestIDFixtureServer(t)

	req := httptest.NewRequest("GET", "/no-such-route", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get(middleware.RequestIDHeader); got == "" {
		t.Fatalf("X-Request-Id missing on error response")
	}
}

// TestRequestID_DiffersAcrossRequests checks two independent requests get
// two different IDs, so the header is actually usable to disambiguate them
// in logs.
func TestRequestID_DiffersAcrossRequests(t *testing.T) {
	s := newRequestIDFixtureServer(t)

	get := func() string {
		req := httptest.NewRequest("GET", "/healthz", nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Header().Get(middleware.RequestIDHeader)
	}

	first, second := get(), get()
	if first == "" || second == "" {
		t.Fatalf("expected non-empty ids, got %q and %q", first, second)
	}
	if first == second {
		t.Fatalf("expected different ids across requests, both were %q", first)
	}
}

// TestRequestID_ClientSuppliedHeaderIsIgnored checks that a client-provided
// X-Request-Id is never echoed back. chi's middleware.RequestID trusts that
// header verbatim when present; stripClientRequestID (requestid.go) exists
// specifically to defeat that, since this ID lands in structured logs and an
// attacker-chosen value would be log injection.
func TestRequestID_ClientSuppliedHeaderIsIgnored(t *testing.T) {
	s := newRequestIDFixtureServer(t)

	const injected = "attacker-controlled-value"
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set(middleware.RequestIDHeader, injected)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	got := rec.Header().Get(middleware.RequestIDHeader)
	if got == "" {
		t.Fatalf("X-Request-Id missing on response")
	}
	if got == injected {
		t.Fatalf("server echoed back the client-supplied X-Request-Id %q; it must always generate its own", injected)
	}
}
