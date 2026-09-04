package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// -------------------------------------------------------- validateWebhookEvents

// TestValidateWebhookEvents_AllApitypesConstants pins every event constant
// apitypes.WebhookEvent defines as accepted here. This is the regression
// test for the bug where repo.moved / repo.transfer_requested /
// repo.archived / repo.unarchived were fired by the server (repos.go,
// transfers.go) and documented in docs/dev/api-contract.md §9, but
// validWebhookEvents only listed 5 of the 9 events, so a subscription to any
// of the other 4 was rejected with "unknown event" and could never be
// created.
func TestValidateWebhookEvents_AllApitypesConstants(t *testing.T) {
	for _, e := range []apitypes.WebhookEvent{
		apitypes.WebhookEventRepoPush,
		apitypes.WebhookEventRepoCreated,
		apitypes.WebhookEventRepoDeleted,
		apitypes.WebhookEventRepoMoved,
		apitypes.WebhookEventRepoTransferRequested,
		apitypes.WebhookEventRepoArchived,
		apitypes.WebhookEventRepoUnarchived,
		apitypes.WebhookEventRepoRefDeleted,
		apitypes.WebhookEventRunFinished,
		apitypes.WebhookEventRunFailed,
	} {
		if err := validateWebhookEvents([]apitypes.WebhookEvent{e}); err != nil {
			t.Errorf("validateWebhookEvents(%q) = %v, want nil", e, err)
		}
	}
}

func TestValidateWebhookEvents_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		events []apitypes.WebhookEvent
	}{
		{"empty", nil},
		{"unknown event", []apitypes.WebhookEvent{"repo.renamed"}},
		{"duplicate event", []apitypes.WebhookEvent{
			apitypes.WebhookEventRepoArchived, apitypes.WebhookEventRepoArchived,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWebhookEvents(tt.events); err == nil {
				t.Errorf("validateWebhookEvents(%v) = nil, want an error", tt.events)
			}
		})
	}
}

// ------------------------------------------------- create + match, over HTTP

// TestCreateWebhook_NewEventsCanBeSubscribedAndMatched drives the real
// create-webhook endpoint (over HTTP, against the same fixture the transfer
// tests use) for each of the four events that validWebhookEvents was
// missing, then confirms store.ListMatchingWebhooks -- what fireWebhook
// consults to find subscribers -- actually returns the new subscription.
// Before the fix, step 1 failed with 400 "unknown event" for all four.
func TestCreateWebhook_NewEventsCanBeSubscribedAndMatched(t *testing.T) {
	events := []apitypes.WebhookEvent{
		apitypes.WebhookEventRepoMoved,
		apitypes.WebhookEventRepoTransferRequested,
		apitypes.WebhookEventRepoArchived,
		apitypes.WebhookEventRepoUnarchived,
	}
	for _, event := range events {
		t.Run(string(event), func(t *testing.T) {
			f := newTransferFixture(t)
			ctx := context.Background()
			tok := f.token(f.alice, "write")

			resp := f.do("POST", "/api/v1/namespaces/alice/webhooks", tok, map[string]any{
				"url":    "https://example.com/hook",
				"events": []string{string(event)},
			})
			if resp.status() != 200 {
				t.Fatalf("create webhook for event %q: status = %d, body = %s",
					event, resp.status(), resp.rec.Body.String())
			}
			var body apitypes.CreateWebhookResponse
			resp.json(t, &body)
			if len(body.Events) != 1 || body.Events[0] != event {
				t.Fatalf("created webhook events = %v, want [%q]", body.Events, event)
			}

			ns, err := f.st.GetNamespace(ctx, "alice")
			if err != nil {
				t.Fatalf("GetNamespace: %v", err)
			}
			matches, err := f.st.ListMatchingWebhooks(ctx, ns.ID, nil, string(event))
			if err != nil {
				t.Fatalf("ListMatchingWebhooks: %v", err)
			}
			if len(matches) != 1 || matches[0].ID != body.ID {
				t.Fatalf("ListMatchingWebhooks(%q) = %+v, want exactly the webhook just created", event, matches)
			}

			// A namespace subscribed to a different event must not match --
			// this is the same lookup fireWebhook relies on to decide who
			// gets a delivery.
			other, err := f.st.ListMatchingWebhooks(ctx, ns.ID, nil, "repo.push")
			if err != nil {
				t.Fatalf("ListMatchingWebhooks(repo.push): %v", err)
			}
			if len(other) != 0 {
				t.Fatalf("ListMatchingWebhooks(repo.push) = %+v, want no match for a webhook scoped to %q", other, event)
			}
		})
	}
}

