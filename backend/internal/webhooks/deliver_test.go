package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// recordingHandler remembers what each request carried, so a test can ask
// which hosts saw the signature header rather than only whether a delivery
// succeeded.
type recordingHandler struct {
	mu      sync.Mutex
	hits    int
	headers []http.Header

	status int
	body   string
}

func (h *recordingHandler) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hits++
	h.headers = append(h.headers, r.Header.Clone())
}

func (h *recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.record(r)
	if h.status != 0 {
		w.WriteHeader(h.status)
	}
	_, _ = w.Write([]byte(h.body))
}

func (h *recordingHandler) snapshot() (int, []http.Header) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits, append([]http.Header(nil), h.headers...)
}

// testDispatcher builds a Dispatcher wired for httptest servers. They listen on
// loopback, which the SSRF guard refuses by design, so these tests use the same
// opt-out an operator running a local receiver would.
func testDispatcher() *Dispatcher {
	return New(nil, Options{AllowPrivateTargets: true})
}

func testJob(url string) *store.WebhookDeliveryJob {
	return &store.WebhookDeliveryJob{
		DeliveryID:    7,
		WebhookID:     3,
		Event:         "repo.push",
		Payload:       []byte(`{"repo":"acme/model"}`),
		URL:           url,
		Secret:        "s3cret",
		WebhookActive: true,
	}
}

// TestDeliverySignsTheConfiguredEndpoint is the baseline the redirect test
// below is measured against: the endpoint an operator configured does get the
// signature, and it is a MAC over the payload keyed with that webhook's secret.
func TestDeliverySignsTheConfiguredEndpoint(t *testing.T) {
	endpoint := &recordingHandler{}
	srv := httptest.NewServer(endpoint)
	defer srv.Close()

	job := testJob(srv.URL)
	status, _, err := testDispatcher().deliver(context.Background(), job)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if status == nil || *status != http.StatusOK {
		t.Fatalf("status = %v, want 200", status)
	}
	hits, headers := endpoint.snapshot()
	if hits != 1 {
		t.Fatalf("endpoint saw %d requests, want 1", hits)
	}
	if got, want := headers[0].Get("X-Thinkingface-Signature"), Sign([]byte(job.Secret), job.Payload); got != want {
		t.Errorf("signature header = %q, want %q", got, want)
	}
}

// TestDeliveryDoesNotFollowRedirects is the regression test for the signature
// leak: http.Client strips Authorization across a host boundary but forwards
// custom headers untouched, so following a 3xx handed X-Thinkingface-Signature
// -- computed for the configured endpoint, with that endpoint's secret -- to
// whatever host the Location header named. The redirect must not be followed at
// all, and the 3xx itself must be recorded as the failure it is so the operator
// can see why nothing is being delivered.
func TestDeliveryDoesNotFollowRedirects(t *testing.T) {
	elsewhere := &recordingHandler{}
	other := httptest.NewServer(elsewhere)
	defer other.Close()

	redirector := &recordingHandler{}
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirector.record(r)
		http.Redirect(w, r, other.URL+"/hook", http.StatusFound)
	}))
	defer first.Close()

	job := testJob(first.URL)
	status, _, err := testDispatcher().deliver(context.Background(), job)

	if hits, headers := elsewhere.snapshot(); hits != 0 {
		t.Fatalf("the redirect target received %d request(s) carrying signature %q",
			hits, headers[0].Get("X-Thinkingface-Signature"))
	}
	if firstHits, _ := redirector.snapshot(); firstHits != 1 {
		t.Fatalf("the configured endpoint saw %d requests, want exactly 1", firstHits)
	}
	if err == nil {
		t.Fatal("a redirecting endpoint must be recorded as a failed delivery")
	}
	if status == nil || *status != http.StatusFound {
		t.Fatalf("status = %v, want the 302 itself so the delivery history shows it", status)
	}
}
