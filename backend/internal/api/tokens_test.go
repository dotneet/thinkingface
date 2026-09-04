package api

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
	"github.com/dotneet/thinkingface/backend/internal/auth"
)

// Token management (docs/dev/api-contract.md "Token management"), driven over
// real HTTP against the same fixture the transfer and namespace tests use.

func TestCreateToken_ExpiresInDays(t *testing.T) {
	f := newTransferFixture(t)
	write := f.token(f.alice, "write")

	before := time.Now()

	cases := []struct {
		name          string
		expiresInDays any // nil to omit the field entirely
		wantNilExpiry bool
	}{
		{"omitted", nil, true},
		{"explicit zero", 0, true},
		{"7 days", 7, false},
		{"at the cap (365)", 365, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"name": "tok-" + tc.name, "scope": "read"}
			if tc.expiresInDays != nil {
				body["expires_in_days"] = tc.expiresInDays
			}
			resp := f.do("POST", "/api/v1/tokens", write, body)
			if resp.status() != 200 {
				t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
			}
			var created apitypes.CreateTokenResponse
			resp.json(t, &created)
			if tc.wantNilExpiry {
				if created.ExpiresAt != nil {
					t.Fatalf("expires_at = %v, want nil", created.ExpiresAt)
				}
				return
			}
			if created.ExpiresAt == nil {
				t.Fatalf("expires_at = nil, want a timestamp")
			}
			days := tc.expiresInDays.(int)
			want := before.AddDate(0, 0, days)
			if d := created.ExpiresAt.Sub(want); d < -time.Minute || d > time.Minute {
				t.Fatalf("expires_at = %v, want close to %v (now + %d days)", created.ExpiresAt, want, days)
			}
		})
	}
}

// Negative expires_in_days and anything past the 365-day cap are rejected
// before a token is minted.
func TestCreateToken_ExpiresInDays_Rejected(t *testing.T) {
	f := newTransferFixture(t)
	write := f.token(f.alice, "write")

	for _, days := range []int{-1, 366} {
		resp := f.do("POST", "/api/v1/tokens", write, map[string]any{
			"name": "tok", "scope": "read", "expires_in_days": days,
		})
		if resp.status() != 400 {
			t.Fatalf("expires_in_days=%d status = %d, want 400 (body %s)", days, resp.status(), resp.rec.Body.String())
		}
	}

	// Nothing was created by the rejected requests.
	list := f.do("GET", "/api/v1/tokens", write, nil)
	var body apitypes.TokenListResponse
	list.json(t, &body)
	// The one bootstrapping "write" token from f.token is the only one that
	// should exist.
	if len(body.Items) != 1 {
		t.Fatalf("tokens after rejected creates = %+v, want 1 (the fixture's own token)", body.Items)
	}
}

// An unknown scope is refused rather than silently downgraded to read. The
// downgrade used to mint a read-only token with a 200, so a typo surfaced
// only at the first write -- far from the request that caused it, and with
// nothing pointing back at it.
func TestCreateToken_UnknownScopeIsRejected(t *testing.T) {
	f := newTransferFixture(t)
	write := f.token(f.alice, "write")

	for _, scope := range []string{"admin", "wriet", "READ", ""} { //nolint:misspell // intentional typo fixture
		resp := f.do("POST", "/api/v1/tokens", write, map[string]any{
			"name": "tok", "scope": scope,
		})
		if resp.status() != 400 {
			t.Fatalf("scope=%q status = %d, want 400 (body %s)", scope, resp.status(), resp.rec.Body.String())
		}
	}

	// Nothing was created by the rejected requests.
	list := f.do("GET", "/api/v1/tokens", write, nil)
	var body apitypes.TokenListResponse
	list.json(t, &body)
	// The one bootstrapping "write" token from f.token is the only one that
	// should exist.
	if len(body.Items) != 1 {
		t.Fatalf("tokens after rejected creates = %+v, want 1 (the fixture's own token)", body.Items)
	}
}

// createExpiredToken mints a token whose expires_at is already in the past,
// bypassing the API (which cannot backdate a token) by calling the store
// directly with a computed expiry -- the same path handleCreateToken takes
// for a token expiring in the future.
func createExpiredToken(t *testing.T, f *transferFixture, name string) string {
	t.Helper()
	tok, hash, err := auth.NewToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := f.st.CreateToken(context.Background(), f.alice.ID, name, "write", hash, &past); err != nil {
		t.Fatalf("create expired token: %v", err)
	}
	return tok
}

// An expired token is refused by everything except the owner's own token
// list, where it stays visible (with a past expires_at) so the owner can see
// why it stopped working and delete it.
func TestListTokens_IncludesExpired(t *testing.T) {
	f := newTransferFixture(t)
	write := f.token(f.alice, "write")
	createExpiredToken(t, f, "old-laptop")

	resp := f.do("GET", "/api/v1/tokens", write, nil)
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}
	var body apitypes.TokenListResponse
	resp.json(t, &body)

	// The fixture's own "write" token (no expiry) plus the expired one.
	if len(body.Items) != 2 {
		t.Fatalf("items = %+v, want 2", body.Items)
	}
	var found bool
	for _, item := range body.Items {
		if item.Name != "old-laptop" {
			continue
		}
		found = true
		if item.ExpiresAt == nil || !item.ExpiresAt.Before(time.Now()) {
			t.Fatalf("expired token expires_at = %v, want a past timestamp", item.ExpiresAt)
		}
	}
	if !found {
		t.Fatalf("expired token missing from list: %+v", body.Items)
	}
}

// An expired token no longer authenticates a request, and it fails the same
// way a token that never existed does -- the server does not distinguish
// "expired" from "unknown" to an unauthenticated caller.
func TestExpiredToken_FailsAuthentication(t *testing.T) {
	f := newTransferFixture(t)
	expired := createExpiredToken(t, f, "old-laptop")

	resp := f.do("GET", "/api/v1/tokens", expired, nil)
	if resp.status() != 401 {
		t.Fatalf("status = %d, want 401 (body %s)", resp.status(), resp.rec.Body.String())
	}
}

// DeleteToken works on an expired token exactly like a live one -- it is
// still the owner's row to clean up.
func TestDeleteToken_WorksOnExpiredToken(t *testing.T) {
	f := newTransferFixture(t)
	write := f.token(f.alice, "write")
	createExpiredToken(t, f, "old-laptop")

	listResp := f.do("GET", "/api/v1/tokens", write, nil)
	var body apitypes.TokenListResponse
	listResp.json(t, &body)
	var id int64
	for _, item := range body.Items {
		if item.Name == "old-laptop" {
			id = item.ID
		}
	}
	if id == 0 {
		t.Fatalf("expired token not found in list: %+v", body.Items)
	}

	del := f.do("DELETE", "/api/v1/tokens/"+strconv.FormatInt(id, 10), write, nil)
	if del.status() != 204 {
		t.Fatalf("delete status = %d, want 204 (body %s)", del.status(), del.rec.Body.String())
	}
}
