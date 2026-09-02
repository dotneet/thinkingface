// Webhook subscriptions: CRUD for the settings UI, plus delivery history and
// manual redelivery. Firing events (Fire) lives in internal/webhooks and is
// wired into the syncer, repository CRUD, and experiment ingest instead of
// here.

package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/store"
	"github.com/dotneet/thinkingface/backend/internal/webhooks"
)

// fireWebhook records webhook deliveries for event, best-effort: a failure
// here (or a Server built without a Dispatcher, as in tests) must never fail
// the request that triggered it.
func (s *Server) fireWebhook(ctx context.Context, event, ns string, repoID *int64, payload any) {
	if s.webhooks == nil {
		return
	}
	if err := s.webhooks.Fire(ctx, event, ns, repoID, payload); err != nil {
		slog.Error("fire webhook", "event", event, "namespace", ns, "error", err)
	}
}

// validWebhookEvents is the closed set apitypes.WebhookEvent allows. Kept as
// a lookup so create/update can reject a typo instead of silently storing an
// event nothing will ever fire.
var validWebhookEvents = map[apitypes.WebhookEvent]bool{
	apitypes.WebhookEventRepoPush:              true,
	apitypes.WebhookEventRepoCreated:           true,
	apitypes.WebhookEventRepoDeleted:           true,
	apitypes.WebhookEventRepoMoved:             true,
	apitypes.WebhookEventRepoTransferRequested: true,
	apitypes.WebhookEventRepoArchived:          true,
	apitypes.WebhookEventRepoUnarchived:        true,
	apitypes.WebhookEventRepoRefDeleted:        true,
	apitypes.WebhookEventRunFinished:           true,
	apitypes.WebhookEventRunFailed:             true,
}

func generateWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func validateWebhookEvents(events []apitypes.WebhookEvent) error {
	if len(events) == 0 {
		return errors.New("events must not be empty")
	}
	seen := map[apitypes.WebhookEvent]bool{}
	for _, e := range events {
		if !validWebhookEvents[e] {
			return fmt.Errorf("unknown event %q", e)
		}
		if seen[e] {
			return fmt.Errorf("event %q listed more than once", e)
		}
		seen[e] = true
	}
	return nil
}

func toWebhookAPI(w *store.Webhook) apitypes.Webhook {
	events := make([]apitypes.WebhookEvent, 0, len(w.Events))
	for _, e := range w.Events {
		events = append(events, apitypes.WebhookEvent(e))
	}
	return apitypes.Webhook{
		ID: w.ID, Namespace: w.Namespace, RepoFullName: w.RepoFullName(),
		URL: w.URL, Events: events, Active: w.Active,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func toWebhookDeliveryAPI(d *store.WebhookDelivery) apitypes.WebhookDelivery {
	payload := map[string]any{}
	_ = json.Unmarshal(d.Payload, &payload)
	return apitypes.WebhookDelivery{
		ID: d.ID, Event: apitypes.WebhookEvent(d.Event), Payload: payload,
		Status: apitypes.WebhookDeliveryStatus(d.Status), Attempts: d.Attempts,
		LastAttemptAt: d.LastAttemptAt, ResponseStatus: d.ResponseStatus,
		ResponseBody: d.ResponseBody, CreatedAt: d.CreatedAt,
	}
}

// requireNamespaceAdmin resolves the namespace named in the URL and checks
// the caller may administer webhooks on it. Under an organisation that means
// admin, not write: a webhook carries the namespace's secrets to an external
// URL, which is an administrative act rather than a content change
// (docs/dev/organization-design.md §4). A personal namespace's owner is its
// admin, so nothing changes there.
func (s *Server) requireNamespaceAdmin(w http.ResponseWriter, r *http.Request, ns string) (*store.User, bool) {
	user, ok := s.requireWrite(w, r)
	if !ok {
		return nil, false
	}
	role, err := s.roleIn(r.Context(), user, ns)
	if err != nil {
		internalError(w, "check namespace access", err)
		return nil, false
	}
	if role < RoleAdmin {
		forbidden(w, "you do not have admin access to namespace "+ns)
		return nil, false
	}
	return user, true
}

// webhookNotFound is the single answer for "no such webhook" and "not yours",
// and they must stay indistinguishable. Webhook ids are small sequential
// integers in the URL, so an answer that differed between the two would let
// anyone walk 1..N and read back the list of every webhook on the instance
// together with the namespace that owns it -- the 403's own message named it
// ("you do not have admin access to namespace alice"). handleDecideTransfer
// avoids the same trap deliberately (transfers.go).
func webhookNotFound(w http.ResponseWriter) {
	notFound(w, "webhook not found")
}

// loadWebhookForAdmin loads the webhook named in the URL and checks the
// caller may administer it (same bar as its owning namespace).
//
// The authorisation failure is folded into a 404 rather than reported as a
// 403: see webhookNotFound. That is why this cannot simply call
// requireNamespaceAdmin, which answers 403 with the namespace's name in it.
func (s *Server) loadWebhookForAdmin(w http.ResponseWriter, r *http.Request) (*store.Webhook, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		badRequest(w, "webhook id must be a number")
		return nil, false
	}
	// Authentication still answers for itself: an anonymous or read-scoped
	// caller gets 401/403 without any webhook being looked up, which says
	// nothing about which ids exist.
	user, ok := s.requireWrite(w, r)
	if !ok {
		return nil, false
	}
	hook, err := s.store.GetWebhook(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webhookNotFound(w)
			return nil, false
		}
		handleStoreError(w, "load webhook", err)
		return nil, false
	}
	role, err := s.roleIn(r.Context(), user, hook.Namespace)
	if err != nil {
		internalError(w, "check namespace access", err)
		return nil, false
	}
	if role < RoleAdmin {
		webhookNotFound(w)
		return nil, false
	}
	return hook, true
}

