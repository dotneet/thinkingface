package api

import "testing"

// TestHFDeleteRepoDefaultsNamespaceToCaller covers the same namespace
// fallback handleHFCreateRepo already has: huggingface_hub's
// HfApi.delete_repo("myrepo") (no "/" in the repo id) sends
// {"name": "myrepo", "organization": null}, and the caller's own namespace
// is implied. Before this fix handleHFDeleteRepo skipped the fallback that
// handleHFCreateRepo has, so every such delete looked up a repo in
// namespace "" and 404'd -- create worked but delete never did.
func TestHFDeleteRepoDefaultsNamespaceToCaller(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("alice", "myrepo", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("DELETE", "/api/repos/delete", tok, map[string]any{
		"type": "model", "name": "myrepo", "organization": nil,
	})
	if resp.status() != 200 {
		t.Fatalf("status = %d, body = %s", resp.status(), resp.rec.Body.String())
	}

	if r := f.do("GET", "/api/v1/repos/model/alice/myrepo", tok, nil); r.status() != 404 {
		t.Fatalf("repo still resolves after delete: status = %d, body = %s", r.status(), r.rec.Body.String())
	}
}

// TestHFDeleteRepoRejectsOtherNamespace makes sure the fallback above did not
// loosen the existing admin check: naming someone else's namespace
// explicitly must still be refused, exactly as before this fix.
func TestHFDeleteRepoRejectsOtherNamespace(t *testing.T) {
	f := newTransferFixture(t)
	f.repo("bob", "other", "model")
	tok := f.token(f.alice, "write")

	resp := f.do("DELETE", "/api/repos/delete", tok, map[string]any{
		"type": "model", "name": "bob/other",
	})
	if resp.status() != 403 {
		t.Fatalf("status = %d, want 403, body = %s", resp.status(), resp.rec.Body.String())
	}

	// The repository must still be there.
	if r := f.do("GET", "/api/v1/repos/model/bob/other", "", nil); r.status() != 200 {
		t.Fatalf("repo missing after rejected delete: status = %d, body = %s", r.status(), r.rec.Body.String())
	}
}
