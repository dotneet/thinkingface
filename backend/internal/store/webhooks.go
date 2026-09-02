package store

import (
	"context"
	"time"
)

// Webhook is a subscription to repository/experiment events, scoped to a
// namespace and optionally narrowed to one repository inside it.
type Webhook struct {
	ID          int64
	NamespaceID int64
	Namespace   string
	// RepoID is nil for a namespace-wide subscription.
	RepoID    *int64
	RepoName  string // "" unless RepoID is set
	RepoKind  string // "" unless RepoID is set
	URL       string
	Secret    string
	Events    []string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RepoFullName is "" for a namespace-wide webhook.
func (w *Webhook) RepoFullName() string {
	if w.RepoID == nil {
		return ""
	}
	return w.Namespace + "/" + w.RepoName
}

// WebhookDelivery is one attempted (or pending) delivery of an event to a
// webhook's URL.
type WebhookDelivery struct {
	ID             int64
	WebhookID      int64
	Event          string
	Payload        []byte // raw JSON
	Status         string // pending | success | failed
	Attempts       int
	LastAttemptAt  *time.Time
	ResponseStatus *int
	ResponseBody   string
	CreatedAt      time.Time
}

// WebhookDeliveryJob is a claimed delivery joined with the webhook it targets,
// which is everything the delivery worker needs to make the HTTP call.
type WebhookDeliveryJob struct {
	DeliveryID    int64
	WebhookID     int64
	Event         string
	Payload       []byte
	Attempts      int
	URL           string
	Secret        string
	WebhookActive bool
}

const webhookColumns = `w.id, w.namespace_id, n.name, w.repo_id,
	COALESCE(r.name, ''), COALESCE(r.kind, ''), w.url, w.secret, w.events, w.active,
	w.created_at, w.updated_at`

func (s *Store) scanWebhook(row rowScanner) (*Webhook, error) {
	w := &Webhook{}
	err := row.Scan(&w.ID, &w.NamespaceID, &w.Namespace, &w.RepoID,
		&w.RepoName, &w.RepoKind, &w.URL, &w.Secret, s.d.stringArrayDest(&w.Events), &w.Active,
		&w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, norm(err)
	}
	return w, nil
}

const webhookFromClause = `webhooks w
	JOIN namespaces n ON n.id = w.namespace_id
	LEFT JOIN repositories r ON r.id = w.repo_id`

// CreateWebhook records a new subscription. events must already be validated
// against the set the API contract allows.
func (s *Store) CreateWebhook(ctx context.Context, namespaceID int64, repoID *int64, url, secret string, events []string, active bool) (*Webhook, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO webhooks (namespace_id, repo_id, url, secret, events, active)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		namespaceID, repoID, url, secret, s.d.stringArrayArg(events), active).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.GetWebhook(ctx, id)
}

// GetWebhook loads one webhook by id.
func (s *Store) GetWebhook(ctx context.Context, id int64) (*Webhook, error) {
	return s.scanWebhook(s.db.QueryRow(ctx,
		`SELECT `+webhookColumns+` FROM `+webhookFromClause+` WHERE w.id = $1`, id))
}