// resolveWebhookRepoScope turns the "kind/name" scope string from
// CreateWebhookRequest into a repository id inside ns, or nil for a
// namespace-wide webhook. Scoping to a repository outside ns, or one that
// does not exist, is rejected rather than silently creating an
// unreachable subscription.
func (s *Server) resolveWebhookRepoScope(w http.ResponseWriter, r *http.Request, ns, scope string) (*int64, bool) {
	if scope == "" {
		return nil, true
	}
	kind, name, ok := strings.Cut(scope, "/")
	if !ok || kind == "" || name == "" {
		badRequest(w, `repo must be "kind/name", e.g. "dataset/my-metrics"`)
		return nil, false
	}
	if kind != "dataset" && kind != "model" {
		badRequest(w, "repo kind must be dataset or model")
		return nil, false
	}
	repo, err := s.store.GetRepo(r.Context(), kind, ns, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			badRequest(w, "repository "+ns+"/"+name+" does not exist")
			return nil, false
		}
		internalError(w, "load repository", err)
		return nil, false
	}
	return &repo.ID, true
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	if _, ok := s.requireNamespaceAdmin(w, r, ns); !ok {
		return
	}
	namespace, err := s.store.GetNamespace(r.Context(), ns)
	if err != nil {
		handleStoreError(w, "load namespace", err)
		return
	}
	rows, err := s.store.ListWebhooksForNamespace(r.Context(), namespace.ID)
	if err != nil {
		internalError(w, "list webhooks", err)
		return
	}
	items := make([]apitypes.Webhook, 0, len(rows))
	for i := range rows {
		items = append(items, toWebhookAPI(&rows[i]))
	}
	writeJSON(w, http.StatusOK, apitypes.WebhookListResponse{Items: items})
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	ns := chi.URLParam(r, "ns")
	user, ok := s.requireNamespaceAdmin(w, r, ns)
	if !ok {
		return
	}
	namespace, err := s.store.GetNamespace(r.Context(), ns)
	if err != nil {
		handleStoreError(w, "load namespace", err)
		return
	}
	var req apitypes.CreateWebhookRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON with url and events") {
		return
	}
	if err := webhooks.ValidateTargetURL(req.URL, s.cfg.AllowPrivateWebhookTargets); err != nil {
		badRequest(w, err.Error())
		return
	}
	if err := validateWebhookEvents(req.Events); err != nil {
		badRequest(w, err.Error())
		return
	}
	repoID, ok := s.resolveWebhookRepoScope(w, r, ns, req.Repo)
	if !ok {
		return
	}
	secret, err := generateWebhookSecret()
	if err != nil {
		internalError(w, "generate webhook secret", err)
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	hook, err := s.store.CreateWebhook(r.Context(), namespace.ID, repoID, req.URL, secret,
		eventStrings(req.Events), active)
	if err != nil {
		internalError(w, "create webhook", err)
		return
	}
	if namespace.Kind == "org" {
		s.audit(r.Context(), namespace.ID, user, auditWebhookCreated, hook.URL,
			map[string]any{"webhook_id": hook.ID})
	}
	writeJSON(w, http.StatusOK, apitypes.CreateWebhookResponse{Webhook: toWebhookAPI(hook), Secret: secret})
}

