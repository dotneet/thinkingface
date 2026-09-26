// Tests for the CORS/CSRF rejection log (docs/dev/agent-features.md §1.3):
// the first time a given origin is refused, it is logged once, an operator
// gets a hint pointing at TF_ALLOWED_ORIGINS, repeats of the same origin are
// silent, and a bounded number of distinct origins are ever recorded.

package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// captureHandler is a minimal slog.Handler that records every log record, so
// a test can assert on what was (or was not) logged without depending on
// slog's text/JSON formatting.
type captureHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	msg   string
	attrs map[string]string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]string, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, capturedRecord{msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]capturedRecord, len(h.records))
	copy(out, h.records)
	return out
}

// swapSlog installs a captureHandler as the process-wide slog default for
// the duration of the test and restores the previous one afterward. slog's
// default is global state, so this only works because Go runs a package's
// tests sequentially by default and none of these tests uses t.Parallel.
func swapSlog(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func TestCORSRejection_LoggedOncePerOrigin(t *testing.T) {
	f := newSecFixture(t)
	h := swapSlog(t)

	for range 3 {
		f.do(secRequest{
			method:  "OPTIONS",
			path:    "/api/v1/me",
			headers: map[string]string{"Origin": "https://evil.example"},
		})
	}

	var matches int
	for _, rec := range h.snapshot() {
		if rec.msg == "cors: origin not allowed" && rec.attrs["origin"] == "https://evil.example" {
			matches++
			if !strings.Contains(rec.attrs["hint"], "TF_ALLOWED_ORIGINS") {
				t.Errorf("hint = %q, want it to mention TF_ALLOWED_ORIGINS", rec.attrs["hint"])
			}
		}
	}
	if matches != 1 {
		t.Fatalf("logged %d times for 3 rejections of the same origin, want exactly 1", matches)
	}
}

func TestCORSRejection_NotLoggedForAllowedOrEmptyOrigin(t *testing.T) {
	f := newSecFixture(t)
	h := swapSlog(t)

	// Allowlisted origin: not a rejection.
	f.do(secRequest{
		method:  "OPTIONS",
		path:    "/api/v1/me",
		headers: map[string]string{"Origin": "http://web.test.local"},
	})
	// No Origin header at all: not a browser call, and not a rejection.
	f.do(secRequest{method: "GET", path: "/api/v1/me"})

	for _, rec := range h.snapshot() {
		if strings.HasPrefix(rec.msg, "cors:") {
			t.Fatalf("unexpected cors log entry for an allowed/no-origin request: %+v", rec)
		}
	}
}

func TestCORSRejection_DistinctOriginsEachLoggedOnceUpToCap(t *testing.T) {
	f := newSecFixture(t)
	h := swapSlog(t)

	total := corsRejectionLogCap + 5
	for i := range total {
		origin := fmt.Sprintf("https://evil-%d.example", i)
		f.do(secRequest{
			method:  "OPTIONS",
			path:    "/api/v1/me",
			headers: map[string]string{"Origin": origin},
		})
	}

	var perOrigin, overflow int
	for _, rec := range h.snapshot() {
		switch rec.msg {
		case "cors: origin not allowed":
			perOrigin++
		case "cors: further rejected origins not logged":
			overflow++
		}
	}
	if perOrigin != corsRejectionLogCap {
		t.Fatalf("logged %d distinct origins, want the cap (%d)", perOrigin, corsRejectionLogCap)
	}
	if overflow != 1 {
		t.Fatalf("overflow notice logged %d times, want exactly 1", overflow)
	}
}

// requireSameOrigin is the other caller of corsRejectionLog: a cookie-borne
// cross-origin state change is refused with a 403, and the first refusal of
// a given origin is logged the same way the preflight rejection is.
func TestCSRFRejection_Logged(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct horse battery")
	login := f.do(secRequest{
		method: "POST", path: "/api/v1/auth/login",
		body: map[string]string{"username": "alice", "password": "correct horse battery"},
	})
	cookie := sessionCookie(login)
	if cookie == nil {
		t.Fatalf("login set no session cookie: %s", login.Body.String())
	}

	h := swapSlog(t)
	rec := f.do(secRequest{
		method:  "POST",
		path:    "/api/v1/tokens",
		body:    map[string]string{"name": "x", "scope": "write"},
		headers: map[string]string{"Origin": "https://evil.example"},
		cookies: []*http.Cookie{cookie},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin token mint: status = %d, body = %s; want 403", rec.Code, rec.Body.String())
	}

	var matches int
	for _, r := range h.snapshot() {
		if r.msg == "cors: origin not allowed" && r.attrs["origin"] == "https://evil.example" {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("logged %d times for the CSRF rejection, want exactly 1", matches)
	}
}

// An origin is kept and logged at most corsRejectionLogMaxOrigin bytes long,
// marked as cut: the entry cap bounds how many origins are remembered, and
// this bounds how much each one pins -- a header can be a megabyte.
func TestCORSRejection_LongOriginIsTruncated(t *testing.T) {
	var l corsRejectionLog
	h := swapSlog(t)

	long := "https://" + strings.Repeat("a", 1<<20) + ".example"
	l.logOnce(long)
	// A different origin with the same first 256 bytes is the same entry.
	l.logOnce(long + "x")

	var logged []string
	for _, rec := range h.snapshot() {
		if rec.msg == "cors: origin not allowed" {
			logged = append(logged, rec.attrs["origin"])
		}
	}
	if len(logged) != 1 {
		t.Fatalf("logged %d entries, want 1", len(logged))
	}
	want := long[:corsRejectionLogMaxOrigin] + "...(truncated)"
	if logged[0] != want {
		t.Fatalf("logged origin is %d bytes, want the %d-byte prefix marked as truncated", len(logged[0]), corsRejectionLogMaxOrigin)
	}
	for k := range l.seen {
		if len(k) > corsRejectionLogMaxOrigin+len("...(truncated)") {
			t.Fatalf("remembered a %d-byte origin", len(k))
		}
	}

	// A short origin is logged as sent, and a multi-byte rune split by the
	// cut is dropped rather than logged as invalid UTF-8.
	l.logOnce("https://ok.example")
	split := "https://" + strings.Repeat("b", corsRejectionLogMaxOrigin-len("https://")-1) + "é/rest"
	l.logOnce(split)
	got := map[string]bool{}
	for _, rec := range h.snapshot() {
		got[rec.attrs["origin"]] = true
	}
	if !got["https://ok.example"] {
		t.Fatalf("a short origin was not logged as sent: %v", got)
	}
	if want := split[:corsRejectionLogMaxOrigin-1] + "...(truncated)"; !got[want] {
		t.Fatalf("the split rune was not dropped cleanly")
	}
}