// ListWebhooksForNamespace lists every webhook visible from a namespace's
// settings page: namespace-wide subscriptions plus every repo-scoped one for
// a repository inside it.
func (s *Store) ListWebhooksForNamespace(ctx context.Context, namespaceID int64) ([]Webhook, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+webhookColumns+` FROM `+webhookFromClause+`
		 WHERE w.namespace_id = $1 ORDER BY w.created_at DESC`, namespaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Webhook{}
	for rows.Next() {
		w, err := s.scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// ListMatchingWebhooks finds the active webhooks that should fire for event
// on repoID (nil for a namespace-level event such as repo.created).
func (s *Store) ListMatchingWebhooks(ctx context.Context, namespaceID int64, repoID *int64, event string) ([]Webhook, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+webhookColumns+` FROM `+webhookFromClause+`
		 WHERE w.namespace_id = $1 AND (w.repo_id IS NULL OR w.repo_id = $2)
		   AND w.active AND `+s.d.arrayHas("w.events", "$3"),
		namespaceID, repoID, event)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Webhook{}
	for rows.Next() {
		w, err := s.scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// WebhookUpdate carries the fields an update may change. A nil pointer (or
// nil slice) leaves the corresponding column untouched.
type WebhookUpdate struct {
	URL    *string
	Secret *string
	Events []string
	Active *bool
}

// UpdateWebhook applies a partial update and returns the refreshed row.
func (s *Store) UpdateWebhook(ctx context.Context, id int64, u WebhookUpdate) (*Webhook, error) {
	_, err := s.db.Exec(ctx,
		`UPDATE webhooks SET
		   url = COALESCE($2, url),
		   secret = COALESCE($3, secret),
		   events = COALESCE($4, events),
		   active = COALESCE($5, active),
		   updated_at = now()
		 WHERE id = $1`,
		id, u.URL, u.Secret, s.d.stringArrayArg(u.Events), u.Active)
	if err != nil {
		return nil, err
	}
	return s.GetWebhook(ctx, id)
}

func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	n, err := s.db.Exec(ctx, `DELETE FROM webhooks WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// -------------------------------------------------------------- deliveries

// CreateWebhookDelivery enqueues one delivery attempt, immediately claimable.
func (s *Store) CreateWebhookDelivery(ctx context.Context, webhookID int64, event string, payload []byte) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx,
		`INSERT INTO webhook_deliveries (webhook_id, event, payload) VALUES ($1, $2, $3) RETURNING id`,
		webhookID, event, payload).Scan(&id)
	return id, err
}

// RedeliverWebhookDelivery clones an existing delivery's event and payload
// into a fresh pending row, leaving delivery history intact.
func (s *Store) RedeliverWebhookDelivery(ctx context.Context, deliveryID int64) (int64, error) {
	d, err := s.GetWebhookDelivery(ctx, deliveryID)
	if err != nil {
		return 0, err
	}
	return s.CreateWebhookDelivery(ctx, d.WebhookID, d.Event, d.Payload)
}

func scanDelivery(row rowScanner) (*WebhookDelivery, error) {
	d := &WebhookDelivery{}
	err := row.Scan(&d.ID, &d.WebhookID, &d.Event, &d.Payload, &d.Status, &d.Attempts,
		&d.LastAttemptAt, &d.ResponseStatus, &d.ResponseBody, &d.CreatedAt)
	if err != nil {
		return nil, norm(err)
	}
	return d, nil
}

// The delivery history page size, as docs/dev/api-contract.md documents it
// ("limit (default 30, max 100)").
const (
	defaultDeliveryPageSize = 30
	maxDeliveryPageSize     = 100
)

const deliveryColumns = `id, webhook_id, event, payload, status, attempts,
	last_attempt_at, response_status, response_body, created_at`

func (s *Store) GetWebhookDelivery(ctx context.Context, id int64) (*WebhookDelivery, error) {
	return scanDelivery(s.db.QueryRow(ctx,
		`SELECT `+deliveryColumns+` FROM webhook_deliveries WHERE id = $1`, id))
}

// ListWebhookDeliveries returns one page of delivery history, newest first.
func (s *Store) ListWebhookDeliveries(ctx context.Context, webhookID int64, limit, offset int) ([]WebhookDelivery, int64, error) {
	limit, offset = pageWindow(limit, offset, defaultDeliveryPageSize, maxDeliveryPageSize)
	var total int64
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE webhook_id = $1`, webhookID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+deliveryColumns+` FROM webhook_deliveries WHERE webhook_id = $1
		 ORDER BY id DESC LIMIT $2 OFFSET $3`, webhookID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []WebhookDelivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	return out, total, rows.Err()
}

// ClaimWebhookDelivery atomically takes the next pending, due delivery and
// joins it with the webhook it targets. The claim itself pushes
// next_attempt_at forward by leaseDuration: if this process crashes before
// finishing the delivery, the row becomes claimable again once the lease
// expires, without ever needing a distinct "running" status. It returns nil,
// nil when nothing is due.
//
// The claim and the webhook lookup are two statements in one transaction
// rather than a writable CTE, which SQLite does not have; on Postgres SKIP
// LOCKED still lets several workers claim distinct rows concurrently.
func (s *Store) ClaimWebhookDelivery(ctx context.Context, leaseDuration time.Duration) (*WebhookDeliveryJob, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	j := &WebhookDeliveryJob{}
	err = tx.QueryRow(ctx,
		`UPDATE webhook_deliveries
		 SET attempts = attempts + 1, last_attempt_at = now(),
		     next_attempt_at = `+s.d.nowPlusSeconds("$1")+`
		 WHERE id = (
		   -- Deliveries for a webhook that is currently inactive stay
		   -- pending untouched (no attempts burned) until it is
		   -- reactivated, rather than being retried into "failed".
		   SELECT wd.id FROM webhook_deliveries wd
		   JOIN webhooks w ON w.id = wd.webhook_id
		   WHERE wd.status = 'pending' AND wd.next_attempt_at <= now() AND w.active
		   ORDER BY wd.id`+s.d.forUpdate(" SKIP LOCKED")+` LIMIT 1
		 )
		 RETURNING id, webhook_id, event, payload, attempts`,
		leaseDuration.Seconds(),
	).Scan(&j.DeliveryID, &j.WebhookID, &j.Event, &j.Payload, &j.Attempts)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := tx.QueryRow(ctx,
		`SELECT url, secret, active FROM webhooks WHERE id = $1`, j.WebhookID,
	).Scan(&j.URL, &j.Secret, &j.WebhookActive); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return j, nil
}

// FinishWebhookDelivery records the outcome of a claimed delivery. A failure
// under maxAttempts is left "pending" with next_attempt_at pushed out by
// backoff, so ClaimWebhookDelivery will retry it later; at or past
// maxAttempts it is parked as "failed".
//
// attempts is the count ClaimWebhookDelivery returned, and it is the fencing
// token, exactly as it is in FinishSyncJob (jobs.go): every statement here
// also requires attempts = that value, so a worker can only write the outcome
// of the claim it still holds. The lease alone closes half the race. A worker
// whose lease lapsed while its HTTP call hung would otherwise write 'success'
// -- or park as 'failed' -- over a delivery a second worker has since
// reclaimed and is still making, either burying an attempt the reclaimer is
// about to report or resurrecting a row it already finished. There is no
// 'running' status to test as well: the claim marks the row by incrementing
// attempts, so the counter is the whole token here.
//
// A no-op update therefore means the claim was lost, which is not an error:
// whoever holds it now is responsible for the outcome.
func (s *Store) FinishWebhookDelivery(ctx context.Context, deliveryID int64, success bool, attempts, maxAttempts int, respStatus *int, respBody string, backoff time.Duration) error {
	if success {
		_, err := s.db.Exec(ctx,
			`UPDATE webhook_deliveries
			 SET status = 'success', response_status = $3, response_body = $4
			 WHERE id = $1 AND attempts = $2`, deliveryID, attempts, respStatus, respBody)
		return err
	}
	if attempts >= maxAttempts {
		_, err := s.db.Exec(ctx,
			`UPDATE webhook_deliveries
			 SET status = 'failed', response_status = $3, response_body = $4
			 WHERE id = $1 AND attempts = $2`, deliveryID, attempts, respStatus, respBody)
		return err
	}
	_, err := s.db.Exec(ctx,
		`UPDATE webhook_deliveries
		 SET status = 'pending', response_status = $3, response_body = $4,
		     next_attempt_at = `+s.d.nowPlusSeconds("$5")+`
		 WHERE id = $1 AND attempts = $2`, deliveryID, attempts, respStatus, respBody, backoff.Seconds())
	return err
}