func (s *Server) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	hook, ok := s.loadWebhookForAdmin(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, apitypes.WebhookResponse{Webhook: toWebhookAPI(hook)})
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	hook, ok := s.loadWebhookForAdmin(w, r)
	if !ok {
		return
	}
	actor := currentUser(r.Context())
	var req apitypes.UpdateWebhookRequest
	if !decodeJSON(w, r, maxMetaBody, &req, "request body must be JSON") {
		return
	}
	update := store.WebhookUpdate{Active: req.Active}
	if req.URL != nil {
		if err := webhooks.ValidateTargetURL(*req.URL, s.cfg.AllowPrivateWebhookTargets); err != nil {
			badRequest(w, err.Error())
			return
		}
		update.URL = req.URL
	}
	if req.Events != nil {
		if err := validateWebhookEvents(req.Events); err != nil {
			badRequest(w, err.Error())
			return
		}
		update.Events = eventStrings(req.Events)
	}
	var newSecret string
	if req.RotateSecret {
		secret, err := generateWebhookSecret()
		if err != nil {
			internalError(w, "generate webhook secret", err)
			return
		}
		newSecret = secret
		update.Secret = &secret
	}
	updated, err := s.store.UpdateWebhook(r.Context(), hook.ID, update)
	if err != nil {
		handleStoreError(w, "update webhook", err)
		return
	}
	s.auditNamespace(r.Context(), updated.Namespace, actor, auditWebhookUpdated, updated.URL,
		map[string]any{"webhook_id": updated.ID})
	writeJSON(w, http.StatusOK, apitypes.UpdateWebhookResponse{Webhook: toWebhookAPI(updated), Secret: newSecret})
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	hook, ok := s.loadWebhookForAdmin(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteWebhook(r.Context(), hook.ID); err != nil {
		handleStoreError(w, "delete webhook", err)
		return
	}
	s.auditNamespace(r.Context(), hook.Namespace, currentUser(r.Context()), auditWebhookDeleted, hook.URL,
		map[string]any{"webhook_id": hook.ID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	hook, ok := s.loadWebhookForAdmin(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, total, err := s.store.ListWebhookDeliveries(r.Context(), hook.ID, limit, offset)
	if err != nil {
		internalError(w, "list webhook deliveries", err)
		return
	}
	items := make([]apitypes.WebhookDelivery, 0, len(rows))
	for i := range rows {
		items = append(items, toWebhookDeliveryAPI(&rows[i]))
	}
	writeJSON(w, http.StatusOK, apitypes.WebhookDeliveryListResponse{Items: items, Total: total})
}

func (s *Server) handleRedeliverWebhook(w http.ResponseWriter, r *http.Request) {
	hook, ok := s.loadWebhookForAdmin(w, r)
	if !ok {
		return
	}
	deliveryID, err := strconv.ParseInt(chi.URLParam(r, "deliveryId"), 10, 64)
	if err != nil {
		badRequest(w, "delivery id must be a number")
		return
	}
	existing, err := s.store.GetWebhookDelivery(r.Context(), deliveryID)
	if err != nil {
		handleStoreError(w, "load delivery", err)
		return
	}
	if existing.WebhookID != hook.ID {
		notFound(w, "delivery not found")
		return
	}
	newID, err := s.store.RedeliverWebhookDelivery(r.Context(), deliveryID)
	if err != nil {
		internalError(w, "redeliver webhook", err)
		return
	}
	redelivered, err := s.store.GetWebhookDelivery(r.Context(), newID)
	if err != nil {
		internalError(w, "load redelivered webhook", err)
		return
	}
	writeJSON(w, http.StatusOK, toWebhookDeliveryAPI(redelivered))
}

func eventStrings(events []apitypes.WebhookEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = string(e)
	}
	return out
}
