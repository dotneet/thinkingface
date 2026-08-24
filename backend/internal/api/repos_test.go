package api

import (
	"testing"

	"github.com/dotneet/thinkingface/backend/internal/config"
	"github.com/dotneet/thinkingface/backend/internal/store"
)

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

// TestSSHCloneURL pins the URL shape documented in docs/users/guides/git.md.
// The host comes from TF_PUBLIC_URL while only the port comes from
// TF_SSH_ADDR, and the whole thing is empty when the listener is off, so the
// UI can tell "no SSH here" from "SSH on the default port".
func TestSSHCloneURL(t *testing.T) {
	repo := &store.Repo{Kind: "dataset", Namespace: "admin", Name: "imdb-reviews"}
	model := &store.Repo{Kind: "model", Namespace: "acme", Name: "sentiment-base"}

	cases := []struct {
		name      string
		enabled   bool
		publicURL string
		sshAddr   string
		repo      *store.Repo
		want      string
	}{
		{
			name: "disabled", enabled: false,
			publicURL: "http://localhost:8080", sshAddr: ":2222", repo: repo, want: "",
		},
		{
			name:      "listen address contributes only its port",
			enabled:   true,
			publicURL: "http://localhost:8080", sshAddr: "0.0.0.0:2222", repo: repo,
			want: "ssh://git@localhost:2222/datasets/admin/imdb-reviews.git",
		},
		{
			name: "models sit at the root", enabled: true,
			publicURL: "https://hub.example.com", sshAddr: ":2222", repo: model,
			want: "ssh://git@hub.example.com:2222/models/acme/sentiment-base.git",
		},
		{
			name: "port 22 stays implicit", enabled: true,
			publicURL: "https://hub.example.com", sshAddr: ":22", repo: repo,
			want: "ssh://git@hub.example.com/datasets/admin/imdb-reviews.git",
		},
		{
			name: "http port is dropped, not reused", enabled: true,
			publicURL: "http://192.168.1.10:8080", sshAddr: ":2222", repo: repo,
			want: "ssh://git@192.168.1.10:2222/datasets/admin/imdb-reviews.git",
		},
		{
			name: "address with no port falls back to the implicit 22", enabled: true,
			publicURL: "http://localhost:8080", sshAddr: "", repo: repo,
			want: "ssh://git@localhost/datasets/admin/imdb-reviews.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{
				PublicURL: tc.publicURL, SSHEnabled: tc.enabled, SSHAddr: tc.sshAddr,
			}}
			if got := s.sshCloneURL(tc.repo); got != tc.want {
				t.Fatalf("sshCloneURL = %q, want %q", got, tc.want)
			}
		})
	}
}
