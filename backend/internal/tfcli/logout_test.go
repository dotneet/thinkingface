package tfcli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dotneet/thinkingface/backend/internal/tfcli/config"
)

func saveCredential(t *testing.T, cred config.Credential) {
	t.Helper()
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.Set(cred)
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutNotLoggedIn(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runMain(t, []string{"logout", "http://example.invalid"}, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "not logged in") {
		t.Errorf("stderr = %q, want it to mention not being logged in", errOut)
	}
}

func TestLogoutNoEndpointNoDefault(t *testing.T) {
	isolateEnv(t)
	code, _, errOut := runMain(t, []string{"logout"}, "")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%s", code, errOut)
	}
}

func TestLogoutRemovesPastedTokenWithoutRevoking(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("logout should not call the server for a TokenID-less (pasted) credential, got %s %s", r.Method, r.URL.Path)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	normalized, err := config.NormalizeEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	saveCredential(t, config.Credential{Endpoint: normalized, Token: "tok", TokenID: 0, CreatedAt: time.Now()})

	code, out, errOut := runMain(t, []string{"logout", srv.URL}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "Logged out") {
		t.Errorf("stdout = %q, want confirmation", out)
	}

	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Get(normalized); ok {
		t.Error("credential should have been removed")
	}
}

func TestLogoutRevokesMintedToken(t *testing.T) {
	isolateEnv(t)

	revoked := false
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/tokens/7", func(w http.ResponseWriter, r *http.Request) {
		revoked = true
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	normalized, err := config.NormalizeEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	saveCredential(t, config.Credential{Endpoint: normalized, Token: "tok", TokenID: 7, CreatedAt: time.Now()})

	code, _, errOut := runMain(t, []string{"logout", srv.URL}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut)
	}
	if !revoked {
		t.Error("server never saw DELETE /api/v1/tokens/7")
	}
}

func TestLogoutDefaultEndpoint(t *testing.T) {
	isolateEnv(t)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	normalized, err := config.NormalizeEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	saveCredential(t, config.Credential{Endpoint: normalized, Token: "tok", CreatedAt: time.Now()})

	// No ENDPOINT argument: should fall back to file.DefaultEndpoint, which
	// Set() pointed at this credential.
	code, _, errOut := runMain(t, []string{"logout"}, "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, errOut)
	}
}
