// The current-password check behind PATCH /api/v1/me/password. What matters
// about it is not any single answer but that it has one for every outcome:
// the switch it replaced omitted passwordPending, and an omitted case there
// does not fail -- it falls through into the password change.

package api

import (
	"net/http/httptest"
	"testing"
)

func TestRefusePasswordChange_NoOutcomeFallsThrough(t *testing.T) {
	f := newTransferFixture(t)
	req := httptest.NewRequest("PATCH", "/api/v1/me/password", nil)

	// Every outcome the check can produce, plus one it cannot: a value added
	// to passwordOutcome later and not wired in here must be refused rather
	// than read as consent.
	refusals := []struct {
		name    string
		outcome passwordOutcome
	}{
		{"wrong", passwordWrong},
		{"throttled", passwordThrottled},
		{"overloaded", passwordOverloaded},
		{"disabled", passwordDisabled},
		{"pending", passwordPending},
		{"unknown", passwordOutcome(1 << 20)},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if !f.s.refusePasswordChange(rec, req, "addr", f.alice, tc.outcome) {
				t.Fatalf("outcome %s was not refused; it falls through into the password change", tc.name)
			}
			if rec.Code < 400 {
				t.Fatalf("outcome %s answered %d, want a refusal", tc.name, rec.Code)
			}
		})
	}

	// And the one outcome that is consent writes nothing, leaving the handler
	// to carry on.
	rec := httptest.NewRecorder()
	if f.s.refusePasswordChange(rec, req, "addr", f.alice, passwordOK) {
		t.Fatalf("passwordOK was refused (status %d)", rec.Code)
	}
}

// passwordPending's own answer, spelled out: it is the same 403
// account_pending handleLogin gives, not the account_disabled next to it and
// not a 500. Unreachable through the router today -- identify() refuses an
// unapproved account before any handler runs -- so this is the only place the
// mapping is pinned down.
func TestRefusePasswordChange_PendingAnswersAccountPending(t *testing.T) {
	f := newTransferFixture(t)
	req := httptest.NewRequest("PATCH", "/api/v1/me/password", nil)

	rec := httptest.NewRecorder()
	f.s.refusePasswordChange(rec, req, "addr", f.alice, passwordPending)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := recErrorType(t, rec); got != "account_pending" {
		t.Fatalf("error type = %q, want account_pending", got)
	}
}