// TestWebhookByID_TellsANonAdminNothing pins that /webhooks/{id} answers a
// caller who may not administer the owning namespace exactly as it answers
// one who named an id that does not exist -- same status, same body.
//
// Webhook ids are instance-wide serials, so anything that distinguishes the
// two lets a caller with any write-scoped credential walk the id space and
// read off which namespaces hold webhooks. A 403 does that twice over: it
// confirms the row, and names the namespace in its message. The endpoint
// used to return one, and nothing failed when it did -- there was no test
// here that looked at a status code at all.
func TestWebhookByID_TellsANonAdminNothing(t *testing.T) {
	f := newTransferFixture(t)
	aliceTok := f.token(f.alice, "write")
	bobTok := f.token(f.bob, "write")

	created := f.do("POST", "/api/v1/namespaces/alice/webhooks", aliceTok, map[string]any{
		"url":    "https://example.com/hook",
		"events": []string{string(apitypes.WebhookEventRepoPush)},
	})
	if created.status() != 200 {
		t.Fatalf("create webhook: status = %d, body = %s", created.status(), created.rec.Body.String())
	}
	var hook apitypes.CreateWebhookResponse
	created.json(t, &hook)

	// The owner reaches it, which is what makes the rest of this test mean
	// something: the id is real and the route works.
	if got := f.do("GET", fmt.Sprintf("/api/v1/webhooks/%d", hook.ID), aliceTok, nil); got.status() != 200 {
		t.Fatalf("owner GET: status = %d, body = %s", got.status(), got.rec.Body.String())
	}

	// bob holds a write token and administers his own namespace, but not
	// alice's. He must not be able to tell alice's webhook from a gap in the
	// id space.
	real := f.do("GET", fmt.Sprintf("/api/v1/webhooks/%d", hook.ID), bobTok, nil)
	missing := f.do("GET", fmt.Sprintf("/api/v1/webhooks/%d", hook.ID+9999), bobTok, nil)
	if real.status() != 404 {
		t.Fatalf("non-admin GET of an existing webhook: status = %d, want 404 (body %s)",
			real.status(), real.rec.Body.String())
	}
	if missing.status() != 404 {
		t.Fatalf("GET of a missing webhook: status = %d, want 404 (body %s)",
			missing.status(), missing.rec.Body.String())
	}
	if real.rec.Body.String() != missing.rec.Body.String() {
		t.Fatalf("the two 404s differ, so the id is still distinguishable:\n existing: %s\n  missing: %s",
			real.rec.Body.String(), missing.rec.Body.String())
	}

	// The same for the write paths, which is where a leak would also mutate.
	if got := f.do("DELETE", fmt.Sprintf("/api/v1/webhooks/%d", hook.ID), bobTok, nil); got.status() != 404 {
		t.Fatalf("non-admin DELETE: status = %d, want 404 (body %s)", got.status(), got.rec.Body.String())
	}
	if got := f.do("GET", fmt.Sprintf("/api/v1/webhooks/%d", hook.ID), aliceTok, nil); got.status() != 200 {
		t.Fatalf("webhook gone after a refused DELETE: status = %d", got.status())
	}
}

// TestWebhookRedeliver_TellsAForeignDeliveryNothing pins that redeliver
// answers a deliveryId belonging to someone else's webhook exactly as it
// answers one that does not exist -- same status, same body.
//
// Delivery ids are instance-wide serials, so a distinguishable 404 lets a
// webhook admin on any namespace walk the global delivery-id space.
func TestWebhookRedeliver_TellsAForeignDeliveryNothing(t *testing.T) {
	f := newTransferFixture(t)
	aliceTok := f.token(f.alice, "write")
	bobTok := f.token(f.bob, "write")

	aliceCreated := f.do("POST", "/api/v1/namespaces/alice/webhooks", aliceTok, map[string]any{
		"url":    "https://example.com/alice",
		"events": []string{string(apitypes.WebhookEventRepoPush)},
	})
	if aliceCreated.status() != 200 {
		t.Fatalf("create alice webhook: status = %d, body = %s", aliceCreated.status(), aliceCreated.rec.Body.String())
	}
	var aliceHook apitypes.CreateWebhookResponse
	aliceCreated.json(t, &aliceHook)

	bobCreated := f.do("POST", "/api/v1/namespaces/bob/webhooks", bobTok, map[string]any{
		"url":    "https://example.com/bob",
		"events": []string{string(apitypes.WebhookEventRepoPush)},
	})
	if bobCreated.status() != 200 {
		t.Fatalf("create bob webhook: status = %d, body = %s", bobCreated.status(), bobCreated.rec.Body.String())
	}
	var bobHook apitypes.CreateWebhookResponse
	bobCreated.json(t, &bobHook)

	deliveryID, err := f.st.CreateWebhookDelivery(context.Background(), aliceHook.ID, "repo.push", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("CreateWebhookDelivery: %v", err)
	}

	real := f.do("POST", fmt.Sprintf("/api/v1/webhooks/%d/deliveries/%d/redeliver", bobHook.ID, deliveryID), bobTok, nil)
	missing := f.do("POST", fmt.Sprintf("/api/v1/webhooks/%d/deliveries/%d/redeliver", bobHook.ID, deliveryID+9999), bobTok, nil)
	if real.status() != 404 {
		t.Fatalf("redeliver of a foreign delivery: status = %d, want 404 (body %s)",
			real.status(), real.rec.Body.String())
	}
	if missing.status() != 404 {
		t.Fatalf("redeliver of a missing delivery: status = %d, want 404 (body %s)",
			missing.status(), missing.rec.Body.String())
	}
	if real.rec.Body.String() != missing.rec.Body.String() {
		t.Fatalf("the two 404s differ, so the delivery id is still distinguishable:\n existing: %s\n  missing: %s",
			real.rec.Body.String(), missing.rec.Body.String())
	}

	listed := f.do("GET", fmt.Sprintf("/api/v1/webhooks/%d/deliveries", aliceHook.ID), aliceTok, nil)
	if listed.status() != 200 {
		t.Fatalf("alice list deliveries: status = %d, body = %s", listed.status(), listed.rec.Body.String())
	}
	var page apitypes.WebhookDeliveryListResponse
	listed.json(t, &page)
	if page.Total != 1 {
		t.Fatalf("alice deliveries total = %d, want 1 (a refused redeliver must not clone the row)", page.Total)
	}
}
