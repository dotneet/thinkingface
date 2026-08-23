// Package webhooks fires event notifications (repo.push, repo.created,
// repo.deleted, run.finished, run.failed) at operator-configured URLs.
//
// It follows the same PG-table-as-queue shape as internal/syncer: firing an
// event only inserts webhook_deliveries rows (via Fire), and a small worker
// pool claims and delivers them over HTTP, retrying failures with
// exponential backoff up to MaxAttempts before giving up.
package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/store"
)

// deliveryTimeout bounds one HTTP delivery attempt.
const deliveryTimeout = 10 * time.Second

// leaseDuration is how long a claimed delivery is reserved for before it
// becomes claimable again on its own, in case the worker that claimed it
// crashes mid-delivery. It comfortably exceeds deliveryTimeout.
const leaseDuration = 2 * time.Minute

// maxResponseBodyBytes caps how much of a webhook endpoint's response is kept
// in webhook_deliveries.response_body, per the task's "first few KB".
const maxResponseBodyBytes = 4 << 10

// Dispatcher enqueues and delivers webhook events.
type Dispatcher struct {
	store        *store.Store
	client       *http.Client
	allowPrivate bool

	workers int
	wake    chan struct{}
}

// Options configures a Dispatcher.
type Options struct {
	// AllowPrivateTargets opts out of the SSRF guard, for local development
	// where the webhook receiver legitimately lives on localhost/a private
	// network. Set from TF_WEBHOOKS_ALLOW_PRIVATE_TARGETS; defaults to false.
	AllowPrivateTargets bool
	// Workers is the size of the delivery worker pool. Defaults to 1.
	Workers int
}

func New(st *store.Store, opts Options) *Dispatcher {
	workers := opts.Workers
	if workers < 1 {
		workers = 1
	}
	return &Dispatcher{
		store:        st,
		allowPrivate: opts.AllowPrivateTargets,
		workers:      workers,
		client: &http.Client{
			Timeout:   deliveryTimeout,
			Transport: newDeliveryTransport(opts.AllowPrivateTargets, deliveryTimeout),
		},
		wake: make(chan struct{}, 1),
	}
}

// Fire records a delivery for every active webhook subscribed to event in
// namespace ns, optionally narrowed to repoID (nil for a namespace-level
// event). It never blocks on network I/O: firing only writes rows, and the
// worker pool started by Run does the actual delivering.
func (d *Dispatcher) Fire(ctx context.Context, event, ns string, repoID *int64, payload any) error {
	namespace, err := d.store.GetNamespace(ctx, ns)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("load namespace %q: %w", ns, err)
	}
	hooks, err := d.store.ListMatchingWebhooks(ctx, namespace.ID, repoID, event)
	if err != nil {
		return fmt.Errorf("list matching webhooks: %w", err)
	}
	if len(hooks) == 0 {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", event, err)
	}
	for _, h := range hooks {
		if _, err := d.store.CreateWebhookDelivery(ctx, h.ID, event, raw); err != nil {
			return fmt.Errorf("enqueue delivery for webhook %d: %w", h.ID, err)
		}
	}
	d.nudge()
	return nil
}

func (d *Dispatcher) nudge() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run starts the delivery worker pool and blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range d.workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			d.loop(ctx, id)
		}(i)
	}
	wg.Wait()
}

func (d *Dispatcher) loop(ctx context.Context, id int) {
	// The wake channel covers the common case (an event just fired); the
	// ticker also picks up rows whose backoff just elapsed, which fire
	// nothing on their own.
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		worked, err := d.step(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("webhook delivery worker", "worker", id, "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) step(ctx context.Context) (bool, error) {
	job, err := d.store.ClaimWebhookDelivery(ctx, leaseDuration)
	if err != nil || job == nil {
		return false, err
	}
	respStatus, respBody, deliverErr := d.deliver(ctx, job)
	success := deliverErr == nil
	if !success {
		slog.Warn("webhook delivery failed", "delivery", job.DeliveryID, "webhook", job.WebhookID,
			"event", job.Event, "attempt", job.Attempts, "error", deliverErr)
	}
	err = d.store.FinishWebhookDelivery(ctx, job.DeliveryID, success, job.Attempts, MaxAttempts,
		respStatus, respBody, BackoffDuration(job.Attempts))
	if err != nil {
		return true, fmt.Errorf("finish delivery %d: %w", job.DeliveryID, err)
	}
	return true, nil
}

// deliver sends one HTTP attempt. respStatus/respBody are populated whenever
// the request reached the server at all, even on a non-2xx response, so
// delivery history always shows what the endpoint said.
func (d *Dispatcher) deliver(ctx context.Context, job *store.WebhookDeliveryJob) (*int, string, error) {
	// The claim query already filters to active webhooks; this is a
	// defensive fallback rather than a path that runs in practice.
	if !job.WebhookActive {
		return nil, "", errors.New("webhook is inactive")
	}
	if err := ValidateTargetURL(job.URL, d.allowPrivate); err != nil {
		return nil, "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, job.URL, bytes.NewReader(job.Payload))
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "thinkingface-webhooks/1.0")
	req.Header.Set("X-Thinkingface-Event", job.Event)
	req.Header.Set("X-Thinkingface-Delivery", strconv.FormatInt(job.DeliveryID, 10))
	req.Header.Set("X-Thinkingface-Signature", Sign([]byte(job.Secret), job.Payload))

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	status := resp.StatusCode
	if status < 200 || status >= 300 {
		return &status, string(body), fmt.Errorf("endpoint returned %d", status)
	}
	return &status, string(body), nil
}
