package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/apitypes"
)

// Storage quota administration (GET /api/v1/admin/namespaces, PATCH
// /api/v1/admin/namespaces/{ns}), driven over real HTTP against the security
// fixture -- these endpoints accept a browser session and nothing else, so
// the login path has to be exercised for real.

func decodeInto[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, body)
	}
	return v
}

func TestAdminNamespaces_ListsUsageAndQuota(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct-horse-battery")
	f.user("alice", "correct-horse-battery")
	f.cfg.DefaultStorageQuotaBytes = 1000
	admin := f.session("root", "correct-horse-battery")

	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/namespaces", cookies: admin})
	if rec.Code != http.StatusOK {
		t.Fatalf("list namespaces: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeInto[apitypes.AdminNamespaceListResponse](t, rec.Body.Bytes())
	if got.Total != 2 || len(got.Items) != 2 {
		t.Fatalf("list = %d items, total %d, want 2/2", len(got.Items), got.Total)
	}
	if got.DefaultQuotaBytes == nil || *got.DefaultQuotaBytes != 1000 {
		t.Fatalf("default_quota_bytes = %v, want 1000", got.DefaultQuotaBytes)
	}
	for _, it := range got.Items {
		if it.QuotaBytes != nil {
			t.Errorf("%s has an override of %d, want none", it.Namespace, *it.QuotaBytes)
		}
		// No override, so the instance default is what would actually be
		// enforced -- a screen must not have to work that out for itself.
		if it.EffectiveQuotaBytes == nil || *it.EffectiveQuotaBytes != 1000 {
			t.Errorf("%s effective quota = %v, want the instance default", it.Namespace, it.EffectiveQuotaBytes)
		}
	}
}

// null and 0 are different instructions, and the round trip has to keep them
// apart: null goes back to the instance default, 0 is a quota of zero bytes.
func TestAdminNamespaces_NullClearsAndZeroIsAQuota(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct-horse-battery")
	f.user("alice", "correct-horse-battery")
	f.cfg.DefaultStorageQuotaBytes = 1000
	admin := f.session("root", "correct-horse-battery")

	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/namespaces/alice",
		cookies: admin, body: map[string]any{"quota_bytes": 0}})
	if rec.Code != http.StatusOK {
		t.Fatalf("set quota to zero: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeInto[apitypes.AdminNamespaceUsage](t, rec.Body.Bytes())
	if got.QuotaBytes == nil || *got.QuotaBytes != 0 {
		t.Fatalf("quota_bytes = %v, want an explicit 0", got.QuotaBytes)
	}
	if got.EffectiveQuotaBytes == nil || *got.EffectiveQuotaBytes != 0 {
		t.Fatalf("effective_quota_bytes = %v, want 0: an override beats the default", got.EffectiveQuotaBytes)
	}

	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/namespaces/alice",
		cookies: admin, body: map[string]any{"quota_bytes": nil}})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear quota: status %d, body %s", rec.Code, rec.Body.String())
	}
	got = decodeInto[apitypes.AdminNamespaceUsage](t, rec.Body.Bytes())
	if got.QuotaBytes != nil {
		t.Fatalf("quota_bytes = %d after clearing, want null", *got.QuotaBytes)
	}
	if got.EffectiveQuotaBytes == nil || *got.EffectiveQuotaBytes != 1000 {
		t.Fatalf("effective_quota_bytes = %v, want the instance default back", got.EffectiveQuotaBytes)
	}
}

func TestAdminNamespaces_RejectsBadRequests(t *testing.T) {
	f := newSecFixture(t)
	f.adminUser("root", "correct-horse-battery")
	f.user("alice", "correct-horse-battery")
	admin := f.session("root", "correct-horse-battery")

	// An absent field is refused rather than guessed at: guessing would have
	// to pick between "clear it" and "zero bytes".
	rec := f.do(secRequest{method: "PATCH", path: "/api/v1/admin/namespaces/alice",
		cookies: admin, body: map[string]any{}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: status %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/namespaces/alice",
		cookies: admin, body: map[string]any{"quota_bytes": -1}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative quota: status %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}

	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/namespaces/nobody",
		cookies: admin, body: map[string]any{"quota_bytes": 1}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown namespace: status %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// A quota an organisation admin could raise would not be a quota, so the
// whole surface is site-administrator-only -- including the read.
func TestAdminNamespaces_RefusedForOrdinaryAccounts(t *testing.T) {
	f := newSecFixture(t)
	f.user("alice", "correct-horse-battery")
	alice := f.session("alice", "correct-horse-battery")

	rec := f.do(secRequest{method: "GET", path: "/api/v1/admin/namespaces", cookies: alice})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("list as a non-administrator: status %d, want 403", rec.Code)
	}
	rec = f.do(secRequest{method: "PATCH", path: "/api/v1/admin/namespaces/alice",
		cookies: alice, body: map[string]any{"quota_bytes": 1 << 40}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("raise my own quota: status %d, want 403", rec.Code)
	}
}
