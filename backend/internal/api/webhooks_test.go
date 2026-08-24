package api

import (
	"context"
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
