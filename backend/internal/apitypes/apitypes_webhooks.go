package apitypes

import "time"

// -------------------------------------------------------------- webhooks

// WebhookEvent names one kind of event a webhook may subscribe to.
type WebhookEvent string

const (
	WebhookEventRepoPush    WebhookEvent = "repo.push"
	WebhookEventRepoCreated WebhookEvent = "repo.created"
	WebhookEventRepoDeleted WebhookEvent = "repo.deleted"
	// WebhookEventRepoMoved fires after a repository was transferred or
	// renamed; delivered to the *new* namespace's subscriptions.
	WebhookEventRepoMoved WebhookEvent = "repo.moved"
	// WebhookEventRepoTransferRequested fires when a transfer needs the
	// destination namespace's approval; delivered to that namespace.
	WebhookEventRepoTransferRequested WebhookEvent = "repo.transfer_requested"
	// WebhookEventRepoArchived / RepoUnarchived fire when a repository is
	// frozen read-only and when it is thawed again. Mirroring systems care:
	// an archived repository will not change until it is unarchived.
	WebhookEventRepoArchived   WebhookEvent = "repo.archived"
	WebhookEventRepoUnarchived WebhookEvent = "repo.unarchived"
	// WebhookEventRepoRefDeleted fires when a branch or tag is removed,
	// whether by `git push --delete` or through the API. Creation and
	// update already arrive as repo.push; without this one a mirroring
	// subscriber saw refs appear and never saw them go away.
	WebhookEventRepoRefDeleted WebhookEvent = "repo.ref_deleted"
	WebhookEventRunFinished    WebhookEvent = "run.finished"
	WebhookEventRunFailed      WebhookEvent = "run.failed"
)

// WebhookDeliveryStatus is the lifecycle state of one delivery attempt.
type WebhookDeliveryStatus string

const (
	WebhookDeliveryPending WebhookDeliveryStatus = "pending"
	WebhookDeliverySuccess WebhookDeliveryStatus = "success"
	WebhookDeliveryFailed  WebhookDeliveryStatus = "failed"
)

// Webhook is one subscription as the web UI sees it. Secret is included only
// in the response to a create call (see CreateWebhookResponse); every other
// response omits it.
type Webhook struct {
	ID        int64  `json:"id"`
	Namespace string `json:"namespace"`
	// RepoFullName is "" for a namespace-wide subscription, "ns/name"
	// when scoped to one repository.
	RepoFullName string         `json:"repo_full_name"`
	URL          string         `json:"url"`
	Events       []WebhookEvent `json:"events"`
	Active       bool           `json:"active"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// WebhookListResponse is the body of GET .../webhooks.
type WebhookListResponse struct {
	Items []Webhook `json:"items"`
}

// WebhookResponse wraps a single webhook.
type WebhookResponse struct {
	Webhook Webhook `json:"webhook"`
}

// CreateWebhookRequest is the body of POST .../webhooks.
type CreateWebhookRequest struct {
	// Repo is "" for a namespace-wide webhook, or "kind/name" (e.g.
	// "dataset/my-metrics") to scope it to one repository in the namespace.
	Repo   string         `json:"repo,omitempty"`
	URL    string         `json:"url"`
	Events []WebhookEvent `json:"events"`
	// Active defaults to true when omitted; the field exists so a webhook can
	// be created disabled.
	Active *bool `json:"active,omitempty"`
}

// CreateWebhookResponse returns the freshly minted webhook including its
// secret, which is never shown again — a client that loses it must rotate it
// via UpdateWebhookRequest.
type CreateWebhookResponse struct {
	Webhook `tstype:",extends"`
	Secret  string `json:"secret"`
}

// UpdateWebhookRequest patches a webhook. Nil/omitted fields are left
// unchanged; Events, when present, replaces the whole set. Setting
// RotateSecret regenerates the secret, returned once in the response.
type UpdateWebhookRequest struct {
	URL          *string        `json:"url,omitempty"`
	Events       []WebhookEvent `json:"events,omitempty"`
	Active       *bool          `json:"active,omitempty"`
	RotateSecret bool           `json:"rotate_secret,omitempty"`
}

// UpdateWebhookResponse carries the new secret only when the update rotated
// it.
type UpdateWebhookResponse struct {
	Webhook `tstype:",extends"`
	// Secret is set only when the request rotated it.
	Secret string `json:"secret,omitempty"`
}

// WebhookDelivery is one delivery attempt's history row.
type WebhookDelivery struct {
	ID             int64                 `json:"id"`
	Event          WebhookEvent          `json:"event"`
	Payload        map[string]any        `json:"payload"`
	Status         WebhookDeliveryStatus `json:"status"`
	Attempts       int                   `json:"attempts"`
	LastAttemptAt  *time.Time            `json:"last_attempt_at" tstype:"string | null,required"`
	ResponseStatus *int                  `json:"response_status" tstype:"number | null,required"`
	ResponseBody   string                `json:"response_body"`
	CreatedAt      time.Time             `json:"created_at"`
}

// WebhookDeliveryListResponse is one page of a webhook's delivery history.
type WebhookDeliveryListResponse struct {
	Items []WebhookDelivery `json:"items"`
	Total int64             `json:"total"`
}
