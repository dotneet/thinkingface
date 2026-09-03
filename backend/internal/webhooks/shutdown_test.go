package webhooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// A delivery claimed just before shutdown has already spent an attempt, so
// the outcome has to be recorded even though the context that carried it is
// cancelled. Writing it on the caller's context left the row pending with a
// higher attempt count and nothing to say what the endpoint answered -- a
// webhook could then walk through MaxAttempts across a handful of deploys
// without a single delivery ever being reported as having failed.
func TestStepRecordsTheOutcomeAfterTheContextIsCancelled(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.CreateUser(ctx, "alice", "alice@example.com", "hash", false); err != nil {
		t.Fatalf("create user: %v", err)
	}
	ns, err := st.GetNamespace(ctx, "alice")
	if err != nil {
		t.Fatalf("namespace: %v", err)
	}

	// The endpoint cancels the worker's context while it is answering,
	// which is what a SIGTERM landing mid-delivery looks like: the claim
	// already happened on a live context, and the finish will not.
	cancelCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	hook, err := st.CreateWebhook(ctx, ns.ID, nil, srv.URL, "s3cret", []string{"repo.push"}, true)
	if err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	deliveryID, err := st.CreateWebhookDelivery(ctx, hook.ID, "repo.push", []byte(`{"repo":"alice/m"}`))
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}

	d := New(st, Options{AllowPrivateTargets: true})
	if _, err := d.step(cancelCtx); err != nil {
		t.Fatalf("step: %v", err)
	}
	if cancelCtx.Err() == nil {
		t.Fatal("the endpoint did not cancel the context; the test proves nothing")
	}

	got, err := st.GetWebhookDelivery(ctx, deliveryID)
	if err != nil {
		t.Fatalf("get delivery: %v", err)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
	// Either terminal state is fine; what must not happen is the attempt
	// being spent with nothing recorded about it.
	if got.LastAttemptAt == nil {
		t.Errorf("last_attempt_at is nil: the attempt was spent but never recorded")
	}
}
